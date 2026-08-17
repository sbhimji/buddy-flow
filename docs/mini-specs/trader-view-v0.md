# Mini-spec — Trader view v0 (terminal, Monday POC)

Status: **open** (written 2026-08-16, Sunday evening). Not a numbered
dev-plan story: a provisional trader-facing surface pulled early from 5.x
because the trader liked the cumulative-share read; the real display work
(tiles, Glow, D10 palette) is unchanged and this view retires when 5.1
lands. Product owner decisions taken in chat 2026-08-16.

## What

`cmd/live -view` (and `cmd/replay -view -view-mode trader` for rehearsal):
the 3.0 terminal table with a trader column set, sorted by significance —
the screen IS the morning's story, top to bottom.

Columns: `cum_share`, `cum_share_typ`, `cum_share_z` (the 3.1-D7 family:
share of the tracked tape since open vs typical by this time of day —
**auction-inclusive** since the 2026-08-16 D7 amendment: opening/reopening
crosses count; the closing cross stays outside the `[open, close)` window),
`relative_vol` (completed minute vs matched-minute baseline, Counted slice
— per-minute columns exclude crosses; the two slices on one screen are
deliberate and the footer documents it), and `concentration` (top member's
% of basket $vol, completed minute). No per-minute share z (flickers), no
dev plumbing columns, no cum $vol (owner call — revisit; a
typical-cum-$vol baseline would need the D7 treatment).

## Decisions

- **T1 — Sort by story.** Rows ordered by `cum_share_z` descending; rows
  whose z is a gap sort last; ties break by basket name (determinism).
  Order recomputes every render — baskets migrate as the morning develops.
- **T2 — Significance highlight.** |z| ≥ 2.0 renders the z cell bold —
  green when positive, red when negative. Color is a statement of
  measurement (share above/below OWN typical), never buy/sell language;
  same signed-scale idea 3.8's Glow formalizes (warm/cold), reduced to
  terminal SGR. Threshold is a code constant (SignificantZ = 2.0) —
  trader-tunable at the nightly review like every default; NOT config yet.
  ANSI wraps the padded cell so alignment survives; Render stays a pure
  function of (store state, second) — colored bytes are still
  deterministic bytes; snapshot tests assert them.
- **T3 — Live wiring.** `cmd/live -view` (trader mode implied — that's the
  audience): the view wraps the store as the single observer, identical to
  replay; render loop on wall-clock ticker keyed to the live SIP clock —
  the same code path replay paces, which is the point of event-time
  buckets. Operational logs (status line, feed notices) divert to stderr
  when `-view` is on: `2>live.log` gives a clean table plus a complete
  log. End-of-session output unchanged (prints after the final render).
  Capture stays unconditional; the view is read-only alongside it.
- **T4 — Baselines roll tonight.** `cmd/profiles -days 20` — Friday's
  flat-file-derived `2026-08-14.trades-only.csv` enters the window,
  2026-07-17 drops off; Monday compares against the freshest 20 days.
- **T5 — Registry only.** Trader columns are composed in
  `internal/flowshare` and installed via new devview seams
  (`SetColumns`, `SetRank`, `Column.Style`); the dev view's defaults and
  behavior are untouched.

- **T6 — Plain-English footer (added 2026-08-16, product owner).** Trader
  mode renders a short fixed block below the table defining every column
  for a trader with no context (`flowshare.TraderFooter`, installed via a
  new `SetFooter` seam that travels with the composed column set — the dev
  view gets no footer). Statements of measurement only — no buy/sell or
  recommendation language anywhere (scope law); a test guards the obvious
  violations. The footer is a constant, so Render stays a pure function of
  (store state, second). The clock line keeps naming the completed and
  in-progress minutes but drops the per-column legend clauses — the footer
  explains the columns now.

## Done when

1. Trader-mode replay of the Friday capture renders the five columns for
   all 22 baskets, sorted by cum z, with the highlight engaging where
   |z| ≥ 2 — eyeballed at a `-view-at` minute and in a short paced run.
2. Snapshot test: trader-mode render (incl. ANSI bytes and row order)
   byte-identical across two renders; rank ordering and gap-last rules
   unit-tested.
3. `cmd/live -view` compiles and dry-runs (flag validation, profile
   loading, stderr log diversion) — full live validation is Monday itself.
4. Baselines rebuilt through 2026-08-14, two runs byte-identical.

## Monday runbook

```
# ~09:15 ET, from repo root (capture + live trader view until 20:00):
go run ./cmd/live -view 2>live-2026-08-17.log

# end of day: Ctrl-C once (clean close: manifest + bucket file), then roll:
go run ./cmd/profiles -days 20
```

Pre-open the table shows gaps (no completed session minute yet) — correct,
not broken. 09:30's minute excludes the opening cross from counted volume
(crosses are a separate ledger). First honest full-day cum_share is this
session's own.
