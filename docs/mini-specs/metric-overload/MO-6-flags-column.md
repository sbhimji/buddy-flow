# MO-6 — `flags` column: coincidence glyphs, no composite

Status: **implemented on branch mo-6-flags-column, awaiting review** (2026-08-28;
written 2026-08-26; close notes below). Owner decision **O3** (README).
Gated by MO-3 and MO-5 for the `δ` and `C` glyphs (a glyph whose metric is
absent stays unlit; the column can ship earlier with fewer glyphs). Read
first: `trader-view-v0.md` T2/T7, `ticker-view-v0.md` scope law ("no composite
score, no 'hot' scale"), INITIAL-PROJECT §2.5 ("simultaneity is the
institutional fingerprint").

## What

The leftmost trader column: a fixed-width string of glyphs, one slot per
named measurement, lit when that measurement is beyond its own threshold,
`·` otherwise. The eye scans for several lit together; the code never counts
them.

| slot | glyph | lit when | colour when lit | owner |
|---|---|---|---|---|
| 1 | `Z` | `|cum_share_z| ≥ SignificantZ` (2.0) | green +, red − | flowshare |
| 2 | `R` | `|5m_z| ≥ SignificantZ` | green +, red − | MO-8 (unlit until then) |
| 3 | `B` | breadth > `HighlightFrac` (0.70) ↑ or ↓ | green ↑, red ↓ | breadth |
| 4 | `$` | unusual-volume members ≥ half of the ↑ members (and ≥1) | green | breadth/volume |
| 5 | `δ` | `|delta| ≥ DeltaHighlight` (0.25) | green +, red − | MO-3 |
| 6 | `C` | `|conv_z| ≥ SignificantZ` | green +, red − | MO-5 |

Rendered e.g. `Z·B$δC` / `·····C` / `Z·····`. Width 6, left-aligned.

## Decisions

- **G1 — Each glyph reuses its column's predicate.** No new thresholds: every
  glyph lights exactly when its cell would be coloured (T2/T7/Δ5/L-colour).
  Each metric package exports `Flag(rc) (lit bool, positive bool, ok bool)`;
  `internal/flowshare` composes the column from those. If a cell and its
  glyph ever disagree, that is a bug by definition — a test asserts the
  agreement on a replayed minute.
- **G2 — Never summed, never sorted on, never a "score".** The column is
  presence, not magnitude. Rank stays `cum_share_z`. A future request for
  "sort by number of flags" is a composite and is declined by this spec;
  the reason is the scope law and the APEX decision-layer failure — a count
  of flags is a recommendation wearing a costume.
- **G3 — `$` semantics.** Of the ↑ members 3.2 counts, at least half are also
  on unusual volume per 3.2b (and at least one). "Nine up on volume is money"
  (§2.5) made binary; threshold ledger-tunable like the rest. Gaps in
  breadth → slot unlit (not `·`-vs-gap distinction; unlit is the only
  "no" the column has).
- **G4 — Footer.** One clause per glyph, statements of measurement; through
  the scanner. Also appended to the drill-down footer? No — flags are basket-
  level only; the drill-down keeps `since` as its recency marker.
- **G5 — Deterministic bytes.** ANSI wraps each lit glyph individually;
  unlit `·` unstyled; the padded string is fixed width so alignment survives.

## Done when (replayed data)

1. Unit: every slot lit/unlit/positive/negative at its threshold; `ok=false`
   → unlit; column width constant; determinism.
2. Agreement test: on a replayed 08-2x minute, for every basket row, glyph
   lit ⇔ corresponding cell styled (parsed from the frame bytes).
3. Snapshot at a minute with ≥2 glyphs lit on one row (find one on the
   busiest 08-2x open; record the minute in the close notes).
4. Footer scanner passes; rank order unchanged from MO-1's snapshot at the
   same minute.

## Decisions consumed

O3; T2/T7 highlight rules; VB1–VB4; MO-3 Δ5; MO-5 colouring; MO-8 `5m_z`
threshold; ticker-view-v0 scope law; §2.5; scope law.

## Close notes (2026-08-28, branch `mo-6-flags-column`)

- **Home.** New `internal/flags` (`flags.Set{Z,R,B,Dollar,Delta,Conv}`,
  `Set.Render`, `Set.ExtendTrader` — leftmost column, footer block first).
  The composer is NOT in `internal/flowshare` as the spec sketched:
  `delta`'s tests and `optequity` itself import `flowshare`, and the footer
  prints `delta.DeltaHighlight` / `breadth.HighlightFrac`, so a flowshare
  import of those packages would cycle. flowshare instead exports the
  shared signed predicate `SignedFlag(v, ok, threshold)`, its colour `SGR`,
  and `CumZFlag(rank)` (the `Z` predicate over the cum_share_z key
  `TraderColumns` returns as rank — captured in `cmd/live` / `cmd/replay`
  before premarket wraps it).
- **G1 predicates, one function per cell and glyph:** `flowshare.CumZFlag`
  (cum_share_z Style now calls it), `flowshare.Run.RunFlag` (MO-8, unchanged
  contract, now via `SignedFlag`), `breadth.Calc.Flag` (Style calls it),
  `breadth.VolCalc.DollarFlag` (new, G3), `delta.Flag` / `delta.BasketFlag`
  / `delta.Calc.Flag` (`Style` / `BasketStyle` call them),
  `optequity.Source.ConvFlag` (cell `style` calls the same `SignedFlag`).
- **`$` semantics as built:** lit ⇔ the merged cell prints `k$` with k ≥ 1
  and 2k ≥ up; ok=false (unlit) whenever the cell prints no count — breadth
  gap, `0/N`, `·$`. The glyph restates the visible cell, so the agreement
  test checks it against the parsed breadth text.
- **δ threshold discrepancy.** The spec table says `DeltaHighlight` 0.25;
  the landed constant (MO-3 trader review) is **0.20** on the 5-minute
  window. The glyph reuses the constant (0.20); the table above is stale.
- **Wiring.** `cmd/replay` and `cmd/live` compose the Set after every
  slot's package (options slot filled only when the tape is wired; nil slot
  stays `·`). Basket-level only; the drill-down is untouched.
- **Evidence (08-24 fixture, 04:00–10:00 cut).** 09:45:00 frame: byte-
  identical to the a3cc60b baseline outside the flags column and its two
  footer lines (rank order unchanged); agreement test 22 rows / 28 lit
  signed glyphs, zero disagreements. **≥2-glyph minute: 09:41:00** —
  `critical_minerals ZRB$·C` (five lit: cum +2.9σ, 5m +3.1σ, 1/7 red breadth
  with 1$, conv_z −2.6σ), `power_equipment ··B·δC`; the in-run scan of
  09:33–09:59 found 2+ lit on some row in every minute (red `B` on most
  rows — a de-grossing morning, SPY −0.16%..−0.34%).
- **Owner questions.** (1) `B` lit red on 17/22 rows at 09:45 makes the
  column dense on a broad-down day; that IS the fingerprint, but the eye
  may want `B` muted when it lights universe-wide (F18). (2) The `$`
  half-rule lit on 1/7 ↑ with 1$ — one stock — which reads thin; a floor
  of ≥2 on-volume members or "≥ half of N" is a ledger candidate. (3) Six
  glyphs at 09:35 readable? `R` and `δ` and `C` gap until 09:35/09:33/
  tape-minute, so early rows show ≤3 — probably fine, trader to confirm.
