package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// scpRemoteBase allows only characters that are safe in a legacy scp remote
// path: letters, digits and . _ @ % + = : , - (colon is not a shell
// metacharacter, spaces and metacharacters like ; & | $ ` are rejected).
var scpRemoteBase = regexp.MustCompile(`^[A-Za-z0-9._@%+=:,-]+$`)

// safeScpSource prefixes relative paths so a leading dash can't be read
// as an scp option.
func safeScpSource(p string) string {
	if filepath.IsAbs(p) || strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../") {
		return p
	}
	return "./" + p
}

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
	_, err := os.Stat(expandTilde(arg))
	return err == nil
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

// uploadRun is cmdUpload with injectable stdio and scp binary (for tests).
func uploadRun(arg string, stdout, stderr io.Writer) error {
	src := expandTilde(arg)
	base := filepath.Base(strings.TrimRight(src, "/"))
	if !scpRemoteBase.MatchString(base) {
		return errors.New("file name \"" + base + "\" contains characters that cannot be uploaded safely; rename the file or archive it first (letters, digits and . _ @ % + = : , - only)")
	}

	creds, a := requireLogin()
	me, err := getMe(a)
	if err != nil {
		return err // 401 -> "session expired — run `zert login`"
	}
	if err := ensurePubkey(a, me, stderr); err != nil {
		return err
	}

	rows, err := listVMs(a)
	if err != nil {
		return err
	}
	vm := pickLiveVM(rows)
	if vm == nil {
		fmt.Fprintln(stderr, "no live vm — creating one …")
		vm, err = createVM(a)
		if err != nil {
			return err
		}
	}

	dest := "user@" + vm.SandboxID + ":~/" + base
	safeSrc := safeScpSource(src)

	self, err := os.Executable()
	if err != nil {
		self = "zert"
	}
	scpCmd := exec.Command(scpPath)
	scpCmd.Args = append(scpCmd.Args,
		"-r",
		"-o", proxyCommand(self, vm.SandboxID),
		"-o", "StrictHostKeyChecking=accept-new",
		"--",
		safeSrc, dest,
	)
	scpCmd.Stdout, scpCmd.Stderr = stdout, stderr
	// the tunnel child gets the token via environment, never argv
	scpCmd.Env = childEnv(os.Environ(), creds.Token, creds.Host)

	fmt.Fprintf(stderr, "uploading %s → %s …\n", arg, dest)
	if err := scpCmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return &exitError{ee.ExitCode(), fmt.Errorf("scp exited with %d", ee.ExitCode())}
		}
		return fmt.Errorf("spawn scp: %w", err)
	}
	return nil
}

func cmdUpload(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: zert <path>")
	}
	return uploadRun(args[0], os.Stdout, os.Stderr)
}
