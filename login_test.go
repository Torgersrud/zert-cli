package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

func stubLogin(t *testing.T, status int, body any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/login" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(body)
	}))
}

func overridePrompts(t *testing.T, email, password string) {
	t.Helper()
	oldLine, oldSecret := promptLine, promptSecret
	promptLine = func(string) (string, error) { return email, nil }
	promptSecret = func(string) (string, error) { return password, nil }
	t.Cleanup(func() { promptLine, promptSecret = oldLine, oldSecret })
}

func TestLoginSuccessSavesCredentials(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ts := stubLogin(t, 200, map[string]any{"token": "tok-abc", "ssh_pubkey": nil})
	defer ts.Close()
	overridePrompts(t, "bob@example.com", "hunter2")

	if err := cmdLogin([]string{"--host", ts.URL}); err != nil {
		t.Fatal(err)
	}
	c, err := loadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if c.Token != "tok-abc" || c.Email != "bob@example.com" || c.Host != ts.URL {
		t.Fatalf("saved credentials wrong: %+v", *c)
	}
}

func TestLoginBadPassword(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ts := stubLogin(t, 401, map[string]string{"detail": "invalid credentials"})
	defer ts.Close()
	overridePrompts(t, "bob@example.com", "wrong")

	err := cmdLogin([]string{"--host", ts.URL})
	if err == nil || !strings.Contains(err.Error(), "invalid credentials") {
		t.Fatalf("want invalid-credentials error, got %v", err)
	}
	if _, err := loadCredentials(); err == nil {
		t.Fatal("credentials saved despite failed login")
	}
}

func TestLoginHostPrecedence(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ts := stubLogin(t, 200, map[string]any{"token": "tok-abc"})
	defer ts.Close()
	overridePrompts(t, "bob@example.com", "x")
	t.Setenv("ZERT_HOST", ts.URL)

	if err := cmdLogin(nil); err != nil {
		t.Fatal(err)
	}
	c, _ := loadCredentials()
	if c.Host != ts.URL {
		t.Fatalf("ZERT_HOST not honored: %s", c.Host)
	}
}

func TestLoginRejectsInsecureHostBeforeNetwork(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	overridePrompts(t, "bob@example.com", "x")

	// no server needed: checkHost must fail before any dial
	if err := cmdLogin([]string{"--host", "http://example.com"}); err == nil {
		t.Fatal("expected error for non-loopback http host")
	}
	if _, err := loadCredentials(); err == nil {
		t.Fatal("credentials saved despite rejected host")
	}
}

func TestMeSessionExpiredMapping(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"detail":"invalid or expired token"}`, 401)
	}))
	defer ts.Close()
	_, err := getMe(api{host: ts.URL, token: "stale"})
	if err == nil || !strings.Contains(err.Error(), "session expired — run `zert login`") {
		t.Fatalf("want session-expired message, got %v", err)
	}
}

// --- `zert login --stdin` — the GUI's non-interactive mode -----------------
//
// The zert app drives this binary as its hidden engine and must log a
// customer in without prompts and without secrets in `ps`: credentials
// arrive as two lines on stdin, the outcome as one machine-readable word
// on stdout (ok, bad-credentials, unprovisioned, rate-limited,
// error: <reason>), mirrored by exit status 0-4 so the app can map each to
// its own plain-word sentence. The tests below pin that contract from the
// outside only: argv in, stdin in, stdout/stderr/credentials-file out, and
// what the fake API observed on the wire.

// loginRecord is what the fake login server saw: the credential pair that
// reached the wire and how many times it was asked.
type loginRecord struct {
	mu       sync.Mutex
	email    string
	password string
	calls    int
}

func (r *loginRecord) seen() (email, password string, calls int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.email, r.password, r.calls
}

// stubLoginRecording is stubLogin plus it records the request body, so a
// test can pin that stdin's contents — and never argv — became the wire's
// credential pair.
func stubLoginRecording(t *testing.T, status int, body any) (*httptest.Server, *loginRecord) {
	t.Helper()
	rec := &loginRecord{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/login" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		var sent map[string]string
		json.NewDecoder(r.Body).Decode(&sent)
		rec.mu.Lock()
		rec.calls++
		rec.email, rec.password = sent["email"], sent["password"]
		rec.mu.Unlock()
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(ts.Close)
	return ts, rec
}

// feedStdin replaces os.Stdin with input — the pipe a GUI writes the
// credential pair to.
func feedStdin(t *testing.T, input string) {
	t.Helper()
	old := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatal(err)
	}
	w.Close()
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old })
}

// forbidPrompts arms the interactive prompters to fail the test: stdin mode
// must never scrape a prompt.
func forbidPrompts(t *testing.T) {
	t.Helper()
	oldLine, oldSecret := promptLine, promptSecret
	promptLine = func(string) (string, error) {
		t.Error("stdin mode prompted for the email")
		return "", nil
	}
	promptSecret = func(string) (string, error) {
		t.Error("stdin mode prompted for the password")
		return "", nil
	}
	t.Cleanup(func() { promptLine, promptSecret = oldLine, oldSecret })
}

// wantExitCode asserts err carries the exit status the GUI branches on.
func wantExitCode(t *testing.T, err error, code int) {
	t.Helper()
	if err == nil {
		t.Fatalf("want exit status %d, got success", code)
	}
	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("want exit status %d, got plain error %v", code, err)
	}
	if ee.code != code {
		t.Fatalf("want exit status %d, got %d (%v)", code, ee.code, err)
	}
}

func TestLoginStdinSuccessPrintsOKAndSavesCredentials(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ts, rec := stubLoginRecording(t, 200, map[string]any{"token": "tok-abc", "ssh_pubkey": nil})
	feedStdin(t, "bob@example.com\nhunter2\n")
	forbidPrompts(t)

	var err error
	out := captureStdout(t, func() { err = cmdLogin([]string{"--stdin", "--host", ts.URL}) })
	if err != nil {
		t.Fatal(err)
	}
	if out != "ok\n" {
		t.Fatalf("stdin mode's stdout is the GUI's contract: want %q, got %q", "ok\n", out)
	}
	email, password, _ := rec.seen()
	if email != "bob@example.com" || password != "hunter2" {
		t.Fatalf("stdin's pair never reached the wire: server saw email=%q password=%q", email, password)
	}
	c, err := loadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if c.Token != "tok-abc" || c.Email != "bob@example.com" || c.Host != ts.URL {
		t.Fatalf("saved credentials wrong: %+v", *c)
	}
}

func TestLoginStdinAcceptsCRLFLines(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ts, rec := stubLoginRecording(t, 200, map[string]any{"token": "tok-abc"})
	feedStdin(t, "bob@example.com\r\nhunter2\r\n")

	var err error
	out := captureStdout(t, func() { err = cmdLogin([]string{"--stdin", "--host", ts.URL}) })
	if err != nil {
		t.Fatal(err)
	}
	if out != "ok\n" {
		t.Fatalf("want %q, got %q", "ok\n", out)
	}
	_, password, _ := rec.seen()
	if password != "hunter2" {
		t.Fatalf("CRLF line ending leaked into the password: %q", password)
	}
}

// TestLoginStdinRefusesArgvCredentials pins the ticket's security rule:
// there is no way to hand --stdin mode credentials as arguments, so nothing
// can appear in `ps` output. A stray positional is refused before the
// server is ever dialed.
func TestLoginStdinRefusesArgvCredentials(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ts, rec := stubLoginRecording(t, 200, map[string]any{"token": "tok-abc"})
	feedStdin(t, "bob@example.com\nhunter2\n")

	var err error
	out := captureStdout(t, func() {
		err = cmdLogin([]string{"--stdin", "--host", ts.URL, "bob@example.com", "hunter2"})
	})
	wantExitCode(t, err, 4)
	if !strings.Contains(err.Error(), "never argv") {
		t.Fatalf("refusal must name the rule, got %v", err)
	}
	if !strings.HasPrefix(out, "error: ") {
		t.Fatalf("stdin mode's failures are machine-readable too, got stdout %q", out)
	}
	if _, _, calls := rec.seen(); calls != 0 {
		t.Fatalf("server dialed despite argv credentials: %d calls", calls)
	}
	if _, err := loadCredentials(); err == nil {
		t.Fatal("credentials saved despite refused login")
	}
}

// The outcome words: each server answer a GUI must tell apart gets its own
// machine-readable line on stdout and its own exit status. 401 is wrong
// credentials, 403 the server's word for an account that is not fully
// provisioned — the two the ticket says must never collapse into one.
func TestLoginStdinBadCredentialsAnswer(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ts, _ := stubLoginRecording(t, 401, map[string]string{"detail": "invalid credentials"})
	feedStdin(t, "bob@example.com\nwrong\n")

	var err error
	out := captureStdout(t, func() { err = cmdLogin([]string{"--stdin", "--host", ts.URL}) })
	wantExitCode(t, err, 1)
	if out != "bad-credentials\n" {
		t.Fatalf("want %q, got %q", "bad-credentials\n", out)
	}
	if _, err := loadCredentials(); err == nil {
		t.Fatal("credentials saved despite failed login")
	}
}

func TestLoginStdinUnprovisionedAccountAnswer(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ts, _ := stubLoginRecording(t, 403, map[string]string{"detail": "account disabled"})
	feedStdin(t, "bob@example.com\nhunter2\n")

	var err error
	out := captureStdout(t, func() { err = cmdLogin([]string{"--stdin", "--host", ts.URL}) })
	wantExitCode(t, err, 2)
	if out != "unprovisioned\n" {
		t.Fatalf("want %q, got %q — wrong credentials and an unprovisioned account must answer differently", "unprovisioned\n", out)
	}
	if _, err := loadCredentials(); err == nil {
		t.Fatal("credentials saved despite failed login")
	}
}

func TestLoginStdinRateLimitedAnswer(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ts, _ := stubLoginRecording(t, 429, map[string]string{"detail": "too many attempts, retry later"})
	feedStdin(t, "bob@example.com\nhunter2\n")

	var err error
	out := captureStdout(t, func() { err = cmdLogin([]string{"--stdin", "--host", ts.URL}) })
	wantExitCode(t, err, 3)
	if out != "rate-limited\n" {
		t.Fatalf("want %q, got %q", "rate-limited\n", out)
	}
}

func TestLoginStdinUnreachableServerErrorAnswer(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ts, _ := stubLoginRecording(t, 200, map[string]any{"token": "tok-abc"})
	ts.Close() // the GUI's next branch: nothing answered
	feedStdin(t, "bob@example.com\nhunter2\n")

	var err error
	out := captureStdout(t, func() { err = cmdLogin([]string{"--stdin", "--host", ts.URL}) })
	wantExitCode(t, err, 4)
	if !strings.HasPrefix(out, "error: ") {
		t.Fatalf("want an error: <reason> line, got %q", out)
	}
	if _, err := loadCredentials(); err == nil {
		t.Fatal("credentials saved despite failed login")
	}
}

func TestLoginStdinWantsTwoLines(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ts, rec := stubLoginRecording(t, 200, map[string]any{"token": "tok-abc"})
	feedStdin(t, "bob@example.com\n") // password line missing

	var err error
	out := captureStdout(t, func() { err = cmdLogin([]string{"--stdin", "--host", ts.URL}) })
	wantExitCode(t, err, 4)
	if !strings.Contains(err.Error(), "two lines") {
		t.Fatalf("input-format error must name the format, got %v", err)
	}
	if !strings.HasPrefix(out, "error: ") {
		t.Fatalf("stdin mode's failures are machine-readable too, got stdout %q", out)
	}
	if _, _, calls := rec.seen(); calls != 0 {
		t.Fatalf("server dialed despite malformed stdin: %d calls", calls)
	}
}

// TestLoginInteractiveAccountDisabledKeepsVocabulary pins that the new 403
// branch changed nothing for humans at a terminal: the same apiError
// vocabulary, no exitError, so `zert login` keeps its single exit status
// for every refused login.
func TestLoginInteractiveAccountDisabledKeepsVocabulary(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ts := stubLogin(t, 403, map[string]string{"detail": "account disabled"})
	defer ts.Close()
	overridePrompts(t, "bob@example.com", "hunter2")

	err := cmdLogin([]string{"--host", ts.URL})
	if err == nil || !strings.Contains(err.Error(), "account disabled") {
		t.Fatalf("want account-disabled error, got %v", err)
	}
	var ee *exitError
	if errors.As(err, &ee) {
		t.Fatalf("interactive refusals carry no exit status of their own, got %d", ee.code)
	}
	if _, err := loadCredentials(); err == nil {
		t.Fatal("credentials saved despite failed login")
	}
}

// TestUsageDocumentsStdinLogin pins the mode's contract where a GUI author
// reads it: the flag, the two-line stdin format, the never-argv rule and
// the outcome words all appear in `zert help`.
func TestUsageDocumentsStdinLogin(t *testing.T) {
	out := captureStderr(t, usage)
	for _, want := range []string{"--stdin", "two lines", "never argv", "bad-credentials", "unprovisioned", "rate-limited"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage is the pinned contract: %q missing from it", want)
		}
	}
}
