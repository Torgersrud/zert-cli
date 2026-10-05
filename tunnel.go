package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

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
	token := os.Getenv("ZERT_TUNNEL_TOKEN")
	host := os.Getenv("ZERT_HOST")
	insecure := false
	if token == "" || host == "" {
		c, err := loadCredentials()
		if err != nil {
			return errors.New("run `zert login` first")
		}
		if token == "" {
			token = c.Token
		}
		if host == "" {
			host = c.Host
			insecure = c.Insecure
		}
	}
	if !insecure {
		normalized, err := checkHost(host)
		if err != nil {
			return err
		}
		host = normalized
	}
	return tunnelRun(context.Background(), host, id, token, os.Stdin, os.Stdout)
}
