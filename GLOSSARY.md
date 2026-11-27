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

**Door module**:
The one in-process module below the command runners that owns the CLI's
whole conversation with the door (ADR-0001): the ensure ask and its one
safe `502` retry, the listing pre-check that decides the outcome, the one
wording per outcome, the quota story, and the which-row policy. Its
interface is the two asks below; every path that needs the machine —
`zert vm`, `zert <path>`, `zert extract`, the stable name, `zert status` —
asks it. The caller decides whether and where to render: the stable name
renders nothing.
_Avoid_: per-path listing pre-checks and wake-or-create helpers — a
caller that words the outcome from its own listing is the drift the
module retired

**The machine ask**:
"Give me my machine" — the door module's first ask: one ensure ask,
answered with the machine plus which of the three outcomes happened —
**answered as-is** (a running machine handed back), **woken** (an Asleep
one woken), **placed** (nothing answerable, so one placed). The 201
response carries no outcome word, so the module derives the answer from
the listing snapshot it takes itself before the ask — never a caller's
guess; dead rows answer nothing, so a listing of them is told creating,
matching what the ask then does.
_Avoid_: found / waking / creating spelled by a caller from a listing —
one wording per outcome, derived once inside the module

**The which-row ask**:
"Which row is my machine" — the door module's second ask, answered typed
from its own listing snapshot: the machine (a single row whatever state
word it carries, or the one answerable row among several), **none**, or
a several-row anomaly. No path reads a listing row past that answer.
_Avoid_: rows-first picks — `rows[0]` acting for the Customer's machine
is the bug the typed answer retires

**The several-row anomaly**:
The which-row ask's typed refusal to name a machine: under the one-machine
law — one Customer, one machine, true by construction, the vocabulary of
record's Ensure — a listing that does not reduce to one row is a
server-side anomaly, admitted never guessed. Status admits it and points
at `zert ls`; every path that would act on a row refuses — the copy arms
refuse before any bytes move.
_Avoid_: resolving it client-side, acting on the first row, "several
machines" as if the Customer had several

**Stable name**:
The one word an ssh configuration can carry forever: `zert tunnel me`, the
hidden command's sentinel. Every connection through it resolves, from the
caller's credentials, to that customer's one machine — one ensure ask, then
the relay dials the concrete id that ask answered, so the id exists only
inside that ask and the tunnel URL and nothing the Customer types, sees or
configures ever carries one.
_Avoid_: hostname router, alias
