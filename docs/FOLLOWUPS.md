# Follow-ups (require server-side changes)

These items were identified in the security audit but are out of scope for the
client because they need API changes. Tracked here so they are not lost.

## Host key pinning

`zert` connects with `StrictHostKeyChecking=accept-new`, so the first
connection to each VM is trust-on-first-use through the relay. A proper fix:
the API returns the VM's host key (or fingerprint) when the VM is created, and
the client writes it to a temporary `UserKnownHostsFile` and connects with
`StrictHostKeyChecking=yes`. This requires a new field on the create-VM /
`/v1/me` responses.

## Real token revocation endpoint

`zert logout` (S5) sends a best-effort `POST /v1/logout`, but the backend does
not yet revoke tokens server-side. Once a real revocation endpoint exists,
confirm the path/semantics and treat a 2xx as authoritative.

## Idempotency key for `POST /v1/vm` — closed

Closed, and not by a key: `POST /v1/vm` became idempotent **ensure** — "give me
my machine" (backend ticket `Torgersrud/zert-backend#121`; the contract is
zert-backend's `docs.md`, section "VM ensure request contract"). The same ask
always answers the Customer's one machine — live → that one, Asleep → woken,
nothing → created — so the CLI's one `502` retry (`ensureVM` in the door
module, `door.go`) returns
the machine the failed ask already placed instead of minting a second. The
retry keeps its trigger and its single attempt; refusals place nothing and are
never retried. CLI side: [Torgersrud/zert-cli#18](https://github.com/Torgersrud/zert-cli/issues/18).
