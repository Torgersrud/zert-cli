package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"golang.org/x/term"
)

var sshPath = "ssh"
var scpPath = "scp"

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }

type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

type api struct {
	host  string
	token string
}

func (a api) do(method, path string, body any) (*http.Response, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, strings.TrimRight(a.host, "/")+path, rd)
	if err != nil {
		return nil, nil, err
	}
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	if a.token != "" {
		req.Header.Set("authorization", "Bearer "+a.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return resp, data, err
}

// detail extracts FastAPI's {"detail": "..."} message.
func detail(data []byte) string {
	var m map[string]any
	if json.Unmarshal(data, &m) == nil {
		if d, ok := m["detail"].(string); ok && d != "" {
			return d
		}
	}
	if s := strings.TrimSpace(string(data)); s != "" {
		return s
	}
	return "unexpected server response"
}

// mapped converts a non-2xx status into the CLI error vocabulary (phase2.md).
func mapped(resp *http.Response, data []byte) error {
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return &apiError{resp.StatusCode, "session expired — run `zert login`"}
	case http.StatusTooManyRequests:
		ra := resp.Header.Get("retry-after")
		if ra == "" {
			ra = "later"
		}
		return &apiError{resp.StatusCode, "too many requests, retry after " + ra}
	}
	return &apiError{resp.StatusCode, detail(data)}
}

var (
	promptLine = func(label string) (string, error) {
		fmt.Fprint(os.Stderr, label)
		var b []byte
		c := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(c)
			if n == 1 {
				if c[0] == '\n' {
					break
				}
				b = append(b, c[0])
				continue
			}
			if err != nil {
				return "", err
			}
		}
		return strings.TrimSpace(string(b)), nil
	}
	promptSecret = func(label string) (string, error) {
		fmt.Fprint(os.Stderr, label)
		b, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
)

func usage() {
	fmt.Fprint(os.Stderr, `zert — customer CLI

usage:
  zert login [--host URL] [--insecure]
                                 authenticate and store credentials
  zert logout                    delete stored credentials
  zert me                        show profile, quota, pubkey
  zert ls                        list your VMs
  zert vm [--keep] [ssh args..]  create a VM and ssh into it (kill on exit)
  zert kill <id>                 terminate a VM
  zert <path>                    copy a local dir/file into your vm
                                 (creates one if none is running)
                                 names may only use letters, digits and
                                 ._@%+=:,- — rename or archive the rest

host: --host flag > $ZERT_HOST > stored credentials
`)
}

func cmdLogin(args []string) error {
	fs := newFlagSet("login")
	host := fs.String("host", "", "server URL")
	insecure := fs.Bool("insecure", false, "allow plain http:// hosts (credentials sent unencrypted)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *host == "" {
		*host = os.Getenv("ZERT_HOST")
	}
	if *host == "" {
		if c, err := loadCredentials(); err == nil {
			*host = c.Host
		}
	}
	if *host == "" {
		return errors.New("no server: pass --host or set ZERT_HOST")
	}
	normalized, err := checkHostAllowInsecure(*host, *insecure)
	if err != nil {
		return err
	}
	if *insecure && strings.HasPrefix(normalized, "http://") {
		fmt.Fprintln(os.Stderr, "zert: WARNING: sending credentials over an unencrypted connection")
	}
	email, err := promptLine("email: ")
	if err != nil {
		return err
	}
	password, err := promptSecret("password: ")
	if err != nil {
		return err
	}
	resp, data, err := api{host: normalized}.do("POST", "/v1/login",
		map[string]string{"email": email, "password": password})
	if err != nil {
		return err
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return &apiError{resp.StatusCode, detail(data)}
	case http.StatusTooManyRequests:
		return &apiError{resp.StatusCode, "too many attempts, wait a minute"}
	default:
		return &apiError{resp.StatusCode, detail(data)}
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &out); err != nil || out.Token == "" {
		return errors.New("malformed login response")
	}
	if err := saveCredentials(&Credentials{Host: normalized, Email: email, Token: out.Token, Insecure: *insecure}); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "logged in as %s (%s)\n", email, normalized)
	return nil
}

func cmdLogout([]string) error {
	if err := deleteCredentials(); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "logged out (server tokens expire after 30 days)")
	return nil
}

func requireLogin() (*Credentials, api) {
	c, err := loadCredentials()
	if err != nil {
		fmt.Fprintln(os.Stderr, "zert: not logged in — run `zert login`")
		os.Exit(1)
	}
	host := c.Host
	if !c.Insecure {
		h, err := checkHost(c.Host)
		if err != nil {
			fmt.Fprintf(os.Stderr, "zert: stored host is not allowed (use https): %v\n", err)
			os.Exit(1)
		}
		host = h
	}
	return c, api{host: host, token: c.Token}
}

type meInfo struct {
	Email     string  `json:"email"`
	SSHPubkey *string `json:"ssh_pubkey"`
	Quota     int     `json:"quota"`
}

func getMe(a api) (*meInfo, error) {
	resp, data, err := a.do("GET", "/v1/me", nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, mapped(resp, data)
	}
	var me meInfo
	if err := json.Unmarshal(data, &me); err != nil {
		return nil, err
	}
	return &me, nil
}

func cmdMe([]string) error {
	_, a := requireLogin()
	me, err := getMe(a)
	if err != nil {
		return err
	}
	pub := "none"
	if me.SSHPubkey != nil && *me.SSHPubkey != "" {
		pub = *me.SSHPubkey
	}
	fmt.Printf("email:  %s\nquota:  %d\npubkey: %s\n", me.Email, me.Quota, pub)
	return nil
}

type vmRow struct {
	SandboxID string  `json:"sandbox_id"`
	ExpiresAt float64 `json:"expires_at"`
	Live      bool    `json:"live"`
	Creating  bool    `json:"creating"`
}

func listVMs(a api) ([]vmRow, error) {
	resp, data, err := a.do("GET", "/v1/vm", nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, mapped(resp, data)
	}
	var rows []vmRow
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, err
	}
	for _, r := range rows {
		if !validSandboxID(r.SandboxID) {
			return nil, errors.New("malformed server response: invalid vm id")
		}
	}
	return rows, nil
}

func expiryLabel(r vmRow) string {
	switch {
	case r.Creating:
		return "creating"
	case !r.Live:
		return "expired"
	default:
		return time.Until(time.Unix(int64(r.ExpiresAt), 0)).Round(time.Second).String() + " left"
	}
}

func printVMs(rows []vmRow) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tLIVE\tEXPIRES\tCREATING")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%v\t%s\t%v\n", r.SandboxID, r.Live, expiryLabel(r), r.Creating)
	}
	w.Flush()
}

func cmdLS([]string) error {
	_, a := requireLogin()
	rows, err := listVMs(a)
	if err != nil {
		return err
	}
	printVMs(rows)
	return nil
}

func cmdKill(args []string) error {
	if len(args) != 1 || args[0] == "" {
		return errors.New("usage: zert kill <id>")
	}
	if !validSandboxID(args[0]) {
		return errors.New("invalid vm id")
	}
	_, a := requireLogin()
	resp, data, err := a.do("DELETE", "/v1/vm/"+args[0], nil)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return mapped(resp, data)
	}
	fmt.Printf("killed %s\n", args[0])
	return nil
}

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
	resp, data, err := a.do("POST", "/v1/me/key", map[string]string{"pubkey": pub})
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return mapped(resp, data)
	}
	fmt.Fprintln(stderr, "uploaded ssh pubkey ("+strings.Fields(pub)[0]+" …)")
	return nil
}

// findLocalPubkey returns the customer's own ssh public key.
func findLocalPubkey() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	for _, name := range []string{"id_ed25519.pub", "id_rsa.pub"} {
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
		// one automatic retry per phase2.md
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

	self, err := os.Executable()
	if err != nil {
		self = "zert"
	}
	sshCmd := exec.Command(sshPath)
	sshCmd.Args = append(sshCmd.Args,
		"-o", proxyCommand(self, vm.SandboxID),
		"-o", "StrictHostKeyChecking=accept-new",
	)
	sshCmd.Args = append(sshCmd.Args, sshArgs...)
	sshCmd.Args = append(sshCmd.Args, "user@"+vm.SandboxID)
	sshCmd.Stdin, sshCmd.Stdout, sshCmd.Stderr = stdin, stdout, stderr
	// the tunnel child gets the token via environment, never argv
	sshCmd.Env = append(os.Environ(), "ZERT_TUNNEL_TOKEN="+creds.Token)

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
		sshCmd.Process.Signal(sig)
		waitErr = <-done
	}

	if !keep {
		fmt.Fprintln(stderr, "killing vm "+vm.SandboxID+" …")
		resp, data, err := a.do("DELETE", "/v1/vm/"+vm.SandboxID, nil)
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

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch cmd := os.Args[1]; cmd {
	case "login":
		err = cmdLogin(os.Args[2:])
	case "logout":
		err = cmdLogout(os.Args[2:])
	case "me":
		err = cmdMe(os.Args[2:])
	case "ls":
		err = cmdLS(os.Args[2:])
	case "vm":
		err = cmdVM(os.Args[2:])
	case "kill":
		err = cmdKill(os.Args[2:])
	case "tunnel": // hidden: used as ssh ProxyCommand
		err = cmdTunnel(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		if !looksLikePath(cmd) {
			fmt.Fprintf(os.Stderr, "zert: unknown command %q\n\n", cmd)
			usage()
			os.Exit(2)
		}
		err = cmdUpload(os.Args[1:])
	}
	if err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			if ee.err != nil {
				fmt.Fprintln(os.Stderr, "zert:", ee.err)
			}
			os.Exit(ee.code)
		}
		fmt.Fprintln(os.Stderr, "zert:", err)
		os.Exit(1)
	}
}
