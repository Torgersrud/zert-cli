package main

import "errors"

// The cross-command wordings: every customer-facing truth two paths share
// is spelled once here, as a constructor — the pattern errNoLiveVM
// (extract.go) already proves. A wording that names the way back is a
// rule about the product, not about the command that happens to hit it,
// so no table, guard or mapper may restate one.

var (
	// errSessionExpired is the 401 truth: the session is gone and
	// `zert login` is the way back. The API's HTTP 401 mapping and the
	// tunnel's 4401 close code are the same rule on two wires.
	errSessionExpired = errors.New("session expired — run `zert login`")
)
