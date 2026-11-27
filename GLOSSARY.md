# zert CLI

The customer-facing command line for the zert hosted-VM product. This
glossary holds the settled words of this repo; the product vocabulary of
record is `~/dev/GLOSSARY.md` and wins on any conflict.

## Language

**Entry**:
Entering a VM — opening a connection to it (`zert vm`, `zert <path>`, the
stable name). The act; ssh is only the mechanism.
_Avoid_: ssh in, reconnect, login (the act is entering, not authenticating)

**Attach**:
Opening a connection to a running VM — back into the same shell, running
programs and scrollback included. What you're attached to is kept alive.
The CLI has no attach command: the word names the act, and `zert vm`,
`zert <path>` and the stable name are how the Customer makes one.
_Avoid_: reconnect (the VM never left, so nothing is being re-), "ssh in"
(that's the mechanism, not the act)

**Ensure**:
The door's one answer to "give me my machine" (`POST /v1/vm`): a live machine
is answered as it is, an Asleep one is woken, and only a customer with nothing
gets one placed. Asking twice is the same question and answers the same
machine — which is what makes the CLI's one `502` retry safe.
_Avoid_: create for the ask (a create is only one of ensure's three outcomes)

**Stable name**:
The one word an ssh configuration can carry forever: `zert tunnel me`, the
hidden command's sentinel. Every connection through it resolves, from the
caller's credentials, to that customer's one machine — one ensure ask, then
the relay dials the concrete id that ask answered, so the id exists only
inside that ask and the tunnel URL and nothing the Customer types, sees or
configures ever carries one.
_Avoid_: hostname router, alias
