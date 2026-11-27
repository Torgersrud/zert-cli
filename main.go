package main

import (
	"errors"
	"fmt"
	"os"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }

func usage() {
	fmt.Fprint(os.Stderr, `zert — customer CLI

usage:
  zert login [--host URL] [--insecure]
                                 authenticate and store credentials
  zert login --stdin [--host URL] [--insecure]
                                 non-interactive login for GUIs: the
                                 email then the password arrive as
                                 two lines on stdin — never argv, so
                                 nothing shows in process listings; the
                                 outcome is one machine-readable word on
                                 stdout (ok, bad-credentials,
                                 unprovisioned, rate-limited,
                                 error: <reason>) with the matching
                                 exit status 0-4
  zert logout                    delete stored credentials
  zert me                        show profile, quota, pubkey
  zert key register              register the account's ssh public key —
                                 the key line arrives on stdin, never argv
                                 (zert key register < ~/.ssh/id_ed25519.pub);
                                 one account has one active laptop, so
                                 registering over a previous key displaces
                                 that laptop — its key stops working
  zert status                    your machine in plain words — running with
                                 its time left, asleep, or none yet
  zert ls                        the technical listing: raw ids with state
                                 words (running, asleep, creating) and
                                 time left
  zert vm [ssh args..]           your VM and an ssh session into it — a
                                 machine is placed only if you have none,
                                 a sleeping one wakes; leaving the
                                 session never kills the VM — only zert
                                 kill ends it — an idle machine falls
                                 asleep and your next connect wakes it
  zert attach [id] [ssh args..]
                                 attach to your VM — the same shell comes
                                 back, and the machine need not be
                                 running: a sleeping one wakes (with one
                                 VM the id is optional, with several
                                 running it is required, with none it
                                 says so); leaving this session never
                                 kills the VM either
  zert kill <id>..|all           terminate VM(s)
  zert extract <vm-path> [dir]   copy a file/dir from your live vm
                                 into [dir] (default: current dir); vm
                                 paths resolve against the vm user's home
  zert <path> [ssh args..]
                                 copy a local dir/file into your vm
                                 (wakes a sleeping one, places a new one
                                 if you have none, and sshs into it after
                                 the copy; the vm stays up when you
                                 leave — extra ssh args also enter an
                                 already-running vm);
                                 names may only use letters, digits and
                                 ._@%+=:,- — rename or archive the rest;
                                 the copy is streamed as one compressed
                                 tar, symlinks are followed

host: --host flag > $ZERT_HOST > stored credentials
      (tunnel: $ZERT_HOST must match your logged-in host unless
       $ZERT_TUNNEL_TOKEN is also set)
`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch cmd := os.Args[1]; cmd {
	case "login":
		err = cmdLogin(os.Args[2:])
	case "logout":
		err = cmdLogout(os.Args[2:])
	case "me":
		err = cmdMe(os.Args[2:])
	case "key":
		if looksLikePath(cmd) { // a local file/dir named "key" still uploads
			err = cmdUpload(os.Args[1:])
		} else {
			err = cmdKey(os.Args[2:])
		}
	case "status":
		err = cmdStatus(os.Args[2:])
	case "ls":
		err = cmdLS(os.Args[2:])
	case "vm":
		err = cmdVM(os.Args[2:])
	case "attach":
		err = cmdAttach(os.Args[2:])
	case "kill":
		err = cmdKill(os.Args[2:])
	case "extract":
		err = cmdExtract(os.Args[2:])
	case "tunnel": // hidden: used as ssh ProxyCommand
		err = cmdTunnel(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	case "version", "-version", "--version":
		fmt.Println(version)
	default:
		if !looksLikePath(cmd) {
			fmt.Fprintf(os.Stderr, "zert: unknown command %q\n\n", cmd)
			usage()
			os.Exit(2)
		}
		err = cmdUpload(os.Args[1:])
	}
	if err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			if ee.err != nil {
				fmt.Fprintln(os.Stderr, "zert:", ee.err)
			}
			os.Exit(ee.code)
		}
		fmt.Fprintln(os.Stderr, "zert:", err)
		os.Exit(1)
	}
}
