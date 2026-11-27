package main

import (
	"regexp"
	"strings"
	"testing"
)

// timeLeftRE pins that a running machine's answer carries a duration, without
// depending on the exact second the test ran.
var timeLeftRE = regexp.MustCompile(`[0-9]+(h[0-9]+m[0-9]+s|m[0-9]+s|s) left`)

func TestStatusRunningWithTimeLeft(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	fs.mu.Lock()
	fs.live = []string{"vm1"}
	fs.mu.Unlock()

	out := captureStdout(t, func() {
		if err := cmdStatus(nil); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "running") {
		t.Fatalf("status must say running, got %q", out)
	}
	if !timeLeftRE.MatchString(out) {
		t.Fatalf("status must say the time left, got %q", out)
	}
	if !strings.Contains(out, "goes to sleep") {
		t.Fatalf("status must say what the time is left before, got %q", out)
	}
	if strings.Contains(out, "vm1") {
		t.Fatalf("raw ids have no place on the customer surface, got %q", out)
	}
}

func TestStatusNoMachinePointsAtVM(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)

	out := captureStdout(t, func() {
		if err := cmdStatus(nil); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "no machine") {
		t.Fatalf("status must say there is no machine, got %q", out)
	}
	if !strings.Contains(out, "zert vm") {
		t.Fatalf("status must point at the command that makes one, got %q", out)
	}
}

func TestStatusCreating(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	fs.mu.Lock()
	fs.creating = []string{"vm1"}
	fs.mu.Unlock()

	out := captureStdout(t, func() {
		if err := cmdStatus(nil); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "being created") {
		t.Fatalf("status must say the machine is being created, got %q", out)
	}
	if strings.Contains(out, "running") {
		t.Fatalf("a machine mid-create is not running yet, got %q", out)
	}
}

func TestStatusUnknownStateWordDoesNotCrashOrLie(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	// a row that is none of the states this zert knows: not live, not
	// asleep, not creating — what a future server's new state word looks
	// like to this build
	fs.mu.Lock()
	fs.expired = []string{"vm1"}
	fs.mu.Unlock()

	out := captureStdout(t, func() {
		if err := cmdStatus(nil); err != nil {
			t.Fatalf("an unknown state word must not fail the command: %v", err)
		}
	})
	if !strings.Contains(out, "does not know") {
		t.Fatalf("status must admit the state is unknown, got %q", out)
	}
	if strings.Contains(out, "expired") {
		t.Fatalf("an unknown state is never called expired, got %q", out)
	}
	if strings.Contains(out, "no machine") {
		t.Fatalf("a machine in an unknown state still exists, got %q", out)
	}
	if strings.Contains(out, "running") || strings.Contains(out, "asleep") {
		t.Fatalf("an unknown state must not be named a known one, got %q", out)
	}
}

func TestUsageNamesStatusAndStateWords(t *testing.T) {
	out := captureStderr(t, usage)
	if !strings.Contains(out, "zert status") {
		t.Fatalf("usage must name the status command, got %q", out)
	}
	if !strings.Contains(out, "asleep") {
		t.Fatalf("usage must name the asleep state word, got %q", out)
	}
	if !strings.Contains(out, "zert ls") {
		t.Fatalf("usage must keep naming the technical listing, got %q", out)
	}
}

func TestStatusSleepingSaysAsleep(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	fs.mu.Lock()
	fs.asleep = []string{"vm1"}
	fs.mu.Unlock()

	out := captureStdout(t, func() {
		if err := cmdStatus(nil); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "asleep") {
		t.Fatalf("status must say asleep, got %q", out)
	}
	if strings.Contains(out, "expired") {
		t.Fatalf("a sleeping machine is never expired, got %q", out)
	}
	if strings.Contains(out, "no machine") {
		t.Fatalf("a sleeping machine is never \"no machine\", got %q", out)
	}
	if strings.Contains(out, "vm1") {
		t.Fatalf("raw ids have no place on the customer surface, got %q", out)
	}
}
