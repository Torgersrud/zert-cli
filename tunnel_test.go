package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestKeepaliveReportsPingFailure(t *testing.T) {
	errc := make(chan error, 1)
	var calls int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go keepalive(ctx, 5*time.Millisecond, 5*time.Millisecond, func(context.Context) error {
		if atomic.AddInt32(&calls, 1) == 3 {
			return errors.New("pong timeout")
		}
		return nil
	}, errc)
	select {
	case err := <-errc:
		if !strings.Contains(err.Error(), "pong timeout") {
			t.Fatalf("got %v, want keepalive pong timeout", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("keepalive never reported the failing ping")
	}
}

func TestKeepaliveStopsOnCancel(t *testing.T) {
	pinged := make(chan struct{}, 10)
	errc := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go keepalive(ctx, 5*time.Millisecond, 5*time.Millisecond, func(context.Context) error {
		select {
		case pinged <- struct{}{}:
		default:
		}
		return nil
	}, errc)
	<-pinged
	cancel()
	time.Sleep(50 * time.Millisecond)
	select {
	case err := <-errc:
		t.Fatalf("keepalive reported %v after cancel", err)
	default:
	}
}

func TestWSURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://zert.example.com":                 "wss://zert.example.com/v1/tunnel/abc",
		"http://127.0.0.1:8081":                    "ws://127.0.0.1:8081/v1/tunnel/abc",
		"zert.example.com":                         "wss://zert.example.com/v1/tunnel/abc",
		"https://zert.example.com/x/?token=secret": "wss://zert.example.com/v1/tunnel/abc",
	} {
		got, err := wsURL(in, "abc")
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got != want {
			t.Errorf("wsURL(%q) = %s, want %s", in, got, want)
		}
		if strings.Contains(got, "token=") {
			t.Errorf("token leaked into URL: %s", got)
		}
	}
}

func stubTunnelServer(t *testing.T, onWS func(w http.ResponseWriter, r *http.Request, ws *websocket.Conn)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/tunnel/") {
			http.Error(w, "nope", 404)
			return
		}
		wsconn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		onWS(w, r, wsconn)
	}))
}

func TestTunnelRelayAndAuthHeader(t *testing.T) {
	var gotAuth string
	ts := stubTunnelServer(t, func(_ http.ResponseWriter, r *http.Request, ws *websocket.Conn) {
		gotAuth = r.Header.Get("authorization")
		ctx := context.Background()
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		ws.Write(ctx, websocket.MessageBinary, data)
		ws.Close(websocket.StatusNormalClosure, "bye")
	})
	defer ts.Close()

	var out bytes.Buffer
	pr, pw := io.Pipe()
	go func() {
		pw.Write([]byte("hello-bytes"))
		// keep open until the server closes; tunnelRun returns on ws close
	}()
	err := tunnelRun(context.Background(), ts.URL, "vm1", "tok-1", pr, &out)
	pw.Close()
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "hello-bytes" {
		t.Fatalf("relay got %q", out.String())
	}
	if gotAuth != "Bearer tok-1" {
		t.Fatalf("auth header = %q, want Bearer token", gotAuth)
	}
}

func TestTunnelRelaysLargeFrame(t *testing.T) {
	// coder/websocket's default read limit is 32 KiB; a single larger frame
	// (port-forwarded burst) used to kill the tunnel with a 1009 close.
	big := bytes.Repeat([]byte("x"), 64*1024)
	ts := stubTunnelServer(t, func(_ http.ResponseWriter, _ *http.Request, ws *websocket.Conn) {
		ctx := context.Background()
		ws.Write(ctx, websocket.MessageBinary, big)
		ws.Close(websocket.StatusNormalClosure, "bye")
	})
	defer ts.Close()

	var out bytes.Buffer
	pr, _ := io.Pipe() // never delivers: relay ends via ws close
	if err := tunnelRun(context.Background(), ts.URL, "vm1", "tok", pr, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), big) {
		t.Fatalf("relay got %d bytes, want %d", out.Len(), len(big))
	}
}

// captureStderr, shared with the other suites, lives in helpers_test.go.

func TestTunnelCloseCodes(t *testing.T) {
	for _, tc := range []struct {
		code websocket.StatusCode
		want string
	}{
		{4401, "session expired"},
		// the sleeper's refusal shares the wire with the stranger's: the
		// server closes a sleeping id and an id that is not the
		// Customer's with the same bare 4403 and no reason, so the
		// message must cover both truths and teach the way back
		{4403, "no such vm (or not yours), or it is asleep — connecting to it (zert vm / ssh zert) wakes it"},
		{4410, "vm expired — start a new one"},
	} {
		code := tc.code
		ts := stubTunnelServer(t, func(_ http.ResponseWriter, _ *http.Request, ws *websocket.Conn) {
			ws.Close(code, "")
		})
		pr, _ := io.Pipe() // never delivers, never closes: relay ends via ws close
		var out bytes.Buffer
		msg := captureStderr(t, func() {
			if err := tunnelRun(context.Background(), ts.URL, "vm1", "tok", pr, &out); err != nil {
				t.Errorf("code %d: unexpected error %v", code, err)
			}
		})
		ts.Close()
		if !strings.Contains(msg, tc.want) {
			t.Errorf("code %d: stderr %q, want %q", code, msg, tc.want)
		}
	}
}

func TestTunnelTextFrameToStderr(t *testing.T) {
	// server notices arrive as text frames; they must never reach stdout,
	// which is ssh's byte stream (mid-stream text = fatal stream corruption)
	ts := stubTunnelServer(t, func(_ http.ResponseWriter, _ *http.Request, ws *websocket.Conn) {
		ctx := context.Background()
		ws.Write(ctx, websocket.MessageText, []byte("vm unreachable (it may have expired)"))
		ws.Write(ctx, websocket.MessageBinary, []byte("real-ssh-bytes"))
		ws.Close(websocket.StatusCode(4410), "")
	})
	defer ts.Close()

	var out bytes.Buffer
	pr, _ := io.Pipe()
	msg := captureStderr(t, func() {
		if err := tunnelRun(context.Background(), ts.URL, "vm1", "tok", pr, &out); err != nil {
			t.Fatal(err)
		}
	})
	if out.String() != "real-ssh-bytes" {
		t.Fatalf("stdout = %q, want only the binary frame", out.String())
	}
	if !strings.Contains(msg, "vm unreachable") {
		t.Fatalf("stderr = %q, want the text notice", msg)
	}
}

func TestTunnelCloseReasonPreferred(t *testing.T) {
	ts := stubTunnelServer(t, func(_ http.ResponseWriter, _ *http.Request, ws *websocket.Conn) {
		ws.Close(websocket.StatusCode(4410), "session limit reached")
	})
	defer ts.Close()

	var out bytes.Buffer
	pr, _ := io.Pipe()
	msg := captureStderr(t, func() {
		if err := tunnelRun(context.Background(), ts.URL, "vm1", "tok", pr, &out); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(msg, "session limit reached") {
		t.Fatalf("stderr = %q, want the server close reason", msg)
	}
	if strings.Contains(msg, "vm expired") {
		t.Fatalf("stderr = %q, want reason instead of the code-table fallback", msg)
	}
}

func TestTunnelDialTimeout(t *testing.T) {
	// blackhole listener: TCP connects but nothing ever answers, like an
	// MTU/CGNAT stall on a phone hotspot — must fail bounded, not hang ssh
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			if _, err := ln.Accept(); err != nil {
				return
			}
		}
	}()

	oldTO, oldR := dialTimeout, dialRetries
	dialTimeout, dialRetries = 200*time.Millisecond, 0
	defer func() { dialTimeout, dialRetries = oldTO, oldR }()

	start := time.Now()
	var out bytes.Buffer
	pr, _ := io.Pipe()
	err = tunnelRun(context.Background(), "http://"+ln.Addr().String(), "vm1", "tok", pr, &out)
	if err == nil || !strings.Contains(err.Error(), "check your connection") {
		t.Fatalf("got %v, want bounded handshake error", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("dial took %v, want bounded by the handshake deadline", time.Since(start))
	}
}

func TestTunnelDialRetriesTransientFailure(t *testing.T) {
	var attempts int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("no hijacker")
				return
			}
			c, _, err := hj.Hijack() // kill the connection mid-handshake
			if err == nil {
				c.Close()
			}
			return
		}
		wsconn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		wsconn.Close(websocket.StatusNormalClosure, "bye")
	}))
	defer ts.Close()

	oldTO, oldR, oldB := dialTimeout, dialRetries, dialBackoff
	dialTimeout, dialRetries, dialBackoff = 2*time.Second, 2, 10*time.Millisecond
	defer func() { dialTimeout, dialRetries, dialBackoff = oldTO, oldR, oldB }()

	var out bytes.Buffer
	pr, _ := io.Pipe()
	if err := tunnelRun(context.Background(), ts.URL, "vm1", "tok", pr, &out); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("attempts = %d, want the handshake to be retried once", got)
	}
}

func TestTunnelNoRetryOn401(t *testing.T) {
	var attempts int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer ts.Close()

	oldTO, oldR, oldB := dialTimeout, dialRetries, dialBackoff
	dialTimeout, dialRetries, dialBackoff = 2*time.Second, 2, 10*time.Millisecond
	defer func() { dialTimeout, dialRetries, dialBackoff = oldTO, oldR, oldB }()

	var out bytes.Buffer
	pr, _ := io.Pipe()
	err := tunnelRun(context.Background(), ts.URL, "vm1", "tok", pr, &out)
	if err == nil || !strings.Contains(err.Error(), "session expired") {
		t.Fatalf("got %v, want session expired", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("attempts = %d, want no retry after an HTTP answer", got)
	}
}

func TestResolveTunnelTargetMatchingHost(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := saveCredentials(&Credentials{Host: "http://127.0.0.1:9000", Token: "tok", Email: "e"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZERT_TUNNEL_TOKEN", "")
	t.Setenv("ZERT_HOST", "http://127.0.0.1:9000")

	host, token, err := resolveTunnelTarget()
	if err != nil {
		t.Fatal(err)
	}
	if host != "http://127.0.0.1:9000" || token != "tok" {
		t.Fatalf("got (%q, %q), want (http://127.0.0.1:9000, tok)", host, token)
	}
}

func TestResolveTunnelTargetMismatchedHost(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := saveCredentials(&Credentials{Host: "https://a.example", Token: "tok", Email: "e"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZERT_TUNNEL_TOKEN", "")
	t.Setenv("ZERT_HOST", "https://b.example")

	_, _, err := resolveTunnelTarget()
	if err == nil || !strings.Contains(err.Error(), "ZERT_HOST differs from the host you logged in to") {
		t.Fatalf("want ZERT_HOST-differs error, got %v", err)
	}
}

func TestResolveTunnelTargetEnvTokenEnvHost(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZERT_TUNNEL_TOKEN", "envtok")
	t.Setenv("ZERT_HOST", "http://127.0.0.1:1234")

	host, token, err := resolveTunnelTarget()
	if err != nil {
		t.Fatal(err)
	}
	if host != "http://127.0.0.1:1234" || token != "envtok" {
		t.Fatalf("got (%q, %q), want (http://127.0.0.1:1234, envtok)", host, token)
	}
}

// --- the stable name: `zert tunnel me` ----------------------------------

// driveTunnel runs the hidden command the way ssh's ProxyCommand does:
// stdin and stdout are the connection stream, and the child's argv ended at
// the sentinel or the raw id. send arrives as the client's bytes; both the
// relayed stream and everything stderr carried come back for the assertions.
func driveTunnel(t *testing.T, args []string, send string) (out, errOut string, err error) {
	t.Helper()
	oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
	inR, inW, _ := os.Pipe() // *os.File: the streams the tunnel child really gets
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	os.Stdin, os.Stdout, os.Stderr = inR, outW, errW
	defer func() {
		os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr
		inW.Close() // releases tunnelRun's stdin reader, if it is still reading
	}()
	go func() {
		if send != "" {
			inW.Write([]byte(send))
		}
		// stdin stays open: like the relay tests, the session ends via the
		// server's close, not an EOF
	}()
	err = cmdTunnel(args)
	outW.Close()
	errW.Close()
	o, _ := io.ReadAll(outR)
	e, _ := io.ReadAll(errR)
	return string(o), string(e), err
}

// TestTunnelMeEnsuresOnceAndDialsThatMachine pins the stable name's whole
// path: `me` is one ensure ask — never a raw-id dial — and the relay opens
// against the concrete id that ask answered, so a sleeping machine is awake
// before any byte moves. The token rides only the authorization header;
// neither the sentinel nor the token ever reaches the URL, where the id —
// the one thing the Customer never types — exists and nowhere else.
func TestTunnelMeEnsuresOnceAndDialsThatMachine(t *testing.T) {
	fs := newFakeServer(t)
	fs.live = []string{"zzz9"} // the customer's one machine: asleep, woken by the ask
	setupHome(t, fs.URL)

	out, errOut, err := driveTunnel(t, []string{"me"}, "hello-through-the-relay")
	if err != nil {
		t.Fatal(err)
	}
	fs.mu.Lock()
	asks, paths := fs.ensureAsks, append([]string(nil), fs.tunnelPaths...)
	auth := fs.tunnelAuth
	fs.mu.Unlock()
	if asks != 1 {
		t.Fatalf("the sentinel is exactly one ensure ask, got %d", asks)
	}
	if len(paths) != 1 || paths[0] != "/v1/tunnel/zzz9" {
		t.Fatalf("must dial the concrete id ensure answered, got dialled paths %v", paths)
	}
	if strings.Contains(paths[0], testToken) {
		t.Fatalf("the token must never reach the URL: %s", paths[0])
	}
	if auth != "Bearer "+testToken {
		t.Fatalf("token must ride only the authorization header, got %q", auth)
	}
	if out != "hello-through-the-relay" {
		t.Fatalf("the relay's byte path must be unchanged, got %q", out)
	}
	if strings.Contains(errOut, "zzz9") {
		t.Fatalf("the id must stay invisible to the Customer, stderr said: %q", errOut)
	}
}

// TestTunnelMePlacesTheFirstMachine pins the stable name's other half: a
// Customer with nothing gets their machine placed by the same one ask —
// setup ends before they notice it began — and the tunnel dials what was
// placed.
func TestTunnelMePlacesTheFirstMachine(t *testing.T) {
	fs := newFakeServer(t) // nothing live: ensure places the one machine
	setupHome(t, fs.URL)

	out, _, err := driveTunnel(t, []string{"me"}, "first-connect")
	if err != nil {
		t.Fatal(err)
	}
	asks, placed := fs.ensureCounts()
	fs.mu.Lock()
	paths := append([]string(nil), fs.tunnelPaths...)
	fs.mu.Unlock()
	if asks != 1 || len(placed) != 1 || placed[0] != "vm123" {
		t.Fatalf("one ask must place the one machine, got %d asks, placed %v", asks, placed)
	}
	if len(paths) != 1 || paths[0] != "/v1/tunnel/vm123" {
		t.Fatalf("must dial the machine the ask placed, got dialled paths %v", paths)
	}
	if out != "first-connect" {
		t.Fatalf("the relay's byte path must be unchanged, got %q", out)
	}
}

// TestTunnelMeRefusalPassesThrough pins that a refused ensure reaches the
// Customer as its message — the wake path or the reason, never a silent
// hang: the ask is not retried, and a machine that could not be woken is
// never dialled.
func TestTunnelMeRefusalPassesThrough(t *testing.T) {
	fs := newFakeServer(t)
	fs.create = func(w http.ResponseWriter, _ int) {
		http.Error(w, `{"detail":"wake failed: the sleeping machine could not be woken"}`, 503)
	}
	setupHome(t, fs.URL)

	_, _, err := driveTunnel(t, []string{"me"}, "")
	if err == nil || !strings.Contains(err.Error(), "could not be woken") {
		t.Fatalf("the refusal's message must pass through, got %v", err)
	}
	fs.mu.Lock()
	asks, paths := fs.ensureAsks, append([]string(nil), fs.tunnelPaths...)
	fs.mu.Unlock()
	if asks != 1 {
		t.Fatalf("a refusal is final: want 1 ask, got %d", asks)
	}
	if len(paths) != 0 {
		t.Fatalf("a refused wake must open no relay, dialled %v", paths)
	}
}

// TestTunnelRawIDIsUntouched pins the escape hatch: a raw id dials the
// relay exactly as before — no ensure ask — the sentinel is the one word
// `me` and nothing else, and anything that is neither a valid id nor the
// sentinel keeps its invalid-id refusal.
func TestTunnelRawIDIsUntouched(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)

	out, _, err := driveTunnel(t, []string{"vm123"}, "raw-id-bytes")
	if err != nil {
		t.Fatal(err)
	}
	// the sentinel is exact: `ME` is a valid id and dials as one
	outME, _, err := driveTunnel(t, []string{"ME"}, "exact-sentinel")
	if err != nil {
		t.Fatalf("a valid id is never the sentinel, got %v", err)
	}
	if _, _, err := driveTunnel(t, []string{"not an id"}, ""); err == nil ||
		!strings.Contains(err.Error(), "invalid vm id") {
		t.Fatalf("want the invalid-id refusal for a non-sentinel word, got %v", err)
	}
	fs.mu.Lock()
	asks, paths := fs.ensureAsks, append([]string(nil), fs.tunnelPaths...)
	fs.mu.Unlock()
	if asks != 0 {
		t.Fatalf("the raw-id path must never ensure, got %d asks", asks)
	}
	if len(paths) != 2 || paths[0] != "/v1/tunnel/vm123" || paths[1] != "/v1/tunnel/ME" {
		t.Fatalf("raw ids must dial exactly as before, got dialled paths %v", paths)
	}
	if out != "raw-id-bytes" || outME != "exact-sentinel" {
		t.Fatalf("the raw-id relay must be unchanged, got %q and %q", out, outME)
	}
}
