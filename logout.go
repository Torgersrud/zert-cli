package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// requires server support for token revocation; update logoutPath if the API differs
const logoutPath = "/v1/logout"

func cmdLogout([]string) error {
	revoked := false
	if c, err := loadCredentials(); err == nil {
		req, _ := http.NewRequest("POST", strings.TrimRight(c.Host, "/")+logoutPath, nil)
		req.Header.Set("authorization", "Bearer "+c.Token)
		if resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			revoked = resp.StatusCode >= 200 && resp.StatusCode < 300
		}
	}
	if err := deleteCredentials(); err != nil {
		return err
	}
	if revoked {
		fmt.Fprintln(os.Stderr, "logged out")
	} else {
		fmt.Fprintln(os.Stderr, "logged out locally (server did not confirm token revocation; tokens expire after 30 days)")
	}
	return nil
}
