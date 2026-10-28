package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The fake-ssh generator and the shared Entry-path assertions used below live
// in helpers_test.go; the tar fixtures here stay, they are extract's own.

// tarEntry is one member of the fake vm-side tar streams below.
type tarEntry struct {
	name string
	body string
	mode os.FileMode
	typ  byte // tar.Type*; 0 means a regular file
}

// makeTarGz writes entries the way the vm's `tar -czf -` would and returns
// the file path for fakeSSHStreamingStdout to replay on stdout.
func makeTarGz(t *testing.T, entries ...tarEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stream.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := e.mode.Perm()
		if mode == 0 {
			mode = 0644
		}
		h := &tar.Header{
			Name:     e.name,
			Typeflag: typ,
			Mode:     int64(mode),
			ModTime:  time.Unix(1700000000, 0),
			Format:   tar.FormatGNU,
		}
		if typ == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if typ == tar.TypeSymlink {
			h.Linkname = "nowhere"
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExtractHappyPath(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	tarPath := makeTarGz(t,
		tarEntry{name: "out/", typ: tar.TypeDir, mode: 0755},
		tarEntry{name: "out/a.txt", body: "hello", mode: 0644},
		tarEntry{name: "out/exec.sh", body: "#!/bin/sh\n", mode: 0755},
	)
	t.Setenv("FAKE_SSH_STDOUT", tarPath)
	logPath := fakeSSHStreamingStdout(t)
	fs.live = []string{"vmlive"}
	fs.create = func(w http.ResponseWriter, _ int) {
		t.Error("unexpected POST /v1/vm while a vm is live")
		http.Error(w, `{"detail":"no"}`, 500)
	}
	dest := t.TempDir()

	if err := extractRun("~/out", dest, os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dest, "out", "a.txt"))
	if err != nil || string(b) != "hello" {
		t.Fatalf("out/a.txt = %q, %v", b, err)
	}
	st, err := os.Stat(filepath.Join(dest, "out", "exec.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&0111 == 0 {
		t.Fatalf("exec bit lost: mode=%v", st.Mode())
	}
	logged := readLog(t, logPath)
	assertSSHCommonOptions(t, logged, "vmlive")
	if !strings.Contains(logged, "-czf -") {
		t.Fatalf("remote tar command missing: %s", logged)
	}
	// ~/out splits to dir ~ base out; ~ stays unquoted so the remote shell
	// expands it, base is quoted
	if !strings.Contains(logged, `-C ~ -- 'out'`) {
		t.Fatalf("remote tar -C/base wrong: %s", logged)
	}
	assertTokenInChildEnvOnly(t, logged)
	// extract is a non-interactive stream: it never lands in tmux and never
	// asks ssh for a tty
	if strings.Contains(logged, "tmux") || strings.Contains(logged, " -t ") {
		t.Fatalf("tmux or a forced -t leaked into the extract ssh argv: %s", logged)
	}
	if n := len(argLines(logged)); n != 1 {
		t.Fatalf("extract must spawn exactly one ssh, got %d: %s", n, logged)
	}
	assertNoVMDeleted(t, fs, "extract")
}

func TestExtractCreatesDestDir(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	tarPath := makeTarGz(t, tarEntry{name: "report.txt", body: "q3", mode: 0644})
	t.Setenv("FAKE_SSH_STDOUT", tarPath)
	fakeSSHStreamingStdout(t)
	fs.live = []string{"vmlive"}
	dest := filepath.Join(t.TempDir(), "new", "nested")

	if err := extractRun("~/report.txt", dest, os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dest, "report.txt"))
	if err != nil || string(b) != "q3" {
		t.Fatalf("nested dest not created/populated: %q, %v", b, err)
	}
}

func TestExtractNoLiveVM(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)
	fs.create = func(w http.ResponseWriter, _ int) {
		t.Error("extract must never create a vm")
		http.Error(w, `{"detail":"no"}`, 500)
	}

	err := extractRun("~/out", t.TempDir(), os.Stdout, os.Stderr)
	if err == nil || !strings.Contains(err.Error(), "no live vm") {
		t.Fatalf("want no-live-vm error, got %v", err)
	}
	assertNoSSHSpawned(t, logPath, "no live vm")
}

func TestExtractTarSlipRefused(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	fs.live = []string{"vmlive"}

	t.Run("traversal", func(t *testing.T) {
		parent := t.TempDir()
		dest := filepath.Join(parent, "dest")
		tarPath := makeTarGz(t, tarEntry{name: "../evil", body: "pwned", mode: 0644})
		t.Setenv("FAKE_SSH_STDOUT", tarPath)
		fakeSSHStreamingStdout(t)

		err := extractRun("~/out", dest, os.Stdout, os.Stderr)
		if err == nil || !strings.Contains(err.Error(), "traversal") || !strings.Contains(err.Error(), "../evil") {
			t.Fatalf("want traversal refusal, got %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(parent, "evil")); statErr == nil {
			t.Fatal("traversal entry escaped the destination")
		}
	})
	t.Run("absolute", func(t *testing.T) {
		tarPath := makeTarGz(t, tarEntry{name: "/abs", body: "pwned", mode: 0644})
		t.Setenv("FAKE_SSH_STDOUT", tarPath)
		fakeSSHStreamingStdout(t)

		err := extractRun("~/out", t.TempDir(), os.Stdout, os.Stderr)
		if err == nil || !strings.Contains(err.Error(), "absolute") || !strings.Contains(err.Error(), "/abs") {
			t.Fatalf("want absolute-path refusal, got %v", err)
		}
	})
}

func TestExtractSymlinkSkipped(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	tarPath := makeTarGz(t,
		tarEntry{name: "out/link", typ: tar.TypeSymlink, mode: 0777},
		tarEntry{name: "out/real.txt", body: "data", mode: 0644},
	)
	t.Setenv("FAKE_SSH_STDOUT", tarPath)
	fakeSSHStreamingStdout(t)
	fs.live = []string{"vmlive"}
	dest := t.TempDir()

	if err := extractRun("~/out", dest, os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "out", "link")); err == nil {
		t.Fatal("symlink entry must be skipped, not created")
	}
	if _, err := os.Stat(filepath.Join(dest, "out", "real.txt")); err != nil {
		t.Fatalf("regular file next to symlink missing: %v", err)
	}
}

func TestExtractSingleFile(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	tarPath := makeTarGz(t, tarEntry{name: "note.txt", body: "hi", mode: 0644})
	t.Setenv("FAKE_SSH_STDOUT", tarPath)
	logPath := fakeSSHStreamingStdout(t)
	fs.live = []string{"vmlive"}
	dest := t.TempDir()

	// bare name (no slash) resolves against the vm home, i.e. -C ~ (unquoted)
	if err := extractRun("note.txt", dest, os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dest, "note.txt"))
	if err != nil || string(b) != "hi" {
		t.Fatalf("note.txt = %q, %v", b, err)
	}
	if logged := readLog(t, logPath); !strings.Contains(logged, `-C ~ -- 'note.txt'`) {
		t.Fatalf("bare name not resolved against vm home: %s", logged)
	}
}

func TestExtractBadRemotePathNoNetwork(t *testing.T) {
	var hits int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, `{}`, http.StatusInternalServerError)
	}))
	defer ts.Close()
	setupHome(t, ts.URL)
	fakeSSH(t)

	if err := extractRun("a;b", t.TempDir(), os.Stdout, os.Stderr); err == nil {
		t.Fatal("expected error for bad vm path")
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("bad vm path hit the network %d times", n)
	}

	for _, tc := range []struct {
		in, dir, base string
		ok            bool
	}{
		{".", "", "", false},
		{"..", "", "", false},
		{"/", "", "", false},
		{"a;b", "", "", false},
		{"~/out", "~", "out", true},
		{"out", "~", "out", true},
		{"~/a/b/", "~/a", "b", true},
		{"/a/b", "/a", "b", true},
		{"/b", "", "", false},
		{"it's", "", "", false},
	} {
		dir, base, err := splitRemotePath(tc.in)
		if !tc.ok {
			if err == nil {
				t.Fatalf("%q must be refused, got %q %q", tc.in, dir, base)
			}
			continue
		}
		if err != nil {
			t.Fatalf("splitRemotePath(%q): %v", tc.in, err)
		}
		if dir != tc.dir || base != tc.base {
			t.Fatalf("splitRemotePath(%q) = %q %q, want %q %q", tc.in, dir, base, tc.dir, tc.base)
		}
	}
}

// TestExtractRemoteCmdShellExpansion proves the quoting of extractRemoteCmd
// against a real sh and tar: it runs the command verbatim with the fake "vm"
// home in $HOME and extracts what comes back. This is the semantics the
// fake-ssh tests cannot check (they only compare strings).
func TestExtractRemoteCmdShellExpansion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell quoting is not what cmd.exe speaks")
	}
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("no system tar")
	}
	vmHome := t.TempDir()
	t.Setenv("HOME", vmHome)
	write := func(rel, body string) {
		p := filepath.Join(vmHome, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("out/a.txt", "hello")
	write("my proj/out/a.txt", "spaced")

	for _, tc := range []struct {
		vmPath, dir, base string
	}{
		{"~/out", "~", "out"},
		{"out", "~", "out"},
		{"~/my proj/out", "~/my proj", "out"},
	} {
		dir, base, err := splitRemotePath(tc.vmPath)
		if err != nil {
			t.Fatalf("splitRemotePath(%q): %v", tc.vmPath, err)
		}
		if dir != tc.dir || base != tc.base {
			t.Fatalf("splitRemotePath(%q) = %q %q, want %q %q", tc.vmPath, dir, base, tc.dir, tc.base)
		}
		cmd := exec.Command("sh", "-c", extractRemoteCmd(dir, base))
		var out, errBuf bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errBuf
		if err := cmd.Run(); err != nil {
			t.Fatalf("%q: sh -c %s: %v\n%s", tc.vmPath, extractRemoteCmd(dir, base), err, errBuf.String())
		}
		dest := t.TempDir()
		files, _, err := extractTarGz(dest, &out)
		if err != nil {
			t.Fatalf("%q: extractTarGz: %v", tc.vmPath, err)
		}
		if files != 1 {
			t.Fatalf("%q: extracted %d files, want 1", tc.vmPath, files)
		}
		b, err := os.ReadFile(filepath.Join(dest, "out", "a.txt"))
		want := "hello"
		if strings.Contains(tc.vmPath, "my proj") {
			want = "spaced"
		}
		if err != nil || string(b) != want {
			t.Fatalf("%q: out/a.txt = %q, %v", tc.vmPath, b, err)
		}
	}
}

func TestExtractSSHFailureExitCode(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	tarPath := makeTarGz(t, tarEntry{name: "note.txt", body: "hi", mode: 0644})
	t.Setenv("FAKE_SSH_STDOUT", tarPath)
	fakeSSHStreamingStdout(t)
	t.Setenv("FAKE_SSH_EXIT", "1")
	fs.live = []string{"vmlive"}

	// ssh's exit code must win over any downstream stream fallout
	err := extractRun("~/note.txt", t.TempDir(), os.Stdout, os.Stderr)
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 1 {
		t.Fatalf("want exit 1, got %v", err)
	}
}

func TestExtractDefaultDestCwd(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	tarPath := makeTarGz(t, tarEntry{name: "note.txt", body: "hi", mode: 0644})
	t.Setenv("FAKE_SSH_STDOUT", tarPath)
	fakeSSHStreamingStdout(t)
	fs.live = []string{"vmlive"}
	cwd := t.TempDir()
	t.Chdir(cwd)

	if err := extractRun("~/note.txt", "", os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cwd, "note.txt")); err != nil {
		t.Fatalf("empty dest must extract into cwd: %v", err)
	}
}
