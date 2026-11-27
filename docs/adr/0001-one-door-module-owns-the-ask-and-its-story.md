# One door module owns the ask and its story — callers decide whether and where to render

Status: accepted.

Every path that needed the Customer's machine used to hold its own
conversation with the door, and the paths disagreed. `zert vm` pre-checked
the listing and worded found-or-creating inline; the copy-in path ran a
different pre-check (`wakeOrCreate`) with its own no-live wording; the
stable name asked ensure silently; `zert status` folded its own answerable
predicate over the rows; and the copy arms picked the first Live row with
`pickLiveVM` — so a several-live listing silently streamed to whichever
row sorted first, and an expired row was told "found" until fe9fd0e fixed
the wording predicate on the two paths that had it. The history is the
motivation, and it is uniform: every recent repair in this area fixed
wording or predicate drift between paths — fe9fd0e (entry and copy-in
learned that dead rows answer nothing), 3ed9a4b (the tunnel's close table
claimed every 4403 was a stranger's id when a sleeping machine the
Customer owns answers the same bare code), d4db8cf (status read `rows[0]`),
ccca0ea (the readme's name-one rule said "several" where the code demanded
an id only for several *running*), and cd544d0 — the one-helper repair
that unified the no-live wording for the escape hatch and copy-in, two of
five ensure-adjacent call sites, leaving the primary entry, status and the
stable name out. A repair that must name its paths by hand is a repair
that misses one. Spec #30 (landed in #33–#36; this record is its paperwork)
made the bug class unmakeable instead of fixing its instances.

**The decision that is not obvious: the whole conversation with the door
lives in one in-process module, `door.go`, sitting directly below the
command runners — and the callers keep only the rendering decision.** The
module's interface is two asks: "give me my machine" (`askMachine`),
answered with the machine plus a typed outcome — **answered as-is**,
**woken** or **placed** — and "which row is my machine" (`askWhichRow`),
answered with the machine, **none**, or a typed **several-row ambiguity**.
Behind that interface live the ensure wire ask with its one safe `502`
retry (refusals are answers, never retried — ensure's idempotency is what
makes the retry safe, and the retry keeps exactly the shape the wire
contract allows), the listing pre-check that derives the outcome, the one
wording per outcome, the quota story, and every row predicate. The module
takes an `api` value and never calls `requireLogin`: it is not a command,
and login and exit policy are the runner's business — the stable name
builds its own `api` from `resolveTunnelTarget` and hands the ask
`io.Discard`, so silence stays a caller choice, not a door omission.

The outcome is derived inside the module because the wire contract leaves
no alternative: the 201 body carries no outcome word (`state` is always
`"running"` by then; the server's audit line knows created, woke or
returned live — the response does not say). A caller that words the
outcome must therefore guess from a listing snapshot, and a snapshot
taken before the ask can be stale. Taking the snapshot, deriving the
outcome and tolerating the staleness once, behind the interface, is the
whole point: a listing whose rows are all unanswerable is told as
creating, matching what the ask then does, and no caller can guess
differently from another.

## Considered options

- **Per-path wording (the status quo ante)** — rejected: five surfaces
  worded one ask five ways, and every repair had to name its paths by
  hand — cd544d0 covered two of five call sites, and fe9fd0e's predicate
  fix had to land twice. A sleep-model change must be a one-file change,
  or the next one misses a path again; the drift fixes above each did.
- **Rows-first picks (`pickLiveVM`)** — rejected: a several-live listing
  handed the copy arms whichever row sorted first, so a file could land
  on — or leave — a machine the Customer did not choose. The several-row
  anomaly is now a typed answer the door refuses to resolve: the copy
  arms refuse on it before any bytes move, status admits it, and entry
  never meets it, because the ensure ask needs no listing to proceed.
- **A wider seam — the module as a command, owning login or exit** —
  rejected: the module prints to no global and owns no exit code. The
  quota refusal's listing and hint render through the caller's writer and
  the refusal returns as an ordinary error for the main error path to
  exit on; a helper that captures globals is a helper the caller can
  neither test nor silence, and the stable name needs both.
- **Waiting for the wire to carry the outcome** — rejected: the door's
  wire contract is taken as it is (spec #30, out of scope). If the 201
  ever carries an outcome word, the derivation in `askMachine` is the one
  place to delete.

## Consequences

- The next sleep-model change lands in `door.go` and its tests. The
  outcome for each starting state — live, asleep, none, expired-only,
  several — is pinned once at the ask's own interface, against the
  existing door fake; each command suite pins only its rendering
  end-to-end.
- The several-row anomaly is pinned once at the door (the typed answer)
  and once per caller for its rendering only: status admits it
  (`errUnusualListing`), the copy arms refuse (`errSeveralMachinesNoCopy`).
- Every path asks the door: `zert vm`, `zert <path>`, `zert extract` and
  the stable name through the machine ask — the copy arms ask it when the
  row answer is none or not running — and `zert status` and the copy
  arms' row pick through the which-row ask. `errNoLiveVM`, the extract
  path's refusal that assumed the door would not answer, died with the
  wording it worded.
- Wordings that name the way back are rules about the product, not about
  the command that hits them: they live in `wording.go` as constructors
  (`errSessionExpired`, `errVMExpired`, `errInvalidVMID`,
  `errUnusualListing`, `errSeveralMachinesNoCopy`), while the outcome
  stories live in `door.go`, one per outcome, told before the ask
  explains its wait. No table, guard or mapper may restate one.
- `zert status` keeps its unknown-state honesty: a state word this build
  has never seen is admitted and pointed at `zert ls`, never guessed —
  the door reports what the row's flags say.
