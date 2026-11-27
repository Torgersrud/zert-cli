package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The key register contract, as the zert app's wizard calls it (issue #20):
// argv is exactly `key register`, the public key line arrives on stdin —
// never argv, where a process listing could read it — and POST /v1/me/key
// is the one endpoint that sets or rotates the account key. The tests here
// drive keyRegisterRun/cmdKey, the runner seam the other verbs use too,
// and observe only what a caller observes: the POST the fake server got,
// the error vocabulary, and the exit path (a returned error is exit 1).

// TestKeyRegisterPostsStdinKey pins the happy path: the key line arrives on
// stdin with its trailing newline, is posted to /v1/me/key as the clean
// line, and the verb reports the registration on stderr.
func TestKeyRegisterPostsStdinKey(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)

	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAppwizard bob@laptop"
	var stderr strings.Builder
	if err := keyRegisterRun(strings.NewReader(key+"\n"), &stderr); err != nil {
		t.Fatal(err)
	}
	fs.mu.Lock()
	uploaded := fs.keyUploaded
	fs.mu.Unlock()
	if uploaded != key {
		t.Fatalf("stdin key not posted to /v1/me/key: %q", uploaded)
	}
	if !strings.Contains(stderr.String(), "registered ssh pubkey") {
		t.Fatalf("no registration confirmation on stderr: %q", stderr.String())
	}
}

// TestKeyRegisterRefusesKeyOnArgv pins the security rule the app's seam
// tests rely on: the key line never travels in argv. Any extra argument —
// a key line included — is a usage error, and nothing is posted.
func TestKeyRegisterRefusesKeyOnArgv(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)

	err := cmdKey([]string{"register", "ssh-ed25519 AAAAC3neverInArgv bob"})
	if err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("want usage error for key on argv, got %v", err)
	}
	fs.mu.Lock()
	uploaded := fs.keyUploaded
	fs.mu.Unlock()
	if uploaded != "" {
		t.Fatalf("key from argv was posted anyway: %q", uploaded)
	}
}

// TestKeyRegisterUnknownSubcommandIsUsageError pins that argv is exactly
// `key register`: the bare verb and any other subcommand are usage errors
// decided before a credential is even loaded.
func TestKeyRegisterUnknownSubcommandIsUsageError(t *testing.T) {
	for _, args := range [][]string{{}, {"rotate"}, {"register", "extra", "args"}} {
		if err := cmdKey(args); err == nil || !strings.Contains(err.Error(), "usage") {
			t.Fatalf("want usage error for args %v, got %v", args, err)
		}
	}
}

// TestKeyRegisterInvalidKeyMapsServerError pins the failure path the app
// maps to human words: the server owns validity, so a key it refuses comes
// back as its 400 reason — a returned error, which main turns into exit 1
// with the message on stderr.
func TestKeyRegisterInvalidKeyMapsServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/me/key" {
			http.Error(w, `{"detail":"not a valid openssh public key"}`, 400)
			return
		}
		http.Error(w, `{"detail":"invalid or expired token"}`, 401)
	}))
	defer ts.Close()
	setupHome(t, ts.URL)

	err := keyRegisterRun(strings.NewReader("garbage data\n"), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "not a valid openssh public key") {
		t.Fatalf("want the server's invalid-key reason, got %v", err)
	}
}
