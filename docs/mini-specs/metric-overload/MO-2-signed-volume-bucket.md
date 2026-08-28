# MO-2 — Signed volume into the 1-second store (dev-plan 3.3, part 1)

Status: **implemented on branch mo-2-signed-volume, awaiting review** (2026-08-27; written 2026-08-26). Story: DEV-PLAN Phase 3 → 3.3 [F2],
split in two: this spec is the classifier + storage; MO-3 is the view.
Gated by MO-1 (column order settled). **This is the only story in the folder
that touches `internal/ingest` and `internal/bucket`.** Read first:
DEV-PLAN 3.3 (the cascade, the 2026-08-11 vendor note, the 2026-08-14
on-time amendment, the ingest-cap note), `docs/foundations/print-inclusion.md`
(quote-condition validity — 3.3's appendix), `1.4-bucket-store.md`,
`docs/foundations/stores.md`, backlog "Bucket CSV column-compatibility
contract".

## What

Classify every eligible print by aggressor side against the in-memory NBBO
that `internal/ingest` already maintains for exactly this purpose
(`types.go` package doc: "current NBBO — the Lee-Ready lookup"), and store the
result in the 1-second bucket so it is replayable, persisted all day at one
resolution, and available to every read-time metric:

    Bucket gains   AskSide, BidSide ClassAgg   (Trades, Shares, Dollars)
                   TickRule int64       (prints classified by the tick rule — the weakest step)
                   Late     int64       (prints skipped as late — denominator-only)
    Unclassified = Counted − AskSide − BidSide, derived at read time; never stored.

Top-level `Trades/Shares/Dollars` and `Class[]` are untouched.

## The cascade (one policy source: new `internal/aggressor`)

Step (1) of the dev-plan cascade — venue-supplied side — does not exist on
Massive SIP consolidated (vendor note 2026-08-11); the cascade starts at (2).
For each trade, in order, the first rule that fires wins:

1. **Eligibility.** Class ∈ {CONTINUOUS, BLOCK} only (`classify.Class` doc:
   NON_PRICE_FORMING "excluded from Lee-Ready"; crosses, duplicates and
   NON_FLOW never). Ineligible → not counted anywhere here (it is still in
   `Class[]` as before).
2. **On-time.** `SipTs − PartTs > LateTolerance` or `PartTs == 0` → `Late++`,
   unclassified. `LateTolerance = 1s` (default; the flat-file lateness study
   sizes it — backlog). Rationale: a late print's book is not the book it
   executed against.
3. **Book valid.** NBBO snapshot from `t.State.NBBO()`: `Bid > 0 && Ask > 0 &&
   Bid <= Ask` and the last quote's conditions leave the NBBO usable per
   `print-inclusion.md`'s quote-condition table (consumed here for the first
   time — the table's "usable for classification" column becomes a `classify`
   func). Invalid → unclassified.
4. **Quote-relative.** `Price >= Ask` → AskSide; `Price <= Bid` → BidSide.
5. **Tick rule.** Strictly inside the spread: compare with the symbol's last
   *different* eligible trade price (new `SymbolState` field, pipeline-
   goroutine-only like `lastTradeSeq`): higher → AskSide, lower → BidSide, no prior
   different price → unclassified. Counted in `TickRule` either way it lands.
6. Anything else → unclassified.

**Ordering guarantee, verified:** `ingest.Pipeline.Run` applies each quote to
`SymbolState` before calling the observer for the next message, on the one
pipeline goroutine — so the NBBO read inside `ObserveTrade` is the book as of
arrival, identical in live and replay.

## Decisions

- **S0 — Names are measurements too.** Fields and columns are `AskSide`/`BidSide` (`ask_*`/`bid_*`), matching the options taxonomy (`ask_side`/`bid_side`, 7.3 K5); no identifier says buy or sell.
- **S1 — Storage, not read-time.** Signed sums cannot be recomputed from the
  bucket store (the book is gone by then), so they are stored — the 1.4
  "store what cannot be derived" rule. One resolution, all day (CLAUDE.md).
- **S2 — Home.** Policy in `internal/aggressor` (side, tolerance, book
  validity); the bucket calls it from `ObserveTrade` and adds to the new
  fields. `internal/ingest` gains only the last-different-price field and its
  update in `applyTrade`. `internal/classify` gains the quote-condition
  usability func. Nothing else under ingest/feed changes.
- **S3 — Determinism.** No wall clock, no goroutine-order dependence (single
  pipeline goroutine), explicit `float64(price*size)` before `+=` (the FMA
  note in `ObserveTrade`). Two replays of one capture → identical signed
  columns, byte for byte.
- **S4 — CSV: eight additive columns** (`ask_trades, ask_shares, ask_dollars,
  bid_trades, bid_shares, bid_dollars, tick_rule, late` — naming
  follows the existing per-class pattern). The reader maps by name and sets
  `Store.HasSigned = false` when they are absent, the `profile.HasCumDollars`
  posture: old bucket files load, every signed read renders a gap. The
  column-compatibility contract (backlog) is honored: readers never index by
  position.
- **S5 — The honesty line.** `cmd/live` and `cmd/replay` print at end of
  session, next to the existing `done:` stats: `aggressor: eligible=N
  ask=x% bid=y% tick=z% late=l% unclassified=u%`. The tick share is the F2
  data-quality metric the dev-plan asks for; it is reported, never hidden.
  `CondOverflow` nonzero → the line is prefixed `!!` (ingest-cap note).
- **S6 — Backfill the corpus.** Every recorded capture carries quotes, so
  `cmd/replay -capture <day> -buckets` regenerates that day's bucket file with
  the signed columns. Flat-file days replayed with `-trades -quotes` likewise;
  `.trades-only.csv` days stay `HasSigned=false` forever (honest). The
  regenerated files are byte-identical to the old ones in every pre-existing
  column (checked by diffing the projected columns).
- **S7 — Per-print truth is not claimed.** Package doc states, verbatim from
  the dev plan: midpoint / price-improved retail executions fall to the tick
  rule; DeltaRatio is an aggregate approximation, not print-level truth;
  Lee-Ready degrades at the open (F2 — MO-3's D9 window is the mitigation).

## Done when (replayed recorded data)

1. Unit (`internal/aggressor`): table-driven — at ask, above ask, at bid,
   below bid, inside → tick up / tick down / no prior price, crossed book,
   zero bid, unusable quote condition, late by 1ns over tolerance, `PartTs=0`,
   ineligible class; every branch lands in exactly one counter.
2. Bucket unit: `Window` sums the new fields; `Unclassified` derived equals
   `Counted − AskSide − BidSide` on a hand-built store; CSV round-trips; an old CSV
   (no signed columns) loads with `HasSigned=false`.
3. Replay of one production capture twice → identical signed columns and an
   identical `aggressor:` stats line; the line is recorded in the story close
   notes (first measured ask/bid/tick/late split on this tape).
4. Independent check: one symbol-minute's `ask_dollars`/`bid_dollars`
   recomputed in Python from the capture (trades + quotes, same cascade)
   matches the bucket CSV to the printed digit
   (`discovery-scripts/view-verify/verify-aggressor.py`).
5. Corpus regenerated (S6); projected pre-existing columns byte-identical to
   the previous files for every day.
6. `go test -race ./internal/...` green.

## Decisions consumed

DEV-PLAN 3.3 cascade + vendor note + on-time amendment + ingest-cap note;
D9 (consumed by MO-3); D15 precedent (unclassified in the denominator only);
0.3 print inclusion + quote-condition appendix; 1.4 store rule; F2; scope law.
