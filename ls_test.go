package main

import (
	"strings"
	"testing"
)

// ls is the raw-id escape hatch: the one surface where a sandbox id may
// appear, and the honest technical view of every state the server reports.
func TestLSSleepingIsNotExpired(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	fs.mu.Lock()
	fs.asleep = []string{"vm1"}
	fs.mu.Unlock()

	out := captureStdout(t, func() {
		if err := cmdLS(nil); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "asleep") {
		t.Fatalf("ls must render a sleeping machine as asleep, got %q", out)
	}
	if strings.Contains(out, "expired") {
		t.Fatalf("a sleeping machine is never expired, got %q", out)
	}
	if !strings.Contains(out, "vm1") {
		t.Fatalf("the technical listing keeps the raw id, got %q", out)
	}
}
