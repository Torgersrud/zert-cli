package main

// VM ssh invocation module — the one answer to "how does zert run ssh
// against a vm". Two named entry points share every private policy:
//
//   - enterVM: the interactive Entry (`zert vm` and the post-copy entry of
//     `zert <path>`), tmux wrap included;
//   - execInVM: a non-interactive exec with piped stdio (the transfer
//     module's two copy streams), whose interface cannot express the tmux wrap
//     or a forced -t — "a piped stream never gets the wrap" is structural.
//
// The invariants that hold on every path live once here: the base options
// (self-exec ProxyCommand, accept-new host keys, the keepalive pair), the
// user@<id> destination, the token reaching the tunnel child via environment
// and never argv, the ssh argv grammar — one scanner answers, in a single
// walk, the args forwarded verbatim, the deprecated --keep swallow, and
// where a user remote command starts —, the spawn/exit error wording,
// SIGINT/SIGTERM forwarding to the ssh child, and — per the product's
// ADR-0002 — that entering a vm never ends its life: no function in this
// module so much as mentions DELETE; only `zert kill` or the vm's expiry
// does.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/term"
)

// sshPath is the ssh binary seam (tests replace it).
var sshPath = "ssh"

// defaultStdinIsInteractive asks the terminal whether stdin is one — the
// same golang.org/x/term check login.go uses to read the password.
func defaultStdinIsInteractive() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// stdinIsInteractive is the seam that says whether an entry is interactive
// (tests replace it; the production answer reads os.Stdin, which is what
// every entry path hands to ssh).
var stdinIsInteractive = defaultStdinIsInteractive

// childEnv returns base with exactly one ZERT_TUNNEL_TOKEN and one ZERT_HOST,
// set to token/host. Any pre-existing values are removed first (last wins).
func childEnv(base []string, token, host string) []string {
	out := make([]string, 0, len(base)+2)
	for _, kv := range base {
		if strings.HasPrefix(kv, "ZERT_TUNNEL_TOKEN=") || strings.HasPrefix(kv, "ZERT_HOST=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "ZERT_TUNNEL_TOKEN="+token, "ZERT_HOST="+host)
}

// sshOptsWithArg lists the ssh(1) option letters whose value is taken as a
// separate argument (-b -c -D -E -e -F -I -i -J -l -L -m -O -o -p -Q -R -S
// -w): such a value is not positional and never starts a remote command.
const sshOptsWithArg = "bcDEeFIiJlLmOopQRSw"

// sshScan is what one walk of the ssh argv grammar answers: the args to
// forward verbatim to ssh, whether the deprecated --keep was swallowed (it
// changes nothing, and the caller owns its deprecation notice), and
// whether the forwarded args carry the start of a user-supplied remote
// command — the first token ssh would read as neither an option nor an
// option's value.
type sshScan struct {
	args      []string
	sawKeep   bool
	remoteCmd bool
}

// scanSSHArgs is the one scanner of the ssh argv grammar: a single walk
// answers everything any path asks of the customer's ssh arguments — the
// args forwarded verbatim (everything after "--" is passthrough), the
// --keep swallow, and the bare-token position where a user remote command
// starts. The option table above is the grammar's one copy: a change to it
// — a new option-with-value letter, long options — lands here and nowhere
// else, because every consumer reads this scan and nothing walks the args
// again.
func scanSSHArgs(args []string) sshScan {
	var scan sshScan
	options := true       // "--" ends option territory: past it, everything is ssh's
	valuePending := false // the next forwarded token is an option's value, never positional
	for _, a := range args {
		if options {
			switch a {
			case "--":
				options = false
				continue
			case "--keep", "-keep":
				// swallowed wherever option territory finds it — even
				// blind, in an option's value position — because it
				// changes nothing wherever it appears
				scan.sawKeep = true
				continue
			}
		}
		scan.args = append(scan.args, a)
		if scan.remoteCmd {
			continue // the command started earlier; the rest is its words
		}
		if valuePending {
			valuePending = false // an option's value never starts a command
			continue
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			// unknown option letters count as plain flags, so a following
			// bare token lands on the command side — erring that way
			// leaves the argv unwrapped, the safe reading of "passed
			// through"
			if len(a) == 2 && strings.ContainsRune(sshOptsWithArg, rune(a[1])) {
				valuePending = true
			}
			continue
		}
		scan.remoteCmd = true // the bare token: a user command starts here
	}
	return scan
}

// warnKeepDeprecated prints the one deprecation notice for the --keep flag,
// which no longer changes anything: no entry path kills a vm on exit. The
// scanner answers sawKeep; each entry path that scans once prints this at
// most once.
func warnKeepDeprecated(stderr io.Writer) {
	fmt.Fprintln(stderr, "--keep is obsolete — vms no longer die on exit")
}

// TmuxWrapCmd is the remote command an interactive entry runs in place
// of a bare login shell: it enters — creating when missing — the one
// tmux session "zert", so a Detach (detach key, dropped link, dead
// laptop) only drops the viewer and the next Attach restores the same
// shell. When the guest has no tmux (a vm from the pre-tmux Template)
// the guard falls back to a plain login shell, exactly what an
// unwrapped entry gets. It is exported so the suite that pins the ssh
// invocation shares this one literal instead of duplicating it
// byte-for-byte — the wrap is the contract, and a contract is spelled
// once.
const TmuxWrapCmd = `command -v tmux >/dev/null 2>&1 && exec tmux new -As zert || exec "${SHELL:-sh}" -l`

// selfExe is the path the ProxyCommand execs: zert running itself as the
// tunnel child. The bare-name fallback keeps the invocation alive in the
// unlikely case the OS cannot tell us our own path.
func selfExe() string {
	self, err := os.Executable()
	if err != nil {
		self = "zert"
	}
	return self
}

// vmUserDest is the ssh destination inside every vm: the guest user at the
// vm's id. Commands may print it; only this module puts it into an
// invocation.
func vmUserDest(vmID string) string {
	return "user@" + vmID
}

// vmLink is the machine one ssh invocation dials together with the
// credentials its tunnel child needs — the pair every vm ssh path
// travels with, bundled so the invocation and transfer seams carry the
// dial facts as one value instead of a clump of arguments. Naming the
// machine stays the caller's: a link is handed over already resolved.
type vmLink struct {
	creds *Credentials
	vm    *vmRow
}

// vmSSHCommand assembles what every ssh invocation against a vm shares: the
// base options — the self-exec ProxyCommand dialling this vm through the
// tunnel, accept-new host keys, the keepalive pair — the child's stdio, and
// the session token in the child's environment, never its argv. Callers
// append the destination and any remote command(s) in their path's order.
func vmSSHCommand(link vmLink, stdin io.Reader, stdout, stderr io.Writer) *exec.Cmd {
	sshCmd := exec.Command(sshPath)
	sshCmd.Args = append(sshCmd.Args,
		"-o", proxyCommand(selfExe(), link.vm.SandboxID),
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=4",
	)
	sshCmd.Stdin, sshCmd.Stdout, sshCmd.Stderr = stdin, stdout, stderr
	// the tunnel child gets the token via environment, never argv
	sshCmd.Env = childEnv(os.Environ(), link.creds.Token, link.creds.Host)
	return sshCmd
}

// sshResult gives every path the same two failure wordings: the ssh child's
// own exit code is preserved as an exitError so zert exits with what ssh
// exited with, and anything else that went wrong around the child reports
// the one wording "run ssh: %w".
func sshResult(waitErr error) error {
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) {
		return &exitError{ee.ExitCode(), fmt.Errorf("ssh exited with %d", ee.ExitCode())}
	}
	if waitErr != nil {
		return fmt.Errorf("run ssh: %w", waitErr)
	}
	return nil
}

// runWithSignalForwarding is the module's one spawn-and-wait tail, the whole
// signal-forwarding invariant in one place: it registers SIGINT/SIGTERM with
// this process (deregistered on the way out), starts the child, waits on it
// in a goroutine, and when a signal arrives while the child runs it forwards
// that signal to the child before reaping it. The child's fate then goes
// through sshResult, so every entry point reads failure the same way.
func runWithSignalForwarding(sshCmd *exec.Cmd) error {
	sigc := make(chan os.Signal, 2)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigc)
	if err := sshCmd.Start(); err != nil {
		// a child that never was, in the one wording every path reports;
		// no path tells it from "the child ran and lost" — the transfer
		// module releases its codec the same way either way
		return fmt.Errorf("spawn ssh: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- sshCmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case sig := <-sigc:
		_ = sshCmd.Process.Signal(sig)
		waitErr = <-done
	}
	return sshResult(waitErr)
}

// enterVM opens an interactive ssh session to the vm through the tunnel,
// forwarding signals, with the guest login living inside the tmux session
// "zert" so any Detach restores the same shell on the next entry. When the
// session ends — whatever the reason — the vm keeps running: only `zert
// kill` or an expired Idle window ever ends it. Only an *interactive*
// entry wraps: a piped stdin never gets the tmux command or the forced
// -t, exactly the plain ssh of before the wrap existed. The wrap decision
// consumes the scanner's answer — scan.remoteCmd is where the one walk
// judged the user's command to start, so no second walk happens here.
func enterVM(link vmLink, scan sshScan, stdin io.Reader, stdout, stderr io.Writer) error {
	// the wrap is the remote command only for an interactive entry without
	// a remote command of the user's own — their args then reach ssh
	// verbatim, unwrapped, exactly as before tmux
	wrapped := stdinIsInteractive() && !scan.remoteCmd
	sshCmd := vmSSHCommand(link, stdin, stdout, stderr)
	if wrapped {
		// ssh only gives an explicit remote command a tty on request,
		// and tmux needs one
		sshCmd.Args = append(sshCmd.Args, "-t")
	}
	sshCmd.Args = append(sshCmd.Args, scan.args...)
	sshCmd.Args = append(sshCmd.Args, vmUserDest(link.vm.SandboxID))
	if wrapped {
		sshCmd.Args = append(sshCmd.Args, TmuxWrapCmd)
	}
	return runWithSignalForwarding(sshCmd)
}

// execInVM runs remoteCmd inside the vm as a non-interactive ssh exec with
// piped stdio — the transfer module's two copy streams, and nothing else.
// The interface cannot express the tmux wrap or a forced -t, only a fixed
// command over pipes, so "a piped stream never gets the wrap" is a fact of
// the signature rather than a rule to remember. The pipes around the call
// are the transfer module's, and it closes them once this returns either
// way, so a child that never started is released exactly like a finished
// one. A non-nil error from here is the authoritative failure and outranks
// stream fallout (an ssh exit error arrives as an *exitError carrying
// ssh's exit code). Like the interactive Entry, a signal that reaches zert
// is forwarded to the ssh child, so a Ctrl-C during a long copy or extract
// stops that child instead of leaving its fate to chance.
func execInVM(link vmLink, remoteCmd string, stdin io.Reader, stdout, stderr io.Writer) error {
	sshCmd := vmSSHCommand(link, stdin, stdout, stderr)
	sshCmd.Args = append(sshCmd.Args, vmUserDest(link.vm.SandboxID), remoteCmd)
	return runWithSignalForwarding(sshCmd)
}
