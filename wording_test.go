package main

import "testing"

// The cross-command wordings, pinned verbatim and once each at the
// constructor that owns them (wording.go). Every path that hits one of
// these truths speaks the constructor's message, so these pins are the
// only places a suite spells a wording out in full — the behavior tests
// assert fragments at their own seams (the close-code relay, the kill and
// tunnel refusals, the ssh invocation).

// The 401 truth is one rule on two wires: the API's HTTP 401 and the
// tunnel's 4401 close both say the session is gone and name the way back.
func TestSessionExpiredWording(t *testing.T) {
	if got, want := errSessionExpired.Error(), "session expired — run `zert login`"; got != want {
		t.Fatalf("session-expired wording = %q, want %q", got, want)
	}
}
