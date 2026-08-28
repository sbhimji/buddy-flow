# MO-1 — Trader column set v1 (prune, merge, rename)

Status: **open** (written 2026-08-26). Folder: `metric-overload/README.md`
(owner decision O2: first story). Gated by: `indiv-tickers` merged (it touches
`internal/devview`, `internal/profile`, both commands, and adds
`internal/tickerview` — this story edits on top of it, never beside it).
Read first: `trader-view-v0.md` (T1/T2/T5/T6/T7/T8), `3.2b-volume-breadth.md`
(VB4 gap rules), `premarket-view-v0.md`, `ticker-view-v0.md`.

## What

Clear the real estate the later stories need and remove the two naming traps
the inventory found, without changing any number that stays on screen.

1. **Drop from the trader set** (dev view keeps them, name-ordered as today):
   `relative_vol` (single completed minute — flickers; the de-flickered trader
   form is MO-8's `5m_z`) and per-minute `concentration` (the day version is
   the one that reconciles with `cum_share`).
2. **Delete the duplicate.** `flowshare.TraderColumns` builds its own
   `relative_vol`; `devview.defaultColumns` has the memoized one. After (1)
   only the devview one remains. Delete `flowshare.Share` (exported, zero
   non-test callers; superseded by `winSnap`).
3. **Merge `breadth` + `up_on_vol` into one cell:** `7/9 5$` = up/N per 3.2,
   then the count of those ↑ members also on unusual volume per 3.2b. Width
   fits `12/12 12$`. Highlight rule unchanged (T7: bold green >70% ↑, bold red
   >70% ↓ — applied to the whole cell). Gap rules preserved exactly: when 3.2
   breadth gaps the cell gaps; when only the volume side is unmeasurable the
   cell renders `7/9 ·$` (VB4: "cannot measure" is never "measured zero").
4. **Premarket:** `pre_vol` and `pre_conc` leave the session table (they
   return on MO-7's tab). `pre_share` stays as the single post-open context
   column and remains the pre-open rank key (premarket-view-v0, unchanged).
5. **Rename ticker `rvol` → `rvol_sh`** in the drill-down. It is completed-
   minute **shares** over the matched-minute median shares; the basket-level
   `relative_vol` was dollars. The footer already disclosed the difference;
   the name must too. `$_z` unchanged.
6. **`CLAUDE.md` status line:** the repo is no longer pre-code. Replace the
   "Status: pre-code" paragraph with a two-line pointer: Phases 1–3 partial +
   Phase 7 landed; `docs/mini-specs/` is the story ledger; this folder is the
   current curation pass. Nothing else in `CLAUDE.md` changes.

Resulting trader row after this story (order per README, absent slots simply
absent): `cum_share · cum_share_typ · cum_share_z · breadth · concentration_day · pre_share`.

## Decisions

- **P1 — Nothing moves to config.** Every threshold stays a code constant
  (T2 posture); this story adds none.
- **P2 — Footer.** `flowshare.TraderFooter` loses the `relative_vol` and
  `concentration` clauses, gains the merged-cell clause ("`7/9 5$`: of 9
  members, 7 have outperformed SPY by more than 10 bps for 3 consecutive
  minutes; 5 of those 7 also printed more than 1.5× their matched-minute
  median dollars in each of those minutes"). `PreFooter` shrinks to
  `pre_share`. The scanner test still guards both.
- **P3 — Dev view untouched** except the registry losing nothing: dev mode
  still registers every column, including the ones the trader set dropped.
- **P4 — Snapshot discipline.** Trader snapshot fixtures are regenerated once
  in this PR and reviewed by eye against the pre-change frame at the same
  `-view-at` minute: every surviving cell byte-identical, only columns removed
  or merged.

## Done when (replayed data)

1. `go run ./cmd/replay -capture <08-2x> -view -view-mode trader -view-at 09:45:00`
   renders the six-column set; surviving cells match the previous frame's
   bytes at that minute (diff of the two frames shows only removed/merged
   columns).
2. Unit: merged cell renders `x/y z$`, `x/y ·$`, and gaps in the VB4 cases;
   `rvol_sh` header present and `rvol` absent in the drill-down snapshot.
3. `go vet ./... && go test ./...` green; `flowshare.Share` gone; one
   `relative_vol` implementation remains.
4. Footer scanner passes; `CLAUDE.md` status paragraph replaced.

## Decisions consumed

T1/T2/T5/T6/T7/T8 (trader-view-v0); VB1–VB4 (3.2b); premarket-view-v0
rank fallback; ticker-view-v0 V1–V5; scope law.
