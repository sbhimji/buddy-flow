# Backlog

Items deliberately deferred with enough detail to pick up cold. Each entry says what,
why deferred, what to verify or build, and where it lands when resolved. Procurement
deferrals live in `docs/foundations/data.md` §Deferred; story-scoped backlog bullets
stay on their story in `DEV-PLAN.md` — this file is for items that need more detail
than a bullet.

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
