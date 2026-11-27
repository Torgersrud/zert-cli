package main

import (
	"strings"
	"testing"
)

// TestUsageTeachesNoAttachCommand pins the entry story where the Customer
// reads it: usage documents the surfaces that exist — `zert vm`, `zert
// <path>`'s blurb, the copy and kill verbs — and never names the removed
// attach command, because usage is the one surface a lost Customer reads.
func TestUsageTeachesNoAttachCommand(t *testing.T) {
	out := captureStderr(t, usage)
	if strings.Contains(out, "attach") {
		t.Fatalf("usage must not name the removed attach command, got %q", out)
	}
	for _, want := range []string{"zert vm", "zert extract", "zert kill"} {
		if !strings.Contains(out, want) {
			t.Fatalf("usage must still document %s, got %q", want, out)
		}
	}
}
