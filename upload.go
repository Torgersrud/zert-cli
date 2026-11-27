package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// remoteBaseName allows only characters that are safe as a remote file name
// and in CLI output: letters, digits and . _ @ % + = : , - (spaces and shell
// metacharacters like ; & | $ ` are rejected).
var remoteBaseName = regexp.MustCompile(`^[A-Za-z0-9._@%+=:,-]+$`)

// uploadRemoteCmd is the remote command every upload streams into: the local
// side sends a gzip-compressed tar on stdin and the vm untars it into $HOME.
const uploadRemoteCmd = "tar -xzf - -C ~"

// expandTilde resolves a leading ~ against the user's home directory.
func expandTilde(arg string) string {
	if arg == "~" || strings.HasPrefix(arg, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(arg, "~"))
		}
	}
	return arg
}

// looksLikePath reports whether arg names an existing local file or directory.
func looksLikePath(arg string) bool {
	if arg == "" || strings.Contains(arg, "@") {
		return false
	}
	// #nosec G703 -- stat of a user CLI arg is the dispatch heuristic by design
	_, err := os.Stat(expandTilde(arg))
	return err == nil
}

// refuseUnsafe rejects uploading the filesystem root or the user's home dir.
// resolved must be the EvalSymlinks+Abs-resolved source path.
func refuseUnsafe(resolved string) error {
	if filepath.Dir(resolved) == resolved {
		return errors.New("refusing to upload the filesystem root")
	}
	if home, err := os.UserHomeDir(); err == nil {
		h := home
		if ha, e := filepath.Abs(home); e == nil {
			h = ha
			if hs, e := filepath.EvalSymlinks(ha); e == nil {
				h = hs
			}
		}
		if resolved == h {
			return errors.New("refusing to upload your home directory")
		}
	}
	return nil
}

// packTar streams src (file or dir) as a gzip-compressed tar rooted at base
// into w, and returns the raw byte count. Symlinks are dereferenced (like
// scp -r did); broken symlinks and non-regular, non-directory files are
// skipped. The compressed total is the transfer module's answer — the pipe
// is its, and so is the counting.
func packTar(w io.Writer, src, base string) (int64, error) {
	zw, err := gzip.NewWriterLevel(w, gzip.BestSpeed)
	if err != nil {
		return 0, err
	}
	tw := tar.NewWriter(zw)
	var raw int64
	// #nosec G703 -- src is the user-requested upload source, guarded by refuseUnsafe before we get here
	fi, err := os.Stat(src)
	if err != nil {
		return 0, err
	}
	if fi.IsDir() {
		err = addTree(tw, src, base, fi, &raw)
	} else {
		err = addFile(tw, src, base, fi, &raw)
	}
	if err == nil {
		if cerr := tw.Close(); cerr != nil {
			err = cerr
		} else if cerr := zw.Close(); cerr != nil {
			err = cerr
		}
	}
	return raw, err
}

// addTree writes dir (as an entry plus contents) recursively. name uses
// forward slashes and is the tar entry prefix for dir.
func addTree(tw *tar.Writer, dir, name string, fi os.FileInfo, raw *int64) error {
	hdr, err := tar.FileInfoHeader(fi, "")
	if err != nil {
		return err
	}
	hdr.Name = name + "/"
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		// #nosec G703 -- walking the user-requested source tree is the point of the command
		st, err := os.Stat(p)
		if err != nil {
			continue // broken symlink or vanished file
		}
		en := name + "/" + e.Name()
		switch {
		case st.IsDir():
			if err := addTree(tw, p, en, st, raw); err != nil {
				return err
			}
		case st.Mode().IsRegular():
			if err := addFile(tw, p, en, st, raw); err != nil {
				return err
			}
		}
	}
	return nil
}

// addFile writes one regular file as tar entry name.
func addFile(tw *tar.Writer, path, name string, fi os.FileInfo, raw *int64) error {
	if !fi.Mode().IsRegular() {
		return nil
	}
	hdr, err := tar.FileInfoHeader(fi, "")
	if err != nil {
		return err
	}
	hdr.Name = name
	// #nosec G304 G703 -- opening files from the user-requested source tree is the point of the command
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	n, err := io.Copy(tw, f)
	*raw += n
	return err
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	v, i := float64(n), 0
	for un := []string{"KiB", "MiB", "GiB", "TiB"}; ; i++ {
		v /= unit
		if v < unit || i == len(un)-1 {
			return fmt.Sprintf("%.1f %s", v, un[i])
		}
	}
}

// uploadRun is cmdUpload with injectable stdio (for tests). The source is
// streamed as a tar.gz through a single ssh exec (`tar -xzf - -C ~`), which
// avoids scp's per-file round trips and compresses the whole tree as one
// stream (compression happens client-side, before the encrypted tunnel).
// sshArgs are the customer's ssh arguments after the path, unparsed: the
// one scan that answers them — the args forwarded verbatim, the swallowed
// --keep with its notice on stderr, where a user command starts — happens
// here, inside the runner, and its answers apply to the interactive Entry
// after the copy — always when this call created the vm, and also for a
// pre-existing live vm when the scan forwards args.
func uploadRun(arg string, sshArgs []string, stdin io.Reader, stdout, stderr io.Writer) error {
	// the one walk: everything the grammar answers — the args ssh gets,
	// the swallowed --keep, where the user's command starts — is answered
	// here, once, before the copy decides anything, so even a refused
	// upload has warned for a swallowed --keep
	scan := scanSSHArgs(sshArgs)
	if scan.sawKeep {
		warnKeepDeprecated(stderr)
	}

	src := expandTilde(arg)
	trimmed := strings.TrimRight(src, "/")
	base := filepath.Base(trimmed)
	if trimmed != "" && (base == "." || base == "..") {
		return errors.New(`refusing to upload "." or ".." — pass a specific file or directory`)
	}
	if !remoteBaseName.MatchString(base) {
		return errors.New("file name \"" + base + "\" contains characters that cannot be uploaded safely; rename the file or archive it first (letters, digits and . _ @ % + = : , - only)")
	}

	abs, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	// best effort: EvalSymlinks fails for paths that don't exist, which is fine
	resolved := abs
	if r, e := filepath.EvalSymlinks(abs); e == nil {
		resolved = r
	}
	if err := refuseUnsafe(resolved); err != nil {
		return err
	}

	creds, vm, created, err := resolveCopyTarget(stderr)
	if err != nil {
		return err
	}

	dest := vmUserDest(vm.SandboxID)

	kind := "path"
	// #nosec G703 -- stat of the user-requested source, only used to label output
	if st, err := os.Stat(src); err == nil {
		if st.IsDir() {
			kind = "dir"
		} else {
			kind = "file"
		}
	}
	fmt.Fprintf(stderr, "uploading %s (%s) → %s:~/%s\n", abs, kind, dest, base)

	// what is genuinely upload's: the source and base validated above, the
	// remote command every upload spells, and the pack codec with its
	// wording — the pipe, the counting, the spawn-failure release and the
	// failure precedence belong to the transfer module
	var raw int64
	packed, err := transferToVM(creds, vm, uploadRemoteCmd, stdout, stderr,
		func(w io.Writer) error {
			r, packErr := packTar(w, src, base)
			raw = r
			return packErr
		},
		func(e error) error {
			return fmt.Errorf("packing %s: %w", abs, e)
		})
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "uploaded %s (%s → %s compressed)\n", abs, humanBytes(raw), humanBytes(packed))

	// leaving the entered vm never kills it: only `zert kill` or its expiry
	// ends a vm — and --keep no longer decides anything either.
	if created || len(scan.args) > 0 {
		fmt.Fprintf(stderr, "entering vm %s …\n", vm.SandboxID)
		return enterVM(creds, vm, scan, stdin, stdout, stderr)
	}
	return nil
}

func cmdUpload(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: zert <path> [ssh args..]")
	}
	return uploadRun(args[0], args[1:], os.Stdin, os.Stdout, os.Stderr)
}
