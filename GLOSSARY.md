# zert CLI

The customer-facing command line for the zert hosted-VM product. This
glossary holds the settled words of this repo; the product vocabulary of
record is `~/dev/GLOSSARY.md` and wins on any conflict.

## Language

**Entry**:
Entering a VM — opening a connection to it, whether a fresh one (`zert vm`)
or one already running (`zert attach`). The act; ssh is only the mechanism.
_Avoid_: ssh in, reconnect, login (the act is entering, not authenticating)

**Attach**:
As the product defines it (see `~/dev/GLOSSARY.md`): opening a connection to
a running VM — the customer's word for an entry into a VM that already
exists. Under the sleep model the machine need not be running: the escape
hatch's raw id resolves through the ensure ask before the relay opens, so a
sleeping machine is woken rather than reported expired.
_Avoid_: reconnect, re- coinages for the act

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
