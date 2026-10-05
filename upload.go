package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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

// pickLiveVM returns the first live row, or nil.
func pickLiveVM(rows []vmRow) *vmRow {
	for i := range rows {
		if rows[i].Live {
			return &rows[i]
		}
	}
	return nil
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// packTar streams src (file or dir) as a gzip-compressed tar rooted at base
// into w, and returns the raw and compressed byte counts. Symlinks are
// dereferenced (like scp -r did); broken symlinks and non-regular,
// non-directory files are skipped.
func packTar(w io.Writer, src, base string) (int64, int64, error) {
	cw := &countWriter{w: w}
	zw, err := gzip.NewWriterLevel(cw, gzip.BestSpeed)
	if err != nil {
		return 0, 0, err
	}
	tw := tar.NewWriter(zw)
	var raw int64
	// #nosec G703 -- src is the user-requested upload source, guarded by refuseUnsafe before we get here
	fi, err := os.Stat(src)
	if err != nil {
		return 0, 0, err
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
	return raw, cw.n, err
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
func uploadRun(arg string, stdin io.Reader, stdout, stderr io.Writer) error {
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

	creds, a := requireLogin()
	// run the two independent profile/vm lookups concurrently
	var me *meInfo
	meCh := make(chan error, 1)
	go func() {
		var err error
		me, err = getMe(a)
		meCh <- err
	}()
	rows, err := listVMs(a)
	if meErr := <-meCh; meErr != nil {
		return meErr // 401 -> "session expired — run `zert login`"
	}
	if err != nil {
		return err
	}
	if err := ensurePubkey(a, me, stderr); err != nil {
		return err
	}

	vm := pickLiveVM(rows)
	created := false
	if vm == nil {
		fmt.Fprintln(stderr, "no live vm — creating one …")
		vm, err = createVM(a)
		if err != nil {
			return err
		}
		created = true
	}

	dest := "user@" + vm.SandboxID
	self, err := os.Executable()
	if err != nil {
		self = "zert"
	}
	sshCmd := exec.Command(sshPath)
	sshCmd.Args = append(sshCmd.Args,
		"-o", proxyCommand(self, vm.SandboxID),
		"-o", "StrictHostKeyChecking=accept-new",
		dest,
		uploadRemoteCmd,
	)
	sshCmd.Stdout, sshCmd.Stderr = stdout, stderr
	// the tunnel child gets the token via environment, never argv
	sshCmd.Env = childEnv(os.Environ(), creds.Token, creds.Host)

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

	pr, pw := io.Pipe()
	sshCmd.Stdin = pr
	if err := sshCmd.Start(); err != nil {
		_ = pw.Close()
		return fmt.Errorf("spawn ssh: %w", err)
	}
	packDone := make(chan error, 1)
	var raw, packed int64
	go func() {
		r, p, err := packTar(pw, src, base)
		raw, packed = r, p
		pw.CloseWithError(err)
		packDone <- err
	}()
	waitErr := sshCmd.Wait()
	packErr := <-packDone
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			return &exitError{ee.ExitCode(), fmt.Errorf("ssh exited with %d", ee.ExitCode())}
		}
		return fmt.Errorf("run ssh: %w", waitErr)
	}
	// a broken pipe with a clean ssh exit just means ssh closed stdin early
	if packErr != nil && !errors.Is(packErr, io.ErrClosedPipe) {
		return fmt.Errorf("packing %s: %w", abs, packErr)
	}
	fmt.Fprintf(stderr, "uploaded %s (%s → %s compressed)\n", abs, humanBytes(raw), humanBytes(packed))

	if created {
		fmt.Fprintf(stderr, "entering vm %s (killed on exit) …\n", vm.SandboxID)
		return sshVM(creds, a, vm, nil, false, stdin, stdout, stderr)
	}
	return nil
}

func cmdUpload(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: zert <path>")
	}
	return uploadRun(args[0], os.Stdin, os.Stdout, os.Stderr)
}
