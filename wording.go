package main

import "errors"

// The cross-command wordings: every customer-facing truth two paths share
// is spelled once here, as a constructor — the pattern the constructors
// below prove. A wording that names the way back is a
// rule about the product, not about the command that happens to hit it,
// so no table, guard or mapper may restate one.

var (
	// errSessionExpired is the 401 truth: the session is gone and
	// `zert login` is the way back. The API's HTTP 401 mapping and the
	// tunnel's 4401 close code are the same rule on two wires.
	errSessionExpired = errors.New("session expired — run `zert login`")

	// errVMExpired is the dead-machine truth: the vm is gone and `zert
	// vm` is the way back — one wording for every path that meets a
	// dead machine, never one per path.
	errVMExpired = errors.New("vm expired — start a new one with `zert vm`")

	// errInvalidVMID is the id guard kill and tunnel share: a word that
	// is neither the stable name nor a valid id is refused before any
	// ask is made.
	errInvalidVMID = errors.New("invalid vm id")

	// errUnusualListing is the several-row truth: under the one-machine
	// law a listing that does not name one machine is a server-side
	// anomaly, no command may guess which row is the Customer's, and
	// `zert ls` is where the server's raw answer shows. Status admits
	// it; every path that would act on a row refuses on it.
	errUnusualListing = errors.New("your machine listing is unusual — `zert ls` shows what the server says")
)
