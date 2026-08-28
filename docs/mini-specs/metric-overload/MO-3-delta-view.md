# MO-3 — `delta` and `class%` on basket rows and the drill-down (3.3, part 2)

Status: **open** (written 2026-08-26). Story: DEV-PLAN 3.3 [F2], view half.
Gated by MO-2 (signed columns in the store). Read first: MO-2, `3.1-flowshare.md`
(D5 render nulls, registry-only wiring), `ticker-view-v0.md` V5 (the reason
per-ticker delta was excluded from v0 — revisited here), DEV-PLAN Appendix B D9.

## What

The ask-side question, with its honesty number attached, at both levels:

    delta(B, m)  = (Σ AskSide.Dollars − Σ BidSide.Dollars) / Σ Counted.Dollars   over basket B, completed minute m   ∈ [−1, +1]
    class%(B, m) = (Σ AskSide.Dollars + Σ BidSide.Dollars) / Σ Counted.Dollars                                        ∈ [0, 1]

Per ticker: the same two, per symbol, in the drill-down. Unclassified dollars
sit in the denominator only (MO-2 / D15 precedent) — uncertainty damps δ
toward 0, never biases it. Renders `+0.31` / `62%`.

## Decisions

- **Δ1 — Raw, no z, in this story.** There is no 20-day baseline for δ until
  20 signed bucket days exist (MO-2 S6 backfill helps; the corpus decides).
  The z rides MO-9. A raw bounded ratio is honest to render as-is; the
  footer says so.
- **Δ2 — D9 open suppression.** Both cells gap for the first 30 s after
  09:30:00 (the completed-minute rule already makes 09:30's minute render at
  09:31; the window is applied to the *prints*, so a 09:30 minute's δ is
  computed over `[09:30:30, 09:31)` only — stated in the footer: "09:30
  minute excludes the first 30 s"). `D9Window = 30s`, code constant.
- **Δ3 — `class%` is not optional.** Wherever δ renders, `class%` renders
  beside it. A +0.30 on 40% classified and a +0.30 on 85% classified are
  different statements; the pair is the measurement. Per-ticker δ is allowed
  (V5 revisited) for exactly this reason plus Δ2.
- **Δ4 — Gaps.** `HasSigned=false` (old bucket file, trades-only day) → both
  gap; counted dollars 0 → both gap; classified dollars 0 with counted > 0 →
  δ gaps, `class%` renders `0%` (a true measurement).
- **Δ5 — Highlight.** δ cell bold green when ≥ `+DeltaHighlight`, bold red
  when ≤ `−DeltaHighlight`; `DeltaHighlight = 0.25` (default, ledger-tunable,
  code constant). Colour is a statement of which side of the book the
  prints hit — the footer wording is "dollars printed at/above the ask minus
  at/below the bid, over counted dollars"; never buy/sell. `class%` unstyled.
- **Δ6 — Window.** Trader cell = last completed minute (matches every other
  completed-minute column). A 5-minute trailing δ is *legal* — it is a ratio
  of sums over the window, not a sum of medians — and reads smoother; it
  registers on the **dev view** as `delta_5m` alongside `delta` so the
  ledger can judge which earns the trader seat. Default: 1-minute on trader
  until watched.
- **Δ7 — Home.** New `internal/delta` (basket and ticker calcs from the
  store, memoized per render via `RowCtx` like flowshare). Wiring via the
  column registry only; trader set gains `delta`, `class%` after
  `concentration_day` (README order); drill-down gains `delta`, `class%`
  after `of_basket`. Footer clause through the scanner test.

## Done when (replayed data)

1. Unit: hand-built store → known δ and class%; D9 boundary (print at
   09:30:29.999 excluded, 09:30:30.000 included); every Δ4 gap path;
   highlight engages at exactly ±0.25 and not at ±0.249; determinism.
2. Replay `-view-mode trader -view-at 10:00:00` on a regenerated 08-2x day:
   δ/class% populate for all 22 baskets and in a `-basket SEMIS` drill-down;
   two renders byte-identical.
3. One basket-minute δ and class% recomputed in Python from the regenerated
   bucket CSV match the rendered digits.
4. Footer scanner passes; the `aggressor:` stats line from MO-2 for the
   replayed day is quoted in the close notes beside the first δ screen.

## Decisions consumed

MO-2 S1–S7; D9 (30 s); D15 precedent; 3.1 D5 nulls; T2 colour posture;
ticker-view-v0 V5 (revisited, reason recorded); F2; scope law.
