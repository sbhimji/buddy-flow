# Buddy-Flow (MORNING TAPE)

Real-time money-flow observation instrument. Read `CLAUDE.md` first, then
`docs/DEV-PLAN.md` — the project is spec-driven and the docs are normative.
This file covers only what a developer can *run* today (through story 1.3).

Requirements: Go 1.21+. Live capture additionally needs `MASSIVE_API_KEY` in
`.env` at the repo root (gitignored). Replay needs no key and no market hours
— it is the development environment for everything.

## Data layout (all gitignored)

```
data/capture/<date>/stream.jsonl   captured live sessions (+ manifest.json)
data/trades/<date>.csv.gz          vendor flat files (whole market, by ticker)
data/quotes/<date>.csv.gz
```

## Replay a captured session

The main loop of development. Drives the same decoder and pipeline as the
live websocket; deterministic — same capture in, identical output out, at
any speed.

```
# instant (default): acceptance runs, batch checks
go run ./cmd/ingest -capture data/capture/2026-08-13/stream.jsonl

# real-time: frames at recorded wall-clock spacing (a 6.5h session takes 6.5h;
# dead gaps are capped at 30s so a kill-hole doesn't stall you)
go run ./cmd/ingest -capture data/capture/2026-08-13/stream.jsonl -speed 1

# accelerated ×20: a morning in ~20 minutes
go run ./cmd/ingest -capture data/capture/2026-08-13/stream.jsonl -speed 20
```

Output: universe message counts, sequence-regression/malformed/decode
counters, top symbols, and final NBBO for spot-check symbols (`-nbbo
SPY,NVDA,AAPL` to choose). Exit codes: 0 ok, 1 pipeline lost messages,
2 flag misuse, 3 unreadable input.

## Replay vendor flat files

Flat files are ticker-sorted with no arrival timestamps, so there is no
pacing — always instant. They are the vendor's authoritative record: used
for count reconciliation, time-windowed studies, and stress testing.

```
# full session, universe-filtered (the 1.1 acceptance run)
go run ./cmd/ingest -trades data/trades/2026-08-11.csv.gz -quotes data/quotes/2026-08-11.csv.gz

# time window (ET), e.g. book state as of 09:35:00 via -to
go run ./cmd/ingest -quotes data/quotes/2026-08-11.csv.gz -date 2026-08-11 -to 09:35:00

# whole-market stress mode (~5× universe load; skips the universe filter)
go run ./cmd/ingest -trades data/trades/2026-08-11.csv.gz -full
```

Note: `-from` truncates quote history, so final-NBBO output is suppressed on
those runs (a book built mid-stream is not "as of" anything).

## Capture a live session

Capture is unconditional — there is deliberately no flag to disable it.
Start before the open; stops at 20:00 ET by default (full SIP session).

```
go run ./cmd/capture                  # until 20:00 ET
go run ./cmd/capture -until 16:30:00  # shorter (smoke tests)
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
