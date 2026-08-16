# Buddy-Flow (MORNING TAPE)

Real-time money-flow observation instrument. Read `CLAUDE.md` first, then
`docs/DEV-PLAN.md` — the project is spec-driven and the docs are normative.
This file covers only what a developer can *run* today (through story 3.0).
For what each data artifact *is* and what regenerates from what, see
`docs/foundations/stores.md`.

Requirements: Go 1.21+. Live capture additionally needs `MASSIVE_API_KEY` in
`.env` at the repo root (gitignored). Replay needs no key and no market hours
— it is the development environment for everything.

## Data layout (all gitignored)

```
data/capture/<date>/stream.jsonl        captured live sessions (+ manifest.json)
data/flat-files/trades/<date>.csv.gz    vendor flat files (whole market, by ticker)
data/flat-files/quotes/<date>.csv.gz
data/buckets/<date>.csv                 persisted 1-second bucket stores (1.4)
                                        (.trades-only.csv = bootstrap; .partial.csv = never in baselines)
data/profiles/<SYMBOL>.csv              20-day per-ticker minute baselines (2.1)
data/profiles/_floors.csv               σ floors for every z (2.2)
```

## Replay a captured session

The main loop of development. Drives the same decoder and pipeline as the
live websocket; deterministic — same capture in, identical output out, at
any speed.

```
# instant (default): acceptance runs, batch checks
go run ./cmd/replay -capture data/capture/2026-08-13/stream.jsonl

# real-time: frames at recorded wall-clock spacing (a 6.5h session takes 6.5h;
# dead gaps are capped at 30s so a kill-hole doesn't stall you)
go run ./cmd/replay -capture data/capture/2026-08-13/stream.jsonl -speed 1

# accelerated ×20: a morning in ~20 minutes
go run ./cmd/replay -capture data/capture/2026-08-13/stream.jsonl -speed 20
```

Output: universe message counts, sequence-regression/malformed/decode
counters, top symbols, and final NBBO for spot-check symbols (`-nbbo
SPY,NVDA,AAPL` to choose). Exit codes: 0 ok, 1 pipeline lost messages,
2 flag misuse, 3 unreadable input.

Add `-buckets data/buckets/<date>.csv` to persist the 1-second bucket store
after the run (whole-session captures only; the filename must self-declare
partial coverage — the command enforces this from the data).

## Developer view (story 3.0)

The learning instrument: one row per basket, one column per metric,
auto-refreshing as the replayed clock advances. Capture replay only.

```
# watch a session at ×60 (an hour of tape per minute)
go run ./cmd/replay -capture data/capture/2026-08-14/stream.jsonl -view -speed 60

# instant, then render the table as of a chosen ET second (spot checks —
# buckets are event-time keyed, so a post-hoc render is exact)
go run ./cmd/replay -capture data/capture/2026-08-14/stream.jsonl -view -view-at 13:01:00
```

Baselines come from `-profiles` (default `data/profiles`) — build profiles
first. Columns to date: `N` member count; the last *completed* minute's
counted $vol, its 20-day matched-minute baseline (sum of member medians),
and their ratio; the in-progress minute accumulating (compared to nothing:
a partial minute vs a full-minute baseline would always read low); the 3.1
flow columns — `flow_share` (in-progress minute's share of the tracked
universe), `flow_share_z` (completed minute vs its 20d share baseline,
σ-guarded), `concentration` (top member's % of basket $vol), and the
since-open story `cum_share` / `cum_share_typ` / `cum_share_z` (share of
the tape so far today vs typical by this time). The header names the
minutes on screen. `·` is a defined gap (no baseline / outside the regular
session), never a zero.

## Build baseline profiles (stories 2.1–2.2)

Nightly roll and bootstrap are the same command: discover the most recent
20 bucket files, rebuild every profile from scratch, write the σ floors in
the same run (`data/profiles/_floors.csv`, fraction from `-sigma-floor-frac`,
default 0.25).

```
# build 189 profiles + floors from the last 20 days of bucket files
go run ./cmd/profiles -days 20 -through 2026-08-13

# RVOL sanity check of a session against existing profiles (reported, not gated)
go run ./cmd/profiles -rvol-check data/buckets/2026-08-14.trades-only.csv -rvol-minute 13:00
```

## Replay vendor flat files

Flat files are ticker-sorted with no arrival timestamps, so there is no
pacing — always instant. They are the vendor's authoritative record: used
for count reconciliation, time-windowed studies, and stress testing.

```
# full session, universe-filtered (the 1.1 acceptance run)
go run ./cmd/replay -trades data/flat-files/trades/2026-08-11.csv.gz -quotes data/flat-files/quotes/2026-08-11.csv.gz

# time window (ET), e.g. book state as of 09:35:00 via -to
go run ./cmd/replay -quotes data/flat-files/quotes/2026-08-11.csv.gz -date 2026-08-11 -to 09:35:00

# whole-market stress mode (~5× universe load; skips the universe filter)
go run ./cmd/replay -trades data/flat-files/trades/2026-08-11.csv.gz -full
```

Note: `-from` truncates quote history, so final-NBBO output is suppressed on
those runs (a book built mid-stream is not "as of" anything).

## Capture a live session

Capture is unconditional — there is deliberately no flag to disable it.
Start before the open; stops at 20:00 ET by default (full SIP session).

```
go run ./cmd/live                  # until 20:00 ET
go run ./cmd/live -until 16:30:00  # shorter (smoke tests)
```

Ctrl-C closes cleanly and writes the manifest; a second Ctrl-C hard-kills
(the capture file survives — restart appends to the same session, and the
manifest records the resume). Auth failure or an unwritable disk stops the
process loudly (exit 1) rather than running uncaptured.

## Tests

```
go test ./...          # includes a flat-file acceptance pass if data/ files exist (~70s)
go test -race ./internal/...
```

## Universe

189 equity symbols from `docs/foundations/morning-tape-baskets-v2.json`
(trader-owned — never edit; loader: `internal/universe`).
