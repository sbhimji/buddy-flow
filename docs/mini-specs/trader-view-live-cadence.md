# Trader view live cadence — per-second since-open columns

**Story:** trader-view follow-up (owner-directed 2026-08-17 evening, after the first
live trader session). **Touches:** `internal/flowshare` only (cells plumbing); no
renderer, store, or ingest changes. Supersedes trader-view-v0's "since open through
completed minute" window for `cum_share`/`concentration_day` display only.

## What

The view already re-renders every second; the columns waited for minute boundaries.
Per-column cadence, decided by the owner (chat, 2026-08-17):

| column            | cadence            | window                                        |
|-------------------|--------------------|-----------------------------------------------|
| cum_share         | **every second**   | [open, now] incl. auctions — live numerator AND denominator |
| concentration_day | **every second**   | same window/slice as cum_share (reconciliation invariant kept) |
| cum_share_typ     | steps each minute  | baseline through last completed minute (its native resolution) |
| cum_share_z       | steps each minute  | **frozen intra-minute**: completed-minute share vs matched baseline |
| relative_vol      | unchanged          | last completed minute |
| breadth           | unchanged (owner)  | 3.2 three-state, minute-anchored persistence |
| concentration     | unchanged (owner)  | last completed minute |

**Why z is frozen:** an intra-minute z would compare a partially-filled live share
against full-minute baselines — drift that means nothing. The z is computed from the
completed-minute cum share (the same operand as before), so within a minute it holds
still while `cum_share` moves. The two may visibly diverge intra-minute; that is
honest, not a bug: one is a live measurement, the other a baselined comparison.

## Mechanics

The live window is the existing completed-minute cum snapshot `[open, cm)` plus a
small **delta snapshot** `[cm, atSec+1)` in the same auction-inclusive slice — the
expensive since-open pass is unchanged and the delta costs ≤60 buckets per symbol per
render. Both snapshots memoize per render as before. Clamps: live end never passes
the close (closing cross stays excluded, unchanged deferral); before the first
completed minute the cum part is empty and the delta carries alone — so `cum_share`
appears from the first counted print after 09:30:00 (auction included), a minute
earlier than before, while typ/z still first appear at 09:31.

## Acceptance (on replayed data)

1. Unit: at a mid-minute second, `cum_share`/`concentration_day` include in-progress
   minute prints; `cum_share_z` and `cum_share_typ` render identically at every
   second of that minute and equal the prior (minute-anchored) values.
2. Unit: during the first session minute, `cum_share` renders (auction + early
   prints) while typ/z gap.
3. Replay of a recorded session: `-view-at` at HH:MM:00 vs HH:MM:SS shows cum_share
   moving intra-minute with z unchanged; replaying twice is byte-identical.
4. Footer/legends state the cadence split; no buy/sell language (existing guard).
