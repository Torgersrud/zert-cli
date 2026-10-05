package main

import (
	"regexp"
	"runtime"
	"strings"
)

// sandboxIDRe accepts a server-supplied or user-supplied vm id: a leading
// alphanumeric char followed by up to 63 word characters.
var sandboxIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// validSandboxID reports whether id is safe to embed in a ProxyCommand, a
// URL path, or a user@<id> host argument.
func validSandboxID(id string) bool {
	return sandboxIDRe.MatchString(id)
}

// proxyCommand builds the ssh ProxyCommand option, shell-quoting the CLI's own
// path (which may contain spaces or metacharacters) so OpenSSH's shell
// invocation cannot be hijacked. The id must already be validated.
func proxyCommand(self, id string) string {
	if runtime.GOOS == "windows" {
		// Windows paths cannot legally contain `"`, but stay defensive.
		self = strings.ReplaceAll(self, `"`, "")
		return `ProxyCommand="` + self + `" tunnel ` + id
	}
	return "ProxyCommand='" + strings.ReplaceAll(self, "'", `'\''`) + "' tunnel " + id
}
