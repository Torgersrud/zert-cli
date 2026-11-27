package main

// The door module's suite: the ensure ask and its outcome story, pinned
// once at the module's interface — askMachine, "give me my machine" —
// against the existing door fake, which models the door's ensure contract
// (what was asked, what woke, what was placed). No command runner, no
// credentials, no ssh binary is involved: the door takes an api value,
// and what a test observes is what crosses the door — the asks it
// received, what was placed, the story told to the writer the caller
// handed over — never how the module is assembled.

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

// askMachine discarding is the caller that renders nothing: the stable
// name's choice, taken here so the cases below assert only what their
// own writer saw.
func askQuietly(a api) (*vmRow, doorOutcome, error) {
	return askMachine(a, io.Discard)
}

// TestDoorAskOutcomes pins the three outcomes once, one per starting
// state: the machine plus which of the three things the ask did —
// answered as-is, woken, or placed — and the one wording each outcome
// carries, told to the caller's writer before the ask explains its wait.
// The outcome comes from the door, never from a caller's guess at a
// listing: dead rows answer nothing, so a listing whose rows are all
// unanswerable is told as creating, matching what the ask then does. A
// several-row listing is the second ask's question (landing (b) of spec
// #30): the first ask proceeds — the door answers the machine — and the
// story is the outcome's, never a refusal.
func TestDoorAskOutcomes(t *testing.T) {
	cases := []struct {
		name    string
		live    []string
		asleep  []string
		expired []string
		want    doorOutcome
		wantVM  string
		story   string
		notSay  string
	}{
		{
			name:   "a running machine is answered as it was",
			live:   []string{"vmlive"},
			want:   doorAnswered,
			wantVM: "vmlive",
			story:  "found your vm — entering it",
		},
		{
			name:   "a sleeping machine is woken under its own id",
			asleep: []string{"vms"},
			want:   doorWoken,
			wantVM: "vms",
			story:  "no live vm — waking yours …",
			notSay: "found your vm",
		},
		{
			name:   "a customer with nothing gets one placed",
			want:   doorPlaced,
			wantVM: "vm123",
			story:  "no live vm — creating one …",
			notSay: "found your vm",
		},
		{
			// TestEnsureExpiredRowStillMeansCreating, moved from the vm
			// suite: an expired row is a dead machine — the ask places
			// one instead of answering it, so the story says creating,
			// never found, never waking.
			name:    "a dead row is not a machine: one is placed",
			expired: []string{"vmdead"},
			want:    doorPlaced,
			wantVM:  "vm123",
			story:   "no live vm — creating one …",
			notSay:  "found your vm",
		},
		{
			name:   "a several-row listing still gets its machine answered",
			live:   []string{"vmA", "vmB"},
			want:   doorAnswered,
			wantVM: "vmA",
			story:  "found your vm — entering it",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeServer(t)
			fs.live = tc.live
			fs.asleep = tc.asleep
			fs.expired = tc.expired

			var stderr bytes.Buffer
			vm, outcome, err := askMachine(api{host: fs.URL}, &stderr)
			if err != nil {
				t.Fatal(err)
			}
			if outcome != tc.want {
				t.Fatalf("outcome = %v, want %v", outcome, tc.want)
			}
			if vm.SandboxID != tc.wantVM {
				t.Fatalf("the ask must answer the Customer's machine %s, got %s", tc.wantVM, vm.SandboxID)
			}
			if got := stderr.String(); got != tc.story+"\n" {
				t.Fatalf("the story told to the caller's writer = %q, want %q", got, tc.story+"\n")
			}
			if tc.notSay != "" && strings.Contains(stderr.String(), tc.notSay) {
				t.Fatalf("the story must never say %q, got %q", tc.notSay, stderr.String())
			}
			asks, placed := fs.ensureCounts()
			if asks != 1 {
				t.Fatalf("the ask is exactly one ensure call, got %d", asks)
			}
			if outcome == doorPlaced {
				if len(placed) != 1 || placed[0] != tc.wantVM {
					t.Fatalf("a placed outcome must place the one machine, placed %v", placed)
				}
			} else if len(placed) != 0 {
				t.Fatalf("%v places nothing, placed %v", outcome, placed)
			}
		})
	}
}

// TestDoorAsksFromItsOwnSnapshot pins where the outcome comes from: the
// module takes the listing snapshot itself, before the ask — a caller
// that never lists anything still gets the true story, and a caller that
// holds an older listing is not consulted.
func TestDoorAsksFromItsOwnSnapshot(t *testing.T) {
	fs := newFakeServer(t)
	fs.asleep = []string{"vms"}

	vm, outcome, err := askQuietly(api{host: fs.URL})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != doorWoken || vm.SandboxID != "vms" {
		t.Fatalf("the sleeper must be woken under its own id, got %v %s", outcome, vm.SandboxID)
	}
	fs.mu.Lock()
	live, asleep := append([]string(nil), fs.live...), append([]string(nil), fs.asleep...)
	fs.mu.Unlock()
	if len(asleep) != 0 || len(live) != 1 || live[0] != "vms" {
		t.Fatalf("the ask must wake the sleeper, live=%v asleep=%v", live, asleep)
	}
}
