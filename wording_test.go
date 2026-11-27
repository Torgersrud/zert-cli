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

// The dead-machine truth: the vm is gone and `zert vm` is the way back.
// One wording, whatever path meets the corpse — today the tunnel's 4410
// close; the door module's entry paths inherit the same constructor.
func TestVMExpiredWording(t *testing.T) {
	if got, want := errVMExpired.Error(), "vm expired — start a new one with `zert vm`"; got != want {
		t.Fatalf("vm-expired wording = %q, want %q", got, want)
	}
}

// The id guard kill and tunnel share: a word that is neither the stable
// name nor a valid id is refused before any ask is made — the "same guard
// as kill and tunnel" comment, made code.
func TestInvalidVMIDWording(t *testing.T) {
	if got, want := errInvalidVMID.Error(), "invalid vm id"; got != want {
		t.Fatalf("invalid-id wording = %q, want %q", got, want)
	}
}

// The copy arms' several-row refusal: the anomaly is admitted, the copy
// is refused — a file must never land on or leave a machine the Customer
// did not choose — and `zert ls` is where the server's raw answer shows.
// Status admits the same anomaly without refusing, because it acts on no
// row; the copy arms act, so their wording names the refusal.
func TestSeveralMachinesNoCopyWording(t *testing.T) {
	if got, want := errSeveralMachinesNoCopy.Error(),
		"several machines are listed — refusing to copy to or from one you did not choose; `zert ls` shows what the server says"; got != want {
		t.Fatalf("copy-refusal wording = %q, want %q", got, want)
	}
}
