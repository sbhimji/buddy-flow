# MO-8 — Run metrics: `since`, `5m_z`, `vs_SPY`

Status: **open** (written 2026-08-26). The "is there a run to chase" story
(README question → metric map). Owner decision O2: after signed volume and
the prune. Gated by MO-1 (column order). Read first: `3.1-flowshare.md`
(D5 per-minute `flow_share_z` — the atom; D7 cum family), `3.2-breadth.md`
(B1–B4 anchors, dead-band, SPY reference), DEV-PLAN 3.6 (RelPerf, D8) and 3.7
(ShiftSlope — this story builds its input, not the detector),
`ticker-view-v0.md` (`since` at ticker level).

## What

`cum_share_z` is a level. Because it integrates from the open, a 09:35 burst
holds a basket at +2.5σ at 10:15 even if flow normalised half an hour ago. A
run is level **plus** recency, current pressure, and price response. Three
columns, inserted after `cum_share_z` (README order):

    since  = first ET minute today at which |cum_share_z| ≥ SignificantZ; blank if never
             (the ticker-view `since`, lifted to basket level; same threshold, same clock)
    5m_z   = mean of flow_share_z over the last RunWindow = 5 completed minutes
             (the per-minute atom from 3.1 D5, averaged — a mean of z's, NOT a sum of medians)
    vs_SPY = mean over members of (member since-open return) − SPY since-open return, equal-weighted
             (3.6 RelPerf; reference point D8 = since open, identical to breadth's anchors)

The row reads as a sentence, and the footer says so: "+2.4σ since 09:41,
5m +1.8σ, +0.9% vs SPY" (share above typical, began 09:41, still above
typical in the last five minutes, price ahead of the index) versus "+2.4σ
since 09:33, 5m −0.2σ, flat vs SPY" (share above typical since 09:33, last
five minutes at typical, price with the index). Measurements; no
recommendation.

## Decisions

- **R1 — `5m_z` is a mean of z's.** Each minute's `flow_share_z` is already
  matched-bucket and σ-guarded; averaging five of them needs no new baseline
  and does not commit the medians-don't-commute sin (D7's reason). Gaps in
  any of the five minutes → gap (never a mean over fewer). Gap until five
  completed session minutes (09:36 first render). `RunWindow = 5`, code
  constant, ledger-tunable; the 15-minute variant §2.4 also names registers
  on the dev view as `15m_z` for comparison.
- **R2 — `5m_z` is 3.7's input, not 3.7.** No CUSUM, no slope, no sentence
  here. When 3.7 is picked up, its ShiftSlope is `d/dt` of this series, and
  its disjoint-set treatment (A6) applies there, not here — display keeps
  full membership (A6(e)).
- **R3 — `since` at basket level** uses `cum_share_z`'s completed-minute
  series scanned from the open each render (570..m — cheap; or memoized
  first-crossing per basket per render). Renders `09:41`; blank if never;
  a basket that crossed and fell back keeps its `since` (the record), as in
  ticker-view-v0. Negative crossings show `since` too — the sign is on the z
  beside it.
- **R4 — `vs_SPY` reuses breadth's machinery.** `internal/breadth` already
  computes every member's since-open return vs SPY (B2/B3 anchors,
  `FirstTradePrice`/`LastTradePrice`); new `internal/relperf` takes the
  per-member returns from a breadth-exported accessor and averages them
  equal-weighted (3.6: so the term measures the sector, not its largest
  member). Members without an anchor are excluded and the count is
  disclosed on the dev view (`vs_SPY_n`). SPY unmeasurable → gap. Rendered
  `+0.92%`; coloured by the same ±10 bps dead-band as the SPY status line
  (green above, red below, none inside) — the index's own posture rule,
  applied to the basket.
- **R5 — Colour and flags.** `5m_z` bold green/red at ±`SignificantZ`
  (glyph `R`, MO-6). `since` unstyled. `vs_SPY` as R4.
- **R6 — Estimator caveat, carried.** `flow_share_z` is a bounded-ratio z
  (less skewed than dollar z's) — `5m_z` inherits that; the log-space
  evaluation (MO-9) still lists it for the asymmetry count.

## Done when (replayed data)

1. Unit: `5m_z` window boundaries (4 vs 5 completed minutes; a gap inside
   the window; 09:36 first value); `since` first-crossing incl. cross-and-
   fall-back; `vs_SPY` equal-weight on a hand-built three-member basket incl.
   an un-anchored member; determinism.
2. Replay `-view-mode trader -view-at 10:00:00` on the busiest 08-2x open:
   all three columns populate; one basket's `5m_z` and `vs_SPY` recomputed
   in Python from the bucket CSV + share profile (5m_z) and the tape
   anchors (vs_SPY) match the rendered digits.
3. The two example sentences above are found, or their nearest real
   equivalents, on a replayed morning and recorded in the close notes — the
   story is accepted when a run and a spent run are visibly different rows.
4. Footer scanner passes; rank unchanged.

## Decisions consumed

O2; 3.1 D5/D7 (atom, medians-don't-commute); 3.2 B1–B4 + T9; 3.6 + D8;
3.7 (input only) + A6(e); ticker-view-v0 `since`; T2; scope law.
