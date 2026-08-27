# Mini-spec — Ticker view v0 (drill-down + crossings strip)

Status: **open** (written 2026-08-26, product owner decisions taken in chat).
Not a numbered dev-plan story: the explanation layer under the basket view.
A basket z is an average that hides its own cause; when SEMIS lights up the
trader's next question is "the group, or NVDA alone?" — this story answers it.
The owner's metric-curation pass (personal backlog: which metrics together
tell a stock's story, presented the way conviction/options is) runs AFTER
this ships and on top of it: v0 shows every per-ticker number so the
curation can be judged on real mornings, not guessed.

Gated by: 8.1 committed. Read first: `CLAUDE.md` (scope law), `trader-view-v0.md`
(T1/T2 — sort and significance), `3.1-flowshare.md` (D7 auction-inclusive
cum family), `7.7-conviction-view.md` (per-ticker options profiles).

## Scope law, restated for this story

Ticker level is where APEX went wrong. Every rendered string is a measurement
of one ticker against its OWN 20-day matched-minute history. No composite
score, no "hot" scale, no ranked list whose key is not a named metric, no
price-change-as-signal. A ticker row *explains* a basket row; it never
competes with it. FlowShare and breadth are basket concepts and do not
appear at ticker level (owner, 2026-08-26).

## What

Two surfaces on the existing frame format (the 3.0 terminal table; the
interactive web view is a separate later story — see "Not in this story"):

### 1. Drill-down — one basket's members

`cmd/replay -view -view-mode trader -basket SEMIS` and, live, a keypress /
query on the frame server (`?basket=SEMIS`) render the basket's members as
rows under the basket line. All members shown (baskets are 5–12 names —
nothing to trim), sorted by `cum_$_z` descending, gaps last, ties by symbol.

Columns (every one is per-ticker, computed from the bucket store at read
time; no new stores):

| column | what | baseline |
|---|---|---|
| `last` | last trade price | — |
| `open%` | % from the opening cross (cross VWAP = dollars/shares of the opening-cross class; blank until the cross prints) | — |
| `cum_$` | $vol since open, auction-inclusive (D7 slice) | — |
| `cum_$_typ` | typical `cum_$` by this minute-of-day | **new** per-ticker cum-dollar profile (below) |
| `cum_$_z` | (cum_$ − typ) / max(MAD-σ, floor) | same |
| `rvol` | completed-minute counted shares / matched-minute median (existing 2.1) | per-ticker profile |
| `$_z` | completed-minute dollars z (existing 2.2 floors) | per-ticker profile |
| `of_basket` | this ticker's share of the basket's `cum_$` (the concentration number from the ticker's side) | — |
| `conv_z`, `net_z` | 7.6 per-ticker options z, last completed minute | profiles-options |
| `since` | ET minute `cum_$_z` first crossed ±SignificantZ today; blank if never | — |

Honesty labels in the footer: `$_z`/`rvol` exclude crosses, `cum_$` includes
them (the two slices on one screen are deliberate, as in trader-view-v0);
`conv_z` is null until the options profile gate (MinProfiledDays) is met;
`open%` is from the auction cross, not the prior close (prior close is not in
the store — deferred, see below).

### 2. Crossings strip — across all baskets

At the bottom of the trader frame, after the basket table and before the
footer: tickers whose `cum_$_z` crossed +SignificantZ today, **newest crossing
first**, capped at 8 rows:

    crossed +2.0σ  09:34 SNDK  +3.1σ  cum_$ 41M (typ 9M)  MEMORY   conv_z +2.4
                   09:41 VIAV  +2.6σ  cum_$ 12M (typ 4M)  OPTICS   conv_z  —

- The threshold IS `SignificantZ` (trader-view T2) — the two panels can never
  disagree about what "significant" means. Negative crossings are listed too
  (`crossed −2.0σ`), separately, since "volume collapsed vs typical" is a
  different statement.
- A ticker stays listed while |z| ≥ threshold; it drops off when it falls
  back (the `since` minute is the record; the ledger keeps history).
- More than 8 crossings → the strip shows the 8 newest and a count:
  `+14 more (broad)`. Thirty names "popping" is a breadth statement, not a
  list — the breadth line already says it. Same rule as the de-grossing
  precedence: a broad-tape day must read as broad.
- Time order, not z order (owner): a strip sorted by z is a screener.

## Baseline addition — per-ticker cum-dollar profile (the one builder change)

`cmd/profiles` gains, per ticker, a `cum_dollars` family: for each session
minute m, the median and MAD over the 20 days of **cumulative** dollars from
open through m, auction-inclusive (D7 slice — same window as the cum-share
family, so `since`/`cum_$_z` and the basket cum-share story share a clock).
Sum-of-medians is NOT a substitute (median of a sum ≠ sum of medians; the
trader-view-v0 owner note already flagged this). Floors: `sigma_floor_cumdollars`
column in `_floors.csv`, same frac rule (2.2 D2). σ guard before every z.
Written as extra columns on the existing per-ticker profile CSV (one file per
ticker stays the rule); readers that ignore the columns are unaffected.
Stamp/inputs line unchanged. **No S3 code in `internal/profile`** (8.3 seam).

## Decisions consumed / owner defaults

- V1 sort key = `cum_$_z` (owner default 2026-08-26; alternatives % from
  open, conv_z — ledger-tunable, not config).
- V2 drill-down shows all members; strip caps at 8, time-ordered, threshold =
  SignificantZ (2.0). Tunable at the nightly review like every default.
- V3 no FlowShare/breadth/dark-share dot at ticker level.
- V4 `open%` is vs the opening cross. Prior-close % deferred: needs the
  previous session's closing cross per ticker, which the nightly could write
  as a one-line-per-ticker table — decide after v0 is watched.
- V5 NetDelta / Lee-Ready per ticker is NOT in v0 (F2: degrades at the open,
  the moment this view is for). Revisit in the curation pass.

## Done when (replayed data, not live)

1. `profiles` writes the cum-dollar family; a ticker's `cum_$_typ` at 10:00
   on a replayed 08-2x day sits near its actual `cum_$` on an average day
   (reported, not gated — the 2.1 RVOL-check posture).
2. `replay -view -view-mode trader -basket SEMIS -view-at 09:45:00` on 08-20
   renders the drill-down with every column populated or honestly blank;
   snapshot test on the frame bytes (Render stays a pure function of store
   state + second).
3. The strip on 08-2x replays lists crossings in time order with `since`
   minutes; on a replayed broad day (the de-grossing reference day, once
   recorded — until then the busiest 08-2x open) the strip shows `+N more
   (broad)` rather than a wall of names.
4. Zero buy/sell language; every string is a measurement (reviewed against
   the scope law checklist).
5. `cmd/live -view` frames carry the strip; drill-down reachable via the frame
   server without touching the capture process.
6. Zero changes under `internal/ingest`, `internal/feed`, `internal/bucket`.

## Not in this story

The interactive web view (click a basket → panel). That is the presentation
rethink the owner has backlogged; it replaces the terminal frame format and
should be specified with the curation pass, not before it. v0 delivers the
numbers in the existing format so they can be watched now.

Prior-close %, per-ticker NetDelta, any ticker-level composite — see V4/V5
and the scope law.
