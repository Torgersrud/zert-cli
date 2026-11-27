package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// The --stdin mode's machine-readable outcome words — the one line on
// stdout the zert app parses — and the exit statuses that mirror them, so
// the GUI can branch on either. The words map to the app's plain-word
// sentences: bad-credentials is "that password doesn't match this
// account", unprovisioned is "your account isn't fully set up — contact
// support".
const (
	stdinOK                = "ok"
	stdinBadCredentials    = "bad-credentials"
	stdinUnprovisioned     = "unprovisioned"
	stdinRateLimited       = "rate-limited"
	stdinExitBadPassword   = 1
	stdinExitUnprovisioned = 2
	stdinExitRateLimited   = 3
	stdinExitOther         = 4
)

// readLoginStdin reads the --stdin mode's credential pair: two lines, the
// email first and the password second. Line endings may be \n or \r\n.
// Surrounding space is the email's to lose, but the password's to keep —
// only the line ending is trimmed from it.
func readLoginStdin() (string, string, error) {
	r := bufio.NewReader(os.Stdin)
	emailLine, err := readStdinLine(r)
	if err != nil {
		return "", "", badStdinInput(err)
	}
	passwordLine, err := readStdinLine(r)
	if err != nil {
		return "", "", badStdinInput(err)
	}
	email := strings.TrimSpace(emailLine)
	if email == "" || passwordLine == "" {
		return "", "", badStdinInput(errors.New("got an empty line"))
	}
	return email, passwordLine, nil
}

func badStdinInput(err error) error {
	return fmt.Errorf("login --stdin wants two lines on stdin — email, then password: %v", err)
}

func readStdinLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil && !(err == io.EOF && line != "") {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}

// loginFail shapes a non-HTTP failure for whichever mode is running: the
// interactive path gets the bare error untouched, --stdin additionally
// prints its outcome word on stdout (error: <reason>) and exits with the
// status the GUI branches on.
func loginFail(useStdin bool, err error) error {
	if !useStdin {
		return err
	}
	fmt.Println("error: " + err.Error())
	return &exitError{stdinExitOther, err}
}

// loginStatusFail shapes an HTTP-status failure. The interactive path keeps
// its exact apiError vocabulary; --stdin additionally prints its
// status-specific outcome word on stdout and exits with the matching
// status.
func loginStatusFail(useStdin bool, status, exit int, word, msg string) error {
	if !useStdin {
		return &apiError{status, msg}
	}
	if word == "" {
		word = "error: " + msg
	}
	fmt.Println(word)
	return &exitError{exit, errors.New(msg)}
}

func cmdLogin(args []string) error {
	fs := newFlagSet("login")
	host := fs.String("host", "", "server URL")
	insecure := fs.Bool("insecure", false, "allow plain http:// hosts (credentials sent unencrypted)")
	useStdin := fs.Bool("stdin", false, "non-interactive mode for GUIs: email then password arrive as two lines on stdin (never argv); one machine-readable outcome word goes to stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Credentials never travel as arguments: in stdin mode a stray
	// positional (a password pasted where a flag belongs) is refused
	// before anything is sent.
	if *useStdin && fs.NArg() > 0 {
		return loginFail(true, errors.New("login --stdin takes no arguments — credentials arrive on stdin, never argv"))
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
		return loginFail(*useStdin, errors.New("no server: pass --host or set ZERT_HOST"))
	}
	normalized, err := checkHostAllowInsecure(*host, *insecure)
	if err != nil {
		return loginFail(*useStdin, err)
	}
	if *insecure && strings.HasPrefix(normalized, "http://") {
		fmt.Fprintln(os.Stderr, "zert: WARNING: sending credentials over an unencrypted connection")
	}
	var email, password string
	if *useStdin {
		email, password, err = readLoginStdin()
		if err != nil {
			return loginFail(true, err)
		}
	} else {
		email, err = promptLine("email: ")
		if err != nil {
			return err
		}
		password, err = promptSecret("password: ")
		if err != nil {
			return err
		}
	}
	resp, data, err := api{host: normalized}.do("POST", "/v1/login",
		map[string]string{"email": email, "password": password})
	if err != nil {
		return loginFail(*useStdin, err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return loginStatusFail(*useStdin, resp.StatusCode, stdinExitBadPassword, stdinBadCredentials, detail(data))
	case http.StatusForbidden:
		// the server's "account disabled": the account exists but is not
		// fully provisioned — a different GUI sentence than wrong
		// credentials, hence its own outcome word and exit status.
		return loginStatusFail(*useStdin, resp.StatusCode, stdinExitUnprovisioned, stdinUnprovisioned, detail(data))
	case http.StatusTooManyRequests:
		return loginStatusFail(*useStdin, resp.StatusCode, stdinExitRateLimited, stdinRateLimited, "too many attempts, wait a minute")
	default:
		return loginStatusFail(*useStdin, resp.StatusCode, stdinExitOther, "", detail(data))
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &out); err != nil || out.Token == "" {
		return loginFail(*useStdin, errors.New("malformed login response"))
	}
	if err := saveCredentials(&Credentials{Host: normalized, Email: email, Token: out.Token, Insecure: *insecure}); err != nil {
		return loginFail(*useStdin, err)
	}
	if *useStdin {
		fmt.Println(stdinOK)
		return nil
	}
	fmt.Fprintf(os.Stderr, "logged in as %s (%s)\n", email, normalized)
	return nil
}
