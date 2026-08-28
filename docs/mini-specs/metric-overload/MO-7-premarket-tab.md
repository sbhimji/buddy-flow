# MO-7 — Premarket tab: its own frame, two-tab web server

Status: **open** (written 2026-08-26). Owner decision **O4** (README).
Gated by MO-1 (which removes `pre_vol`/`pre_conc` from the session table).
Read first: `premarket-view-v0.md`, `trader-view-live-cadence.md`,
`tools/live_view_server.py` (frame split, `?basket=` drill), backlog
"Premarket typ/z — baselines seeded from extended-hours aggs".

## What

**Go side — a second frame.** `cmd/live -view` renders, on the same tick and
from the same store, a premarket frame to `-pre-log` (default
`data/live-pre.log`, written the same way `data/live.log` is — a full-screen
redraw per frame): the premarket column set only —

    BASKET  pre_vol  pre_share  pre_conc            ranked by pre_share desc, gaps last, ties by name

with `premarket.PreFooter` (its full three-column form) and the clock line.
A second `devview` instance over the same `bucket.Store` and the same
observer fan-out (`cmd/live` already composes the observer; the two views
share the single store read, no second pipeline). The frame keeps rendering
after 09:30 with frozen values (premarket-view-v0: "kept on screen as context
all day") so the tab is never blank.

**Server side — two tabs.** `tools/live_view_server.py` gains `--pre-log`;
`/frame?tab=session|pre` returns the respective frame; the page renders two
tab buttons (`PREMARKET` / `SESSION`). On load, JS picks the tab by the ET
wall clock: before 09:30 → `pre`, otherwise `session`; the trader can switch
any time; the chosen tab persists in `sessionStorage` for the day. The
staleness line (`log written N.Ns ago`) is per tab. `?basket=` (drill) applies
to the session tab only; on the premarket tab it is ignored.

## Decisions

- **T1 — No new math, no new slice.** The premarket frame reads exactly the
  premarket-view-v0 calcs (`profile.ExtendedHours`, window `[04:00,
  min(now, 09:30))`). No z, no typ — the backlog's extended-hours-aggs
  baseline is the tab's future, noted in the footer as "raw dollars; no
  typical yet".
- **T2 — Clock-keyed default, never gap-keyed** (same posture as the rank
  fallback): the page decides by the ET clock in the browser, computed with
  `Intl.DateTimeFormat('en-US', {timeZone: 'America/New_York'})` — no
  server clock dependency, no DST hand-rolling.
- **T3 — One renderer, two column sets.** `devview.Render` is unchanged;
  the second instance gets `SetColumns/SetRank/SetFooter` with the premarket
  set. Both frames are pure in (store state, second); the render loop calls
  both on the same second so the two logs' clock lines agree.
- **T4 — Replay parity.** `cmd/replay -view -view-mode premarket` renders the
  premarket frame (for `-view-at 08:00:00` checks and snapshot tests) — the
  same code path the live second instance uses.
- **T5 — Launchd.** `tools/launchd` plist and `bin/live` add `-pre-log`; the
  server's LaunchAgent adds `--pre-log`. Read-only followers; killing the
  server never touches capture (unchanged posture).

## Done when (replayed data)

1. `cmd/replay -capture <a capture that starts pre-04:00 or the 08-2x
   premarket> -view -view-mode premarket -view-at 08:00:00`: three columns
   populate, ranked by `pre_share`; at `-view-at 10:00:00` the same values
   (frozen); two renders byte-identical; snapshot test.
2. Server unit tests (stdlib `unittest`): tab selection by clock at
   09:29:59 and 09:30:00 ET; `?tab=pre` and `?tab=session` return the
   respective logs; `?basket=` ignored on `pre`; staleness per tab.
3. Live dry-run: `cmd/live -view -pre-log …` writes both logs; the page
   shows both tabs; the frame server serves both.
4. Footer scanner passes on the premarket footer (already guarded).

## Decisions consumed

O4; premarket-view-v0 (slice, window, no-z, footer honesty, rank fallback);
trader-view-live-cadence; T3/T6 seams (`SetColumns/SetRank/SetFooter`);
scope law.
