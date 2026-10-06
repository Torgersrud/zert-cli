package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type fakeServer struct {
	*httptest.Server
	mu          sync.Mutex
	deleted     []string
	keyUploaded string
	pubkey      *string  // what /v1/me reports
	live        []string // sandbox ids reported as live by GET /v1/vm
	create      func(w http.ResponseWriter, tries int)
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	fs := &fakeServer{pubkey: strptr("ssh-ed25519 AAAAC3 serverkey bob")}
	var muCreateMu sync.Mutex
	creates := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/me", func(w http.ResponseWriter, r *http.Request) {
		fs.mu.Lock()
		pub := fs.pubkey
		fs.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{
			"email": "bob@example.com", "ssh_pubkey": pub, "quota": 1,
		})
	})
	mux.HandleFunc("/v1/me/key", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		fs.mu.Lock()
		fs.keyUploaded = body["pubkey"]
		fs.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
	mux.HandleFunc("/v1/vm", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			muCreateMu.Lock()
			creates++
			n := creates
			muCreateMu.Unlock()
			if fs.create != nil {
				fs.create(w, n)
				return
			}
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]any{
				"sandbox_id": "vm123", "expires_at": json.Number("9999999999"),
			})
			return
		}
		fs.mu.Lock()
		var rows []map[string]any
		for _, id := range fs.live {
			rows = append(rows, map[string]any{
				"sandbox_id": id, "expires_at": 9999999999.0, "live": true,
			})
		}
		fs.mu.Unlock()
		if rows == nil {
			rows = []map[string]any{}
		}
		json.NewEncoder(w).Encode(rows)
	})
	mux.HandleFunc("/v1/vm/", func(w http.ResponseWriter, r *http.Request) {
		fs.mu.Lock()
		fs.deleted = append(fs.deleted, strings.TrimPrefix(r.URL.Path, "/v1/vm/"))
		fs.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"killed": "vm123"})
	})
	fs.Server = httptest.NewServer(mux)
	t.Cleanup(fs.Close)
	return fs
}

func strptr(s string) *string { return &s }

// setupHome points HOME/XDG at temp dirs and stores credentials for the fake API.
func setupHome(t *testing.T, host string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if err := saveCredentials(&Credentials{Host: host, Email: "bob@example.com", Token: "tok-9"}); err != nil {
		t.Fatal(err)
	}
	return home
}

// fakeSSH records argv + tunnel token env, exits with code from FAKE_SSH_EXIT.
func fakeSSH(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ssh")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"ARGS $*\" >> \"$FAKE_SSH_LOG\"\n" +
		"printf '%s\\n' \"TOKEN $ZERT_TUNNEL_TOKEN\" >> \"$FAKE_SSH_LOG\"\n" +
		"cat > /dev/null\n" +
		"exit ${FAKE_SSH_EXIT:-0}\n"
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "ssh.log")
	t.Setenv("FAKE_SSH_LOG", logPath)
	t.Cleanup(func() { sshPath = "ssh" })
	sshPath = path
	return logPath
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestVMKillOnExit(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)

	if err := vmRun(nil, strings.NewReader(""), os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	fs.mu.Lock()
	deleted := fs.deleted
	fs.mu.Unlock()
	if len(deleted) != 1 || deleted[0] != "vm123" {
		t.Fatalf("expected DELETE vm123 once, got %v", deleted)
	}
	logged := readLog(t, logPath)
	if !strings.Contains(logged, "ARGS -o ProxyCommand=") || !strings.Contains(logged, "tunnel vm123") {
		t.Fatalf("ProxyCommand missing from ssh argv: %s", logged)
	}
	if !strings.Contains(logged, "user@vm123") {
		t.Fatalf("ssh destination missing: %s", logged)
	}
	if !strings.Contains(logged, "-o ServerAliveInterval=30") || !strings.Contains(logged, "-o ServerAliveCountMax=4") {
		t.Fatalf("ssh keepalive options missing: %s", logged)
	}
	if !strings.Contains(logged, "TOKEN tok-9") {
		t.Fatalf("token not passed via ZERT_TUNNEL_TOKEN env: %s", logged)
	}
	if strings.Contains(logged, "tok-9 ") && strings.Contains(logged, "TOKEN ") {
		// token must be in env line only, never in args
		for _, line := range strings.Split(logged, "\n") {
			if strings.HasPrefix(line, "ARGS") && strings.Contains(line, "tok-9") {
				t.Fatalf("token leaked into ssh argv: %s", line)
			}
		}
	}
}

func TestVMKeepSkipsDelete(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	fakeSSH(t)

	if err := vmRun([]string{"--keep"}, strings.NewReader(""), os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	fs.mu.Lock()
	deleted := fs.deleted
	fs.mu.Unlock()
	if len(deleted) != 0 {
		t.Fatalf("--keep must not DELETE, got %v", deleted)
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
	if _, err := os.Stat(logPath); err == nil {
		t.Fatal("ssh spawned despite invalid vm id")
	}
}

func TestVMQuota409ShowsList(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	fakeSSH(t)
	fs.create = func(w http.ResponseWriter, _ int) {
		http.Error(w, `{"detail":"quota reached (1 live VM)"}`, 409)
	}

	err := vmRun(nil, strings.NewReader(""), os.Stdout, os.Stderr)
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 1 {
		t.Fatalf("want exit 1, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "quota reached") {
		t.Fatalf("want quota-reached error, got %v", err)
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
