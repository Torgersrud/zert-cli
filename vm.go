package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var sshPath = "ssh"

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
		fmt.Fprintln(os.Stderr, "hint: zert kill <id>")
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

// childEnv returns base with exactly one ZERT_TUNNEL_TOKEN and one ZERT_HOST,
// set to token/host. Any pre-existing values are removed first (last wins).
func childEnv(base []string, token, host string) []string {
	out := make([]string, 0, len(base)+2)
	for _, kv := range base {
		if strings.HasPrefix(kv, "ZERT_TUNNEL_TOKEN=") || strings.HasPrefix(kv, "ZERT_HOST=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "ZERT_TUNNEL_TOKEN="+token, "ZERT_HOST="+host)
}

// vmRun is cmdVM with injectable stdio and ssh binary (for kill-on-exit tests).
func vmRun(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	keep := false
	var sshArgs []string
	passthrough := false
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

	return sshVM(creds, a, vm, sshArgs, keep, stdin, stdout, stderr)
}

// sshVM opens an interactive ssh session to the vm through the tunnel,
// forwarding signals and killing the vm on exit unless keep is set.
func sshVM(creds *Credentials, a api, vm *vmRow, sshArgs []string, keep bool, stdin io.Reader, stdout, stderr io.Writer) error {
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
	)
	sshCmd.Args = append(sshCmd.Args, sshArgs...)
	sshCmd.Args = append(sshCmd.Args, "user@"+vm.SandboxID)
	sshCmd.Stdin, sshCmd.Stdout, sshCmd.Stderr = stdin, stdout, stderr
	// the tunnel child gets the token via environment, never argv
	sshCmd.Env = childEnv(os.Environ(), creds.Token, creds.Host)

	sigc := make(chan os.Signal, 2)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigc)
	if err := sshCmd.Start(); err != nil {
		return fmt.Errorf("spawn ssh: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- sshCmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case sig := <-sigc:
		_ = sshCmd.Process.Signal(sig)
		waitErr = <-done
	}

	if !keep {
		fmt.Fprintln(stderr, "killing vm "+vm.SandboxID+" …")
		resp, data, err := a.do("DELETE", vmPath(vm.SandboxID), nil)
		if err != nil || resp.StatusCode != http.StatusOK {
			fmt.Fprintf(stderr, "zert: kill failed: %v %s\n", err, detail(data))
		}
	}

	var ee *exec.ExitError
	if errors.As(waitErr, &ee) {
		return &exitError{ee.ExitCode(), fmt.Errorf("ssh exited with %d", ee.ExitCode())}
	}
	return waitErr
}

func cmdVM(args []string) error {
	return vmRun(args, os.Stdin, os.Stdout, os.Stderr)
}
