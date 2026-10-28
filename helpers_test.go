package main

// Shared test scaffolding for every path that runs ssh against a vm: the
// interactive Entry (`zert vm`, `zert attach`, and the post-copy entry of
// `zert <path>`), upload's copy stream, and `zert extract`.
//
// Nothing here opens a new seam. Every test drives a command runner
// (vmRun/attachRun/uploadRun/extractRun) with injectable stdio against the
// two seams the production code already has: the API through the in-process
// fake below plus the credentials setupHome writes, and the ssh binary
// through the sshPath package var. What a test may observe is what a caller
// of a runner observes — the argv and child environment the fake ssh
// recorded, the HTTP calls the API fake recorded, and the bytes that
// streamed through the pipes — never how the command was assembled.
//
// The assertions at the bottom are the rules all these paths share: the same
// ssh option block and destination, the token in the child's environment and
// nowhere in argv, and no path ever ending a vm's life (the product's
// ADR-0002, ~/dev/docs/adr). Each exists exactly once here, so a drift on one
// path fails the suite.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// --- the API fake -------------------------------------------------------

type fakeServer struct {
	*httptest.Server
	mu          sync.Mutex
	deleted     []string
	keyUploaded string
	pubkey      *string        // what /v1/me reports
	live        []string       // sandbox ids reported as live by GET /v1/vm
	expired     []string       // sandbox ids reported as not live by GET /v1/vm
	deleteFail  map[string]int // id -> status code for failing DELETEs
	create      func(w http.ResponseWriter, tries int)
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	fs := &fakeServer{pubkey: strptr("ssh-ed25519 AAAAC3 serverkey bob")}
	var muCreateMu sync.Mutex
	creates := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/me", func(w http.ResponseWriter, r *http.Request) {
		fs.mu.Lock()
		pub := fs.pubkey
		fs.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{
			"email": "bob@example.com", "ssh_pubkey": pub, "quota": 1,
		})
	})
	mux.HandleFunc("/v1/me/key", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		fs.mu.Lock()
		fs.keyUploaded = body["pubkey"]
		fs.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
	mux.HandleFunc("/v1/vm", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			muCreateMu.Lock()
			creates++
			n := creates
			muCreateMu.Unlock()
			if fs.create != nil {
				fs.create(w, n)
				return
			}
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]any{
				"sandbox_id": "vm123", "expires_at": json.Number("9999999999"),
			})
			return
		}
		fs.mu.Lock()
		var rows []map[string]any
		for _, id := range fs.live {
			rows = append(rows, map[string]any{
				"sandbox_id": id, "expires_at": 9999999999.0, "live": true,
			})
		}
		for _, id := range fs.expired {
			rows = append(rows, map[string]any{
				"sandbox_id": id, "expires_at": 1.0, "live": false,
			})
		}
		fs.mu.Unlock()
		if rows == nil {
			rows = []map[string]any{}
		}
		json.NewEncoder(w).Encode(rows)
	})
	mux.HandleFunc("/v1/vm/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/v1/vm/")
		fs.mu.Lock()
		fs.deleted = append(fs.deleted, id)
		code := fs.deleteFail[id]
		fs.mu.Unlock()
		if code != 0 {
			http.Error(w, `{"detail":"kill refused"}`, code)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"killed": "vm123"})
	})
	fs.Server = httptest.NewServer(mux)
	t.Cleanup(fs.Close)
	return fs
}

func strptr(s string) *string { return &s }

// testToken is the session token every fake login stores, and therefore the
// string the token assertions look for.
const testToken = "tok-9"

// setupHome points HOME/XDG at temp dirs and stores credentials for the fake API.
func setupHome(t *testing.T, host string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if err := saveCredentials(&Credentials{Host: host, Email: "bob@example.com", Token: testToken}); err != nil {
		t.Fatal(err)
	}
	return home
}

// --- the fake-ssh generator family --------------------------------------

// The data directions the fake ssh needs, one per kind of vm ssh invocation:
// an interactive Entry carries nothing worth keeping, the upload copy stream
// is a tar.gz the tests unpack afterwards, and extract plays a prepared
// tar.gz back on the stdout the CLI untars.
const (
	streamSwallowStdin = "cat > /dev/null"
	streamRecordStdin  = `cat > "$FAKE_SSH_STDIN"`
	streamReplayStdout = `cat "$FAKE_SSH_STDOUT"`
)

// fakeSSHScript installs the one fake ssh every path runs: it records argv
// and the tunnel token env of every call, moves the data stream as stream
// says, and exits with the code in FAKE_SSH_EXIT. It returns the log it
// appends to. The three wrappers below differ only in the stream direction —
// exactly the difference between the Entry, the copy and the extract stream.
func fakeSSHScript(t *testing.T, stream string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ssh")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"ARGS $*\" >> \"$FAKE_SSH_LOG\"\n" +
		"printf '%s\\n' \"TOKEN $ZERT_TUNNEL_TOKEN\" >> \"$FAKE_SSH_LOG\"\n" +
		stream + "\n" +
		"exit ${FAKE_SSH_EXIT:-0}\n"
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "ssh.log")
	t.Setenv("FAKE_SSH_LOG", logPath)
	t.Cleanup(func() { sshPath = "ssh" })
	sshPath = path
	return logPath
}

// fakeSSH is an interactive Entry's ssh: it records argv + tunnel token env,
// swallows stdin, and exits with the code from FAKE_SSH_EXIT.
func fakeSSH(t *testing.T) string {
	t.Helper()
	return fakeSSHScript(t, streamSwallowStdin)
}

// fakeSSHRecordingStdin is fakeSSH plus it stores ssh's full stdin (the
// tar.gz stream) at the returned path.
func fakeSSHRecordingStdin(t *testing.T) (logPath, stdinPath string) {
	t.Helper()
	stdinPath = filepath.Join(t.TempDir(), "stdin.tar.gz")
	t.Setenv("FAKE_SSH_STDIN", stdinPath)
	return fakeSSHScript(t, streamRecordStdin), stdinPath
}

// fakeSSHStreamingStdout is fakeSSH with the data direction flipped: instead
// of swallowing stdin it replays the file at $FAKE_SSH_STDOUT (a prepared
// tar.gz) on stdout, which is the pipe the CLI untars. The caller must set
// FAKE_SSH_STDOUT before calling.
func fakeSSHStreamingStdout(t *testing.T) string {
	t.Helper()
	return fakeSSHScript(t, streamReplayStdout)
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// argLines returns the recorded argv of each ssh call, in call order.
func argLines(logged string) []string {
	var out []string
	for _, line := range strings.Split(logged, "\n") {
		if strings.HasPrefix(line, "ARGS ") {
			out = append(out, line)
		}
	}
	return out
}

// --- the assertions every vm ssh path shares ----------------------------

// assertSSHCommonOptions pins the option block every vm ssh invocation puts
// together the same way: the self-exec ProxyCommand that dials this vm
// through the tunnel, accept-new host keys, the keepalive pair, and the
// user@<id> destination. The ProxyCommand check is prefix-anchored on purpose
// — it is the first option on every path, so an assembly that no longer
// starts with it is caught here too.
func assertSSHCommonOptions(t *testing.T, logged, vmID string) {
	t.Helper()
	if !strings.Contains(logged, "ARGS -o ProxyCommand=") || !strings.Contains(logged, "tunnel "+vmID) {
		t.Fatalf("ProxyCommand for %s missing from ssh argv: %s", vmID, logged)
	}
	if !strings.Contains(logged, "-o StrictHostKeyChecking=accept-new") {
		t.Fatalf("ssh host-key policy missing: %s", logged)
	}
	assertSSHKeepalive(t, logged)
	if !strings.Contains(logged, "user@"+vmID) {
		t.Fatalf("ssh destination missing: %s", logged)
	}
}

// assertSSHKeepalive pins the keepalive pair, so no path's session drops
// sooner than another's.
func assertSSHKeepalive(t *testing.T, logged string) {
	t.Helper()
	if !strings.Contains(logged, "-o ServerAliveInterval=30") || !strings.Contains(logged, "-o ServerAliveCountMax=4") {
		t.Fatalf("ssh keepalive options missing: %s", logged)
	}
}

// assertTokenInChildEnvOnly states the security rule once: the session token
// reaches the tunnel child through the ZERT_TUNNEL_TOKEN environment and
// never through argv, where it would land in process listings and logs. The
// fake ssh records the environment value as a TOKEN line and each call's
// argv as ARGS lines, so checking both halves pins the whole rule.
func assertTokenInChildEnvOnly(t *testing.T, logged string) {
	t.Helper()
	if !strings.Contains(logged, "TOKEN "+testToken) {
		t.Fatalf("token not passed via ZERT_TUNNEL_TOKEN env: %s", logged)
	}
	for _, line := range strings.Split(logged, "\n") {
		if strings.HasPrefix(line, "ARGS") && strings.Contains(line, testToken) {
			t.Fatalf("token leaked into ssh argv: %s", line)
		}
	}
}

// assertNoVMDeleted pins that no ssh path Ends a vm's life: an Entry —
// entered, attached or copied through — leaves the vm Detached at most, and
// only `zert kill` or an expired Idle window ends it. why names the path, so
// a failure says which one drifted.
func assertNoVMDeleted(t *testing.T, fs *fakeServer, why string) {
	t.Helper()
	fs.mu.Lock()
	deleted := fs.deleted
	fs.mu.Unlock()
	if len(deleted) != 0 {
		t.Fatalf("%s must never kill the vm, got deletes %v", why, deleted)
	}
}

// assertNoSSHSpawned pins that a command refused before ssh really stopped
// there: the fake ssh creates its log on the first call, so a missing log
// means no child was ever started.
func assertNoSSHSpawned(t *testing.T, logPath, why string) {
	t.Helper()
	if _, err := os.Stat(logPath); err == nil {
		t.Fatalf("ssh spawned despite %s", why)
	}
}

// --- misc shared fixtures ------------------------------------------------

// tmuxWrapCmd is the remote command every interactive entry runs, pinned
// verbatim: enter/attach the one tmux session "zert" when the guest has
// tmux, plain login shell when it does not (a vm from the pre-tmux
// Template). It is the contract, so the test owns the literal — and the
// literal is a deliberate byte-for-byte duplicate of vm.go's
// interactiveRemoteCmd: if you change one, edit both literals together.
const tmuxWrapCmd = `command -v tmux >/dev/null 2>&1 && exec tmux new -As zert || exec "${SHELL:-sh}" -l`

// fakeTTY makes stdinIsInteractive report a terminal — a test binary's
// stdin never is one — so the interactive paths can pin their tmux wrap.
func fakeTTY(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { stdinIsInteractive = defaultStdinIsInteractive })
	stdinIsInteractive = func() bool { return true }
}

// tempDirNamed returns a fresh, predictably named directory — the local side
// of an upload, where the name is part of the tar stream.
func tempDirNamed(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// captureStderr runs fn with os.Stderr redirected to a pipe and returns what
// was written. Only needed for code that hardwires os.Stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = old
	b, _ := io.ReadAll(r)
	return string(b)
}
