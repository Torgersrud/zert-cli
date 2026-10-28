package main

import (
	"encoding/json"
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

func createVM(a api) (*vmRow, error) {
	resp, data, err := a.do("POST", "/v1/vm", map[string]any{})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusBadGateway {
		// one automatic retry per phase2.md. NOTE: POST /v1/vm is NOT
		// idempotent, so a 502 that actually reached the backend could create
		// a duplicate VM; requires a server-side idempotency key to fix.
		resp, data, err = a.do("POST", "/v1/vm", map[string]any{})
		if err != nil {
			return nil, err
		}
	}
	switch resp.StatusCode {
	case http.StatusCreated:
	case http.StatusConflict:
		fmt.Fprintln(os.Stderr, detail(data))
		fmt.Fprintln(os.Stderr, "your VMs:")
		if rows, err := listVMs(a); err == nil {
			printVMs(rows)
		}
		fmt.Fprintln(os.Stderr, "hint: zert attach <id> attaches to a live vm; "+
			"zert kill <id> (or zert kill all) frees a slot")
		return nil, &exitError{1, errors.New("quota reached")}
	default:
		return nil, mapped(resp, data)
	}
	var out struct {
		SandboxID string  `json:"sandbox_id"`
		ExpiresAt float64 `json:"expires_at"`
	}
	if err := json.Unmarshal(data, &out); err != nil || out.SandboxID == "" {
		return nil, errors.New("malformed create response")
	}
	if !validSandboxID(out.SandboxID) {
		return nil, errors.New("malformed server response: invalid vm id")
	}
	return &vmRow{SandboxID: out.SandboxID, ExpiresAt: out.ExpiresAt, Live: true}, nil
}

// parseSSHArgs returns the args forwarded verbatim to ssh — everything
// after "--" is passthrough — and swallows the deprecated --keep flag:
// when it was given, this one place prints its deprecation notice, so
// every entry path (vm, attach, upload) warns exactly once.
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
		return err // 401 -> "session expired — run `zert login`"
	}
	if err := ensurePubkey(a, me, stderr); err != nil {
		return err
	}

	fmt.Fprintln(stderr, "creating vm …")
	vm, err := createVM(a)
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
