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
  zert logout                    delete stored credentials
  zert me                        show profile, quota, pubkey
  zert ls                        list your VMs
  zert vm [--keep] [ssh args..]  create a VM and ssh into it (kill on exit)
  zert kill <id>                 terminate a VM
   zert <path>                    copy a local dir/file into your vm
                                  (creates one if none is running, and
                                  sshs into it after the copy if this
                                  command created it — killed on exit)
                                  names may only use letters, digits and
                                  ._@%+=:,- — rename or archive the rest
                                  the copy is streamed as one compressed
                                  tar; symlinks are followed

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
	case "ls":
		err = cmdLS(os.Args[2:])
	case "vm":
		err = cmdVM(os.Args[2:])
	case "kill":
		err = cmdKill(os.Args[2:])
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
