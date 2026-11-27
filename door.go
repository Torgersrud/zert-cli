package main

// The door module — the one place that owns the ensure ask and its
// outcome story. Every path that needs the Customer's machine asks it
// and gets the machine plus which of the three outcomes happened, told
// in one wording. What every path through this module gets, once:
//
//   - the ask: POST /v1/vm, "give me my machine" — the door's idempotent
//     ensure, with its one safe retry (a 502 only; refusals are answers,
//     never retried), the 201-running guard and the invalid-id guard;
//   - the outcome: answered as-is, woken or placed — typed here, derived
//     from the listing snapshot the module takes itself before the ask,
//     never guessed from a listing by the caller; a listing whose rows
//     are all unanswerable is told as creating, matching what the ask
//     then does;
//   - the story: one wording per outcome, told to the writer the caller
//     hands over before the ask explains its wait — the caller decides
//     whether and where to render, and the stable name renders nothing;
//   - the quota story: the 409 refusal's listing and hint render through
//     the caller's writer, and the refusal comes back as an ordinary
//     error — the module prints to no global and owns no exit policy.
//
// The module takes an api value and never calls requireLogin: it is not
// a command, and the login and exit policy are the runner's business —
// the stable name builds its own api from resolveTunnelTarget. The
// interface's first ask is askMachine; the second ask ("which row is my
// machine") joins it with landing (b) of spec #30.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
)

// doorOutcome is which of the three things the ensure ask did — the
// answer the interface reports alongside the machine. The 201 body never
// carries an outcome word (state is always "running" by then), so the
// module derives it from the snapshot it took before the ask.
type doorOutcome int

const (
	// doorAnswered: the machine was already there, running, and the ask
	// handed it back as it was.
	doorAnswered doorOutcome = iota + 1

	// doorWoken: the machine was Asleep and the ask woke it.
	doorWoken

	// doorPlaced: nothing answerable was listed and the ask placed a
	// machine.
	doorPlaced
)

func (o doorOutcome) String() string {
	switch o {
	case doorAnswered:
		return "answered as-is"
	case doorWoken:
		return "woken"
	case doorPlaced:
		return "placed"
	}
	return "unknown outcome"
}

// story is the outcome's one wording — the story every path that asks
// tells the Customer, derived from the outcome the ask reports, never
// guessed from a listing by the caller.
func (o doorOutcome) story() string {
	switch o {
	case doorAnswered:
		return "found your vm — entering it"
	case doorWoken:
		return "no live vm — waking yours …"
	case doorPlaced:
		return "no live vm — creating one …"
	}
	return ""
}

// hasLiveVM reports whether any listed row is a machine already running.
func hasLiveVM(rows []vmRow) bool {
	for _, r := range rows {
		if r.Live {
			return true
		}
	}
	return false
}

// hasAsleepVM reports whether any listed row is a sleeper.
func hasAsleepVM(rows []vmRow) bool {
	for _, r := range rows {
		if r.Asleep {
			return true
		}
	}
	return false
}

// askMachine is the door's first ask: "give me my machine" — one ensure
// ask, answered with the machine plus which of the three outcomes
// happened. The outcome is derived from a listing snapshot taken inside
// this ask, before the POST: the response carries no outcome word, and
// the caller never guesses from a listing — the snapshot, the derivation
// and the wording are the door's. stderr is the caller's writer: the
// story is told there before the ask (the wait it explains is the
// door's), and the quota refusal's listing and hint land there on a 409;
// a caller that renders nothing — the stable name — passes io.Discard.
func askMachine(a api, stderr io.Writer) (*vmRow, doorOutcome, error) {
	rows, err := listVMs(a)
	if err != nil {
		return nil, 0, err
	}
	var outcome doorOutcome
	switch {
	case hasLiveVM(rows):
		outcome = doorAnswered
	case hasAsleepVM(rows):
		outcome = doorWoken
	default:
		// rows that are neither running nor asleep — none at all,
		// expired, or a state word this build has never seen — are dead
		// rows: they answer nothing, and the ask places a machine, so
		// the story says creating
		outcome = doorPlaced
	}
	fmt.Fprintln(stderr, outcome.story())
	vm, err := ensureVM(a)
	if err != nil {
		return nil, 0, err
	}
	return vm, outcome, nil
}

// ensureVM is the wire ask itself — POST /v1/vm, the door's idempotent
// ensure call ("give me my machine"): a live machine is answered as it
// is, an Asleep one is woken, and only a Customer with nothing gets one
// placed. The response says what the machine is, never which of the
// three happened — the outcome story is askMachine's, derived from the
// snapshot it took.
func ensureVM(a api) (*vmRow, error) {
	resp, data, err := a.do("POST", "/v1/vm", map[string]any{})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusBadGateway {
		// one automatic retry, and a safe one: the route is idempotent
		// ensure, so a 502 that actually reached the door answered the
		// Customer's machine, and asking again returns that machine
		// instead of placing a second one.
		resp, data, err = a.do("POST", "/v1/vm", map[string]any{})
		if err != nil {
			return nil, err
		}
	}
	switch resp.StatusCode {
	case http.StatusCreated:
	case http.StatusConflict:
		fmt.Fprintln(os.Stderr, detail(data))
		fmt.Fprintln(os.Stderr, "your VMs:")
		if rows, err := listVMs(a); err == nil {
			printVMs(rows)
		}
		// attach is gone: the way forward is the listing that names the
		// machines and kill's freed slot — never a command that no
		// longer exists
		fmt.Fprintln(os.Stderr, "hint: zert ls lists your vms; "+
			"zert kill <id> (or zert kill all) frees a slot")
		return nil, &exitError{1, errors.New("quota reached")}
	default:
		return nil, mapped(resp, data)
	}
	var out struct {
		SandboxID string  `json:"sandbox_id"`
		ExpiresAt float64 `json:"expires_at"`
		State     string  `json:"state"`
	}
	if err := json.Unmarshal(data, &out); err != nil || out.SandboxID == "" {
		return nil, errors.New("malformed ensure response")
	}
	if !validSandboxID(out.SandboxID) {
		return nil, errors.New("malformed server response: invalid vm id")
	}
	// every 201 outcome — created, woken or returned live — hands over a
	// running machine, so anything else is a door that did not keep the
	// contract, and the CLI refuses to enter it
	if out.State != "running" {
		return nil, errors.New("malformed ensure response: vm not running")
	}
	return &vmRow{SandboxID: out.SandboxID, ExpiresAt: out.ExpiresAt, Live: true}, nil
}
