package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// httpClient is the shared client for API calls. The websocket tunnel does NOT
// use it (long-lived stream), so only request/response calls get a timeout.
var httpClient = &http.Client{Timeout: 30 * time.Second}

type api struct {
	host  string
	token string
}

func (a api) do(method, path string, body any) (*http.Response, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		rd = bytes.NewReader(b)
	}
	// #nosec G704 -- a.host is the customer's own API host from stored credentials, enforced to https by checkHost (plain http only for loopback)
	req, err := http.NewRequest(method, strings.TrimRight(a.host, "/")+path, rd)
	if err != nil {
		return nil, nil, err
	}
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	if a.token != "" {
		req.Header.Set("authorization", "Bearer "+a.token)
	}
	// #nosec G704 -- same validated customer host as NewRequest above; this is the tool's purpose, not user-tainted URL fetch
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return resp, data, err
}

// vmPath builds the /v1/vm/{id} path, escaping the id as a single path
// segment (defense in depth on top of validSandboxID).
func vmPath(id string) string {
	return "/v1/vm/" + url.PathEscape(id)
}

// detail extracts FastAPI's {"detail": "..."} message.
func detail(data []byte) string {
	var m map[string]any
	if json.Unmarshal(data, &m) == nil {
		if d, ok := m["detail"].(string); ok && d != "" {
			return sanitize(d)
		}
	}
	if s := strings.TrimSpace(string(data)); s != "" {
		return sanitize(s)
	}
	return "unexpected server response"
}

// mapped converts a non-2xx status into the CLI error vocabulary (phase2.md).
func mapped(resp *http.Response, data []byte) error {
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return &apiError{resp.StatusCode, "session expired — run `zert login`"}
	case http.StatusTooManyRequests:
		ra := sanitize(resp.Header.Get("retry-after"))
		if ra == "" {
			ra = "later"
		}
		return &apiError{resp.StatusCode, "too many requests, retry after " + ra}
	}
	return &apiError{resp.StatusCode, detail(data)}
}

func requireLogin() (*Credentials, api) {
	c, err := loadCredentials()
	if err != nil {
		fmt.Fprintln(os.Stderr, "zert: not logged in — run `zert login`")
		os.Exit(1)
	}
	host := c.Host
	if !c.Insecure {
		h, err := checkHost(c.Host)
		if err != nil {
			fmt.Fprintf(os.Stderr, "zert: stored host is not allowed (use https): %v\n", err)
			os.Exit(1)
		}
		host = h
	}
	return c, api{host: host, token: c.Token}
}
