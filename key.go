package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// key register — the account-key verb the zert app's wizard calls after
// generating its dedicated keypair (issue #20). The public key line arrives
// on stdin, never argv: argv is visible to any process listing, and the
// app's seam tests pin that nothing key-shaped ever appears there. POST
// /v1/me/key is the one endpoint that can set or rotate the account key.
//
// Rotation semantics (the one-laptop law, zert-app#1): one account has one
// active laptop, and the account key is what identifies it. Registering a
// new key over a previous one displaces that laptop — the old key stops
// working at once, and with it every Entry from that machine. There is no
// confirm step, here or server-side: the zert app's wizard asks the
// customer beforehand, using `zert me`'s `pubkey:` line as the pre-check.

// cmdKey dispatches the key verb. Today `register` is the only subcommand,
// and argv must spell exactly that — a key line on argv is a usage error,
// never a registration.
func cmdKey(args []string) error {
	if len(args) != 1 || args[0] != "register" {
		return errors.New("usage: zert key register < ~/.ssh/id_ed25519.pub")
	}
	return keyRegisterRun(os.Stdin, os.Stderr)
}

// keyRegisterRun is `zert key register` with injectable stdio — the runner
// seam the tests drive, like vmRun. The key line is read from
// stdin whole, stripped of its trailing newline, and posted to /v1/me/key.
// The server owns validity: a key it refuses comes back as its 400 reason
// (mapped() in api.go), which main turns into exit 1 with the message on
// stderr — the failure shape the app maps to human words.
func keyRegisterRun(stdin io.Reader, stderr io.Writer) error {
	_, a := requireLogin()
	b, err := io.ReadAll(stdin)
	if err != nil {
		return err
	}
	key := strings.TrimSpace(string(b))
	resp, data, err := a.do("POST", "/v1/me/key", map[string]string{"pubkey": key})
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return mapped(resp, data)
	}
	note := "registered ssh pubkey"
	if fields := strings.Fields(key); len(fields) > 0 {
		note += " (" + fields[0] + " …)"
	}
	fmt.Fprintln(stderr, note)
	return nil
}
