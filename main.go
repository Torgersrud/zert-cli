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
	fmt.Fprint(os.Stderr, `zert — your hosted vm, from the command line

  zert login [--stdin]        authenticate (GUIs: the --stdin contract is in the README)
  zert vm                     your machine, and an ssh session into it
  zert <path>                 copy a local file or dir into your vm, then enter it
  zert extract <vm-path>      copy a file or dir out of your vm
  zert status                 your machine in plain words — running, asleep, or none yet
  zert ls                     the technical listing: ids, state words, time left
  zert kill [<id>..]          end vms — bare kill ends every one you have, same as all

read more: github.com/Torgersrud/zert-cli#readme
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
