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
	"encoding/json"
	"errors"
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

// TestEnsureTwiceAnswersSameMachine, moved from the vm suite to the seam
// that owns the contract: POST /v1/vm is the ensure ask, so a second ask
// must answer the machine the first one placed — never place a second —
// and the outcome story must tell which of the two happened. The fake
// places a machine only when the account has none, so "the second ask
// placed nothing" is read off fs.placed.
func TestEnsureTwiceAnswersSameMachine(t *testing.T) {
	fs := newFakeServer(t)

	first, outcome, err := askQuietly(api{host: fs.URL})
	if err != nil {
		t.Fatal(err)
	}
	second, outcome2, err := askQuietly(api{host: fs.URL})
	if err != nil {
		t.Fatal(err)
	}

	if first.SandboxID != second.SandboxID || first.SandboxID != "vm123" {
		t.Fatalf("both asks must answer the same machine, got %s then %s", first.SandboxID, second.SandboxID)
	}
	if outcome != doorPlaced || outcome2 != doorAnswered {
		t.Fatalf("the story must say placed then found, got %v then %v", outcome, outcome2)
	}
	asks, placed := fs.ensureCounts()
	if len(placed) != 1 || placed[0] != "vm123" {
		t.Fatalf("the second ask must place nothing, placements: %v", placed)
	}
	if asks != 2 {
		t.Fatalf("each ask must reach the door, want 2 POSTs, got %d", asks)
	}
}

// The retry shape, moved from the vm suite and pinned here once: exactly
// one retry, and only on a 502 — a refusal is an answer, never retried,
// and it places nothing.

func TestEnsure502RetriesOnce(t *testing.T) {
	fs := newFakeServer(t)
	fs.create = func(w http.ResponseWriter, tries int) {
		if tries == 1 {
			http.Error(w, `{"detail":"failed to bootstrap VM, try again"}`, 502)
			return
		}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"sandbox_id": "vm123", "expires_at": json.Number("9999999999"), "state": "running"})
	}

	vm, _, err := askQuietly(api{host: fs.URL})
	if err != nil {
		t.Fatalf("502 retry should succeed, got %v", err)
	}
	if vm.SandboxID != "vm123" {
		t.Fatalf("the retried ask must answer the machine, got %s", vm.SandboxID)
	}
	if asks, _ := fs.ensureCounts(); asks != 2 {
		t.Fatalf("the retry is one more ask: want 2 POSTs, got %d", asks)
	}
}

// TestEnsure502RetryPlacesOneMachine routes the headline through the
// retry itself: an ask that dies on the door's far side (502) places
// nothing, the retry places the one machine, and a later ask answers
// that same machine without placing anything — the retried bad-gateway
// answer returns the Customer's one machine, never a second.
func TestEnsure502RetryPlacesOneMachine(t *testing.T) {
	fs := newFakeServer(t)
	fs.create = func(w http.ResponseWriter, tries int) {
		fs.mu.Lock()
		placed := len(fs.placed) > 0
		fs.mu.Unlock()
		if !placed {
			if tries == 1 {
				// the ask reached the door and died past it: the
				// bootstrap failed, so nothing was placed
				http.Error(w, `{"detail":"failed to bootstrap VM, try again"}`, 502)
				return
			}
			fs.mu.Lock()
			fs.placed = append(fs.placed, "vm123")
			fs.mu.Unlock()
		}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{
			"sandbox_id": "vm123", "expires_at": json.Number("9999999999"),
			"state": "running",
		})
	}

	if _, _, err := askQuietly(api{host: fs.URL}); err != nil {
		t.Fatalf("502 retry should succeed, got %v", err)
	}
	if _, _, err := askQuietly(api{host: fs.URL}); err != nil {
		t.Fatal(err)
	}
	asks, placed := fs.ensureCounts()
	if len(placed) != 1 || placed[0] != "vm123" {
		t.Fatalf("retry places the one machine, later asks place nothing: %v", placed)
	}
	if asks != 3 {
		t.Fatalf("want 3 asks (502, retry, later ask), got %d", asks)
	}
}

// TestEnsureRefusalNotRetried pins that a refusal is final: the ensure
// refusals (400 no/invalid pubkey, 429 rate-limited, 503 wake/arm failed)
// place nothing and are never retried — only the bad gateway asks again.
func TestEnsureRefusalNotRetried(t *testing.T) {
	for _, code := range []int{400, 429, 503} {
		fs := newFakeServer(t)
		fs.create = func(w http.ResponseWriter, _ int) {
			http.Error(w, `{"detail":"refused"}`, code)
		}

		_, _, err := askQuietly(api{host: fs.URL})
		if err == nil {
			t.Fatalf("%d: want the refusal as an error", code)
		}
		asks, placed := fs.ensureCounts()
		if asks != 1 {
			t.Fatalf("%d: a refusal must not be retried, got %d asks", code, asks)
		}
		if len(placed) != 0 {
			t.Fatalf("%d: a refusal places nothing, got %v", code, placed)
		}
	}
}

// TestEnsure502RetriedOnceOnly pins the retry's shape: the 502 keeps its
// trigger and its single attempt — a door that answers 502 twice gets two
// asks, never a third.
func TestEnsure502RetriedOnceOnly(t *testing.T) {
	fs := newFakeServer(t)
	fs.create = func(w http.ResponseWriter, _ int) {
		http.Error(w, `{"detail":"failed to bootstrap VM, try again"}`, 502)
	}

	_, _, err := askQuietly(api{host: fs.URL})
	if err == nil || !strings.Contains(err.Error(), "bootstrap") {
		t.Fatalf("want the 502 word after one retry, got %v", err)
	}
	if asks, _ := fs.ensureCounts(); asks != 2 {
		t.Fatalf("the retry is one attempt: want 2 asks, got %d", asks)
	}
}

// TestEnsureRequiresRunningState pins the response contract's one promise
// about the machine: every 201 hands over a running machine, whatever
// outcome got it there. A door that answers any other state word is not
// obeying the ensure contract, and the CLI refuses to hand the machine
// over.
func TestEnsureRequiresRunningState(t *testing.T) {
	fs := newFakeServer(t)
	fs.create = func(w http.ResponseWriter, _ int) {
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{
			"sandbox_id": "vm123", "expires_at": json.Number("9999999999"),
			"state": "asleep",
		})
	}

	_, _, err := askQuietly(api{host: fs.URL})
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("want a not-running refusal, got %v", err)
	}
}

// TestEnsureRejectsMaliciousID pins the id guard at the door: the id the
// response carries is refused unless it passes validSandboxID, before any
// path can put it into an ssh argv. (The no-ssh half of the old vm-suite
// pin stays there: TestVMNoSpawnOnMaliciousID.)
func TestEnsureRejectsMaliciousID(t *testing.T) {
	fs := newFakeServer(t)
	fs.create = func(w http.ResponseWriter, _ int) {
		w.WriteHeader(201)
		io.WriteString(w, `{"sandbox_id":"x; touch /tmp/pwned","expires_at":9999999999,"state":"running"}`)
	}
	_, _, err := askQuietly(api{host: fs.URL})
	if err == nil || !strings.Contains(err.Error(), "invalid vm id") {
		t.Fatalf("want invalid-vm-id error, got %v", err)
	}
}

// TestDoorWhichRowAnswers pins the second ask once: "which row is my
// machine" — answered with the machine, none, or a typed several-row
// ambiguity, never an implicit rows-first pick. The answer comes from a
// listing snapshot the ask takes itself, and it asks the door for
// nothing: reading is all this ask does. A single row is the machine
// whatever state word it carries — the unknown-state honesty is the
// caller's, reporting the row the door named, never "no machine" — and
// among several rows only the one answerable row (running, asleep, or
// being made) can be the machine; any other several-row listing is the
// anomaly, answered typed so no path can read rows[0] past it.
func TestDoorWhichRowAnswers(t *testing.T) {
	cases := []struct {
		name     string
		live     []string
		asleep   []string
		creating []string
		expired  []string
		want     doorRowAnswer
		wantVM   string // "" — the answer names no machine
	}{
		{
			name: "an empty listing answers none",
			want: doorRowNone,
		},
		{
			name:   "one running row is the machine",
			live:   []string{"vmlive"},
			want:   doorRowMachine,
			wantVM: "vmlive",
		},
		{
			name:   "one sleeping row is the machine",
			asleep: []string{"vms"},
			want:   doorRowMachine,
			wantVM: "vms",
		},
		{
			name:     "one row mid-create is the machine",
			creating: []string{"vmc"},
			want:     doorRowMachine,
			wantVM:   "vmc",
		},
		{
			// the unknown-state honesty starts here: a single row is
			// the machine even when its state word is one this build
			// does not know — the caller reports the row's state, so
			// an expired row is reported as the state it carries,
			// never as "no machine"
			name:    "one dead row is still the machine, its state the caller's to report",
			expired: []string{"vmdead"},
			want:    doorRowMachine,
			wantVM:  "vmdead",
		},
		{
			// the anomaly, typed: the one-machine law says the
			// Customer has one machine, so two rows that both answer
			// are a server-side anomaly — the answer admits it instead
			// of naming whichever row sorted first
			name: "several answerable rows are a typed ambiguity, never rows[0]",
			live: []string{"vmA", "vmB"},
			want: doorRowSeveral,
		},
		{
			name:   "a running row and a sleeper are the same typed ambiguity",
			live:   []string{"vmlive"},
			asleep: []string{"vms"},
			want:   doorRowSeveral,
		},
		{
			name:    "the one answerable row among dead ones is the machine",
			live:    []string{"vmlive"},
			expired: []string{"vmdead"},
			want:    doorRowMachine,
			wantVM:  "vmlive",
		},
		{
			name:    "several rows with nothing answerable are the same anomaly",
			expired: []string{"vmA", "vmB"},
			want:    doorRowSeveral,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeServer(t)
			fs.live = tc.live
			fs.asleep = tc.asleep
			fs.creating = tc.creating
			fs.expired = tc.expired

			which, row, err := askWhichRow(api{host: fs.URL})
			if err != nil {
				t.Fatal(err)
			}
			if which != tc.want {
				t.Fatalf("the ask answered %v, want %v", which, tc.want)
			}
			if tc.wantVM == "" && row != nil {
				t.Fatalf("%v names no machine, got the row %s", which, row.SandboxID)
			}
			if tc.wantVM != "" && (row == nil || row.SandboxID != tc.wantVM) {
				t.Fatalf("the ask must name the machine %s, got %v", tc.wantVM, row)
			}
			// the which-row ask is a read: it places nothing and
			// costs no ensure ask
			if asks, placed := fs.ensureCounts(); asks != 0 || len(placed) != 0 {
				t.Fatalf("reading the listing places nothing and asks for nothing, got %d asks, placed %v", asks, placed)
			}
		})
	}
}

// TestEnsureQuota409ShowsList, moved from the vm suite and freed of its
// global capture: the quota refusal's whole story — the door's message,
// the Customer's listing and the way forward — renders through the
// writer the caller handed the ask, and the refusal comes back as an
// ordinary error, because the exit code is the main error path's to
// decide, not a helper's. The 409 backstop is a refusal: one ask, no
// retry.
func TestEnsureQuota409ShowsList(t *testing.T) {
	fs := newFakeServer(t)
	fs.create = func(w http.ResponseWriter, _ int) {
		http.Error(w, `{"detail":"quota reached (1 live VM)"}`, 409)
	}

	var stderr bytes.Buffer
	_, _, err := askMachine(api{host: fs.URL}, &stderr)
	if err == nil || !strings.Contains(err.Error(), "quota reached") {
		t.Fatalf("want quota-reached error, got %v", err)
	}
	var ee *exitError
	if errors.As(err, &ee) {
		t.Fatalf("the door owns no exit policy: the quota refusal must be an ordinary error, got %T", err)
	}
	story := stderr.String()
	if !strings.Contains(story, "quota reached (1 live VM)") {
		t.Fatalf("the door's refusal message must render to the caller's writer, got %q", story)
	}
	if !strings.Contains(story, "your VMs:") || !strings.Contains(story, "LIVE") {
		t.Fatalf("the listing must render to the caller's writer, got %q", story)
	}
	// attach is gone, so the hint must not teach it — and the way forward
	// must still be stated: the listing names the machines, kill frees a slot
	if strings.Contains(story, "attach") {
		t.Fatalf("quota-409 hint must not teach the removed attach command, got %q", story)
	}
	if !strings.Contains(story, "zert ls") {
		t.Fatalf("quota-409 hint must state a way forward that is not attach, got %q", story)
	}
	if !strings.Contains(story, "zert kill <id>") {
		t.Fatalf("quota-409 hint must still offer zert kill, got %q", story)
	}
	if asks, _ := fs.ensureCounts(); asks != 1 {
		t.Fatalf("a 409 refusal must not be retried, got %d asks", asks)
	}
}
