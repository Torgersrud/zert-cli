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

// tunnelRun relays raw bytes between in/out and the /v1/tunnel/{id} relay.
// The token travels only in the authorization header (never the URL/argv).
func tunnelRun(ctx context.Context, host, id, token string, in io.Reader, out io.Writer) error {
	u, err := wsURL(host, id)
	if err != nil {
		return err
	}
	opts := &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	}
	ws, resp, err := websocket.Dial(ctx, u, opts)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return errors.New(closeCodeMsg[4401])
		}
		return err
	}
	defer ws.Close(websocket.StatusNormalClosure, "")

	errc := make(chan error, 2)
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
			_, data, rerr := ws.Read(ctx)
			if rerr != nil {
				var ce websocket.CloseError
				if errors.As(rerr, &ce) {
					if msg, ok := closeCodeMsg[ce.Code]; ok {
						fmt.Fprintf(os.Stderr, "zert tunnel: %s\n", msg)
					}
					errc <- nil
					return
				}
				errc <- rerr
				return
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
