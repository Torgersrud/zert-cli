package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestAttachRunsInsideTmuxSession(t *testing.T) {
	fs := newFakeServer(t)
	fs.live = []string{"vma"}
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)
	fakeTTY(t)

	if err := attachRun(nil, strings.NewReader(""), os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	line := argLines(readLog(t, logPath))[0]
	// Attach enters the same named session every interactive entry uses —
	// that is what makes a second Attach a mirror and a Detach restorable
	if !strings.Contains(line, "-t user@vma "+tmuxWrapCmd) {
		t.Fatalf("attach did not enter the tmux session: %s", line)
	}
	assertNoVMDeleted(t, fs, "detaching from tmux")
}

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
	assertNoSSHSpawned(t, logPath, "an expired vm")
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
	assertNoSSHSpawned(t, logPath, "an invalid vm id")
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
	assertNoSSHSpawned(t, logPath, "no live vm")
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
	assertNoSSHSpawned(t, logPath, "an ambiguous vm choice")
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
	assertTokenInChildEnvOnly(t, logged)
	assertNoVMDeleted(t, fs, "attach")
}

// The id may land after ssh options or --keep: it must still pick the vm,
// never get executed as a remote command on an auto-selected one.

func TestAttachIDAfterOptionArgs(t *testing.T) {
	fs := newFakeServer(t)
	fs.live = []string{"vma", "vmb"}
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)

	if err := attachRun([]string{"-L", "80:80", "vmb"},
		strings.NewReader(""), os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	line := argLines(readLog(t, logPath))[0]
	if !strings.Contains(line, "tunnel vmb") || !strings.Contains(line, "user@vmb") {
		t.Fatalf("id after option args did not select the vm: %s", line)
	}
	if !strings.Contains(line, "-L 80:80 user@vmb") {
		t.Fatalf("-L not forwarded before the destination: %s", line)
	}
	if strings.Contains(line, "vma") {
		t.Fatalf("auto-selected a vm instead of the named one: %s", line)
	}
}

func TestAttachIDAfterKeepFlag(t *testing.T) {
	fs := newFakeServer(t)
	fs.live = []string{"vma", "vmb"}
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)
	var stderr bytes.Buffer

	if err := attachRun([]string{"--keep", "vmb"},
		strings.NewReader(""), os.Stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	line := argLines(readLog(t, logPath))[0]
	if !strings.Contains(line, "tunnel vmb") || !strings.Contains(line, "user@vmb") {
		t.Fatalf("id after --keep did not select the vm: %s", line)
	}
	if strings.Contains(line, "--keep") {
		t.Fatalf("--keep must not reach ssh argv: %s", line)
	}
	if n := strings.Count(stderr.String(), "obsolete"); n != 1 {
		t.Fatalf("want exactly one --keep deprecation notice on stderr, got %d in %q", n, stderr.String())
	}
}

func TestAttachIDWithRemoteCommandStaysCommand(t *testing.T) {
	fs := newFakeServer(t)
	fs.live = []string{"vma", "vmb"}
	setupHome(t, fs.URL)
	logPath := fakeSSH(t)

	if err := attachRun([]string{"vmb", "--", "uptime"},
		strings.NewReader(""), os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	line := argLines(readLog(t, logPath))[0]
	if !strings.Contains(line, "tunnel vmb") {
		t.Fatalf("wrong vm entered: %s", line)
	}
	// "--" is ssh's own end-of-options marker: what follows is the command
	// side, so the vm runs uptime unwrapped, not the tmux session
	if strings.Contains(line, "tmux") || !strings.HasSuffix(line, "uptime user@vmb") {
		t.Fatalf("remote command after -- was not run in the named vm: %s", line)
	}
}

func TestSplitAttachArgs(t *testing.T) {
	for _, tc := range []struct {
		args []string
		id   string
		rest string
	}{
		{nil, "", ""},
		{[]string{"vma"}, "vma", ""},
		{[]string{"vma", "-L", "80:80"}, "vma", "-L 80:80"},
		// an option's value is never the id candidate
		{[]string{"-L", "80:80", "vma"}, "vma", "-L 80:80"},
		{[]string{"-l", "zert", "-p", "2222", "vma"}, "vma", "-l zert -p 2222"},
		// --keep is a flag, not an option with a value
		{[]string{"--keep", "vma"}, "vma", "--keep"},
		// a bare token past ssh's "--" is command territory, never an id
		{[]string{"--", "vma"}, "", "-- vma"},
		// a bare token that is not a vm id stays exactly where it is:
		// the remote command ssh reads today
		{[]string{"-v", "x;rm"}, "", "-v x;rm"},
		// the documented leading position is the id position, valid or not
		// (attachRun answers an invalid one with "invalid vm id")
		{[]string{"x;rm"}, "x;rm", ""},
	} {
		id, rest := splitAttachArgs(tc.args)
		if id != tc.id || strings.Join(rest, " ") != tc.rest {
			t.Errorf("splitAttachArgs(%q) = (%q, %q), want (%q, %q)",
				tc.args, id, strings.Join(rest, " "), tc.id, tc.rest)
		}
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
	assertSSHCommonOptions(t, logged, "vma")
	assertTokenInChildEnvOnly(t, logged)
	if n := len(argLines(logged)); n != 1 {
		t.Fatalf("want exactly one ssh call, got %d in %s", n, logged)
	}
	assertNoVMDeleted(t, fs, "attach")
}
