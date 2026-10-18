package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// wsURL converts an http(s) host into the wss tunnel URL for a sandbox.
func wsURL(host, id string) (string, error) {
	if host == "" {
		return "", errors.New("no host")
	}
	u, err := url.Parse(host)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" {
		u, err = url.Parse("https://" + host)
		if err != nil {
			return "", err
		}
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}
	u.Path = "/v1/tunnel/" + id
	u.RawQuery = ""
	return u.String(), nil
}

var closeCodeMsg = map[websocket.StatusCode]string{
	4401: "session expired — run `zert login`",
	4403: "no such vm (or not yours)",
	4410: "vm expired",
}

// closeMsg prefers the server's close reason, falling back to the
// code table for servers that close without one.
func closeMsg(ce websocket.CloseError) string {
	if ce.Reason != "" {
		return ce.Reason
	}
	return closeCodeMsg[ce.Code]
}

const (
	keepaliveEvery   = 25 * time.Second
	keepaliveTimeout = 20 * time.Second
	// maxFrame bounds a single relayed WS message; matches the server-side
	// relay limit in ~/dev/public.py (16 MiB).
	maxFrame = 16 << 20
)

// Dial deadlines are vars so tests can shrink them. The WS handshake has no
// built-in bound in coder/websocket and ssh's ConnectTimeout does not apply
// to ProxyCommand children, so a stalled link (CGNAT/MTU blackholes on
// phone hotspots) would otherwise hang ssh forever. Never set
// http.Client.Timeout here: v1.8.15 applies it to the whole tunnel
// lifetime, not just the handshake (dial.go cloneWithDefaults).
var (
	dialTimeout = 20 * time.Second
	dialRetries = 2
	dialBackoff = time.Second
)

// dialWS performs the tunnel handshake with a per-attempt deadline and
// retries transient network failures; opening a relay is side-effect-free,
// so retrying is safe (unlike POST /v1/vm). A non-101 HTTP response means
// the server answered with intent — no retry.
func dialWS(ctx context.Context, u, token string) (*websocket.Conn, error) {
	opts := &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	}
	var lastErr error
	for attempt := 0; attempt <= dialRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(dialBackoff << (attempt - 1)):
			}
		}
		// the deadline covers only the handshake; the hijacked Conn
		// returned by Dial is detached from the request ctx
		actx, cancel := context.WithTimeout(ctx, dialTimeout)
		ws, resp, err := websocket.Dial(actx, u, opts)
		cancel()
		if err == nil {
			return ws, nil
		}
		if resp != nil {
			if resp.StatusCode == http.StatusUnauthorized {
				return nil, errors.New(closeCodeMsg[4401])
			}
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		lastErr = err
	}
	return nil, fmt.Errorf("could not reach %s — check your connection: %w", u, lastErr)
}

// keepalive pings every interval on a dead-link probe schedule and reports the
// first failure to errc: ping frames also reset idle timers on intermediaries.
func keepalive(ctx context.Context, interval, timeout time.Duration, ping func(context.Context) error, errc chan<- error) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, timeout)
			err := ping(pctx)
			cancel()
			if err != nil {
				errc <- fmt.Errorf("keepalive: %w", err)
				return
			}
		}
	}
}

// tunnelRun relays raw bytes between in/out and the /v1/tunnel/{id} relay.
// The token travels only in the authorization header (never the URL/argv).
func tunnelRun(ctx context.Context, host, id, token string, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	u, err := wsURL(host, id)
	if err != nil {
		return err
	}
	ws, err := dialWS(ctx, u, token)
	if err != nil {
		return err
	}
	defer ws.Close(websocket.StatusNormalClosure, "")
	// default read limit is 32 KiB; port-forwarded traffic arrives as single
	// large frames and would trip a 1009 close mid-session
	ws.SetReadLimit(maxFrame)

	errc := make(chan error, 3)
	go keepalive(ctx, keepaliveEvery, keepaliveTimeout, ws.Ping, errc)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, rerr := in.Read(buf)
			if n > 0 {
				if werr := ws.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					errc <- werr
					return
				}
			}
			if rerr == io.EOF {
				errc <- nil
				return
			}
			if rerr != nil {
				errc <- rerr
				return
			}
		}
	}()
	go func() {
		for {
			mt, data, rerr := ws.Read(ctx)
			if rerr != nil {
				var ce websocket.CloseError
				if errors.As(rerr, &ce) {
					if msg := closeMsg(ce); msg != "" {
						fmt.Fprintf(os.Stderr, "zert tunnel: %s\n", msg)
					}
					errc <- nil
					return
				}
				errc <- rerr
				return
			}
			// text frames are server notices, never ssh bytes: writing them to
			// stdout corrupts the encrypted stream (fatal "Connection corrupted")
			if mt == websocket.MessageText {
				fmt.Fprintf(os.Stderr, "zert tunnel: %s\n", strings.TrimSpace(string(data)))
				continue
			}
			if _, werr := out.Write(data); werr != nil {
				errc <- werr
				return
			}
		}
	}()
	return <-errc
}

func cmdTunnel(args []string) error {
	fs := newFlagSet("tunnel")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: zert tunnel <vm-id>")
	}
	id := fs.Arg(0)
	if !validSandboxID(id) {
		return errors.New("invalid vm id")
	}
	host, token, err := resolveTunnelTarget()
	if err != nil {
		return err
	}
	return tunnelRun(context.Background(), host, id, token, os.Stdin, os.Stdout)
}

// resolveTunnelTarget decides which host and token the tunnel connects to.
// A stored token is never sent to a host other than the one that issued it.
func resolveTunnelTarget() (host, token string, err error) {
	envToken := os.Getenv("ZERT_TUNNEL_TOKEN")
	envHost := os.Getenv("ZERT_HOST")

	// Both supplied via env: allowed; no stored token is involved.
	if envToken != "" && envHost != "" {
		h, e := checkHost(envHost)
		if e != nil {
			return "", "", e
		}
		return h, envToken, nil
	}

	c, e := loadCredentials()
	if e != nil {
		return "", "", errors.New("run `zert login` first")
	}
	token = envToken
	if token == "" {
		token = c.Token // stored token
	}

	if envHost != "" {
		stored, e1 := checkHostAllowInsecure(c.Host, c.Insecure)
		if e1 != nil {
			return "", "", e1
		}
		envNorm, e2 := checkHostAllowInsecure(envHost, c.Insecure)
		if e2 != nil {
			return "", "", e2
		}
		if strings.EqualFold(envNorm, stored) {
			return envNorm, token, nil
		}
		// Host mismatch: only tolerated when the token itself came from env.
		if envToken != "" {
			return envNorm, token, nil
		}
		return "", "", errors.New("ZERT_HOST differs from the host you logged in to; run `zert login`")
	}

	// host comes from stored credentials
	if !c.Insecure {
		h, e1 := checkHost(c.Host)
		if e1 != nil {
			return "", "", e1
		}
		return h, token, nil
	}
	return c.Host, token, nil
}
