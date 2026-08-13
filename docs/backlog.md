# Backlog

Items deliberately deferred with enough detail to pick up cold. Each entry says what,
why deferred, what to verify or build, and where it lands when resolved. Procurement
deferrals live in `docs/foundations/data.md` §Deferred; story-scoped backlog bullets
stay on their story in `DEV-PLAN.md` — this file is for items that need more detail
than a bullet.

## Trader's "Layers" spec reconciliation (deferred from story 0.5, needs trader)

**Logged:** 2026-08-12. **Was:** DEV-PLAN story 0.5 [A9]; removed so Phase 0 can close.

The trader refers to features by a "Layer N" numbering from a spec version never handed
over; our Build Spec v2 uses §-numbering. Leaked references: **Layer 6** = bond gate
(mapped — ZN/ZB yield-velocity overlay, already documented) and **Layer 8** =
leadership-divergence detector (**unmapped — described in no document we hold**; 3.1's
ex-leader flow series is its *suspected* input, inference only). Risk: layers we've
never heard named may exist in his mental model of the product.

**The ask when picked up:** get his layer list or five minutes walking through it;
reconcile against §-numbering; capture the Layer 8 definition **before** scoping or
building anything called leadership-divergence.

## Member-earnings-day baseline flag (LOW priority — trader input wanted)

**Logged:** 2026-08-12 (D4 discussion). **Lands in:** story 2.1 baselines, as a refinement.

A ticker's own earnings day distorts *its* 20-day profile far more than any macro day
distorts everyone's — the morning after earnings can print 5–20× normal volume in every
bucket, and under median/MAD one such day is absorbed, but two in one lookback start to
move the spread. v1 handles macro days only (D4: flag + include, macro-dates calendar).
The refinement: flag member-earnings days in per-ticker profiles (needs an earnings
calendar source — new data dependency, hence deferred). Whether flagged earnings days
should also be *excluded* per ticker changes what "normal" means for single names —
that call is the trader's; take it to him when this is picked up.

## Cancels/corrections — nightly reconciliation (LOW priority)

**Logged:** 2026-08-11 (0.3). **Decision:** v1 ignores canceled/corrected trades
entirely. **Lands in:** a nightly job alongside the baseline roll (Phase 2+).

**Why deferred:** the live websocket has no cancel/correction message type at all
(verified in the vendor Go client — corrections exist only on REST/flat files as the
per-trade `correction` indicator), so intraday handling is impossible regardless of
policy. Busts are rare (typically zero per ticker per day); the distortion from
ignoring them is negligible for a same-morning observation instrument.

**Solution when picked up:** nightly reconciliation — pull the day's trades from REST
(`/v3/trades`, `correction` field), join against captured prints by trade ID/sequence
number; for canceled trades subtract the print's contributions (dollars, size, count,
signed volume) from its original 1-second bucket in the correct ledger; for corrections
subtract old values and add amended ones (reclassify if conditions changed). Derived
views self-correct since they read from 1-second storage. Emit a cancel/correction log
(count, size, notional per day) — if the data ever shows bust rates that matter, that
is the evidence to revisit priority. Until built, stored sessions reflect the tape as
delivered live, uncorrected.

## Condition ID 55 (CTA `G`, "Opening Reopening Trade Detail") — verify before final classification

**Logged:** 2026-08-11 (0.3 table review, family 1). **Provisional class:** `CROSS_OPEN`.
**Lands in:** the per-condition table in `docs/foundations/print-inclusion.md`.

**What it is:** a legacy CTA-only condition marking the *constituent detail prints* of an
aggregated opening/reopening trade. Vendor `update_rules` say it updates high/low,
open/close, and volume (consolidated and market-center) — i.e. the SIP treats it as a
real, counted print.

**The risk:** if an exchange reports both the detail prints (`G`) *and* a summary
opening/reopening print (CTA `O` / `5`) for the same auction, counting both
double-counts the cross's dollar volume in the cross ledger. If instead `G` prints
*alone* (the details are the only representation of the auction), then it must be
counted or the cross is under-reported. Which behavior occurs on the modern tape is
unknown — the condition is legacy and may be near-zero frequency.

**What to verify (flat-file day(s), full tape, before the 0.3 table is finalized —
or during Phase 1 flat-file analysis at the latest):**

1. **Frequency:** does `G` appear at all across a full day, universe-wide? If zero
   across several days, keep provisional `CROSS_OPEN` and rely on item 4.
2. **Co-occurrence:** when `G` appears, is there a summary `O`/`5` print for the same
   ticker within the same auction window (same second or a few seconds)?
3. **Size reconciliation:** does the sum of the `G` detail sizes ≈ the summary print's
   size? That confirms the duplicate relationship.
4. **Runtime tripwire regardless of outcome:** the classifier counts `G` occurrences
   as a data-quality metric; any live occurrence surfaces in the nightly review so a
   first-ever appearance triggers a re-check rather than passing silently.

**Decision rule once verified:**
- Details + summary both print → `G` = `DUPLICATE` (summary print carries the volume).
- `G` prints alone → `G` = `CROSS_OPEN` / `CROSS_REOPEN` per the auction it belongs to.
