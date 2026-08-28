# MO-5 — Options columns on the equity screen, live (basket + ticker)

Status: **implemented on branch mo-5-options-live, awaiting review** (written 2026-08-26; implemented 2026-08-27). Owner decision **O1** (README): options
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

## Close notes (2026-08-27, branch `mo-5-options-live`)

- **Home:** new `internal/optequity` — one `Source` (basket `Baselines` +
  `conviction.MinuteBuckets` reader + the per-ticker `tickerview.OptionsSource`)
  built by `LoadReplay` (bucket file, cmd/replay) or `FromFollower`
  (`optfollow.Follower`, cmd/live). `ExtendTrader` anchors on delta's
  `class%` column and flowshare's gap-glyph footer line — a miss panics at
  composition (MO-3 posture). The basket cell is `conviction.BasketMinute`
  → `Baselines.Z` for the last completed minute (clamped at the close like
  the drill-down), memoized per basket per render second.
- **Refusal posture (L2), both commands:** any options load failure —
  stamp mismatch names the file — prints `options columns REFUSED (equity
  table … without conv_z/net_z): <err>` to stderr and the table composes
  without the pair; cmd/replay no longer exits 1 on it (the shared posture
  is what lets the stamp test run on replay).
- **cmd/live lag note:** the follower's clock is RecvNs-based (MO-4) — a
  basket cell here can lag the :8788 table by one poll (250 ms tail poll);
  the equity clock is the SIP timestamp. Both describe the same completed
  minute; only the moment the gap fills differs.
- **Launchd:** `com.buddyflow.live.plist` passes
  `-options-capture data/capture-options/$(date +%F)/stream.jsonl
  -options-profiles data/profiles-options` (date resolved by the shell in
  ET at launch). `bin/live` is a compiled binary (gitignored), not a shell
  wrapper — nothing to edit there; rebuild it.
- Done-when 2 (live dry-run) was not run under the no-network rule:
  cmd/live opens the websocket before anything else; the follower path is
  exercised by the unit tests (`TestFollowerBackedSourceGapsUntilTapeAdvances`)
  and the flag validation ran without network. First live morning is the check.
- Done-when 1's 10:29 digits are reproducible only against the frozen
  profile rows in `internal/optfollow/testdata` (profiles have rolled);
  the equity column is asserted equal to `BasketMinute`→`Z` in a unit test
  and against a Python recomputation at 09:44 on the fixture.

## Decisions consumed

O1; 7.7 V1–V5; MO-4 F2–F4; ticker-view-v0 options columns; T2 colour
posture and `SignificantZ`; 6.6 / D13 (unchanged for driver use); scope law.
