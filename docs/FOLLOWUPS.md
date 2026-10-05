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

## Idempotency key for `POST /v1/vm`

The create-VM call is retried once on `502`. Because `POST /v1/vm` is not
idempotent, a `502` that actually reached the backend could create a duplicate
VM. Fix: the client sends an idempotency key and the backend honors it, after
which the retry becomes safe.
