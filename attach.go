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
	keep, sshArgs := parseSSHArgs(rest)
	if keep {
		warnKeepDeprecated(stderr)
	}
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
	return sshVM(creds, a, vm, sshArgs, stdin, stdout, stderr)
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

// splitAttachArgs takes the optional vm id off the front of the args (ids
// never start with "-", ssh args always do); the rest is forwarded to ssh
// verbatim.
func splitAttachArgs(args []string) (id string, sshArgs []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
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
