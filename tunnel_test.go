package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

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
