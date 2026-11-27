package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// errNoLiveVM is the house wording for a flow that needs a running vm and
// found none; both attach and extract point at the commands that make one.
var errNoLiveVM = errors.New("no live vm — start one with `zert vm` or `zert <path>` first")

// attachRun is cmdAttach with injectable stdio and ssh binary (for the runner
// seam): it resolves the vm to enter and opens the interactive ssh session on
// the same tunnel path `zert vm` uses. Leaving that session never kills the
// vm — only `zert kill` or the vm's expiry ends it.
func attachRun(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	id, rest := splitAttachArgs(args)
	if id != "" && !validSandboxID(id) {
		return errors.New("invalid vm id") // same guard as kill and tunnel
	}
	// same arg rules as zert vm: --keep only warns, the rest reaches ssh verbatim
	sshArgs := parseSSHArgs(stderr, rest)
	creds, a := requireLogin()
	rows, err := listVMs(a)
	if err != nil {
		return err
	}
	vm, err := resolveAttachTarget(a, rows, id, stderr)
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "attaching to vm %s …\n", vm.SandboxID)
	return enterVM(creds, vm, sshArgs, stdin, stdout, stderr)
}

// errNoSuchVM is the house wording for an id that is not one of the
// Customer's machines: absent from their listing, or an ensure ask that
// answered a different machine. It points at the listing, never at a
// machine the Customer did not name.
func errNoSuchVM(id string) error {
	return fmt.Errorf("no such vm %s — zert ls lists your vms", id)
}

// resolveAttachTarget picks the vm to enter: the one named by id, or the lone
// live vm when no id was given. Under the sleep model a machine that is
// Asleep is the Customer's machine waiting, not an expired one — it is woken
// through the door's ensure ask before it is handed back, so no relay ever
// dials a sleeping id. A row the door cannot hand over at all keeps the
// expired wording, and an id the ask does not answer keeps the not-found one.
func resolveAttachTarget(a api, rows []vmRow, id string, stderr io.Writer) (*vmRow, error) {
	if id != "" {
		for i := range rows {
			if rows[i].SandboxID != id {
				continue
			}
			switch {
			case rows[i].Live:
				return &rows[i], nil
			case rows[i].Asleep:
				// the sleeper is woken through the same ask that powers
				// the stable name — ensure is the door's one answer to
				// "give me my machine" — before the relay opens
				fmt.Fprintf(stderr, "vm %s is asleep — waking it …\n", id)
				vm, err := ensureVM(a)
				if err != nil {
					return nil, err
				}
				if vm.SandboxID != id {
					// ensure settled the gone sleeper and placed the
					// fresh machine: the named id is not one the
					// Customer has, and the fresh one is not the
					// machine they asked to enter. The refusal
					// minted that machine — ensure is the door's
					// only wake path and no non-placing
					// wake-by-id exists, so an ask for a sleeper
					// that turns out genuinely gone places one by
					// the door's contract, and this guard declines
					// to enter what the Customer did not name
					// (TestAttachByIDGoneSleeperRefusedByAnswer)
					return nil, errNoSuchVM(id)
				}
				return vm, nil
			default:
				// the server would only answer a bare 4410 close on
				// this dial — say what it means instead
				return nil, fmt.Errorf("vm %s expired — start a new one with `zert vm`", id)
			}
		}
		return nil, errNoSuchVM(id)
	}
	live := liveVMs(rows)
	switch {
	case len(live) == 1:
		return live[0], nil
	case len(live) > 1:
		ids := make([]string, 0, len(live))
		for _, r := range live {
			ids = append(ids, r.SandboxID)
		}
		return nil, fmt.Errorf("%d live vms (%s) — name the one you want: zert attach <id>",
			len(live), strings.Join(ids, ", "))
	}
	// no live vm, but the Customer's one machine may be Asleep: the same
	// ensure ask that places one for a Customer with nothing wakes it, and
	// whatever machine it answers is the one machine — only a Customer
	// with nothing at all is sent to the commands that make one
	if hasAsleepVM(rows) {
		fmt.Fprintln(stderr, "no live vm — waking yours …")
		return ensureVM(a)
	}
	return nil, errNoLiveVM
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

// splitAttachArgs takes the vm id off the args, wherever it was placed.
// A leading bare token is the id position — valid or not, so attachRun
// answers a malformed id with "invalid vm id". Otherwise the args are read
// the way ssh itself reads them (the sshOptsWithArg table shared with
// hasUserRemoteCommand): the first token that is neither an option nor an
// option's value is the id candidate, removed and returned when it
// validates as a vm id. A bare token that is not a vm id stays exactly
// where ssh reads it — the remote command — and nothing past ssh's "--"
// is ever the id.
func splitAttachArgs(args []string) (id string, sshArgs []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--": // end of options: command territory from here on
			return "", args
		case strings.HasPrefix(a, "-") && a != "-":
			if len(a) == 2 && strings.ContainsRune(sshOptsWithArg, rune(a[1])) {
				i++ // this option's value is the next token, not a bare one
			}
		case validSandboxID(a):
			return a, append(args[:i:i], args[i+1:]...)
		default:
			return "", args // not a vm id: leave it as the remote command
		}
	}
	return "", args
}

// liveVMs returns the rows that are live, in listing order.
func liveVMs(rows []vmRow) []*vmRow {
	var out []*vmRow
	for i := range rows {
		if rows[i].Live {
			out = append(out, &rows[i])
		}
	}
	return out
}

func cmdAttach(args []string) error {
	return attachRun(args, os.Stdin, os.Stdout, os.Stderr)
}
