package main

// VM ssh invocation module — the one answer to "how does zert run ssh
// against a vm". Two named entry points share every private policy:
//
//   - enterVM: the interactive Entry (`zert vm`, `zert attach`, and the
//     post-copy entry of `zert <path>`), tmux wrap and signal forwarding
//     included;
//   - execInVM: a non-interactive exec with piped stdio (the upload copy
//     stream, `zert extract`), whose interface cannot express the tmux wrap
//     or a forced -t — "a piped stream never gets the wrap" is structural.
//
// The invariants that hold on every path live once here: the base options
// (self-exec ProxyCommand, accept-new host keys, the keepalive pair), the
// user@<id> destination, the token reaching the tunnel child via environment
// and never argv, the spawn/exit error wording, and — per the product's
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

// hasUserRemoteCommand reports whether the forwarded ssh args contain the
// start of a user-supplied remote command: the first token ssh would read
// as neither an option nor an option's value. Unknown option letters count
// as plain flags, so a following bare token lands on the command side —
// erring that way leaves the argv exactly as today (unwrapped), the safe
// reading of "passed through".
func hasUserRemoteCommand(sshArgs []string) bool {
	for i := 0; i < len(sshArgs); i++ {
		a := sshArgs[i]
		switch {
		case strings.HasPrefix(a, "-") && a != "-":
			if len(a) == 2 && strings.ContainsRune(sshOptsWithArg, rune(a[1])) {
				i++ // this option's value is the next token
			}
		default:
			return true
		}
	}
	return false
}

// interactiveRemoteCmd is the remote command an interactive entry runs in
// place of a bare login shell: it enters — creating when missing — the one
// tmux session "zert", so a Detach (detach key, dropped link, dead laptop)
// only drops the viewer and the next Attach restores the same shell. When
// the guest has no tmux (a vm from the pre-tmux Template) the guard falls
// back to a plain login shell, exactly what an unwrapped entry gets.
const interactiveRemoteCmd = `command -v tmux >/dev/null 2>&1 && exec tmux new -As zert || exec "${SHELL:-sh}" -l`

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

// vmSSHCommand assembles what every ssh invocation against a vm shares: the
// base options — the self-exec ProxyCommand dialling this vm through the
// tunnel, accept-new host keys, the keepalive pair — the child's stdio, and
// the session token in the child's environment, never its argv. Callers
// append the destination and any remote command(s) in their path's order.
func vmSSHCommand(creds *Credentials, vm *vmRow, stdin io.Reader, stdout, stderr io.Writer) *exec.Cmd {
	sshCmd := exec.Command(sshPath)
	sshCmd.Args = append(sshCmd.Args,
		"-o", proxyCommand(selfExe(), vm.SandboxID),
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=4",
	)
	sshCmd.Stdin, sshCmd.Stdout, sshCmd.Stderr = stdin, stdout, stderr
	// the tunnel child gets the token via environment, never argv
	sshCmd.Env = childEnv(os.Environ(), creds.Token, creds.Host)
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

// enterVM opens an interactive ssh session to the vm through the tunnel,
// forwarding signals, with the guest login living inside the tmux session
// "zert" so any Detach restores the same shell on the next entry. When the
// session ends — whatever the reason — the vm keeps running: only `zert
// kill` or an expired Idle window ever ends it. Only an *interactive*
// entry wraps: a piped stdin never gets the tmux command or the forced
// -t, exactly the plain ssh of before the wrap existed.
func enterVM(creds *Credentials, vm *vmRow, sshArgs []string, stdin io.Reader, stdout, stderr io.Writer) error {
	// the wrap is the remote command only for an interactive entry without
	// a remote command of the user's own — their args then reach ssh
	// verbatim, unwrapped, exactly as before tmux
	wrapped := stdinIsInteractive() && !hasUserRemoteCommand(sshArgs)
	sshCmd := vmSSHCommand(creds, vm, stdin, stdout, stderr)
	if wrapped {
		// ssh only gives an explicit remote command a tty on request,
		// and tmux needs one
		sshCmd.Args = append(sshCmd.Args, "-t")
	}
	sshCmd.Args = append(sshCmd.Args, sshArgs...)
	sshCmd.Args = append(sshCmd.Args, vmUserDest(vm.SandboxID))
	if wrapped {
		sshCmd.Args = append(sshCmd.Args, interactiveRemoteCmd)
	}

	sigc := make(chan os.Signal, 2)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigc)
	if err := sshCmd.Start(); err != nil {
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

// execInVM runs remoteCmd inside the vm as a non-interactive ssh exec with
// piped stdio — the copy stream of `zert <path>`, the tar of `zert extract`.
// The interface cannot express the tmux wrap or a forced -t, only a fixed
// command over pipes, so "a piped stream never gets the wrap" is a fact of
// the signature rather than a rule to remember. The caller owns any pipes it
// feeds or drains around the call; a non-nil error from here is the
// authoritative failure and outranks stream fallout (an ssh exit error
// arrives as an *exitError carrying ssh's exit code).
//
// NOTE (ticket #12): SIGINT/SIGTERM forwarding joins enterVM's here, inside
// this function — the interface does not change.
func execInVM(creds *Credentials, vm *vmRow, remoteCmd string, stdin io.Reader, stdout, stderr io.Writer) error {
	sshCmd := vmSSHCommand(creds, vm, stdin, stdout, stderr)
	sshCmd.Args = append(sshCmd.Args, vmUserDest(vm.SandboxID), remoteCmd)
	if err := sshCmd.Start(); err != nil {
		// never leave a caller's stream producer writing into a stdin that
		// got no child
		if closer, ok := stdin.(io.Closer); ok {
			_ = closer.Close()
		}
		return fmt.Errorf("spawn ssh: %w", err)
	}
	return sshResult(sshCmd.Wait())
}
