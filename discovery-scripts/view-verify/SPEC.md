# Spec — independent Python verification of the 3.0 dev view numbers

Written 2026-08-16. Purpose: widen the one-basket/one-minute hand check done
at 3.0 close into all 22 baskets × a handful of minutes, before the first
live morning. This is a verification script, not product code — it lives in
`discovery-scripts/`, runs on demand, and its value is the report it prints.

## What is being verified

The dev view's three completed-minute columns — `$last`, `base`, `rvol` —
for every basket, at K chosen minutes of the 2026-08-14 session, compared
against an independent recomputation that shares as little code as possible
with the view:

- **View side** (Go, capture-fed): `cmd/replay -capture ... -view -view-at`
  renders the table from the 08-14 capture through the live decoder,
  pipeline, RAM bucket store, and profile reader.
- **Python side** (flat-file-fed): recompute the same numbers with stdlib
  Python from `data/buckets/2026-08-14.trades-only.csv` (built from vendor
  flat files, not the capture) and the `data/profiles/*.csv` files directly.

Two near-disjoint paths agreeing is the point. They share only the profile
CSV *files* (both sides read the same baselines — `base` must therefore
match exactly) and the vendor's market data itself.

## Inputs (all already exist; do not rebuild anything)

- `data/capture/2026-08-14/stream.jsonl` — capture, spans 11:51:35–19:59:59 ET.
- `data/buckets/2026-08-14.trades-only.csv` — flat-file-derived bucket store.
- `data/profiles/<SYM>.csv` — 20-day profiles built `-through 2026-08-13`
  (the check-day is correctly excluded from its own baseline).
- `docs/foundations/morning-tape-baskets-v2.json` — basket membership
  (`baskets.<name>.members`; de-duplicate members; benchmarks are NOT rows).

## Minutes to check

Render seconds, all `HH:MM:00` so `$last` is the fully-completed prior
minute; all inside both the capture span and the regular session:

    12:00:00  12:31:00  13:01:00  14:00:00  15:30:00  15:59:00

(`$last` minutes: 11:59, 12:30, 13:00, 13:59, 15:29, 15:58.) Do not use
render seconds ≤ 11:52:59 (capture starts 11:51:35 — the prior minute would
be partially covered) or ≥ 16:01:00 (prior minute outside the profiled
session → gaps, nothing to compare).

## Procedure

1. **View side.** For each render second T, run from the repo root:
   `go run ./cmd/replay -capture data/capture/2026-08-14/stream.jsonl -view -view-at T`
   (~2–3 min each; run sequentially). Parse stdout: the final table starts
   at the line matching `^T ET  2026-08-14`; then a `BASKET ...` header;
   then 22 rows of whitespace-separated fields
   `name N $last base rvol $now`. Basket names contain no spaces.
2. **Python side.** For each basket and each `$last` minute M:
   - `$last` = Σ over members, over epoch seconds `[start(M), start(M)+60)`,
     of counted dollars = `continuous_dollars + block_dollars +
     non_price_forming_dollars` from the bucket CSV (map columns by header
     name). `start(M)` from `zoneinfo("America/New_York")` — never a fixed
     UTC offset.
   - `base` = Σ over members of `median_dollars` at
     `minute_of_day = 60*HH + MM` of M, from each member's profile CSV.
   - `rvol` = `$last / base`.
3. **Format-match.** The view prints through its formatter; port it exactly
   and compare STRINGS, not floats, for `$last` and `base`:
   - `fmtDollars(v)`: `<1e3` → `"%.0f"`; else scale by k/M/B at
     1e3/1e6/1e9 and format the scaled value `x` as `"%.2f"` if `x<10`,
     `"%.1f"` if `x<100`, else `"%.0f"`, with the unit suffix. Use Go/C
     round-half-to-even semantics (Python's `"%.2f"` matches).
   - `rvol` prints as `"%.2f"`; gap is `"·"` (base 0 or out-of-session).

## Pass criteria (per basket × minute)

- **base: exact string match, no tolerance.** Same files feed both sides;
  any mismatch is a bug (wrong minute key, membership drift, summing error).
- **rvol: |view − python| ≤ 0.01** (one last-digit step — the two sides'
  numerators come from different vendor records).
- **$last: relative delta ≤ 0.5%**, computed on the raw Python value vs the
  view's parsed value (parse the view string back to a number; the
  formatter's 3 significant figures bound that parse error at ~0.5%, so
  also report the string-equality rate separately). The ~0.2% capture-vs-
  flat-file residual is known (1.2 reconciliation); the check is that it
  stays small and unbiased, not that it is zero.
- Any `·` on one side must be `·` (or the documented gap condition) on the
  other.

## Report

Write `discovery-scripts/view-verify/output/report.md`: one table per
minute (22 rows: basket, view `$last`/`base`/`rvol`, python equivalents,
$last relative delta, PASS/FAIL per criterion), then a summary block —
totals per criterion, worst $last delta with its basket/minute, and the
mean signed $last delta (a consistent sign = systematic source difference;
mixed signs near zero = noise, both acceptable; a large or one-basket-only
bias = investigate). Print the summary to stdout too. Exit nonzero if any
FAIL.

## Constraints

- Python 3 stdlib only (`csv`, `json`, `zoneinfo`, `subprocess`, `re`).
- Read-only with respect to `data/` and Go code: no rebuilds of profiles or
  buckets, no edits outside `discovery-scripts/view-verify/`.
- The script must be rerunnable for future sessions:
  `python3 verify.py --date 2026-08-14 --capture data/capture/2026-08-14/stream.jsonl`
  with the minute list as a flag (defaults above).

## Done when

The report exists with all 22 baskets × 6 minutes evaluated, every `base`
matches exactly, and the summary explicitly states either "all criteria
pass" or lists each FAIL with the raw numbers needed to investigate it.
