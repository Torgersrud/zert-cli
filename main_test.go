package main

import (
	"strings"
	"testing"
)

// TestUsageIsTheShortAnswer pins the shape of the bare-`zert` answer, not
// its bytes: the few commands that matter are named, the kill blurb
// teaches the bare form, the answer says where to read more — and it must
// not grow back into a dense wall of text.
func TestUsageIsTheShortAnswer(t *testing.T) {
	out := captureStderr(t, usage)
	if lines := strings.Count(out, "\n"); lines > 15 {
		t.Fatalf("the bare answer stays short — at most 15 lines, got %d:\n%s", lines, out)
	}
	for _, want := range []string{
		"zert login", "zert vm", "zert <path>", "zert extract",
		"zert status", "zert ls", "zert kill",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the bare answer must name %s, got %q", want, out)
		}
	}
	if !strings.Contains(out, "bare kill") {
		t.Fatalf("the kill blurb must teach the bare form, got %q", out)
	}
	if !strings.Contains(strings.ToLower(out), "readme") {
		t.Fatalf("the bare answer must say where to read more, got %q", out)
	}
}

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
