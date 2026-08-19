# Premarket view v0 — where premarket dollars concentrate (raw, no z)

**Story:** owner-directed 2026-08-17 night ("trader wakes up tomorrow and sees how
money is moving in the premarket"). Pulls forward the smallest honest slice of the
backlog's extended-hours item; everything that item defers (post-market, separate
ledgers, premarket baselines, ingest-window widening) stays deferred. **Touches:**
new `internal/premarket`, one new slice func in `internal/profile`, trader-view
wiring in the two commands. The 0.3 classification table is untouched.

## What

Three trader-view columns, appended after `concentration_day`, computed from the
1-second store over the window **[04:00, min(now, 09:30)) ET** — live during
premarket, frozen at the open, kept on screen as context all day:

- `pre_vol` — basket premarket dollars (e.g. `$12.3M`). `$0` renders as `$0`
  (a true measurement); gap only before 04:00 / no window.
- `pre_share` — basket share of universe premarket dollars; gap on a 0/0.
- `pre_conc` — top member and its fraction of the basket's premarket dollars.

**Pre-open ranking:** before the first completed session minute rows sort by
`pre_share` (the premarket money story); from 09:31 the existing cum-z rank takes
over. The two scales never mix — the fallback keys on the clock, not on gaps.

## Decisions recorded

- **Slice = the NON_FLOW class through a premarket WINDOW** (`profile.ExtendedHours`).
  Within 04:00–09:30 that is overwhelmingly Form T / Extended Hours Sold OOS
  (conditions 12/13); the other NON_FLOW types are rare admin/settlement records,
  mostly zero-volume. This is a session-scoped read of the existing classes — the
  durable dedicated extended-hours class (backlog) can replace the lens later
  without touching this view's meaning. Never use this slice inside the regular
  session, where NON_FLOW is settlement/admin noise.
- **No z, no "typical", by design** — no premarket baselines exist and 20-day
  matched buckets are likely too thin there (backlog). Raw dollars + share +
  concentration only; the footer says so. **Follow-up accepted 2026-08-17:**
  pre_typ/pre_z seeded from extended-hours aggs, displaying from 07:00 ET only —
  see backlog "Premarket typ/z — baselines seeded from extended-hours aggs".
- **No leakage:** regular-session metrics never read this slice; this view never
  reads counted classes. Separate lenses on the same store, no seams.
- **Honesty carried in the footer:** premarket volume is thin, lumpy, and heavily
  off-exchange (F3 compounds); magnitudes, not σ. Correlational only.

## Acceptance (on replayed data)

1. Unit: a condition-12 print at 08:00 counts in all three columns; a regular
   09:31 print does not; the columns freeze after 09:30 (same values at 10:00 as
   at the open); before 04:00 all three gap; rank falls back pre-open and
   delegates post-open.
2. Replay of a recorded premarket session (`-view-at 08:00:00`): columns populate
   with real premarket flow while every regular column gaps; replay twice →
   identical bytes.
3. Footer defines all three columns; no buy/sell language (existing test guard).
