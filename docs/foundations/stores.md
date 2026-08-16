# Data stores — quick reference

Every place data lives, what it means in business terms, and what regenerates
from what. Vendor channels/endpoints stay in `data.md`; this file is the map
of our own artifacts. Rule of thumb throughout: **the capture and the vendor
flat files are records; everything else is derived and regenerable.**

## The derivation chain

The full story of a live run and everything derived from it. Replay enters
the same pipeline at the same point — that indifference is the design.

```
vendor websocket (cmd/live)          vendor flat files (REST, on demand)
        │                                     │
        │ raw frames, verbatim,               │ counts compared against
        │ BEFORE decode                       │ capture (1.2 reconciliation)
        ▼                                     │ — a check, never a conversion
data/capture/<date>/stream.jsonl ◄╌╌ compare ╌┤
  THE durable record (+ manifest)             │
        │                                     │
        │ live: as received                   │ cmd/replay -trades/-quotes
        │ replay: cmd/replay -capture         │ (always instant: ticker-
        │ (-speed 0/1/N — timing only;        │ sorted, no arrival times,
        │ order and content identical)        │ so nothing to pace)
        ▼                                     │
   ingest queue (~15s headroom at peak) ◄─────┘
        │ single consumer goroutine ── THE HOT PATH: per message,
        ▼                              aggregate-only, never calculate
   decode → update SYMBOL TABLE (RAM: per-ticker current NBBO + counters;
        │   never persisted — reconstructable from capture)
        ▼
   Observer seam → BUCKET STORE (RAM: 1-second × ticker aggregates,
        │          class-split per 0.3; the single storage resolution)
        │          [3.3 will add a print classifier here — the one metric
        │          that needs the at-that-moment NBBO before it's gone]
        │
        │ session close (cmd/live) / -buckets (cmd/replay)
        ▼
data/buckets/<date>.csv  (persisted session; derived — regenerable by replay)
        │
        │ nightly cmd/profiles: rediscover last 20 days, rebuild from scratch
        ▼
data/profiles/<SYM>.csv + _floors.csv  (20-day baselines + σ floors;
        │                               a cache, recomputed never updated)
        │
        │ READ TIME — metrics pull at human cadence, nothing pushed:
        │ minutes/windows summed from 1-second buckets on demand,
        │ basket baselines summed from member profiles on demand,
        │ every z through zguard.Z(value, center, σ, floor)
        ▼
dev view (3.0) ── one row per basket, one registered column per metric;
Phase 3 metrics land here the day each is written (FlowShareZ 3.1,
breadth 3.2, signed volume 3.3, …) → Phase 5 display → nightly ledger (6.5)
```

Reading the chain: the two top boxes are the vendor's records; the two
paths never convert into each other — both just feed the same pipeline,
and everything below the queue regenerates from whichever record fed it
(captures for recorded sessions, flat files for bootstrap days). The hot path
(queue → decode → observers) only aggregates; all metric math happens at
read time from the two RAM stores and the baseline files, which is what
keeps 120k msg/sec processing unblocked while renders happen once a second.

## Capture — `data/capture/<date>/stream.jsonl` (+ `manifest.json`)

The **durable record of a live session**: every websocket frame as received,
verbatim, with arrival timestamps. Written unconditionally by `cmd/live` —
there is deliberately no off switch, because recorded sessions are the test
corpus and the reference days (§6) can only be recorded when they occur.
Crash recovery for everything downstream is replay-and-regenerate from here,
not file repair. The manifest records session bounds, frame counts, and
resumes. This is the only artifact we write that cannot be recreated.

## Vendor flat files — `data/flat-files/{trades,quotes}/<date>.csv.gz`

The **vendor's authoritative after-the-fact record**: whole market,
tick-level, ticker-sorted, no arrival order. Downloaded on demand. Used for
capture-vs-vendor count reconciliation (1.2), condition/TRF studies (0.3),
and bootstrap replays for days we didn't capture. Not our data — a reference
we check ourselves against.

## Bucket store — `internal/bucket.Store` (RAM) → `data/buckets/<date>.csv`

The **single storage resolution of the instrument**: one aggregate per
(ticker, 1-second), all session — trades, shares, dollars, last price, quote
count, split by print class (0.3). The invariant: 1-second all day, coarser
views (minutes, rolling windows) are always *derived* at read time, never
stored — a mid-day resolution seam would distort CUSUM and slopes.

Two forms of the same thing:
- **In RAM** (`bucket.Store`): filled live during a session or replay by the
  pipeline observer; what the dev view and future metrics read while running.
- **On disk** (`data/buckets/<date>.csv`): the store serialized at session
  close — the input the profile builder discovers. Filename declares
  coverage: `<date>.csv` full session, `<date>.trades-only.csv` bootstrap
  (quotes honestly zero; baseline-eligible), `<date>.partial.csv` incomplete
  (never enters baselines).

Derived data: any bucket file regenerates from its capture (or flat file) by
replay. Note the name collision resolved: `internal/bucket` is the Go
*package* (the machinery — types, aggregation, CSV read/write);
`data/buckets/` is the *directory of artifacts* that machinery produces.

## Profiles — `data/profiles/<SYMBOL>.csv`

The **20-day baselines**: per ticker, per ET minute-of-day, median and
robust σ (MAD × 1.4826) of counted share and dollar volume over the last 20
bucket files. What "normal for this minute" means everywhere. Stored per
ticker, never per basket — basket baselines are summed from member profiles
at read time, so basket edits never corrupt history. A **cache, not a
store of record**: rebuilt from scratch by every nightly `cmd/profiles` run
(medians can't be incrementally updated, and don't need to be — the rebuild
takes seconds and self-heals if a bucket file is ever corrected).

## σ floors — `data/profiles/_floors.csv`

The **z-score guard's inputs** (2.2): per ET minute, per family (shares,
dollars), floor = frac × universe median of profile σ. Universe-level (one
file, not per ticker), written by the same build as the profiles so the two
always describe the same 20-day window. Every z in the system divides by
`max(σ, floor)` via `zguard.Z`. Consumers read floor values, never the
fraction (which is recorded in the header comment for audit).

## Symbol table — `internal/ingest.Table` (RAM only, never persisted)

The **per-symbol working state of a running pipeline**: symbol identity,
current NBBO, sequence/regression counters. Exists only for the duration of
a live session or replay; deliberately not persisted — book state is
reconstructable from the capture, and everything durable about a session
lives in the capture + bucket file. Pointers into this table (`SymbolState`)
are how the hot path avoids string lookups.

## Baskets config — `docs/foundations/morning-tape-baskets-v2.json`

The **trader-owned universe and basket map** (D1) — the one store that is an
*input*, not derived: 22 baskets, 164 members + benchmarks, 189 equity
symbols after exclusions. PM edits config, never code; the engine hot-reloads
it (planned); `baskets.md` regenerates from it. Engineering never edits this
file.

## Not stores (easy to confuse)

- `docs/foundations/massive-conditions.json` — committed *snapshot* of the
  vendor's condition-code table, refreshed on demand; reference, not data.
- The nightly ledger (6.5) and metric persistence (4.1) don't exist yet;
  when they do, they extend the bucket file (new columns) and the profile
  dir — no new resolutions, no new stores of record.
