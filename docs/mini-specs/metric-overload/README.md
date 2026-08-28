# Metric overload — the curation story set

Status: **open** (written 2026-08-26; owner decisions taken in chat the same day).
Not one dev-plan story: a folder of bounded stories that together turn the
current trader frame into the screen the trader asked for. Read `CLAUDE.md`
(scope law, invariants) first; every spec here consumes it.

## Why this folder exists

The infrastructure is in place — capture, replay, the 1-second store, 20-day
matched baselines, the trader frame on :8787, the options tape on :8788, and
the per-ticker drill-down (`ticker-view-v0.md`, landing on `indiv-tickers`).
The owner's next question was the metrics as a whole: which still belong,
which don't, what is missing, and how the screen paints one picture. The
trader's three questions, in his words:

1. **Where is money flowing** — which baskets.
2. **Is there a run to chase** — something that started recently and is still going.
3. **What are the individual tickers doing** underneath a basket.

Plus two owner asks: show whether the money being allocated is **ask-side
(aggressor-buy) volume** — not yet measured on equities — and put **options and
equities on one screen**.

## What the inventory found (2026-08-26)

On the live trader frame today: `cum_share`, `cum_share_typ`, `cum_share_z`
(rank key), `relative_vol`, `breadth`, `up_on_vol`, `concentration`,
`concentration_day`, `pre_vol`, `pre_share`, `pre_conc`, the SPY status line,
the crossings strip, and the `?basket=` drill-down.

Spec'd in `INITIAL-PROJECT.md` §2 / `DEV-PLAN.md` Phase 3 but **not built**
(grep-verified, zero code): NetDelta/DeltaRatio and any Lee-Ready (3.3 —
`internal/ingest` maintains the NBBO and nothing consumes it), ShiftSlope /
CUSUM / regime sentence (3.7), VWAP posture (3.4), off-exchange share (3.5 —
venue is decoded at `internal/feed/live.go` but never reaches the bucket),
RelPerf (3.6), Glow (3.8), tiles/sparkline/scrubber (5.x), bond tape (backlog).

Options **is live** on :8788 (`cmd/live-options` → `cmd/replay-options -follow`),
but never inside the equity view live: `cmd/live` passes a `nil` OptionsSource
to the ticker view; `conv_z`/`net_z` in the drill-down are replay-only.

Smells to clear while here: two `relative_vol` implementations
(`internal/devview` and `internal/flowshare`); ticker `rvol` is **shares** while
basket `relative_vol` is **dollars** under a near-identical name; `flowshare.Share`
has no callers; three σ-floor derivations (volume families over tickers, share
families over baskets, options over profiles) — all 0.25×, all correct, but three
places to read; dollar-family z's read hot more easily than cold (right skew —
backlog "estimator reevaluation").

## Owner decisions (chat, 2026-08-26)

- **O1 — Options on the equity screen, live.** Basket `conv_z`/`net_z` on the
  trader table AND `conv_z`/`net_z` in the per-ticker drill-down and strip, as
  **measurement columns** — never a brightness input or driver. This overrides
  the 6.6 gate ("after the trader confirms daily use of v1") for columns only;
  MO-5 records the amendment in DEV-PLAN Appendix C. Brightness/driver use stays
  gated.
- **O2 — Priority.** Signed volume (dev-plan 3.3) and the column prune/merge
  first. Run metrics (`since`, `5m_z`, `vs_SPY`) after.
- **O3 — Row emphasis.** A `flags` glyph column, each glyph a named measurement
  with its own threshold, plus per-cell colors at each column's own threshold.
  **No composite score, no row-tier backgrounds** — the ticker-view "no
  composite" rule (`ticker-view-v0.md` scope law) extends up to basket level.
- **O4 — Premarket is its own tab** on the web server; the default tab is
  picked by the ET clock (before 09:30 → premarket; after → session; the trader
  can switch either way).
- **O5 — Split the work** into sizable stories, each with its own mini-spec,
  branch, and replay acceptance. This folder.

## Question → metric map

| trader question | component | metric | story |
|---|---|---|---|
| where is money flowing | level vs typical by this time | `cum_share_z` (rank key), `cum_share`, `cum_share_typ` | on screen |
| | one name or the group | `concentration_day`, `breadth` | on screen |
| is there a run | recency — when it started | `since` (first ±2σ minute) | MO-8 |
| | current pressure — still running? | `5m_z` (5-min mean of per-minute `flow_share_z`) | MO-8 |
| | price response | `vs_SPY` (equal-weighted RelPerf) | MO-8 |
| individual tickers | the drill-down + crossings strip | `ticker-view-v0` | other session |
| is the money ask-side | equities | `delta` with `class%` beside it | MO-2, MO-3 |
| | options (vendor-sided) | `conv_z`, `net_z` per basket and per ticker | MO-4, MO-5 |
| coincidence of signals | the eye's scan | `flags` glyphs | MO-6 |
| before the bell | where premarket dollars sit | `pre_vol`, `pre_share`, `pre_conc` on their own tab | MO-7 |

`cum_share_z` alone cannot answer "run": it integrates from the open, so a
09:35 burst holds a basket at +2.5σ at 10:15 even after flow normalized. A run
needs level + recency + current pressure + price response; the row then reads
as a sentence — "+2.4σ since 09:41, 5m +1.8σ, +0.9% vs SPY" (still running)
versus "+2.4σ since 09:33, 5m −0.2σ, flat vs SPY" (it happened, it is over).
Both are statements of measurement.

## Metric triage

| metric | verdict | reason |
|---|---|---|
| `cum_share`, `cum_share_typ`, `cum_share_z` | **keep** | the product's core claim; typ is the auditable operand; z is the sort key |
| `concentration_day` | **keep** | reconciles with `cum_share` (same window and slice) |
| `breadth` | **keep, merge** | absorbs `up_on_vol` into one cell `7/9 5$` (MO-1) |
| `up_on_vol` | **merge** | prints breadth's denominator twice today |
| `pre_share` | **keep** | pre-open rank key; one context column after the open |
| `pre_vol`, `pre_conc` | **move** | to the premarket tab (MO-7); three frozen columns all day is real estate |
| `relative_vol` (basket, per minute) | **demote to dev** | single completed minute flickers; duplicated implementation |
| `concentration` (per minute) | **demote to dev** | the day version is the one that reconciles with the story column |
| `flow_share`, `flow_share_z` | **stay dev** | the atom; MO-8's `5m_z` is its de-flickered trader form |
| `breadth_detail`, `vol_detail` | **stay dev** | detail reads |
| crossings strip, drill-down columns | **keep** | `ticker-view-v0`; `rvol` renamed `rvol_sh` (MO-1) |
| `delta`, `class%` | **add** | MO-2/MO-3 — the ask-side question, with its honesty number attached |
| basket `conv_z`, `net_z` | **add** | MO-5 — O1 |
| `flags` | **add** | MO-6 — O3 |
| `since`, `5m_z`, `vs_SPY` | **add** | MO-8 — the run question |
| `delta_z` | **add later** | MO-9 — needs ≥10 signed bucket days |
| VWAP posture (3.4) | **defer** | redundant with breadth + `vs_SPY`; revisit if the ledger asks |
| off-exchange share / dark dot (3.5) | **defer** | venue must first reach the bucket; a *maybe* by F3 |
| Glow (3.8) | **defer** | a composite of two inputs is a worse sort key than `cum_share_z`; O3 bars composites for now |
| CUSUM / regime sentence (3.7) | **defer** | `5m_z` is its input; build the input first |
| bond tape | **backlog** | unchanged |

## Target basket row (after MO-1..MO-8)

Column order is fixed here so snapshot tests change once per story, not
reshuffle:

    flags  cum_share  cum_share_typ  cum_share_z  since  5m_z  vs_SPY  breadth  concentration_day  delta  class%  conv_z  net_z  pre_share

Rank: `cum_share_z` desc (pre-open `pre_share`, unchanged). Every new column
gaps (`·`) until its inputs exist; a story that ships before its neighbour
simply leaves that neighbour's slot absent — order, not presence, is fixed.

## Cross-cutting rules (every spec consumes these)

- **Scope law.** Rendered strings are statements of measurement. "ask-side
  minus bid-side", "above own typical" — never buy/sell, never a recommendation.
  Every new footer passes the existing no-buy/sell substring scanner test
  (`internal/flowshare` / `internal/conviction` precedent).
- **`zguard.Z` is the only z.** σ guard before every z; σ=0 is a defined null.
- **Gaps, never fake zeros.** 0/0 is `·`; below `MinProfiledDays` is `·`.
- **Render is a pure function of (store state, second).** Colored bytes are
  deterministic bytes; snapshot tests assert them.
- **Acceptance on replayed recorded data**, never live.
- **Thresholds are code constants tuned at the nightly ledger** (6.5), not
  config: `SignificantZ` 2.0, `HighlightFrac` 0.70, `VolK` 1.5,
  `PersistMinutes` 3, `MinProfiledDays` 10; new here: `DeltaHighlight` 0.25
  (MO-3), `LateTolerance` 1s (MO-2), `RunWindow` 5 min (MO-8).
- **No new storage resolution; profiles per ticker; 1-second store all day.**
- **Zero changes under `internal/ingest` / `internal/feed` / `internal/bucket`**
  except where a story names them (only MO-2 does).

## Stories, order, gating

| id | story | gated by | touches ingest/bucket? |
|---|---|---|---|
| MO-1 | column prune / merge | `indiv-tickers` merged | no |
| MO-2 | signed volume into the 1-second bucket (3.3 part 1) | MO-1 | **yes** (only story that does) |
| MO-3 | `delta` / `class%` view (3.3 part 2) | MO-2 | no |
| MO-4 | shared read-only options follower + locking | — | no (options side) |
| MO-5 | options columns on the equity screen, live | MO-4, `indiv-tickers` | no |
| MO-6 | `flags` column | MO-3, MO-5 (glyphs for absent metrics stay unlit) | no |
| MO-7 | premarket tab | MO-1 | no |
| MO-8 | run metrics `since`, `5m_z`, `vs_SPY` | MO-1 | no |
| MO-9 | `delta_z` baseline family + log-space z evaluation | MO-2 + ≥10 signed days | no |

Build order: MO-1 → MO-2 → MO-3 → MO-4 → MO-5 → MO-6 → MO-7 → MO-8 → MO-9.
MO-2 goes early because its bucket columns must accumulate days before MO-9
can exist; MO-4 can run in parallel with MO-2/MO-3 (disjoint code). Each story
is its own branch and PR; a story closes only when its "Done when" passes on
replayed data.

## Not in this folder

Tiles / Glow / sparkline (5.1), the regime sentence (3.7), dark share (3.5),
VWAP posture (3.4), the interactive web view that replaces the terminal frame
(the owner's backlogged presentation rethink — specified after these numbers
have been watched, not before), premarket typ/z (backlog), bond tape (backlog).
