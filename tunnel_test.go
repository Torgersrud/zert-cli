package main

import (
	"bytes"
	"context"
	"errors"
	"io"
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

func TestTunnelCloseCodes(t *testing.T) {
	for _, tc := range []struct {
		code websocket.StatusCode
		want string
	}{
		{4401, "session expired"},
		{4403, "no such vm"},
		{4410, "vm expired"},
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
