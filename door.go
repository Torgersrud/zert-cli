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
//     error — the module prints to no global and owns no exit policy;
//   - the which-row ask: "which row is my machine" — answered with the
//     machine, none, or a typed several-row ambiguity, from a listing
//     snapshot the ask takes itself; no path reads a listing row past
//     that answer, so the anomaly is always admitted, never guessed at.
//
// The module takes an api value and never calls requireLogin: it is not
// a command, and the login and exit policy are the runner's business —
// the stable name builds its own api from resolveTunnelTarget. The
// interface is two asks: askMachine ("give me my machine") and
// askWhichRow ("which row is my machine").

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	vm, err := ensureVM(a, stderr)
	if err != nil {
		return nil, 0, err
	}
	return vm, outcome, nil
}

// askWhichRow is the door's second ask: "which row is my machine" —
// answered from a listing snapshot the ask takes itself, never a
// caller's. The answer is typed: the machine (a single row whatever
// state word it carries, or the one answerable row among several),
// none, or a several-row ambiguity the door refuses to resolve — the
// one-machine law says the Customer has one machine, so a listing that
// does not name it is an anomaly to admit, never rows[0] to act on.
// The ask renders nothing and costs no ensure ask: the callers that
// need a machine whatever it takes ask askMachine, and the ones that
// read this answer — status, the copy arms — decide what it means on
// their own paths.
func askWhichRow(a api) (doorRowAnswer, *vmRow, error) {
	rows, err := listVMs(a)
	if err != nil {
		return 0, nil, err
	}
	switch {
	case len(rows) == 0:
		return doorRowNone, nil, nil
	case len(rows) == 1:
		// one row is the machine even when its state word is one this
		// build does not know: the door names the machine, the caller
		// reports the state — never "no machine"
		return doorRowMachine, &rows[0], nil
	}
	var answerable []vmRow
	for _, r := range rows {
		if answerableRow(r) {
			answerable = append(answerable, r)
		}
	}
	if len(answerable) != 1 {
		return doorRowSeveral, nil, nil
	}
	return doorRowMachine, &answerable[0], nil
}

// answerableRow reports whether a listed row is a machine the door can
// answer — running, asleep, or being made. Expired rows and state words
// this build has never seen are dead rows: they answer nothing.
func answerableRow(r vmRow) bool {
	return r.Live || r.Asleep || r.Creating
}

// doorRowAnswer is the which-row ask's answer: the machine, none, or a
// typed ambiguity — the several-row anomaly that no path may resolve by
// reading a listing row past the answer.
type doorRowAnswer int

const (
	// doorRowMachine: the listing names the Customer's one machine — a
	// single row whatever state it carries, or the one answerable row
	// among several.
	doorRowMachine doorRowAnswer = iota + 1

	// doorRowNone: nothing is listed.
	doorRowNone

	// doorRowSeveral: several rows that do not reduce to one machine —
	// a server-side anomaly under the one-machine law, answered typed
	// so every path admits it instead of acting on an arbitrary row.
	doorRowSeveral
)

func (r doorRowAnswer) String() string {
	switch r {
	case doorRowMachine:
		return "machine"
	case doorRowNone:
		return "none"
	case doorRowSeveral:
		return "several"
	}
	return "unknown row answer"
}

// ensureVM is the wire ask itself — POST /v1/vm, the door's idempotent
// ensure call ("give me my machine"): a live machine is answered as it
// is, an Asleep one is woken, and only a Customer with nothing gets one
// placed. The response says what the machine is, never which of the
// three happened — the outcome story is askMachine's, derived from the
// snapshot it took.
func ensureVM(a api, stderr io.Writer) (*vmRow, error) {
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
		// the quota backstop: the whole listing and the way forward
		// render through the caller's writer, and the refusal comes
		// back as an ordinary error — the exit code is the main error
		// path's to decide, never a helper's
		fmt.Fprintln(stderr, detail(data))
		fmt.Fprintln(stderr, "your VMs:")
		if rows, err := listVMs(a); err == nil {
			printVMs(stderr, rows)
		}
		// attach is gone: the way forward is the listing that names the
		// machines and kill's freed slot — never a command that no
		// longer exists
		fmt.Fprintln(stderr, "hint: zert ls lists your vms; "+
			"zert kill <id> (or zert kill all) frees a slot")
		return nil, errors.New("quota reached")
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
