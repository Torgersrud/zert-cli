package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeSCP records argv + tunnel token env, exits with code from FAKE_SCP_EXIT.
func fakeSCP(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-scp")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"ARGS $*\" >> \"$FAKE_SCP_LOG\"\n" +
		"printf '%s\\n' \"TOKEN $ZERT_TUNNEL_TOKEN\" >> \"$FAKE_SCP_LOG\"\n" +
		"exit ${FAKE_SCP_EXIT:-0}\n"
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "scp.log")
	t.Setenv("FAKE_SCP_LOG", logPath)
	t.Cleanup(func() { scpPath = "scp" })
	scpPath = path
	return logPath
}

func tempDirNamed(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func assertNoTokenInArgs(t *testing.T, logged string) {
	t.Helper()
	for _, line := range strings.Split(logged, "\n") {
		if strings.HasPrefix(line, "ARGS") && strings.Contains(line, "tok-9") {
			t.Fatalf("token leaked into scp argv: %s", line)
		}
	}
}

func TestUploadCreatesVMWhenNoneLive(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSCP(t)
	dir := tempDirNamed(t, "mydir")

	if err := uploadRun(dir, os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	logged := readLog(t, logPath)
	if !strings.Contains(logged, "ARGS -r") || !strings.Contains(logged, "tunnel vm123") {
		t.Fatalf("ProxyCommand missing from scp argv: %s", logged)
	}
	if !strings.Contains(logged, "user@vm123:~/mydir") {
		t.Fatalf("scp destination wrong: %s", logged)
	}
	if !strings.Contains(logged, "TOKEN tok-9") {
		t.Fatalf("token not passed via ZERT_TUNNEL_TOKEN env: %s", logged)
	}
	assertNoTokenInArgs(t, logged)
	fs.mu.Lock()
	deleted := fs.deleted
	fs.mu.Unlock()
	if len(deleted) != 0 {
		t.Fatalf("upload must not kill the vm, got %v", deleted)
	}
}

func TestUploadUsesLiveVM(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSCP(t)
	fs.live = []string{"vmlive"}
	fs.create = func(w http.ResponseWriter, _ int) {
		t.Error("unexpected POST /v1/vm while a vm is live")
		http.Error(w, `{"detail":"no"}`, 500)
	}
	dir := tempDirNamed(t, "proj")

	if err := uploadRun(dir+"/", os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	logged := readLog(t, logPath)
	if !strings.Contains(logged, "tunnel vmlive") || !strings.Contains(logged, "user@vmlive:~/proj") {
		t.Fatalf("live vm not used or trailing slash mishandled: %s", logged)
	}
}

func TestUploadPubkeyBootstrap(t *testing.T) {
	fs := newFakeServer(t)
	home := setupHome(t, fs.URL)
	fs.mu.Lock()
	fs.pubkey = nil
	fs.mu.Unlock()
	fakeSCP(t)

	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		t.Fatal(err)
	}
	pub := "ssh-ed25519 AAAATESTKEY bob@laptop"
	if err := os.WriteFile(filepath.Join(sshDir, "id_ed25519.pub"), []byte(pub+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := uploadRun(tempDirNamed(t, "d"), os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	fs.mu.Lock()
	uploaded := fs.keyUploaded
	fs.mu.Unlock()
	if uploaded != pub {
		t.Fatalf("pubkey not uploaded: %q", uploaded)
	}
}

func TestUploadScpFailureExitCode(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	fakeSCP(t)
	t.Setenv("FAKE_SCP_EXIT", "1")

	err := uploadRun(tempDirNamed(t, "d"), os.Stdout, os.Stderr)
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 1 {
		t.Fatalf("want exit 1, got %v", err)
	}
}

func TestLooksLikePath(t *testing.T) {
	dir := tempDirNamed(t, "mydir")
	for _, ok := range []string{dir, dir + "/", "./upload_test.go", "upload_test.go"} {
		if !looksLikePath(ok) {
			t.Fatalf("%s should look like a path", ok)
		}
	}
	for _, no := range []string{"", "vm", "ls", "./nope", "user@host:~/x", "~/definitely-not-here-xyz"} {
		if looksLikePath(no) {
			t.Fatalf("%s should not look like a path", no)
		}
	}
}

func TestUploadDoubleDash(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSCP(t)
	dir := tempDirNamed(t, "mydir")

	if err := uploadRun(dir, os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	logged := readLog(t, logPath)
	if !strings.Contains(logged, "-- "+dir+" user@vm123:~/mydir") {
		t.Fatalf("-- must immediately precede source: %s", logged)
	}
}

func TestUploadRelativeDashPrefixed(t *testing.T) {
	cases := map[string]string{
		"-oProxyCommand=x": "./-oProxyCommand=x",
		"a:b":              "./a:b",
		"./keep":           "./keep",
		"../up":            "../up",
	}
	for in, want := range cases {
		if got := safeScpSource(in); got != want {
			t.Fatalf("safeScpSource(%q) = %q, want %q", in, got, want)
		}
	}
	abs := filepath.Join(t.TempDir(), "x")
	if got := safeScpSource(abs); got != abs {
		t.Fatalf("absolute path must be unchanged, got %q", got)
	}
}

func TestUploadDashFileArrivesPrefixed(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSCP(t)

	if err := uploadRun("-oProxyCommand=x", os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	logged := readLog(t, logPath)
	if !strings.Contains(logged, "-- ./-oProxyCommand=x") {
		t.Fatalf("dash-prefixed name not neutralized: %s", logged)
	}
}

func TestUploadColonFileArrivesPrefixed(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSCP(t)

	if err := uploadRun("a:b", os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	logged := readLog(t, logPath)
	if !strings.Contains(logged, "-- ./a:b") {
		t.Fatalf("colon name must arrive prefixed: %s", logged)
	}
}

func TestUploadMetacharNameRefused(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSCP(t)

	err := uploadRun("a;b", os.Stdout, os.Stderr)
	if err == nil {
		t.Fatal("expected error for shell metacharacter in name")
	}
	if !strings.Contains(err.Error(), "rename") || !strings.Contains(err.Error(), "archive") {
		t.Fatalf("error should mention rename/archive, got: %v", err)
	}
	if _, statErr := os.Stat(logPath); statErr == nil {
		t.Fatal("scp spawned despite bad file name")
	}
	fs.mu.Lock()
	deleted := fs.deleted
	fs.mu.Unlock()
	if len(deleted) != 0 {
		t.Fatalf("no vm operations expected, got deletes %v", deleted)
	}
}

func TestUploadBadNameNoNetwork(t *testing.T) {
	var hits int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, `{}`, http.StatusInternalServerError)
	}))
	defer ts.Close()
	setupHome(t, ts.URL)
	fakeSCP(t)

	if err := uploadRun("a;b", os.Stdout, os.Stderr); err == nil {
		t.Fatal("expected error for bad file name")
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("bad name hit the network %d times", n)
	}
}

func TestUploadRefusesRoot(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSCP(t)
	fs.create = func(w http.ResponseWriter, _ int) {
		t.Error("unexpected POST /v1/vm for refused path")
		http.Error(w, `{"detail":"no"}`, 500)
	}

	err := uploadRun("/", os.Stdout, os.Stderr)
	if err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("expected root refusal, got %v", err)
	}
	if _, statErr := os.Stat(logPath); statErr == nil {
		t.Fatal("scp spawned despite root upload")
	}
	fs.mu.Lock()
	deleted := fs.deleted
	fs.mu.Unlock()
	if len(deleted) != 0 {
		t.Fatalf("no vm operations expected, got deletes %v", deleted)
	}
}

func TestUploadRefusesHome(t *testing.T) {
	fs := newFakeServer(t)
	home := setupHome(t, fs.URL)
	logPath := fakeSCP(t)

	err := uploadRun(home, os.Stdout, os.Stderr)
	if err == nil || !strings.Contains(err.Error(), "home directory") {
		t.Fatalf("expected home refusal, got %v", err)
	}
	if _, statErr := os.Stat(logPath); statErr == nil {
		t.Fatal("scp spawned despite home upload")
	}
}

func TestUploadTempDirPassesThrough(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSCP(t)
	dir := tempDirNamed(t, "sub")

	if err := uploadRun(dir, os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	logged := readLog(t, logPath)
	if !strings.Contains(logged, "-- ") || !strings.Contains(logged, "user@vm123:~/sub") {
		t.Fatalf("temp dir not passed through to scp: %s", logged)
	}
}
