# MO-8 — Run metrics: `since`, `5m_z`, `vs_SPY`

Status: **implemented on branch mo-8-run-metrics, awaiting review** (written 2026-08-26; implemented 2026-08-27). The "is there a run to chase" story
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
  completed session minutes (09:35:00 first render). `RunWindow = 5`, code
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
  `+0.92%`. *Review 2026-08-27:* the cell renders only when measured ≥
  `MinMeasured(N)` = max(min(3, N), ceil(N/2)) (else the gap), and ships
  **unstyled** — the ±10 bps dead-band survives as `relperf.Calc.Flag`
  (MO-6) and as a ledger candidate (close notes).
- **R5 — Colour and flags.** `5m_z` bold green/red at ±`SignificantZ`
  (glyph `R`, MO-6). `since` unstyled. `vs_SPY` unstyled (R4 as reviewed).
- **R6 — Estimator caveat, carried.** `flow_share_z` is a bounded-ratio z
  (less skewed than dollar z's) — `5m_z` inherits that; the log-space
  evaluation (MO-9) still lists it for the asymmetry count.

## Done when (replayed data)

1. Unit: `5m_z` window boundaries (4 vs 5 completed minutes; a gap inside
   the window; 09:35:00 first value); `since` first-crossing incl. cross-and-
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

## Close notes (2026-08-27, branch `mo-8-run-metrics`)

- **Homes.** `since` / `5m_z` (+ dev `15m_z`): `internal/flowshare/run.go`,
  `flowshare.Run` — one per process, `NewRun(store, union, shares, floors)`,
  `ExtendTrader` anchored after `cum_share_z` (panics on a miss, delta's
  posture). `vs_SPY` (+ dev `vs_SPY_n`): new `internal/relperf` over a new
  `breadth.Calc.Returns` accessor (every member's since-open return and
  SPY's, from breadth's own anchors and last prices — no price math in
  relperf), `ExtendTrader` anchored after `5m_z`. Predicates for MO-6:
  `Run.RunFlag` (`R`: |5m_z| ≥ SignificantZ) and `relperf.Calc.Flag`
  (beyond ±DeadBand); each is the cell's own colour predicate (G1).
  Constants: `flowshare.RunWindow = 5`, `flowshare.RunWindowLong = 15`,
  `relperf.DeadBand = breadth.DeadBand`.
- **Series posture (review 2026-08-27: incremental).** The union's
  completed-minute series lives on the `Run` for the session and grows by
  one minute per minute — per new minute one `Window` read per member,
  the full rebuild's recurrence restricted to a suffix, so the bytes are
  identical by induction. Late prints: `bucket.Store` gained an amendment
  ring (`AmendedSince(gen)` — every trade written behind the store's
  latest second records its second; the reader truncates the series to
  the earliest amended minute and recomputes the suffix; a wrapped ring
  (`AmendRing` = 4096 since the last render) or a date change rebuilds
  from the open). Basket z series extend lazily and are recomputed when
  the membership slice's identity changes (hot-reload). The internal/bucket
  change is the review-authorized hook only (+ `TestAmendedSince`).
  Verified: `TestRunIncremental` (late print, two late prints, ring wrap,
  membership replaced, new date, back to the first date — incremental ==
  fresh rebuild at every step) and `TestRunIncrementalMatchesRebuildFixture`
  (env-gated on the fixture path; replays 08-24 through the live decoder
  and compares the three cells for all 22 baskets at every new session
  second 09:30:00–10:00:00 against a fresh rebuild): PASS — 1801 render seconds x 22 baskets, 520,568 amendment generations seen (~290/s: the ring holds ~14 s at that rate), zero mismatches (77 s).
  Per-render cost (`BenchmarkRunPrime`, dense synthetic tape — 164 members
  printing every second; before = rebuild from the open, after = the
  incremental step at a minute boundary; between boundaries a render is a
  memo check):
      before (rebuild from the open)   n=60: 24.9 ms/render   n=389: 113.1 ms/render
      after  (incremental minute step)  n=60:  0.44 ms/minute  n=389:   0.58 ms/minute (other seconds: memo check)
  The per-minute z's are bit-identical to the dev view's `flow_share_z`
  (same single-minute window, same union-order sums;
  `TestRunSeriesMatchesCells` compares the float operands). The cumulative
  series accumulates minute sums while the `cum_share_z` cell reads one
  whole-window sum — same buckets, same time order, different float
  association (≈1e-16 relative): far below the rendered digit and never a
  different basis. Follow-up (not done here — more than the hour the
  review allowed): `tickerview.prime` still makes its own per-member-minute
  pass; it needs per-minute cross dollars and the last minute's counted
  shares beside what this series holds, so consuming the series means
  widening it to a fourth/fifth column and re-pointing the drill-down's
  reads. Ledger it as "one minute series, two readers".
- **`since` blank vs gap (deviation from ticker-view-v0, flagged).** The
  ticker view renders blank both for "never crossed" and "nothing
  measurable". Here blank = every completed minute so far had a defined
  `cum_share_z` inside ±2σ (a measurement: no crossing yet); `·` = no
  minute had a defined z (pre-open, no baseline). The contract's "gaps
  render ·" wins over the ticker view's convention; a blank inside an
  otherwise gapped row would read as a hole.
- **Partial membership on `vs_SPY` (R4; review 2026-08-27).** Mean over
  the members that have an anchor and a last price, rendered only when
  measured ≥ `relperf.MinMeasured(N)` = max(min(`MinMeasuredAbs` 3, N),
  ceil(N × `MinMeasuredFrac` 0.5)) — else the gap; `vs_SPY_n` stays
  dev-only. `TestMinMeasured` covers both sides of the floor. On 08-24
  every basket was n/n at 10:00, so nothing on this tape gaps by the rule.
- **`vs_SPY` ships unstyled (review 2026-08-27).** At the SPY line's
  ±10 bps band the cell was red on 19–21 of 22 rows at every frame on a
  −0.3% SPY morning — weight, not information (the MO-5 net_z argument).
  `relperf.Calc.Flag` keeps the dead-band predicate for MO-6. Ledger
  candidate: colour only when the basket's sign differs from the index's
  (basket up on a down index or down on an up index), which on 08-24 would
  have lit `neoclouds_dc_builders` at 09:36 (+1.32% on a −0.2% SPY) and
  `proof_tier_ai_megacap` at 09:40/09:55/10:00 (+0.06 to +0.19%) and
  nothing else — the rows where the price response actually diverged.
- **First `5m_z` render.** Five completed session minutes exist at
  09:35:00 (09:30–09:34), so the first cell is 09:35:00, not the 09:36 the
  spec text says; the test pins 09:35:00 (`TestRunWindowBoundary`).
- **Frames (08-24 fixture, 2679ad0 baseline vs this branch, same
  `-options-*` flags as MO-5):** at 09:45:00 and 10:00:00 every column
  other than `since`/`5m_z`/`vs_SPY` (and the three footer lines + the
  reading guide) is byte-identical to the baseline
  (`scratchpad/mo-8/strip_cols.py`, `diff` empty); two 09:45 renders
  byte-identical; rank unchanged.
- **Python recomputation at 10:00 (window 09:55–09:59)** from the bucket
  CSV + `data/profiles/baskets/*.csv` + `_floors.csv` + tape anchors
  (`scratchpad/mo-8/recompute.py`), all matching the rendered digits:
  critical_minerals 5m_z +0.6 (minutes −0.13 +0.50 −0.05 +1.54 +1.32),
  since 09:36, vs_SPY −1.24% (7/7); robotics_av +0.5 / 09:30 / −2.45%
  (5/5); semis_analog_power_auto +1.9 / blank (cum z −0.02) / −1.55%;
  proof_tier_ai_megacap +0.2 / blank / +0.19%. SPY −0.18% since open.
- **Trader read, 08-24 09:36–10:00** (SPY −0.34% at 09:45, −0.18% at
  10:00; breadth red nearly everywhere; grid in
  `scratchpad/mo-8/tabulate.py` output):
  - *Live run, then spent:* `robotics_av` at 09:36 read
    `+5.2σ since 09:30, 5m +3.6σ, −2.08% vs SPY` (TSLA 96% of the basket
    — dollars far above typical, still arriving, price 2% behind the
    index); by 09:45 `+2.4σ since 09:30, 5m +0.7σ, −2.76%` and by 09:55
    `+2.4σ since 09:30, 5m +0.1σ, −2.88%` — the level held for twenty
    minutes after the flow normalised, which is exactly the case the
    story exists for.
  - *A run that started later:* `critical_minerals` crossed at 09:36
    (`+2.9σ since 09:36, 5m +3.3σ, −0.58% vs SPY` on the 09:40 frame,
    CLF +11.7σ on the strip), faded (09:50: `5m +0.2σ`), then a second
    leg (09:55: `+2.1σ since 09:36, 5m +2.1σ, −1.59%`).
  - *Spent:* `power_equipment` 09:36 `+1.7σ, 5m +3.8σ` (recency ahead of
    level — HUBB's 09:34 crossing), since 09:36 recorded, then
    `+0.5σ since 09:36, 5m −0.5σ, −0.86% vs SPY` at 10:00;
    `semicap_frontend` `+2.1σ since 09:30` at 09:36 → `+0.5σ, 5m −0.9σ` at
    09:50.
  - *Pressure without level:* `epc_labor` and `semis_compute` at 09:45
    (`5m +2.3σ` with cum z +1.1 / +0.9, no since) and
    `semis_analog_power_auto` at 10:00 (`5m +1.9σ`, cum z −0.0) — the
    `R` glyph would light with `Z` dark, the reverse of robotics at 09:55.
  - `vs_SPY` was negative for 21 of 22 baskets at most frames (−0.4% to
    −3.9%) on a −0.2% to −0.3% index: the morning's flow runs were not
    price runs, which the column states plainly; `neoclouds_dc_builders`
    +1.32% (8/11 ↑) at 09:36 → −0.43% at 09:40 is the one five-minute
    price reversal on the grid.
  - The row sentence works: a live and a spent run are visibly different
    rows (robotics 09:36 vs 09:55; power_equipment 09:36 vs 10:00). The
    spec's `+0.9% vs SPY` case did not occur on this tape — every real
    sentence ended "behind the index".
- **Colour density.** `5m_z` lit 1–3 baskets per frame at ±2σ (fine);
  `vs_SPY` resolved above (unstyled).
- Not run: the busiest 08-2x open (spec Done-when 2 names it) — the only
  fixture available to this story is 08-24 cut to 10:00; the 10:00:00
  frame on it is what was checked.

## Decisions consumed

O2; 3.1 D5/D7 (atom, medians-don't-commute); 3.2 B1–B4 + T9; 3.6 + D8;
3.7 (input only) + A6(e); ticker-view-v0 `since`; T2; scope law.
