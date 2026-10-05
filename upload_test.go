package main

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
