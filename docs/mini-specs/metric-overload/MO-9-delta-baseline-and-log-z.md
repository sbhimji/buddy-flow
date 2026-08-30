# MO-9 — `delta_z` baseline family + log-space z evaluation

Status: **open** (written 2026-08-26). Two calibration halves that share a
`cmd/profiles` run. Gated by MO-2 and **≥10 signed bucket days in the
20-day window** (the first half cannot exist before then; the second half
can start any time after MO-8). Read first: `2.1-profile-builder.md`,
`2.2-sigma-guard.md` (D5 floor rule, null semantics), `3.1-flowshare.md`
D2/D4 (share-family builder pattern), backlog "Baseline estimator
reevaluation — median/MAD choice, σ-floor fraction, log-scale z".

## Half A — `delta_z`

    delta_z(B, m) = zguard.Z(delta(B, m), MedianDelta(B, m), SigmaDelta(B, m), sigma_floor_delta(m))

- **Builder:** per basket per minute, each signed day's completed-minute δ
  (MO-3 formula, D9 window applied to the baseline days too — both sides of
  the comparison must move together), then median and MAD × 1.4826 over the
  days — the 3.1 D2 basket-file pattern (derived, stamped with membership,
  self-invalidating; per-ticker bucket files remain the only stored truth).
  A day without signed columns (`HasSigned=false`) contributes **no sample**
  (a defined gap, never a fake 0 — D3 posture); the row records its sample
  count; `<MinProfiledDays` → z gap.
- **Floor:** `sigma_floor_delta(m) = frac × median over baskets of σ_delta(B, m)`,
  frac 0.25 (D5), written as a new column of `_floors.csv`; readers map by
  name.
- **Render:** dev view first (`delta_z`), coloured at ±`SignificantZ`. Trader
  seat only by ledger evidence — the raw δ + `class%` pair stays the trader's
  cell until the nightly review says the z reads better.
- **Ticker-level `delta_z`:** same family on the per-ticker profile CSV
  (extra columns, `HasDelta` flag) — same one-file-per-ticker rule; renders
  in the drill-down when present.

## Half B — log-space z evaluation (a decision record, not a switch)

Dollar-volume z's are right-skewed: a basket can print 10× typical but not
1/10 (floor at zero), so deviations above the median are systematically
larger than below and z reads hot more easily than cold. The backlog's
remedy is z on `log(volume)`. This half **measures** it on the recorded
corpus, in the dev view, side by side:

- Series: `cum_$_z` (ticker), `$_z` (ticker), `cum_share_z` (basket — bounded
  ratio, expected to need it least), `5m_z` (MO-8). For each: current
  median/MAD z vs log-space median/MAD z (log of the value and of the
  baseline samples; floors recomputed in log space per D5).
- Counts to report, per series, over every replayed day in the window:
  how often |z| ≥ 2 fires hot vs cold under each estimator; how often the
  σ floor engages and on which names; the ticker/basket rank correlation
  between the two at 09:45 and 10:30.
- Rendered as dev-view twin columns (`cum_$_z` / `cum_$_lz` …) so the
  asymmetry is visible on real mornings, not only in a table.
- **Outcome:** a written decision in the close notes with the numbers. A
  switch happens only if the evidence is one-sided, and then as its own
  story re-running 2.1/2.2 acceptance on the corpus (stored profile
  semantics change). Default if not one-sided: keep median/MAD linear,
  record the asymmetry in the footer of the affected columns.

## Decisions

- **B1 — No estimator change in this story.** Both halves add columns and
  evidence; neither alters an existing number on the trader frame.
- **B2 — Threshold tuning waits for this.** `SignificantZ` on dollar-family
  z's (ticker `cum_$_z`, strip threshold) is not retuned until Half B's
  asymmetry count exists — otherwise a threshold is tuned against a known
  bias.
- **B3 — One nightly run.** Both families build in the existing
  `cmd/profiles` invocation; `bin/nightly` unchanged apart from the new
  outputs; byte-deterministic like every profile output.

## Done when (replayed data)

1. Half A unit: hand-built signed days → known median/MAD/z; unsigned days
   skipped and counted; `<10` days → gap; floor column present; stamp
   refusal on membership edit.
2. Half A replay: `delta_z` populates on the dev view for the first day with
   ≥10 signed days in its window; one basket-minute recomputed in Python.
3. Half B: the comparison table and twin columns exist; the close notes
   record the counts and the decision; two profile builds byte-identical.
4. No trader-frame byte changes from this story (snapshot unchanged).

## Decisions consumed

D5/D6 (floor rule, median/MAD); 2.1 ≥10-day rule; 3.1 D2/D2a/D3/D4 basket
family pattern; MO-2/MO-3; MO-8; backlog estimator reevaluation; scope law.
