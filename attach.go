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
	vm, err := resolveAttachTarget(rows, id)
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "attaching to vm %s …\n", vm.SandboxID)
	return enterVM(creds, vm, sshArgs, stdin, stdout, stderr)
}

// resolveAttachTarget picks the vm to enter: the one named by id, or the lone
// live vm when no id was given.
func resolveAttachTarget(rows []vmRow, id string) (*vmRow, error) {
	if id != "" {
		for i := range rows {
			if rows[i].SandboxID == id {
				if !rows[i].Live {
					// the server would only answer a bare 4410 close on
					// this dial — say what it means instead
					return nil, fmt.Errorf("vm %s expired — start a new one with `zert vm`", id)
				}
				return &rows[i], nil
			}
		}
		return nil, fmt.Errorf("no such vm %s — zert ls lists your vms", id)
	}
	live := liveVMs(rows)
	switch len(live) {
	case 0:
		return nil, errNoLiveVM
	case 1:
		return live[0], nil
	default:
		ids := make([]string, 0, len(live))
		for _, r := range live {
			ids = append(ids, r.SandboxID)
		}
		return nil, fmt.Errorf("%d live vms (%s) — name the one you want: zert attach <id>",
			len(live), strings.Join(ids, ", "))
	}
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
