package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"syscall"

	"golang.org/x/term"
)

var (
	promptLine = func(label string) (string, error) {
		fmt.Fprint(os.Stderr, label)
		var b []byte
		c := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(c)
			if n == 1 {
				if c[0] == '\n' {
					break
				}
				b = append(b, c[0])
				continue
			}
			if err != nil {
				return "", err
			}
		}
		return strings.TrimSpace(string(b)), nil
	}
	promptSecret = func(label string) (string, error) {
		fmt.Fprint(os.Stderr, label)
		b, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
)

func cmdLogin(args []string) error {
	fs := newFlagSet("login")
	host := fs.String("host", "", "server URL")
	insecure := fs.Bool("insecure", false, "allow plain http:// hosts (credentials sent unencrypted)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *host == "" {
		*host = os.Getenv("ZERT_HOST")
	}
	if *host == "" {
		if c, err := loadCredentials(); err == nil {
			*host = c.Host
		}
	}
	if *host == "" {
		return errors.New("no server: pass --host or set ZERT_HOST")
	}
	normalized, err := checkHostAllowInsecure(*host, *insecure)
	if err != nil {
		return err
	}
	if *insecure && strings.HasPrefix(normalized, "http://") {
		fmt.Fprintln(os.Stderr, "zert: WARNING: sending credentials over an unencrypted connection")
	}
	email, err := promptLine("email: ")
	if err != nil {
		return err
	}
	password, err := promptSecret("password: ")
	if err != nil {
		return err
	}
	resp, data, err := api{host: normalized}.do("POST", "/v1/login",
		map[string]string{"email": email, "password": password})
	if err != nil {
		return err
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return &apiError{resp.StatusCode, detail(data)}
	case http.StatusTooManyRequests:
		return &apiError{resp.StatusCode, "too many attempts, wait a minute"}
	default:
		return &apiError{resp.StatusCode, detail(data)}
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &out); err != nil || out.Token == "" {
		return errors.New("malformed login response")
	}
	if err := saveCredentials(&Credentials{Host: normalized, Email: email, Token: out.Token, Insecure: *insecure}); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "logged in as %s (%s)\n", email, normalized)
	return nil
}
