# MO-3 — `delta` and `class%` on basket rows and the drill-down (3.3, part 2)

Status: **implemented on branch mo-3-delta-view, awaiting review** (2026-08-27;
written 2026-08-26; review amendments (a)–(f) folded in before implementation).
Story: DEV-PLAN 3.3 [F2], view half.
Gated by MO-2 (signed columns in the store). Read first: MO-2, `3.1-flowshare.md`
(D5 render nulls, registry-only wiring), `ticker-view-v0.md` V5 (the reason
per-ticker delta was excluded from v0 — revisited here), DEV-PLAN Appendix B D9.

## What

The ask-side question, with its honesty number attached, at both levels:

    delta(B, m)  = (Σ QuoteAsk.Dollars − Σ QuoteBid.Dollars) / Σ Counted.Dollars   over basket B, completed minute m   ∈ [−1, +1]
    class%(B, m) = (Σ QuoteAsk.Dollars + Σ QuoteBid.Dollars) / Σ Counted.Dollars                                        ∈ [0, 1]

Both numerators are the **strong** classifiers only — MO-2's `QuoteAsk` /
`QuoteBid` (prints at/beyond the touch or off the midpoint). `class%` is
therefore "share of counted dollars classified at/beyond the touch or off
the midpoint". Tick-rule-classified dollars, late, unclassified, BLOCK and
NON_PRICE_FORMING dollars sit in the denominator only (`profile.Counted`;
MO-2 / D15 precedent) — uncertainty damps δ toward 0, never biases it.
Per ticker: the same two, per symbol, in the drill-down. Renders `+0.31` /
`62%`.

The dev view additionally registers `delta_all = (Σ AskSide − Σ BidSide) /
Σ Counted` (every rule in the numerator) so the nightly ledger can judge
whether the tick rule adds information (see Δ0).

## Decisions

- **Δ0 — Strong-rule numerator (owner decision, review amendment (a)/(b)).**
  The trader `delta` counts quote/midpoint-ruled dollars only; tick-ruled
  dollars stay in the denominator. Rationale: the tick rule is the weakest
  step of the cascade (MO-2: 14% of eligible prints on 08-24) and the F2
  degradation lives there; a δ that leans on it would read confident where
  the book was silent. `delta_all` on the dev view is the counter-evidence
  channel: if the ledger shows it tracking `delta` with more range and no
  more flicker, the owner can promote it. Default until then: strong rules.
- **Δ1 — Raw, no z, in this story.** There is no 20-day baseline for δ until
  20 signed bucket days exist (MO-2 S6 backfill helps; the corpus decides).
  The z rides MO-9. A raw bounded ratio is honest to render as-is; the
  footer says so.
- **Δ2 — D9 open suppression.** Both cells gap for the first 30 s after
  09:30:00 (the completed-minute rule already makes 09:30's minute render at
  09:31; the window is applied to the *prints*, so a 09:30 minute's δ is
  computed over `[09:30:30, 09:31)` only — stated in the footer: "09:30
  minute excludes the first 30 s"). `D9Window = 30s`, code constant.
- **Δ3 — `class%` is not optional.** Wherever δ renders, `class%` renders
  beside it. A +0.30 on 40% classified and a +0.30 on 85% classified are
  different statements; the pair is the measurement. Per-ticker δ is allowed
  (V5 revisited) for exactly this reason plus Δ2.
- **Δ4 — Gaps.** `HasSigned=false` / `Bucket.Signed == nil` (old bucket
  file, trades-only day, a store that is not time-ordered) → both gap,
  never 0; counted dollars 0 → both gap; strong-classified dollars 0 with
  counted > 0 → δ gaps, `class%` renders `0%` (a true measurement).
  `delta_all` gaps when no rule classified anything.
- **Δ5 — Highlight.** δ cell bold green when ≥ `+DeltaHighlight`, bold red
  when ≤ `−DeltaHighlight`; `DeltaHighlight = 0.25` (default, ledger-tunable,
  exported code constant). Colour is a statement of which side of the book
  the prints hit — the footer wording is "dollars printed at/above the ask
  or above the midpoint, minus dollars printed at/below the bid or below the
  midpoint, over all counted dollars, last completed minute"; never
  buy/sell. `class%` unstyled. The drill-down δ cell uses the same
  threshold and colours.
- **Δ6 — Window.** Trader cell = last completed minute (matches every other
  completed-minute column). A 5-minute trailing δ is *legal* — it is a ratio
  of sums over the window, not a sum of medians — and reads smoother; it
  registers on the **dev view** as `delta_5m` alongside `delta` so the
  ledger can judge which earns the trader seat. Default: 1-minute on trader
  until watched.
- **Δ7 — Home.** New `internal/delta` (`Compute` over a `Store` read
  interface — `bucket.Store` satisfies it, tests hand-build one — with a
  per-render-second memo like flowshare's snapshots). Wiring via the column
  registry only: `delta.Calc.ExtendTrader` inserts `delta`, `class%` after
  `concentration_day` (README order — `pre_share` stays last) and its
  footer block before the gap-glyph line; `DevColumns` registers `delta`,
  `class%`, `delta_all`, `delta_5m` on the dev view. The drill-down gains
  `delta`, `class%` after `of_basket` (`tickerview.Row` + `Detail`); the
  crossings strip is unchanged. Both footer blocks go through their
  package's scanner test.

## Done when (replayed data)

1. Unit: hand-built store → known δ, class% and delta_all; D9 boundary
   (print at 09:30:29 excluded, 09:30:30 included); every Δ4 gap path incl.
   `Signed == nil` through a real non-time-ordered `bucket.Store`;
   highlight engages at exactly ±0.25 and not at ±0.249, both signs;
   determinism (same bytes across renders and Calc instances).
2. Replay `-view-mode trader -view-at 09:45:00` on the 08-24 fixture: every
   column other than `delta`/`class%` byte-identical to the post-MO-1
   baseline frame; δ/class% populate for all 22 baskets and in a
   `-basket semis_compute` drill-down at 09:59:00 (the fixture ends at
   10:00); two renders byte-identical.
3. One basket-minute δ and class% recomputed in Python from the fixture's
   bucket CSV match the rendered digits.
4. Footer scanners (delta, tickerview) pass; the `aggressor:` stats line
   from MO-2 for the replayed day is quoted in the close notes beside the
   first δ screen.

## Decisions consumed

MO-2 S1–S7 (as landed: `QuoteAsk`/`QuoteBid` rule split, `Signed` pointer
nil = not recorded); D9 (30 s); D15 precedent; 3.1 D5 nulls; T2 colour
posture; ticker-view-v0 V5 (revisited, reason recorded); F2; scope law;
review amendments (a)–(f) of 2026-08-27 (strong-rule class%, quote-only δ
with `delta_all` on dev, D9 gap posture, README column order, per-ticker
pair, dev-only `delta_5m`).

## Close notes (2026-08-27, branch `mo-3-delta-view`)

First δ screen, 08-24 fixture, 09:45:00 (completed minute 09:44), beside
MO-2's honesty line for the same replay:

    aggressor: eligible=4002448 ask=49.4% bid=49.4% quote=84.7% tick=14.1% late=1.1% nobook=0.1% unclassified(incl late)=1.2%

Basket `class%` at 09:44 ran 59–98% (median 85%), consistent with the
84.8%-of-dollars quote/midpoint share MO-2 measured; the low end was
`epc_labor` at 59%, where `delta_all` (+0.32) and `delta` (−0.04) disagree —
the tick rule is carrying a third of that basket's classification, the case
Δ0 exists for. Python recomputation of every basket's 09:44 pair from the
bucket CSV matched the rendered digits (`scratchpad/mo-3/recompute.py`).

What the column showed 09:30–10:00 on this tape (SPY −0.34% since open,
breadth red across most baskets): minute-level δ flickers — most baskets
change sign every two or three minutes and only `dc_reits` (2 members,
~$3M/min) ever leaves ±0.5. The persistent reads were `power_equipment`
(negative 09:30–09:53, −0.39 to −0.14) and `robotics_av` / `promise_tier_ai`
(negative most minutes to 09:45), all three of which sat at the top of the
`cum_share_z` rank — dollars above typical AND on the bid side, which is
what breadth (1/12, 0/5, 1/10) already said. `semis_compute` and
`semicap_frontend` turned positive from 09:46 as SPY steadied. So: δ agreed
with breadth's direction and added the "on size" qualifier; it did not
agree or disagree with `cum_share_z`, which measures how much, not which
side. The 1-minute cell reads as a flicker column at 09:35; `delta_5m` on
the dev view is the candidate for the trader seat (Δ6 — the ledger decides).
