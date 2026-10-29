package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The API fake, the fake-ssh generator, the tmux wrap literal and the shared
// Entry-path assertions this suite uses live in helpers_test.go.

func TestVMExitNeverKills(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)

	if err := vmRun(nil, strings.NewReader(""), os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	assertNoVMDeleted(t, fs, "leaving an entered vm")
	logged := readLog(t, logPath)
	assertSSHCommonOptions(t, logged, "vm123")
	assertTokenInChildEnvOnly(t, logged)
}

func TestVMKeepWarnsObsoleteAndNeverKills(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	fakeSSH(t)
	var stderr bytes.Buffer

	if err := vmRun([]string{"--keep"}, strings.NewReader(""), os.Stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(stderr.String(), "obsolete"); n != 1 {
		t.Fatalf("want exactly one --keep deprecation notice on stderr, got %d in %q", n, stderr.String())
	}
	if !strings.Contains(stderr.String(), "no longer die on exit") {
		t.Fatalf("notice must say vms no longer die on exit, got %q", stderr.String())
	}
	assertNoVMDeleted(t, fs, "--keep")
}

func TestVMExitCodeNeverKills(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	fakeSSH(t)
	t.Setenv("FAKE_SSH_EXIT", "255") // Ctrl-C / dropped link: ssh dies badly

	err := vmRun(nil, strings.NewReader(""), os.Stdout, io.Discard)
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 255 {
		t.Fatalf("want exit 255 propagated, got %v", err)
	}
	assertNoVMDeleted(t, fs, "ssh exiting 255")
}

func TestVMInteractiveEntryRunsTmuxWrap(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)
	fakeTTY(t)

	if err := vmRun(nil, strings.NewReader(""), os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	lines := argLines(readLog(t, logPath))
	if len(lines) != 1 {
		t.Fatalf("want exactly one ssh call, got %d", len(lines))
	}
	line := lines[0]
	// the wrap is the remote command — it follows the destination — and it
	// comes with -t, because ssh only gives an explicit command a tty once
	// a command is present, and tmux needs one
	if !strings.Contains(line, "-t user@vm123 "+tmuxWrapCmd) || !strings.HasSuffix(line, tmuxWrapCmd) {
		t.Fatalf("tmux wrap not the remote command of the interactive ssh: %s", line)
	}
}

func TestVMSSHArgsForwarded(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)

	err := vmRun([]string{"-L", "8080:localhost:80", "--keep"}, strings.NewReader(""), os.Stdout, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	logged := readLog(t, logPath)
	if !strings.Contains(logged, "ARGS -o ProxyCommand=") ||
		!strings.Contains(logged, "-L 8080:localhost:80 user@vm123") {
		t.Fatalf("user ssh args not forwarded before destination: %s", logged)
	}
}

func TestVMUserRemoteCommandStaysUnwrapped(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)

	err := vmRun([]string{"--", "uptime"}, strings.NewReader(""), os.Stdout, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	line := argLines(readLog(t, logPath))[0]
	// a user-supplied remote command is passed through exactly as today:
	// verbatim in the args, and nothing — no tmux wrap — after the
	// destination
	if strings.Contains(line, "tmux") || !strings.HasSuffix(line, "uptime user@vm123") {
		t.Fatalf("user remote command was wrapped: %s", line)
	}
}

func TestVMSSHOptionArgsStillWrap(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)
	fakeTTY(t)

	err := vmRun([]string{"-L", "8080:localhost:80"}, strings.NewReader(""), os.Stdout, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	line := argLines(readLog(t, logPath))[0]
	// an option's value is not a remote command: port forwarding with no
	// command still lands in the tmux session
	if !strings.Contains(line, "-L 8080:localhost:80 user@vm123 "+tmuxWrapCmd) {
		t.Fatalf("option-only ssh args lost the tmux wrap: %s", line)
	}
}

func TestVMPipedStdinNeverWraps(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)
	t.Cleanup(func() { stdinIsInteractive = defaultStdinIsInteractive })
	stdinIsInteractive = func() bool { return false } // `echo cmd | zert vm`

	if err := vmRun(nil, strings.NewReader("echo hi\n"), os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	line := argLines(readLog(t, logPath))[0]
	// only an interactive entry runs tmux: ssh hands a piped session no
	// pty, so this stays the plain ssh of before the wrap existed — no
	// tmux, no forced -t, nothing after the destination
	if strings.Contains(line, "tmux") || strings.Contains(line, " -t ") ||
		!strings.HasSuffix(line, "user@vm123") {
		t.Fatalf("piped stdin was wrapped in tmux: %s", line)
	}
}

func TestVMPubkeyBootstrap(t *testing.T) {
	fs := newFakeServer(t)
	home := setupHome(t, fs.URL)
	fs.mu.Lock()
	fs.pubkey = nil
	fs.mu.Unlock()
	fakeSSH(t)

	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		t.Fatal(err)
	}
	pub := "ssh-ed25519 AAAATESTKEY bob@laptop"
	if err := os.WriteFile(filepath.Join(sshDir, "id_ed25519.pub"), []byte(pub+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := vmRun([]string{"--keep"}, strings.NewReader(""), os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	fs.mu.Lock()
	uploaded := fs.keyUploaded
	fs.mu.Unlock()
	if uploaded != pub {
		t.Fatalf("pubkey not uploaded: %q", uploaded)
	}
}

func TestVMTokenExpiredNoCreate(t *testing.T) {
	var created bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			created = true
		}
		http.Error(w, `{"detail":"invalid or expired token"}`, 401)
	}))
	defer ts.Close()
	setupHome(t, ts.URL)

	err := vmRun([]string{"--keep"}, strings.NewReader(""), os.Stdout, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "run `zert login`") {
		t.Fatalf("want run-zert-login error, got %v", err)
	}
	if created {
		t.Fatal("VM created despite 401")
	}
}

func TestVM502RetriesOnce(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	fakeSSH(t)
	fs.create = func(w http.ResponseWriter, tries int) {
		if tries == 1 {
			http.Error(w, `{"detail":"failed to bootstrap VM, try again"}`, 502)
			return
		}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"sandbox_id": "vm123", "expires_at": json.Number("9999999999")})
	}

	if err := vmRun([]string{"--keep"}, strings.NewReader(""), os.Stdout, os.Stderr); err != nil {
		t.Fatalf("502 retry should succeed, got %v", err)
	}
}

func TestCreateVMRejectsMaliciousID(t *testing.T) {
	fs := newFakeServer(t)
	fs.create = func(w http.ResponseWriter, _ int) {
		w.WriteHeader(201)
		io.WriteString(w, `{"sandbox_id":"x; touch /tmp/pwned","expires_at":9999999999}`)
	}
	_, err := createVM(api{host: fs.URL})
	if err == nil || !strings.Contains(err.Error(), "invalid vm id") {
		t.Fatalf("want invalid-vm-id error, got %v", err)
	}
	if _, statErr := os.Stat("/tmp/pwned"); statErr == nil {
		t.Fatal("injection executed: /tmp/pwned exists")
	}
}

func TestVMNoSpawnOnMaliciousID(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)
	fs.create = func(w http.ResponseWriter, _ int) {
		w.WriteHeader(201)
		io.WriteString(w, `{"sandbox_id":"x; touch /tmp/pwned"}`)
	}
	if err := vmRun([]string{"--keep"}, strings.NewReader(""), os.Stdout, io.Discard); err == nil {
		t.Fatal("expected error from malicious id")
	}
	assertNoSSHSpawned(t, logPath, "an invalid vm id")
}

func TestVMQuota409ShowsList(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	fakeSSH(t)
	fs.create = func(w http.ResponseWriter, _ int) {
		http.Error(w, `{"detail":"quota reached (1 live VM)"}`, 409)
	}

	// the quota listing and its hint print straight to os.Stderr
	var err error
	hint := captureStderr(t, func() {
		err = vmRun(nil, strings.NewReader(""), os.Stdout, os.Stderr)
	})
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 1 {
		t.Fatalf("want exit 1, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "quota reached") {
		t.Fatalf("want quota-reached error, got %v", err)
	}
	if !strings.Contains(hint, "zert attach <id>") {
		t.Fatalf("quota-409 hint must offer zert attach, got %q", hint)
	}
	if !strings.Contains(hint, "zert kill <id>") {
		t.Fatalf("quota-409 hint must still offer zert kill, got %q", hint)
	}
	// the product glossary bans "re-" coinages for Attach (nothing is being
	// re- — the vm never left), so the hint must use attacher wording
	if strings.Contains(hint, "re-enter") {
		t.Fatalf("quota-409 hint uses a banned re- word, got %q", hint)
	}
	if !strings.Contains(hint, "attaches to a live vm") {
		t.Fatalf("quota-409 hint must say attach attaches to a live vm, got %q", hint)
	}
}

func countPrefix(entries []string, prefix string) []string {
	var got []string
	for _, kv := range entries {
		if strings.HasPrefix(kv, prefix) {
			got = append(got, kv)
		}
	}
	return got
}

func TestChildEnv(t *testing.T) {
	base := []string{"PATH=/bin", "ZERT_HOST=http://old", "ZERT_TUNNEL_TOKEN=old", "OTHER=1"}
	got := childEnv(base, "tok", "https://new")

	if h := countPrefix(got, "ZERT_HOST="); len(h) != 1 || h[0] != "ZERT_HOST=https://new" {
		t.Fatalf("ZERT_HOST entries = %v, want exactly [ZERT_HOST=https://new]", h)
	}
	if tk := countPrefix(got, "ZERT_TUNNEL_TOKEN="); len(tk) != 1 || tk[0] != "ZERT_TUNNEL_TOKEN=tok" {
		t.Fatalf("ZERT_TUNNEL_TOKEN entries = %v, want exactly [ZERT_TUNNEL_TOKEN=tok]", tk)
	}
	for _, kv := range got {
		if kv == "ZERT_HOST=http://old" || kv == "ZERT_TUNNEL_TOKEN=old" {
			t.Fatalf("stale value not removed: %s", kv)
		}
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "PATH=/bin") || !strings.Contains(joined, "OTHER=1") {
		t.Fatalf("unrelated entries dropped: %v", got)
	}
}

func TestEnsurePubkeyEmptyKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "id_ed25519.pub"), []byte("   \n"), 0644); err != nil {
		t.Fatal(err)
	}
	err := ensurePubkey(api{}, &meInfo{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("want empty-key error, got %v", err)
	}
}

func TestEnsurePubkeyRejectsNonKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "id_ed25519.pub"), []byte("garbage data\n"), 0644); err != nil {
		t.Fatal(err)
	}
	err := ensurePubkey(api{}, &meInfo{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "not an ssh public key") {
		t.Fatalf("want not-an-ssh-key error, got %v", err)
	}
}
