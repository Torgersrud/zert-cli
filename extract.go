package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// splitRemotePath splits a vm path into the directory to cd into and the
// basename to archive, both suitable for the remote tar command. A path with
// no slash (or a ~/ prefix) resolves against the vm user's home.
func splitRemotePath(p string) (dir, base string, err error) {
	// the remote command single-quotes both parts, so a quote in the path
	// cannot be escaped out of — refuse it outright
	if strings.ContainsRune(p, '\'') {
		return "", "", errors.New("vm path \"" + p + "\" contains a single quote, which cannot be sent to the vm safely; rename the file or archive it first")
	}
	trimmed := strings.TrimRight(p, "/")
	if trimmed == "" {
		if p == "" {
			return "", "", errors.New("extract needs a path inside the vm, e.g. `zert extract ~/report.pdf`")
		}
		return "", "", errors.New("refusing to extract from the filesystem root")
	}
	// a bare name resolves against the vm user's home, like a fresh ssh cwd
	if !strings.Contains(trimmed, "/") {
		trimmed = "~/" + trimmed
	}
	// path (not filepath): these are POSIX paths inside the vm, whatever OS
	// the CLI itself runs on
	dir, base = path.Dir(trimmed), path.Base(trimmed)
	if dir == "/" {
		// would mean `tar -C /` — same refusal as uploading the root
		return "", "", errors.New("refusing to extract from the filesystem root")
	}
	if base == "." || base == ".." {
		return "", "", errors.New(`refusing to extract "." or ".." — pass a specific file or directory`)
	}
	if !remoteBaseName.MatchString(base) {
		return "", "", errors.New("file name \"" + base + "\" cannot be extracted safely; rename the file or archive it first (letters, digits and . _ @ % + = : , - only)")
	}
	return dir, base, nil
}

// extractRemoteCmd is the remote command every extract streams back: the vm
// tars the entry on stdout and the local side untars it into the destination.
// ssh joins argv into one remote shell string, so the quoting has to live in
// the argument itself; dir and base are quote-free by splitRemotePath.
func extractRemoteCmd(dir, base string) string {
	// a leading ~ must stay OUTSIDE the single quotes: inside them the remote
	// shell does not expand it, and tar -C gets a literal "~" it cannot chdir
	// to. The slash right after ~ must also stay unquoted (~/'a b' works,
	// ~'a b' or '~a b' do not — POSIX only expands when the tilde-prefix is
	// unquoted up to the first unquoted slash).
	q := "'" + dir + "'"
	switch {
	case dir == "~":
		q = "~"
	case strings.HasPrefix(dir, "~/"):
		q = "~/'" + strings.TrimPrefix(dir, "~/") + "'"
	}
	return "tar -czf - -C " + q + " -- '" + base + "'"
}

type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// extractTarGz writes a gzip-compressed tar stream into dir (which must
// exist), returning the number of regular files and the total bytes written.
// Entries are treated as untrusted (they come from the vm): absolute paths,
// .. traversal and anything that would escape dir are refused; only regular
// files and directories are written — symlinks, hardlinks, devices and other
// special entries are skipped.
func extractTarGz(dir string, r io.Reader) (files int, raw int64, err error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return 0, 0, err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return files, raw, nil
		}
		if err != nil {
			return files, raw, err
		}
		if filepath.IsAbs(h.Name) || strings.HasPrefix(h.Name, "/") {
			return files, raw, fmt.Errorf("refusing absolute path in archive: %q", h.Name)
		}
		rel := filepath.Clean(h.Name)
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return files, raw, fmt.Errorf("refusing path traversal in archive: %q", h.Name)
		}
		// #nosec G305 -- extracting a user-requested archive from their own vm, entries guarded above
		target := filepath.Join(dir, rel)
		// belt and braces: the joined path must still sit inside dir
		if target != dir && !strings.HasPrefix(target, dir+string(filepath.Separator)) {
			return files, raw, fmt.Errorf("archive entry escapes the destination: %q", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return files, raw, err
			}
		case tar.TypeReg:
			mode := h.FileInfo().Mode().Perm()
			if mode == 0 {
				mode = 0644
			}
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return files, raw, err
			}
			f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
			if err != nil {
				return files, raw, err
			}
			n, cerr := io.Copy(f, tr)
			raw += n
			if cerr != nil {
				_ = f.Close()
				return files, raw, cerr
			}
			if err := f.Close(); err != nil {
				return files, raw, err
			}
			files++
		}
		// symlinks, hardlinks, devices and friends are skipped by design:
		// they could point outside dir once followed
	}
}

// extractRun is cmdExtract with injectable stdio (for tests). The inverse of
// uploadRun: the vm streams a tar.gz of vmPath over ssh stdout and it is
// unpacked into dest. It never creates, enters or kills a vm — the vm must
// already be live.
func extractRun(vmPathArg, dest string, stdout, stderr io.Writer) error {
	// parse before any network: a bad name must cost zero requests
	dir, base, err := splitRemotePath(vmPathArg)
	if err != nil {
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
	if vm == nil {
		return errNoLiveVM
	}

	if dest == "" {
		dest = "."
	}
	absDest, err := filepath.Abs(expandTilde(dest))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(absDest, 0755); err != nil {
		return err
	}

	remote := "user@" + vm.SandboxID
	self, err := os.Executable()
	if err != nil {
		self = "zert"
	}
	sshCmd := exec.Command(sshPath)
	sshCmd.Args = append(sshCmd.Args,
		"-o", proxyCommand(self, vm.SandboxID),
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=4",
		remote,
		extractRemoteCmd(dir, base),
	)
	sshCmd.Stdin = nil
	sshCmd.Stderr = stderr
	// the tunnel child gets the token via environment, never argv
	sshCmd.Env = childEnv(os.Environ(), creds.Token, creds.Host)

	fmt.Fprintf(stderr, "extracting %s:%s → %s …\n", remote, vmPathArg, absDest)

	pr, pw := io.Pipe()
	sshCmd.Stdout = pw
	if err := sshCmd.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return fmt.Errorf("spawn ssh: %w", err)
	}
	cr := &countReader{r: pr}
	extractDone := make(chan error, 1)
	var files int
	var raw int64
	go func() {
		f, n, err := extractTarGz(absDest, cr)
		if err == nil {
			// tar stops reading at the end-of-archive marker; drain the
			// tail so ssh's stdout copy can finish instead of stalling
			_, err = io.Copy(io.Discard, cr)
		}
		if err != nil {
			// abort: close the read end so a still-writing ssh sees EPIPE
			// and exec's stdout copy unblocks Wait
			pr.CloseWithError(err)
		}
		files, raw = f, n
		extractDone <- err
	}()
	waitErr := sshCmd.Wait()
	_ = pw.Close() // child is gone: hand the untar reader its EOF
	tarErr := <-extractDone
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) {
		// ssh's exit code is the authoritative failure; a downstream stream
		// error is usually just the fallout of the truncated transfer
		return &exitError{ee.ExitCode(), fmt.Errorf("ssh exited with %d", ee.ExitCode())}
	}
	if waitErr != nil {
		return fmt.Errorf("run ssh: %w", waitErr)
	}
	if tarErr != nil && !errors.Is(tarErr, io.ErrClosedPipe) {
		return fmt.Errorf("extracting %s: %w", vmPathArg, tarErr)
	}
	fmt.Fprintf(stderr, "extracted %d files (%s → %s compressed)\n",
		files, humanBytes(raw), humanBytes(cr.n))
	return nil
}

func cmdExtract(args []string) error {
	usage := errors.New("usage: zert extract <vm-path> [local-dir]")
	if len(args) < 1 || len(args) > 2 || strings.HasPrefix(args[0], "-") {
		return usage
	}
	dest := ""
	if len(args) == 2 {
		dest = args[1]
	}
	return extractRun(args[0], dest, os.Stdout, os.Stderr)
}
