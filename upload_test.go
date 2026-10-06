package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeSSHRecordingStdin is fakeSSH plus it stores ssh's full stdin (the
// tar.gz stream) at the returned path.
func fakeSSHRecordingStdin(t *testing.T) (logPath, stdinPath string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ssh")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"ARGS $*\" >> \"$FAKE_SSH_LOG\"\n" +
		"printf '%s\\n' \"TOKEN $ZERT_TUNNEL_TOKEN\" >> \"$FAKE_SSH_LOG\"\n" +
		"cat > \"$FAKE_SSH_STDIN\"\n" +
		"exit ${FAKE_SSH_EXIT:-0}\n"
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	logPath = filepath.Join(t.TempDir(), "ssh.log")
	stdinPath = filepath.Join(t.TempDir(), "stdin.tar.gz")
	t.Setenv("FAKE_SSH_LOG", logPath)
	t.Setenv("FAKE_SSH_STDIN", stdinPath)
	t.Cleanup(func() { sshPath = "ssh" })
	sshPath = path
	return logPath, stdinPath
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
			t.Fatalf("token leaked into ssh argv: %s", line)
		}
	}
}

func argLines(logged string) []string {
	var out []string
	for _, line := range strings.Split(logged, "\n") {
		if strings.HasPrefix(line, "ARGS ") {
			out = append(out, line)
		}
	}
	return out
}

// untarGz decodes a recorded upload stream into name -> entry maps.
func untarGz(t *testing.T, path string) map[string]tar.Header {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	tr := tar.NewReader(zr)
	out := map[string]tar.Header{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		out[h.Name] = *h
	}
	return out
}

func TestUploadCreatesVMWhenNoneLive(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)
	dir := tempDirNamed(t, "mydir")

	if err := uploadRun(dir, strings.NewReader(""), os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	logged := readLog(t, logPath)
	if !strings.Contains(logged, "tunnel vm123") {
		t.Fatalf("ProxyCommand missing from ssh argv: %s", logged)
	}
	if !strings.Contains(logged, "user@vm123") || !strings.Contains(logged, uploadRemoteCmd) {
		t.Fatalf("ssh destination or remote tar command missing: %s", logged)
	}
	if !strings.Contains(logged, "-o ServerAliveInterval=30") || !strings.Contains(logged, "-o ServerAliveCountMax=4") {
		t.Fatalf("ssh keepalive options missing: %s", logged)
	}
	if !strings.Contains(logged, "TOKEN tok-9") {
		t.Fatalf("token not passed via ZERT_TUNNEL_TOKEN env: %s", logged)
	}
	assertNoTokenInArgs(t, logged)
	for _, line := range argLines(logged) {
		if strings.Contains(line, dir) {
			t.Fatalf("local path leaked into ssh argv: %s", line)
		}
	}
	fs.mu.Lock()
	deleted := fs.deleted
	fs.mu.Unlock()
	if len(deleted) != 1 || deleted[0] != "vm123" {
		t.Fatalf("created vm must be killed on exit, got deletes %v", deleted)
	}
}

func TestUploadCreatedVmAutoSshes(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)
	dir := tempDirNamed(t, "mydir")

	if err := uploadRun(dir, strings.NewReader(""), os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	logged := readLog(t, logPath)
	if !strings.Contains(logged, "TOKEN tok-9") {
		t.Fatalf("token not passed via ZERT_TUNNEL_TOKEN env: %s", logged)
	}
	// the first ssh exec carries the tar, the auto-ssh one must not
	var plain bool
	for _, line := range argLines(logged) {
		if !strings.Contains(line, "tar -xzf") {
			plain = true
		}
	}
	if !plain {
		t.Fatalf("no auto-ssh (interactive) session after upload: %s", logged)
	}
	fs.mu.Lock()
	deleted := fs.deleted
	fs.mu.Unlock()
	if len(deleted) != 1 || deleted[0] != "vm123" {
		t.Fatalf("created vm must be killed after auto-ssh exit, got deletes %v", deleted)
	}
}

func TestUploadUsesLiveVM(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)
	fs.live = []string{"vmlive"}
	fs.create = func(w http.ResponseWriter, _ int) {
		t.Error("unexpected POST /v1/vm while a vm is live")
		http.Error(w, `{"detail":"no"}`, 500)
	}
	dir := tempDirNamed(t, "proj")

	if err := uploadRun(dir+"/", strings.NewReader(""), os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	logged := readLog(t, logPath)
	if !strings.Contains(logged, "tunnel vmlive") || !strings.Contains(logged, "user@vmlive") {
		t.Fatalf("live vm not used or trailing slash mishandled: %s", logged)
	}
	if n := len(argLines(logged)); n != 1 {
		t.Fatalf("must not auto-ssh when reusing a live vm, got %d ssh calls: %s", n, logged)
	}
}

func TestUploadPubkeyBootstrap(t *testing.T) {
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

	if err := uploadRun(tempDirNamed(t, "d"), strings.NewReader(""), os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	fs.mu.Lock()
	uploaded := fs.keyUploaded
	fs.mu.Unlock()
	if uploaded != pub {
		t.Fatalf("pubkey not uploaded: %q", uploaded)
	}
}

func TestUploadSSHFailureExitCode(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	fakeSSH(t)
	t.Setenv("FAKE_SSH_EXIT", "1")

	err := uploadRun(tempDirNamed(t, "d"), strings.NewReader(""), os.Stdout, os.Stderr)
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 1 {
		t.Fatalf("want exit 1, got %v", err)
	}
}

func TestUploadTarStreamContents(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	_, stdinPath := fakeSSHRecordingStdin(t)
	fs.live = []string{"vmlive"}

	dir := tempDirNamed(t, "mydir")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "exec.sh"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("world"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "emptydir"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nowhere", filepath.Join(dir, "dangling")); err != nil {
		t.Fatal(err)
	}

	if err := uploadRun(dir, strings.NewReader(""), os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	entries := untarGz(t, stdinPath)
	for _, name := range []string{"mydir/", "mydir/a.txt", "mydir/exec.sh", "mydir/sub/", "mydir/sub/b.txt", "mydir/emptydir/", "mydir/link.txt"} {
		if _, ok := entries[name]; !ok {
			t.Fatalf("tar stream missing %q (got %v)", name, keys(entries))
		}
	}
	if _, ok := entries["mydir/dangling"]; ok {
		t.Fatal("broken symlink must not appear in the tar stream")
	}
	// symlink was dereferenced: a regular file with the target's content
	if h := entries["mydir/link.txt"]; h.Typeflag != tar.TypeReg || h.Size != 5 {
		t.Fatalf("link.txt not dereferenced: type=%c size=%d", h.Typeflag, h.Size)
	}
	if h := entries["mydir/exec.sh"]; h.FileInfo().Mode()&0111 == 0 {
		t.Fatalf("exec bit lost: mode=%v", h.FileInfo().Mode())
	}
	if h := entries["mydir/sub/b.txt"]; h.Size != 5 {
		t.Fatalf("sub/b.txt size = %d, want 5", h.Size)
	}
}

func TestUploadSingleFile(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	_, stdinPath := fakeSSHRecordingStdin(t)
	fs.live = []string{"vmlive"}

	path := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(path, []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := uploadRun(path, strings.NewReader(""), os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	entries := untarGz(t, stdinPath)
	h, ok := entries["note.txt"]
	if !ok || h.Typeflag != tar.TypeReg || h.Size != 2 {
		t.Fatalf("single-file upload wrong: %v %v", ok, entries)
	}
}

func TestUploadDotNamesRefused(t *testing.T) {
	for _, arg := range []string{".", "..", "./", "../x/.."} {
		err := uploadRun(arg, strings.NewReader(""), os.Stdout, os.Stderr)
		if err == nil || !strings.Contains(err.Error(), "specific") {
			t.Fatalf("%q must be refused with a clear message, got %v", arg, err)
		}
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

func TestUploadMetacharNameRefused(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)

	err := uploadRun("a;b", strings.NewReader(""), os.Stdout, os.Stderr)
	if err == nil {
		t.Fatal("expected error for shell metacharacter in name")
	}
	if !strings.Contains(err.Error(), "rename") || !strings.Contains(err.Error(), "archive") {
		t.Fatalf("error should mention rename/archive, got: %v", err)
	}
	if _, statErr := os.Stat(logPath); statErr == nil {
		t.Fatal("ssh spawned despite bad file name")
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
	fakeSSH(t)

	if err := uploadRun("a;b", strings.NewReader(""), os.Stdout, os.Stderr); err == nil {
		t.Fatal("expected error for bad file name")
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("bad name hit the network %d times", n)
	}
}

func TestUploadRefusesRoot(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)
	fs.create = func(w http.ResponseWriter, _ int) {
		t.Error("unexpected POST /v1/vm for refused path")
		http.Error(w, `{"detail":"no"}`, 500)
	}

	err := uploadRun("/", strings.NewReader(""), os.Stdout, os.Stderr)
	if err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("expected root refusal, got %v", err)
	}
	if _, statErr := os.Stat(logPath); statErr == nil {
		t.Fatal("ssh spawned despite root upload")
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
	logPath := fakeSSH(t)

	err := uploadRun(home, strings.NewReader(""), os.Stdout, os.Stderr)
	if err == nil || !strings.Contains(err.Error(), "home directory") {
		t.Fatalf("expected home refusal, got %v", err)
	}
	if _, statErr := os.Stat(logPath); statErr == nil {
		t.Fatal("ssh spawned despite home upload")
	}
}

func keys(m map[string]tar.Header) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
