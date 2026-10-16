# Protocol gaps found while building the frontend

Reported rather than worked around. **No server or protocol change has been
made.** Protocol v1 is untouched.

Two of these are real gaps; the third is a limitation to accept or decide on.
None blocks the frontend, and the workaround taken for each is stated.

---

## 1. Per-claim realizability at the current width is not exposed

**Wanted.** The brief permits greying out a claim that cannot be realized at
the current pool width, "where exposed by protocol".

**Gap.** The view carries `handWidth`, `poolSize` and `evalMode`, and
`claim_space` carries the 367 claims. But nothing says *which* of those
claims a hand of the current width could realize. The server has exactly this
in `poker.RealizableAtWidth`, and it is not on the wire.

**Why I did not compute it client-side.** It is a rule, not presentation. It
needs the per-category minimum width and the High Card rank floor, i.e. that
a five-card high-card hand cannot go below 7 while a two-card one can be 3.
Reimplementing that in TypeScript is exactly the second engine the brief
forbids.

**Workaround taken.** No width-based greying. Every claim above the standing
ordinal is offered, and the builder states plainly that bluffing an
impossible claim is legal and that the server is the sole authority. This is
arguably the better default anyway: hiding or disabling a legal bluff would
misrepresent the rules.

**Minimal closure, if you want it.** Add one optional field per claim in
`claim_space`, computed server-side:

```json
{ "category": "FLUSH", "primary": "Q", "suit": "HEARTS", "minWidth": 5 }
```

The client would then compare `minWidth <= view.handWidth`, which is an
integer comparison, not a rule. Additive, so by the documented versioning
strategy it does not bump v1.

---

## 2. The claim ladder has no display text

**Gap.** `ClaimView` in the history carries a server-rendered `text` field,
so history cannot disagree with the server. The 367 claims in `claim_space`
carry only structure, so the builder has to render its own labels.

**Workaround taken.** `src/protocol/labels.ts` formats labels client-side.
This is presentation, which the brief permits, and it is isolated in one file
with no comparison or evaluation in it. The risk is cosmetic drift: if
`Claim.String()` on the server ever changes wording, the builder and the
history would disagree in wording while agreeing on the claim.

**Minimal closure, if you want it.** Add `text` to each claim in
`claim_space`, from the same `Claim.String()` the history already uses. Also
additive. It would cost roughly 10 KB on a one-off message and delete a file
from the frontend.

---

## 3. No documented browser persistence for the rejoin token

**Not a gap in the server, a decision that needs making.**

`docs/protocol.md` §3 says the token is server-generated, returned once in
`welcome` over TLS, never logged and never in a URL. It says nothing about
browser storage, because until now there was no browser.

**What I did.** Kept the token in memory only. A dropped socket reconnects
and resumes automatically. A **page reload ends the session**, because
nothing persists.

**Why not `sessionStorage`.** It survives a reload but is readable by any
script on the origin, so one XSS hands an attacker a credential that grants
another player's hidden cards. That is a materially weaker security model
than the one protocol.md describes, and the brief says not to invent a weaker
transport.

**Options, for your call.**

| Option | Survives reload | Exposed to XSS |
|---|---|---|
| In memory (shipped) | no | no |
| `sessionStorage` | yes | yes |
| `httpOnly`, `Secure`, `SameSite=Strict` cookie set by the server on join | yes | no |

The cookie is the only option that gets both, and it is a server change:
a `Set-Cookie` on the join path plus reading it on upgrade. I have not made
it. If reload-survival matters for the real product, this is the decision to
take, and it is a protocol and security change rather than a frontend one.

---

## 4. Empty slices arrive as `null`, not `[]`

**Found by the e2e suite, which failed on it.** Go marshals a nil slice as
`null`, so a round with no claims yet sends:

```json
{ "claims": null, "standingOrdinal": -1 }
```

My TypeScript declared `claims: ClaimView[]`, so the first `.length` threw.
The same applies to `players`, and to `witness` inside `lastShow`.

**Workaround taken.** `types.ts` now declares a `RawPlayerView` whose array
fields are nullable, and `sanitizeView` normalises them to real arrays on
ingest, so nothing downstream sees a null. One place, documented.

**Worth knowing regardless.** Every client in every language will hit this,
and a client that assumes an array will crash on an empty round rather than
on an unusual one. Closing it server-side means initialising those slices
to empty rather than leaving them nil before marshalling, which is a purely
representational change with no effect on the wire schema's types. I have
not made it.

---

## Not gaps

Checked and already covered, recorded so the audit is complete:

- **Timer** — `turnDeadline` and `roundEndDeadline` are absolute, so the
  client counts down without needing a server clock.
- **Abandoned game** — `phase == GAME_OVER` with an empty `winner` is
  unambiguous.
- **Elimination** — `players[].status`.
- **Reduced pool** — `evalMode`, `poolSize` and `handWidth` are all present,
  which is enough to explain the mode without evaluating anything.
- **Reveal** — `revealed`, the per-player `cards`, and `lastShow` with its
  witness.
- **SHOW eligibility and claim legality** — `canShow`, `canClaim` and
  `standingOrdinal`, so the client never judges either.
