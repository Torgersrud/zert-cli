package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestAttachExpiredVMExplainsItself(t *testing.T) {
	fs := newFakeServer(t)
	fs.live = []string{"vma"}
	fs.expired = []string{"vmold"}
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)

	err := attachRun([]string{"vmold"}, strings.NewReader(""), os.Stdout, os.Stderr)
	if err == nil || !strings.Contains(err.Error(), "vmold expired") {
		t.Fatalf("want an expired-vm error, got %v", err)
	}
	if !strings.Contains(err.Error(), "start a new one") {
		t.Fatalf("the expiry error must say to start a new one, got %v", err)
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Fatal("ssh spawned for an expired vm")
	}
}

func TestAttachRejectsInvalidID(t *testing.T) {
	fs := newFakeServer(t)
	fs.live = []string{"vma"}
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)

	err := attachRun([]string{"x; touch /tmp/pwned"}, strings.NewReader(""), os.Stdout, io.Discard)
	if err == nil || err.Error() != "invalid vm id" {
		t.Fatalf("want the house invalid-vm-id error, got %v", err)
	}
	if _, statErr := os.Stat("/tmp/pwned"); statErr == nil {
		t.Fatal("injection executed: /tmp/pwned exists")
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Fatal("ssh spawned despite an invalid vm id")
	}
}

func TestAttachKeepWarnsObsolete(t *testing.T) {
	fs := newFakeServer(t)
	fs.live = []string{"vma"}
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)
	var stderr bytes.Buffer

	err := attachRun([]string{"--keep"}, strings.NewReader(""), os.Stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(stderr.String(), "obsolete"); n != 1 {
		t.Fatalf("want exactly one --keep deprecation notice on stderr, got %d in %q", n, stderr.String())
	}
	logged := readLog(t, logPath)
	line := argLines(logged)[0]
	if strings.Contains(line, "--keep") {
		t.Fatalf("--keep must not reach ssh argv: %s", line)
	}
	if !strings.Contains(line, "user@vma") {
		t.Fatalf("want an attach to the live vm, got %s", line)
	}
}

func TestAttachNoLiveVM(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)

	err := attachRun(nil, strings.NewReader(""), os.Stdout, os.Stderr)
	if err == nil || err.Error() != "no live vm — start one with `zert vm` or `zert <path>` first" {
		t.Fatalf("want the house no-live-vm message, got %v", err)
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Fatal("ssh spawned despite no live vm")
	}
}

func TestAttachSeveralLiveVMsDemandID(t *testing.T) {
	fs := newFakeServer(t)
	fs.live = []string{"vma", "vmb"}
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)

	err := attachRun(nil, strings.NewReader(""), os.Stdout, os.Stderr)
	if err == nil || !strings.Contains(err.Error(), "zert attach <id>") {
		t.Fatalf("want a message demanding an explicit id, got %v", err)
	}
	for _, id := range []string{"vma", "vmb"} {
		if !strings.Contains(err.Error(), id) {
			t.Fatalf("message must name the live vms to pick from, got %v", err)
		}
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Fatal("ssh spawned despite an ambiguous vm choice")
	}
}

func TestAttachByIDWithSSHArgs(t *testing.T) {
	fs := newFakeServer(t)
	fs.live = []string{"vma", "vmb"}
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)

	err := attachRun([]string{"vmb", "-t", "-L", "8080:localhost:80"},
		strings.NewReader(""), os.Stdout, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	logged := readLog(t, logPath)
	if n := len(argLines(logged)); n != 1 {
		t.Fatalf("want exactly one ssh call, got %d in %s", n, logged)
	}
	line := argLines(logged)[0]
	if !strings.Contains(line, "tunnel vmb") || !strings.Contains(line, "user@vmb") {
		t.Fatalf("wrong vm entered: %s", line)
	}
	if strings.Contains(line, "vma") {
		t.Fatalf("ssh argv mentions the other vm: %s", line)
	}
	if !strings.Contains(line, "-t -L 8080:localhost:80 user@vmb") {
		t.Fatalf("ssh args not forwarded verbatim before the destination: %s", line)
	}
	if !strings.Contains(logged, "TOKEN tok-9") {
		t.Fatalf("token not passed via ZERT_TUNNEL_TOKEN env: %s", logged)
	}
	assertNoTokenInArgs(t, logged)
	fs.mu.Lock()
	deleted := fs.deleted
	fs.mu.Unlock()
	if len(deleted) != 0 {
		t.Fatalf("attach must never kill the vm, got deletes %v", deleted)
	}
}

func TestAttachSingleLiveVM(t *testing.T) {
	fs := newFakeServer(t)
	fs.live = []string{"vma"}
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)

	if err := attachRun(nil, strings.NewReader(""), os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	logged := readLog(t, logPath)
	if !strings.Contains(logged, "ARGS -o ProxyCommand=") || !strings.Contains(logged, "tunnel vma") {
		t.Fatalf("ProxyCommand missing from ssh argv: %s", logged)
	}
	if !strings.Contains(logged, "user@vma") {
		t.Fatalf("ssh destination missing: %s", logged)
	}
	if !strings.Contains(logged, "-o ServerAliveInterval=30") || !strings.Contains(logged, "-o ServerAliveCountMax=4") {
		t.Fatalf("ssh keepalive options missing: %s", logged)
	}
	if !strings.Contains(logged, "TOKEN tok-9") {
		t.Fatalf("token not passed via ZERT_TUNNEL_TOKEN env: %s", logged)
	}
	assertNoTokenInArgs(t, logged)
	if n := len(argLines(logged)); n != 1 {
		t.Fatalf("want exactly one ssh call, got %d in %s", n, logged)
	}
	fs.mu.Lock()
	deleted := fs.deleted
	fs.mu.Unlock()
	if len(deleted) != 0 {
		t.Fatalf("attach must never kill the vm, got deletes %v", deleted)
	}
}
