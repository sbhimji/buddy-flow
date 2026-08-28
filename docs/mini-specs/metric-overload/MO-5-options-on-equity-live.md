# MO-5 — Options columns on the equity screen, live (basket + ticker)

Status: **open** (written 2026-08-26). Owner decision **O1** (README): options
z columns on the live equity trader table and in the per-ticker drill-down
and strip, as measurement columns. Gated by MO-4 and `indiv-tickers` merged.
Read first: MO-4, `7.7-conviction-view.md`, `ticker-view-v0.md` (`conv_z`/`net_z`
columns, `OptionsSource`), DEV-PLAN 6.6 and Appendix B D13.

## What

`cmd/live -view` gains `-options-capture <stream.jsonl>`, `-options-profiles
<dir>`, `-options-weights <json>` (same flags `cmd/replay` already has; the
validation and `loadOptions` helper move to a shared place both commands
call). With them:

- **Basket columns** `conv_z`, `net_z` after `class%` (README order): last
  completed minute's basket sum (`conviction.BasketMinute`, MO-4 F4) through
  `conviction.Baselines.Z` — the identical numbers :8788 shows. `·` on any
  null (7.7 V1). Bold green ≥ `+SignificantZ`, bold red ≤ `−SignificantZ`
  (2.0 — the same constant the share z uses; the two panels can never
  disagree about "significant"). Footer = `conviction.Footer` verbatim.
- **Ticker columns and strip:** the `nil` at `cmd/live/main.go` (ticker view
  construction) becomes a real `tickerview.OptionsSource` built from
  `Follower.Minute` — the drill-down's `conv_z`/`net_z` and the strip's
  `conv_z` render live exactly as they do in replay today.
- Without `-options-capture`: columns absent, drill-down as today, table
  otherwise byte-identical (7.7 V5 posture). The `bin/live` wrapper and the
  launchd plist pass the day's options capture path; :8788 keeps running
  unchanged.

## Decisions

- **L1 — The 6.6 gate amendment.** DEV-PLAN Appendix C gains:
  "Amendment 2026-08-26 (MO-5) — 6.6's 'after the trader confirms daily use
  of v1' gate is overridden by the product owner for **measurement columns
  only** (`conv_z`, `net_z` on the trader table, drill-down and strip). Any
  brightness/driver use of options premium — Glow weight, tile arcs, D13
  ignition — remains gated on 6.6." The scope law's "secondary overlay,
  never driver" is untouched: a column is a measurement, not an input to
  anything.
- **L2 — Stamps enforced at load** (7.7 V3): a profiles/weights stamp
  mismatch refuses the options columns loudly at startup — never a blended
  number. The equity table still starts; the failure names the file.
- **L3 — Start-up before the tape.** The follower waits for the capture
  file (MO-4 F1); until the first print the columns gap — pre-open renders
  hit that path every morning, correct not broken.
- **L4 — Read-only, separate lifecycle.** The follower stops with the
  view; the options capture process is never touched (7.7 runbook posture).
- **L5 — `flags` glyph `C`** (MO-6) keys on this column; nothing else
  consumes it. No sort-key change: rank stays `cum_share_z`.

## Done when (replayed data)

1. `cmd/replay -view -view-mode trader -options-buckets … -options-profiles
   … -view-at 10:29:00` on 08-24 renders basket `conv_z`/`net_z` equal to the
   7.7-verified digits (semis −0.0/−0.1, megacap +0.4/+0.4, photonics
   +2.7/+2.2); `-basket <one>` drill-down shows ticker columns; two renders
   byte-identical.
2. `cmd/live -view -options-capture <finished 08-2x stream.jsonl> …`
   dry-run against a finished capture: columns populate; without the flag
   the frame is byte-identical to MO-1's.
3. Stamp-mismatch test: edited weights file → startup refuses the options
   columns with the file named; equity table still renders.
4. Appendix C amendment committed in the same PR; footer scanner passes.

## Decisions consumed

O1; 7.7 V1–V5; MO-4 F2–F4; ticker-view-v0 options columns; T2 colour
posture and `SignificantZ`; 6.6 / D13 (unchanged for driver use); scope law.
