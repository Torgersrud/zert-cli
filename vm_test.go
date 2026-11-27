package main

import (
	"bytes"
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
	if !strings.Contains(line, "-t user@vm123 "+TmuxWrapCmd) || !strings.HasSuffix(line, TmuxWrapCmd) {
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
	if !strings.Contains(line, "-L 8080:localhost:80 user@vm123 "+TmuxWrapCmd) {
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

// The ensure contract — the twice-answered machine, the expired-row
// wording, the 502 retry shape, the refusals, the running-state and id
// guards — is the door module's and is pinned once in door_test.go, at
// askMachine's interface. This suite keeps what is the vm path's own:
// the entry behavior, the no-ssh spawn guards and the pubkey bootstrap.

// TestVMSleeperHearsTheWakeStory pins the entry path's rendering of the
// door's woken outcome: a sleeping machine is woken by the ask, and the
// Customer is told the waking — the pause before the entry opens — never
// "found", because the machine was asleep, not there to be found. The
// entry lands in the machine that was asleep.
func TestVMSleeperHearsTheWakeStory(t *testing.T) {
	fs := newFakeServer(t)
	fs.asleep = []string{"vms"}
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)
	var stderr bytes.Buffer

	if err := vmRun(nil, strings.NewReader(""), os.Stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if got := stderr.String(); !strings.Contains(got, "waking yours") || strings.Contains(got, "found your vm") {
		t.Fatalf("a sleeper must be told the wake, never found, got %q", got)
	}
	assertSSHCommonOptions(t, readLog(t, logPath), "vms")
	assertNoVMDeleted(t, fs, "an entry that wakes a sleeper")
}

func TestVMNoSpawnOnMaliciousID(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)
	fs.create = func(w http.ResponseWriter, _ int) {
		w.WriteHeader(201)
		io.WriteString(w, `{"sandbox_id":"x; touch /tmp/pwned","state":"running"}`)
	}
	if err := vmRun([]string{"--keep"}, strings.NewReader(""), os.Stdout, io.Discard); err == nil {
		t.Fatal("expected error from malicious id")
	}
	assertNoSSHSpawned(t, logPath, "an invalid vm id")
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
