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
		json.NewEncoder(w).Encode(map[string]any{"sandbox_id": "vm123", "expires_at": json.Number("9999999999"), "state": "running"})
	}

	if err := vmRun([]string{"--keep"}, strings.NewReader(""), os.Stdout, os.Stderr); err != nil {
		t.Fatalf("502 retry should succeed, got %v", err)
	}
}

// TestEnsureTwiceAnswersSameMachine pins the ticket's headline: POST /v1/vm
// is the ensure ask, so a second ask must answer the machine the first one
// placed — never place a second — and the command's wording must tell the
// Customer which of the two happened. The fake server places a machine only
// when the account has none, and records every placement, so "the second
// call placed nothing" is read off fs.placed.
func TestEnsureTwiceAnswersSameMachine(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)

	var first, second bytes.Buffer
	if err := vmRun(nil, strings.NewReader(""), os.Stdout, &first); err != nil {
		t.Fatal(err)
	}
	if err := vmRun(nil, strings.NewReader(""), os.Stdout, &second); err != nil {
		t.Fatal(err)
	}

	lines := argLines(readLog(t, logPath))
	if len(lines) != 2 {
		t.Fatalf("want one ssh call per ask, got %d: %s", len(lines), lines)
	}
	for _, line := range lines {
		if !strings.Contains(line, "user@vm123") {
			t.Fatalf("both asks must answer the same machine, got %q", line)
		}
	}
	asks, placed := fs.ensureCounts()
	if len(placed) != 1 || placed[0] != "vm123" {
		t.Fatalf("the second ask must place nothing, placements: %v", placed)
	}
	if asks != 2 {
		t.Fatalf("each ask must reach the door, want 2 POSTs, got %d", asks)
	}
	// the ensure response carries no outcome word, so the listing pre-check
	// decides what the command reports
	if !strings.Contains(first.String(), "creating vm") || strings.Contains(first.String(), "found your vm") {
		t.Fatalf("a customer with nothing must be told a machine was created: %q", first.String())
	}
	if !strings.Contains(second.String(), "found your vm") || strings.Contains(second.String(), "creating vm") {
		t.Fatalf("a customer with a machine must be told it was found: %q", second.String())
	}
}

// TestEnsure502RetryPlacesOneMachine routes the ticket's headline through
// the retry itself: an ask that dies on the door's far side (502) places
// nothing, the retry places the one machine, and a later ask answers that
// same machine without placing anything — the retried bad-gateway answer
// returns the Customer's one machine, never a second.
func TestEnsure502RetryPlacesOneMachine(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)
	fs.create = func(w http.ResponseWriter, tries int) {
		fs.mu.Lock()
		placed := len(fs.placed) > 0
		fs.mu.Unlock()
		if !placed {
			if tries == 1 {
				// the ask reached the door and died past it: the
				// bootstrap failed, so nothing was placed
				http.Error(w, `{"detail":"failed to bootstrap VM, try again"}`, 502)
				return
			}
			fs.mu.Lock()
			fs.placed = append(fs.placed, "vm123")
			fs.mu.Unlock()
		}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{
			"sandbox_id": "vm123", "expires_at": json.Number("9999999999"),
			"state": "running",
		})
	}

	if err := vmRun(nil, strings.NewReader(""), os.Stdout, io.Discard); err != nil {
		t.Fatalf("502 retry should succeed, got %v", err)
	}
	if err := vmRun(nil, strings.NewReader(""), os.Stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	asks, placed := fs.ensureCounts()
	if len(placed) != 1 || placed[0] != "vm123" {
		t.Fatalf("retry places the one machine, later asks place nothing: %v", placed)
	}
	if asks != 3 {
		t.Fatalf("want 3 asks (502, retry, later ask), got %d", asks)
	}
	assertSSHCommonOptions(t, readLog(t, logPath), "vm123")
}

// TestEnsureRefusalNotRetried pins that a refusal is final: the ensure
// refusals (400 no/invalid pubkey, 429 rate-limited, 503 wake/arm failed)
// place nothing and are never retried — only the bad gateway asks again.
func TestEnsureRefusalNotRetried(t *testing.T) {
	for _, code := range []int{400, 429, 503} {
		fs := newFakeServer(t)
		setupHome(t, fs.URL)
		logPath := fakeSSH(t)
		fs.create = func(w http.ResponseWriter, _ int) {
			http.Error(w, `{"detail":"refused"}`, code)
		}

		err := vmRun(nil, strings.NewReader(""), os.Stdout, io.Discard)
		if err == nil {
			t.Fatalf("%d: want the refusal as an error", code)
		}
		asks, placed := fs.ensureCounts()
		if asks != 1 {
			t.Fatalf("%d: a refusal must not be retried, got %d asks", code, asks)
		}
		if len(placed) != 0 {
			t.Fatalf("%d: a refusal places nothing, got %v", code, placed)
		}
		assertNoSSHSpawned(t, logPath, "a refused ensure")
	}
}

// TestEnsure502RetriedOnceOnly pins the retry's shape: the 502 keeps its
// trigger and its single attempt — a door that answers 502 twice gets two
// asks, never a third.
func TestEnsure502RetriedOnceOnly(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	fs.create = func(w http.ResponseWriter, _ int) {
		http.Error(w, `{"detail":"failed to bootstrap VM, try again"}`, 502)
	}

	err := vmRun(nil, strings.NewReader(""), os.Stdout, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "bootstrap") {
		t.Fatalf("want the 502 word after one retry, got %v", err)
	}
	asks, _ := fs.ensureCounts()
	if asks != 2 {
		t.Fatalf("the retry is one attempt: want 2 asks, got %d", asks)
	}
}

// TestEnsureRequiresRunningState pins the response contract's one promise
// about the machine: every 201 hands over a running machine, whatever
// outcome got it there. A door that answers any other state word is not
// obeying the ensure contract, and the CLI refuses to enter what it said.
func TestEnsureRequiresRunningState(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)
	fs.create = func(w http.ResponseWriter, _ int) {
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{
			"sandbox_id": "vm123", "expires_at": json.Number("9999999999"),
			"state": "asleep",
		})
	}

	err := vmRun(nil, strings.NewReader(""), os.Stdout, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("want a not-running refusal, got %v", err)
	}
	assertNoSSHSpawned(t, logPath, "a machine not handed over running")
}

func TestEnsureRejectsMaliciousID(t *testing.T) {
	fs := newFakeServer(t)
	fs.create = func(w http.ResponseWriter, _ int) {
		w.WriteHeader(201)
		io.WriteString(w, `{"sandbox_id":"x; touch /tmp/pwned","expires_at":9999999999,"state":"running"}`)
	}
	_, err := ensureVM(api{host: fs.URL})
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
		io.WriteString(w, `{"sandbox_id":"x; touch /tmp/pwned","state":"running"}`)
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
	// the 409 backstop is a refusal: one ask, no retry
	asks, _ := fs.ensureCounts()
	if asks != 1 {
		t.Fatalf("a 409 refusal must not be retried, got %d asks", asks)
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
