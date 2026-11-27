package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ensurePubkey makes sure the server has the customer's ssh pubkey,
// uploading the local one when missing.
func ensurePubkey(a api, me *meInfo, stderr io.Writer) error {
	if me.SSHPubkey != nil && *me.SSHPubkey != "" {
		return nil
	}
	pub, err := findLocalPubkey()
	if err != nil {
		return err
	}
	fields := strings.Fields(pub)
	if len(fields) == 0 {
		return errors.New("ssh public key file is empty")
	}
	switch {
	case strings.HasPrefix(fields[0], "ssh-"),
		strings.HasPrefix(fields[0], "ecdsa-"),
		strings.HasPrefix(fields[0], "sk-"):
	default:
		return errors.New("not an ssh public key: " + fields[0])
	}
	resp, data, err := a.do("POST", "/v1/me/key", map[string]string{"pubkey": pub})
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return mapped(resp, data)
	}
	fmt.Fprintln(stderr, "uploaded ssh pubkey ("+fields[0]+" …)")
	return nil
}

// findLocalPubkey returns the customer's own ssh public key.
func findLocalPubkey() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	for _, name := range []string{"id_ed25519.pub", "id_rsa.pub"} {
		// #nosec G304 -- fixed constant filenames under ~/.ssh, nothing user-tainted in the path
		if b, err := os.ReadFile(filepath.Join(home, ".ssh", name)); err == nil {
			return strings.TrimSpace(string(b)), nil
		}
	}
	return "", errors.New("no ssh key found: run `ssh-keygen -t ed25519` first")
}

// hasAnswerableVM reports whether the listing carries a machine the door's
// ensure ask answers as the Customer's own — one that is running or
// asleep. Rows that are neither (expired, or a state word this build has
// never seen) are dead rows: they answer nothing, and the ask places a
// machine instead.
func hasAnswerableVM(rows []vmRow) bool {
	for _, r := range rows {
		if r.Live || r.Asleep {
			return true
		}
	}
	return false
}

// wakeOrCreate announces the ensure ask a customer without a live machine
// makes, and makes it: the machine is theirs and asleep — woken — or
// nothing answerable is listed and one is placed. The wording lives here
// once, so every path that asks from a listing tells the same story.
func wakeOrCreate(a api, rows []vmRow, stderr io.Writer) (*vmRow, error) {
	if hasAsleepVM(rows) {
		fmt.Fprintln(stderr, "no live vm — waking yours …")
	} else {
		fmt.Fprintln(stderr, "no live vm — creating one …")
	}
	return ensureVM(a)
}

// parseSSHArgs returns the args forwarded verbatim to ssh — everything
// after "--" is passthrough — and swallows the deprecated --keep flag:
// when it was given, this one place prints its deprecation notice, so
// every entry path (vm, upload) warns exactly once.
func parseSSHArgs(stderr io.Writer, args []string) (sshArgs []string) {
	passthrough := false
	keep := false
	for _, a := range args {
		if passthrough {
			sshArgs = append(sshArgs, a)
			continue
		}
		switch a {
		case "--":
			passthrough = true
		case "--keep", "-keep":
			keep = true
		default:
			sshArgs = append(sshArgs, a)
		}
	}
	if keep {
		warnKeepDeprecated(stderr)
	}
	return sshArgs
}

// warnKeepDeprecated prints the one deprecation notice for the --keep flag,
// which no longer changes anything: no entry path kills a vm on exit.
func warnKeepDeprecated(stderr io.Writer) {
	fmt.Fprintln(stderr, "--keep is obsolete — vms no longer die on exit")
}

// vmRun is cmdVM with injectable stdio and ssh binary (for the runner seam).
func vmRun(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	sshArgs := parseSSHArgs(stderr, args)

	creds, a := requireLogin()
	me, err := getMe(a)
	if err != nil {
		return err // 401 -> errSessionExpired
	}
	if err := ensurePubkey(a, me, stderr); err != nil {
		return err
	}

	// The ensure response carries no outcome word, so the listing decides
	// what the command tells the Customer: a row the door can answer —
	// running or asleep — means the ask hands over a machine that is
	// already theirs, found, not created. Dead rows answer nothing, so the
	// ask places one and the wording says so.
	rows, err := listVMs(a)
	if err != nil {
		return err
	}
	if hasAnswerableVM(rows) {
		fmt.Fprintln(stderr, "found your vm — entering it")
	} else {
		fmt.Fprintln(stderr, "creating vm …")
	}
	vm, err := ensureVM(a)
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "vm %s ready (expires in %s)\n", vm.SandboxID,
		time.Until(time.Unix(int64(vm.ExpiresAt), 0)).Round(time.Second))

	return enterVM(creds, vm, sshArgs, stdin, stdout, stderr)
}

func cmdVM(args []string) error {
	return vmRun(args, os.Stdin, os.Stdout, os.Stderr)
}
