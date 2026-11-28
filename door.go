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
//     by comparing the machine id the ask itself answers against the
//     listing snapshot the module takes before it, never guessed from a
//     listing by the caller; an id no row names — or an empty listing —
//     is a placed outcome, so a snapshot that disagrees with the ask
//     still hears the story the ask did;
//   - the story: one wording per outcome, told to the story writer the
//     caller hands over once the ask has answered, from what the ask
//     did — the caller decides whether and where to render, and the
//     stable name renders nothing;
//   - the quota story: the 409 refusal's listing and hint render through
//     the caller's refusal writer — never silenced, because a refusal
//     names the way back — and the refusal comes back as an ordinary
//     error — the module prints to no global and owns no exit policy;
//   - the which-row ask: "which row is my machine" — answered with the
//     machine, none, dead rows, or a typed several-machine ambiguity,
//     from a listing snapshot the ask takes itself; no path reads a
//     listing row past that answer, so the anomaly is always admitted,
//     never guessed at;
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
// module derives it by comparing the machine id the ask answered against
// the snapshot it took before the ask: the id is the ask's own truth.
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

// askMachine is the door's first ask: "give me my machine" — one ensure
// ask, answered with the machine plus which of the three outcomes
// happened. The module takes the listing snapshot itself, before the
// POST, but the wording never comes from the snapshot alone: the machine
// id the ask's own 201 carries is compared against the snapshot's rows
// once the ask has answered, so a snapshot that went stale between the
// listing and the ask still hears the story the ask did. The derivation
// and the wording are the door's; the caller never guesses from a
// listing. story is where the outcome's one wording lands — the caller
// decides whether and where to render, and the stable name passes
// io.Discard, because the story may be silenced. refusal is where a
// refusal's way forward lands: the quota listing and its hint are never
// silenced — a Customer who hit the quota must see what is theirs and
// how a slot frees — so every caller hands its real error stream.
func askMachine(a api, story, refusal io.Writer) (*vmRow, doorOutcome, error) {
	rows, err := listVMs(a)
	if err != nil {
		return nil, 0, err
	}
	vm, err := ensureVM(a, refusal)
	if err != nil {
		return nil, 0, err
	}
	outcome := askOutcome(rows, vm.SandboxID)
	fmt.Fprintln(story, outcome.story())
	return vm, outcome, nil
}

// askOutcome derives which of the three things the ask did from the
// machine id the ask itself answered, compared against the snapshot's
// rows: a row the snapshot shows answerable under that id means the
// machine was already there (answered as-is), a row it shows asleep
// means it was woken, and an id no row names — or an empty listing —
// means one was placed. The snapshot can be stale; the id cannot, so the
// story follows the id.
func askOutcome(rows []vmRow, id string) doorOutcome {
	// the ask's own id, looked up in the snapshot: the rows that carry it
	mine := rowsMatching(rows, func(r vmRow) bool { return r.SandboxID == id })
	switch {
	case len(rowsMatching(mine, rowIsAsleep)) > 0:
		return doorWoken
	case len(rowsMatching(mine, rowIsAnswerable)) > 0:
		return doorAnswered
	}
	// no row carries the ask's id — nothing was listed, or the rows that
	// were are dead ones the ask placed past — so one was placed
	return doorPlaced
}

// askWhichRow is the door's second ask: "which row is my machine" —
// answered from a listing snapshot the ask takes itself, never a
// caller's. The answer is typed: the machine (a single row whatever
// state word it carries, or the one answerable row among several),
// none, several answerable rows — the anomaly the door refuses to
// resolve, because the one-machine law says the Customer has one
// machine and a listing that names several machines it could answer is
// a server-side anomaly to admit, never rows[0] to act on — or dead
// rows, several rows with nothing answerable: the Customer has no
// machine the door could answer, let alone several, so this answer is
// none's equal on every path that acts (the copy arms let the ask
// place) while status, which only reads, admits the listing as unusual.
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
	answerable := rowsMatching(rows, rowIsAnswerable)
	switch len(answerable) {
	case 0:
		return doorRowDead, nil, nil
	case 1:
		return doorRowMachine, &answerable[0], nil
	default:
		return doorRowSeveral, nil, nil
	}
}

// The row predicates, as data: one flag test each, read through the
// one walk below — a sleep-model change edits a predicate here, never
// a loop somewhere else.
var (
	// rowIsLive: a machine already running.
	rowIsLive = func(r vmRow) bool { return r.Live }

	// rowIsAsleep: a sleeper.
	rowIsAsleep = func(r vmRow) bool { return r.Asleep }

	// rowIsAnswerable: a machine the door can answer — running, asleep,
	// or being made. Expired rows and state words this build has never
	// seen are dead rows: they answer nothing.
	rowIsAnswerable = func(r vmRow) bool {
		return rowIsLive(r) || rowIsAsleep(r) || r.Creating
	}
)

// rowsMatching is the one walk the row predicates are read through: the
// rows that satisfy keep, in listing order.
func rowsMatching(rows []vmRow, keep func(vmRow) bool) []vmRow {
	var out []vmRow
	for _, r := range rows {
		if keep(r) {
			out = append(out, r)
		}
	}
	return out
}

// doorRowAnswer is the which-row ask's answer: the machine, none, dead
// rows, or a typed ambiguity — the answers no path may resolve by
// reading a listing row past them.
type doorRowAnswer int

const (
	// doorRowMachine: the listing names the Customer's one machine — a
	// single row whatever state it carries, or the one answerable row
	// among several.
	doorRowMachine doorRowAnswer = iota + 1

	// doorRowNone: nothing is listed.
	doorRowNone

	// doorRowSeveral: several rows that answer — the several-machine
	// anomaly. Under the one-machine law a listing that names several
	// machines the door could answer is a server-side anomaly, answered
	// typed so every path admits it instead of acting on an arbitrary
	// row.
	doorRowSeveral

	// doorRowDead: several rows, every one of them dead — a listing
	// that names rows but no machine the door can answer. The Customer
	// has no machine to choose wrong, so the paths that act read this
	// as none (the machine ask places); status, which only reads,
	// admits the listing as unusual.
	doorRowDead
)

func (r doorRowAnswer) String() string {
	switch r {
	case doorRowMachine:
		return "machine"
	case doorRowNone:
		return "none"
	case doorRowSeveral:
		return "several"
	case doorRowDead:
		return "dead rows"
	}
	return "unknown row answer"
}

// ensureVM is the wire ask itself — POST /v1/vm, the door's idempotent
// ensure call ("give me my machine"): a live machine is answered as it
// is, an Asleep one is woken, and only a Customer with nothing gets one
// placed. The response says what the machine is, never which of the
// three happened — the outcome story is askMachine's, derived from the
// id this ask answered against the snapshot it took. refusal is the
// writer the quota story renders through — the caller's real error
// stream, never silenced.
func ensureVM(a api, refusal io.Writer) (*vmRow, error) {
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
		// render through the caller's refusal writer, and the refusal
		// comes back as an ordinary error — the exit code is the main
		// error path's to decide, never a helper's
		fmt.Fprintln(refusal, detail(data))
		fmt.Fprintln(refusal, "your VMs:")
		if rows, err := listVMs(a); err == nil {
			printVMs(refusal, rows)
		}
		// attach is gone: the way forward is the listing that names the
		// machines and kill's freed slot — never a command that no
		// longer exists
		fmt.Fprintln(refusal, "hint: zert ls lists your vms; "+
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
