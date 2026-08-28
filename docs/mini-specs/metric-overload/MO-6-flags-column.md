# MO-6 — `flags` column: coincidence glyphs, no composite

Status: **open** (written 2026-08-26). Owner decision **O3** (README).
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
