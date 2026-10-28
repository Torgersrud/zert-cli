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

// The fake-ssh generators and the shared Entry-path assertions used below
// live in helpers_test.go.

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

	if err := uploadRun(dir, nil, strings.NewReader(""), os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	logged := readLog(t, logPath)
	assertSSHCommonOptions(t, logged, "vm123")
	if !strings.Contains(logged, uploadRemoteCmd) {
		t.Fatalf("remote tar command missing: %s", logged)
	}
	assertTokenInChildEnvOnly(t, logged)
	for _, line := range argLines(logged) {
		if strings.Contains(line, dir) {
			t.Fatalf("local path leaked into ssh argv: %s", line)
		}
	}
	assertNoVMDeleted(t, fs, "an upload and the entry after it")
}

func TestUploadCreatedVmAutoSshes(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)
	dir := tempDirNamed(t, "mydir")

	if err := uploadRun(dir, nil, strings.NewReader(""), os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	logged := readLog(t, logPath)
	assertTokenInChildEnvOnly(t, logged)
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
	assertNoVMDeleted(t, fs, "the entry after an upload")
}

func TestUploadEntryRunsTmuxCopyStreamNever(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)
	fakeTTY(t)
	dir := tempDirNamed(t, "mydir")

	if err := uploadRun(dir, nil, strings.NewReader(""), os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	lines := argLines(readLog(t, logPath))
	if len(lines) != 2 {
		t.Fatalf("want tar + interactive ssh, got %d lines: %v", len(lines), lines)
	}
	if strings.Contains(lines[0], "tmux") || strings.Contains(lines[0], " -t ") ||
		!strings.Contains(lines[0], uploadRemoteCmd) {
		t.Fatalf("the copy stream must never see tmux or a forced -t: %s", lines[0])
	}
	if !strings.Contains(lines[1], "user@vm123 "+tmuxWrapCmd) {
		t.Fatalf("the post-copy entry must run inside the tmux session: %s", lines[1])
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

	if err := uploadRun(dir+"/", nil, strings.NewReader(""), os.Stdout, os.Stderr); err != nil {
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

func TestUploadSSHArgsForwarded(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)
	dir := tempDirNamed(t, "mydir")

	err := uploadRun(dir, []string{"-L", "8080:localhost:80"}, strings.NewReader(""), os.Stdout, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	logged := readLog(t, logPath)
	if !strings.Contains(logged, "-L 8080:localhost:80 user@vm123") {
		t.Fatalf("user ssh args not forwarded before destination: %s", logged)
	}
	assertTokenInChildEnvOnly(t, logged)
	assertNoVMDeleted(t, fs, "an upload with user ssh args")
}

func TestUploadKeepWarnsObsoleteAndNeverKills(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)
	dir := tempDirNamed(t, "mydir")

	// cmdUpload is the upload path's one --keep call site (inside parseSSHArgs)
	stderr := captureStderr(t, func() {
		if err := cmdUpload([]string{dir, "--keep"}); err != nil {
			t.Fatal(err)
		}
	})
	if n := strings.Count(stderr, "obsolete"); n != 1 {
		t.Fatalf("want exactly one --keep deprecation notice on stderr, got %d in %q", n, stderr)
	}
	if !strings.Contains(stderr, "no longer die on exit") {
		t.Fatalf("notice must say vms no longer die on exit, got %q", stderr)
	}
	var plain bool
	for _, line := range argLines(readLog(t, logPath)) {
		if !strings.Contains(line, "tar -xzf") {
			plain = true
		}
	}
	if !plain {
		t.Fatalf("--keep must still enter the vm: %s", readLog(t, logPath))
	}
	assertNoVMDeleted(t, fs, "--keep")
}

func TestUploadLiveVMWithArgsEntersNoKill(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)
	fs.live = []string{"vmlive"}
	fs.create = func(w http.ResponseWriter, _ int) {
		t.Error("unexpected POST /v1/vm while a vm is live")
		http.Error(w, `{"detail":"no"}`, 500)
	}
	dir := tempDirNamed(t, "proj")

	err := uploadRun(dir, []string{"-L", "9000:localhost:90"}, strings.NewReader(""), os.Stdout, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	logged := readLog(t, logPath)
	if !strings.Contains(logged, "-L 9000:localhost:90 user@vmlive") {
		t.Fatalf("ssh args not forwarded to live vm session: %s", logged)
	}
	if n := len(argLines(logged)); n != 2 {
		t.Fatalf("expected exactly tar + interactive ssh, got %d: %s", n, logged)
	}
	assertNoVMDeleted(t, fs, "entering a live vm after an upload")
}

func TestUploadLiveVMKeepChangesNothing(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)
	fs.live = []string{"vmlive"}
	dir := tempDirNamed(t, "proj")

	stderr := captureStderr(t, func() {
		if err := cmdUpload([]string{dir, "--keep"}); err != nil {
			t.Fatal(err)
		}
	})
	// --keep changes nothing: a pre-existing live vm behaves exactly like
	// the no-flag case — copy only, no interactive entry.
	logged := readLog(t, logPath)
	if n := len(argLines(logged)); n != 1 {
		t.Fatalf("--keep must not enter an already-running vm, got %d ssh calls: %s", n, logged)
	}
	if n := strings.Count(stderr, "obsolete"); n != 1 {
		t.Fatalf("want exactly one --keep deprecation notice on stderr, got %d in %q", n, stderr)
	}
	assertNoVMDeleted(t, fs, "--keep on an already-running vm")
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

	if err := uploadRun(tempDirNamed(t, "d"), nil, strings.NewReader(""), os.Stdout, os.Stderr); err != nil {
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

	err := uploadRun(tempDirNamed(t, "d"), nil, strings.NewReader(""), os.Stdout, os.Stderr)
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 1 {
		t.Fatalf("want exit 1, got %v", err)
	}
	assertNoVMDeleted(t, fs, "ssh that fails")
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

	if err := uploadRun(dir, nil, strings.NewReader(""), os.Stdout, os.Stderr); err != nil {
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
	if err := uploadRun(path, nil, strings.NewReader(""), os.Stdout, os.Stderr); err != nil {
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
		err := uploadRun(arg, nil, strings.NewReader(""), os.Stdout, os.Stderr)
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

	err := uploadRun("a;b", nil, strings.NewReader(""), os.Stdout, os.Stderr)
	if err == nil {
		t.Fatal("expected error for shell metacharacter in name")
	}
	if !strings.Contains(err.Error(), "rename") || !strings.Contains(err.Error(), "archive") {
		t.Fatalf("error should mention rename/archive, got: %v", err)
	}
	assertNoSSHSpawned(t, logPath, "a shell metacharacter in the name")
	assertNoVMDeleted(t, fs, "a refused upload")
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

	if err := uploadRun("a;b", nil, strings.NewReader(""), os.Stdout, os.Stderr); err == nil {
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

	err := uploadRun("/", nil, strings.NewReader(""), os.Stdout, os.Stderr)
	if err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("expected root refusal, got %v", err)
	}
	assertNoSSHSpawned(t, logPath, "an upload of the root directory")
	assertNoVMDeleted(t, fs, "a refused upload")
}

func TestUploadRefusesHome(t *testing.T) {
	fs := newFakeServer(t)
	home := setupHome(t, fs.URL)
	logPath := fakeSSH(t)

	err := uploadRun(home, nil, strings.NewReader(""), os.Stdout, os.Stderr)
	if err == nil || !strings.Contains(err.Error(), "home directory") {
		t.Fatalf("expected home refusal, got %v", err)
	}
	assertNoSSHSpawned(t, logPath, "an upload of the home directory")
}

func keys(m map[string]tar.Header) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
