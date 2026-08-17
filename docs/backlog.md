# Backlog

Items deliberately deferred with enough detail to pick up cold. Each entry says what,
why deferred, what to verify or build, and where it lands when resolved. Procurement
deferrals live in `docs/foundations/data.md` §Deferred; story-scoped backlog bullets
stay on their story in `DEV-PLAN.md` — this file is for items that need more detail
than a bullet.

## ⚑ TOP ITEM — Macro + earnings calendar (baseline correctness; pick up first)

**Logged:** 2026-08-12 (earnings half, D4 discussion); macro-calendar half moved here
from story 2.1 on 2026-08-15 to clear the path to a Monday trader POC. **Lands in:**
2.1 baselines as a refinement + the nightly ledger.

**Macro calendar (was 2.1 scope).** The static calendar file: trading days, half days,
FOMC/CPI-class macro-day flags (2026-08-11 was CPI). Why deferral is numerically safe
*right now*: D4's accepted policy is flag-and-**include** — the flag changes no baseline
number in v1, it only annotates the ledger; and half-day *exclusion* [F11] only bites
when a half-day enters the 20-day lookback (next US half-day is Thanksgiving week —
this MUST land before late November). Until then 2.1 infers trading days from the aggs
data itself (days that return bars) and treats every day as a full day.

**Member-earnings-day flag (original entry).** A ticker's own earnings day distorts
*its* 20-day profile far more than any macro day distorts everyone's — the morning
after earnings can print 5–20× normal volume in every bucket; under median/MAD one such
day is absorbed, but two in one lookback start to move the spread. Needs an earnings
calendar source (new data dependency). Whether flagged earnings days should also be
*excluded* per ticker changes what "normal" means for single names — that call is the
trader's; take it to him when this is picked up.

**When picked up:** one calendar module feeding both flags (macro = universe-wide,
earnings = per-ticker), consumed by the profile builder and stamped into the nightly
ledger; then remove the trading-days-inferred-from-data shortcut in 2.1.

## Baseline estimator reevaluation — median/MAD choice, σ-floor fraction, log-scale z (calibration refinement)

**Logged:** 2026-08-15 (2.2 close discussion). **Lands in:** 2.1/2.2 as a calibration
refinement, evaluated in the 3.0 developer view on replayed sessions; the σ-floor
fraction half tunes at the nightly ledger review (6.5). Not a v1 blocker — everything
here is observable in replay and cheap to flip.

**Decision being kept for now:** median/MAD baselines (D6) with the 2.2 σ guard
(`σ_used = max(σ, floor)`, floor = `-sigma-floor-frac` × universe median σ per minute
per family, default 0.25). The 0.25 is a starting guess at how much extra skepticism
thin names deserve, not a data-derived number — reevaluate once ledger evidence shows
whether floored names produce useful or noisy flags. The rejected alternative
(fall back to mean/stddev when MAD = 0) stays rejected: it switches to the
outlier-dominated estimator exactly on the samples that trip it, still divides by zero
on all-zero samples, and breaks cross-ticker z comparability.

**Three recorded caveats on median itself, to test rather than re-argue:**

1. **Median is "wasteful" on clean data.** When there are no outliers, the median of
   20 samples is a noisier estimate of the center than the mean of the same 20 —
   we're paying a bit of statistical efficiency as an insurance premium. With
   fat-tailed market data the insurance is worth far more than the premium, but it's
   a real trade.

2. **It doesn't fix trends — nothing in a 20-day lookback does.** If a basket's
   volume has been steadily climbing for a month (a theme catching fire), both
   median and mean lag reality and today reads "abnormally high" against a stale
   baseline. That's arguably a feature (a sustained trend *is* unusual flow) — just
   don't credit median with solving it.

3. **Volume data is lopsided, and median makes that visible.** Dollar volume can
   spike 10× above typical but can't go 10× below (floor at zero) — the distribution
   is skewed right. Deviations above the median are systematically larger than
   deviations below it, so z-scores read hot more easily than cold. The standard
   remedy, worth testing in the 3.0 developer view rather than deciding now: compute
   the z on the **logarithm** of volume-like quantities, making "10× the usual" and
   "1/10th the usual" symmetric distances. FlowShare (a bounded ratio) needs this
   less; raw volume and RVOL-adjacent series benefit most.

**When picked up:** side-by-side in the 3.0 dev view on replayed sessions — median/MAD
vs mean/stddev vs log-space z on the same minutes; count how often the σ floor engages
and on which names; check hot/cold z asymmetry empirically. Estimator changes touch
stored profile semantics (2.1) and the floor file (2.2), so any switch re-runs the
2.1/2.2 acceptance criteria on the recorded corpus.

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

## Closing-cross inclusion in cumulative share (deferred by design, 2026-08-16)

**Logged:** 2026-08-16 (D7 auction-inclusive amendment, product owner discussion).
**Lands in:** `internal/profile.BuildShares` + `internal/flowshare` cum window, if ever.

**What:** the 3.1-D7 cumulative share family became auction-inclusive
(`profile.CountedWithAuctions` — opening/reopening crosses count in the since-open
story). The **closing cross does not**: the cum window is `[open, close)` and closing
prints carry 16:00:00-and-later SIP timestamps (measured on 2026-08-14:
CROSS_CLOSE prints span 16:00:00–16:05:05 ET, ~$26.3B vs ~$3.1B of opening
crosses), so the clamp structurally excludes them on both the baseline and live
sides — consistently, which is what matters for the z.

**Decision recorded:** defer. The instrument's user moment is the morning; a
whole-day-inclusive final share is a nice-to-have for the nightly ledger, not a
09:30–10:30 signal.

**Mechanics if picked up:** naively widening the window to `[open, close]` is wrong —
closing prints straggle minutes past 16:00 (see the 16:05:05 span above), so
"include the closing cross" means a window end past the last straggler or an
explicit class-based pull, plus the same change in BuildShares' minute loop (which
currently iterates `[open, close)` minutes only), and a baseline rebuild. Related
open item: condition-55 `G` cross accounting (this file, above) — its
double-count question affects how much cross $vol the ledger would attribute.

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

## Late-print lateness analysis → sizes 3.3's quote-memory window (flat-file study)

**Logged:** 2026-08-14 (decided in discussion). **Lands in:** a
`discovery-scripts` analysis whose numbers go into story 3.3's mini-spec;
complements (does not replace) the 1.5 live late-print lag metric.

**What:** offline study over full-session flat files (2026-08-11, -13, -14, and
future sessions): for every trade, lateness = `sip_timestamp − participant_timestamp`.
Report the distribution (p50/p95/p99/max) overall and split by venue class
(exchange vs TRF) and by late-marked conditions (5, 32, 33); share of prints later
than 1s / 5s / 30s / 5min; and the same cuts **dollar-weighted** (one large late TRF
print matters more than many tiny ones). Evidence already in hand pointing the same
direction: 2.8% of universe prints on 08-11 arrived sequence-regressed, all trade-side.

**Why:** story 3.3's quote-relative classification wants the NBBO *as of execution
time*. How much quote history to keep in memory (the ring-buffer window) should be
sized from measured lateness percentiles, not guessed. The flat files let us measure
this now, across whole sessions, before any live 1.5 data accumulates.

**Scope decision recorded (2026-08-14):** the immediate 3.3 story classifies only
**on-time prints** — execution time within a small tolerance of report time — against
the *current* in-memory NBBO. Late prints stay unclassified in v1 (they count in
DeltaRatio's denominator only, consistent with the existing NON_PRICE_FORMING
treatment). The ring-buffer / classify-against-pt upgrade is a separate follow-up
gated on this analysis; the existing 3.3/0.3 backlog note about a rolling
quote-history window merges into this item.

## Universe loader hardening + baskets schema field (schema half needs trader)

**Logged:** 2026-08-13 (1.1 post-close code review). **Lands in:** `internal/universe`
plus a D1-owner config change.

**Deferral being recorded:** Phase 0 story 0.1 carried "effective-date/versioning
mechanism; config loads, validates (no orphan tickers, no empty baskets), membership
change produces a new version without touching history" to the first loader module.
`internal/universe.Load` (1.1) shipped without any of it — defensible for a
read-and-count skeleton, but the deferral was recorded nowhere until now. Effective-date
stamping is a CLAUDE.md invariant (historical replays must use historical membership);
it becomes load-bearing no later than Phase 2 baselines. Validation (empty baskets,
duplicate members, unknown-symbol checks) should land with it. **Blast radius widened
2026-08-16 (3.0 review):** `LoadBaskets` now feeds *current* membership into historical
capture replays for the dev view — a config edit between a session and its replay
silently changes what the replayed table shows. Tolerable while the PM isn't editing
yet and the view is a dev instrument; the fix is the same effective-date mechanism, and
the config schema (trader-owned) still carries no dates to consume — that schema ask
rides with the `equity: false` proposal below. (`LoadBaskets` de-duplicates members as
of the same review; full validation still pends here.) Also recorded from the 3.1
spec review (2026-08-16): **coverage-drift bias** — a member added to the universe
later has no rows in older bucket files, and zero-materialization makes "absent from
capture" read as "didn't trade", so per-ticker volume profiles (`Row.Days` is
unconditional) and any basket-day sample silently bias low for pre-addition days.
3.1's share profiles mitigate with an all-session-silent skip rule (mini-spec D2b);
the per-ticker volume profiles have no equivalent. Effective-date membership is the
real fix for both.

**Schema proposal for the trader:** the engine currently excludes non-equity symbols by
matching the trader-owned group *name* `bond_gate_futures` — a string the trader may
rename or split at will, silently changing the universe (review finding; ⚠ comment at
the match site). Also: the exclusion only scans benchmark groups, so a non-equity
symbol placed in a basket's `members` leaks through unconditionally. The fix is a data
field on groups (e.g. `equity: false`) filtered on as data, not identity — but the JSON
is trader-owned (D1), so this is a schema *proposal* to take to the trader, not an
engineering edit. Until accepted, the group-name match stands, documented as fragile.

## Shared flat-file reader extraction (LOW priority — refactor)

**Logged:** 2026-08-13 (1.1 review; deferred again at 1.3 close). **Lands in:** a small
`internal/flatfile` package when a second CSV consumer appears (candidate: the 2.1
profile builder if it reads flat files rather than REST aggs).

The open/gz-sniff/header-map/conditions-parse logic is duplicated near-verbatim between
`internal/feed/file.go` and `internal/classify/acceptance_test.go`. Both copies are
test-guarded, so divergence is caught — but the test that reads real session data should
guard the production reader, not a sibling copy. Deferred at 1.3 close because replay
runs on capture files (not CSV), leaving the CSV path with a single production consumer.

## Pre-market / post-market (extended hours) tracking (new display scope — trader input needed)

**Logged:** 2026-08-13. **Lands in:** its own post-v1 story (Phase 6 earliest); touches
ingest hours, bucket store, and a session-scoped *read* of the 0.3 classification —
never the 0.3 table itself.

**What:** v1 is a regular-session instrument by design — Form T (12) and Extended Hours
Sold OOS (13) classify `NON_FLOW`, historical bars are filtered to 09:30–16:00, and
baselines exist only for regular-session buckets. But extended-hours activity is real
context for the open: pre-market concentration on a guide-night gap (the SNDK reference
day) previews where dollar volume will fight at 09:30, and post-market earnings
reactions seed the next morning's watch. The ask: a separate extended-hours view
(pre 04:00–09:30, post 16:00–20:00 ET) showing where $vol is concentrating, kept
strictly apart from the regular-session instrument.

**Why deferred:** the primary user moment is 09:30–10:30, and extended hours are
structurally weaker on every axis the instrument depends on — volumes are thin and
lumpy so a 20-day matched-bucket baseline mostly hits the σ floor; NBBO is sparse so
Lee-Ready is largely unavailable (F2 worsens); off-exchange share is higher (F3
compounds). Building it before the regular-session core is proven inverts the build
order, and whether it earns screen space at all is the trader's call.

**Constraints when picked up:**
- `NON_FLOW` for 12/13 stands for every regular-session metric. Extended tracking reads
  the same stored prints through a session-scoped lens; editing the 0.3 table would
  break replay reproducibility.
- Extended-hours buckets are a separate ledger (like crosses): nothing leaks into
  regular-session baselines, CUSUM, or since-open anchors — no seam at 09:30 or 16:00.
- Ingest/capture window widens (04:00–20:00 ET); capture stays unconditional; disk
  budget and the nightly profile-builder window get re-checked.
- Baseline design is the open question: 20-day matched buckets are likely too thin
  pre-market — either a longer lookback, coarser buckets, or raw $vol with no z at
  first. The aggs endpoint already returns extended hours (currently filtered out),
  so historical seeding needs no new procurement.
- Trader decisions: what renders (pre-open strip? tile brightness before 09:30?),
  and whether post-market matters live or only as next-morning context.

## Bucket CSV column-compatibility contract (binds story 3.3+, no work now)

Recorded 2026-08-15 (1.4 code review). The bucket file's D3 promise — "3.3 adds
columns without breaking old files" — creates an obligation on the FUTURE reader
edit, not on 1.4: `bucket.ReadCSV` maps columns by header name and ignores unknown
columns, so old readers already survive new files. Whoever adds columns (3.3 signed
volume is the expected first case) must read them as **optional** — absent from a
pre-3.3 file means "not recorded then", never an error and never a silent zero
presented as a measurement. Only the 1.4 base columns may hard-error when missing.
Contract is also documented on `ReadCSV` itself; acceptance for the 3.3 change
should include reading a real pre-3.3 bucket file.

## 1.5 — Ingest data-quality monitoring (deferred from Phase 1, 2026-08-15)

Moved out of the critical path to prioritize trader-visible output (2.1 baselines →
3.0 dev view). Not dropped: the story's two *surfaces* don't exist yet — "live in the
dev view" needs 3.0, "nightly data-quality block" needs the ledger (6.5) — so the story
re-enters with those. **Re-entry:** when 3.0 lands, surface the already-collected
counters there (nearly free); when 6.5 lands, add the nightly block.

**Already running in code today (collection exists; only the reporting is deferred):**
per-symbol trade/quote counters, sequence-regression counters (trades/quotes),
decode-error / unknown-symbol / status counts, `CondOverflow`, the unknown-condition
tripwire (per-ID counts, printed at session end and carried in the bucket store),
manifest message counts (reconciliation numbers), capture integrity via
manifest-absence, queue high-water mark.

**Agreed metrics (from the original story, unbuilt):**
- Late-print lag distribution — `report_ts − execution_ts` per print, per venue class
  (exchange / TRF / out-of-sequence); nightly p50/p95/p99/max. Note: the flat-file
  lateness *study* that sizes 3.3's quote-memory window is its own backlog entry and is
  NOT deferred by this move.
- Unaccounted condition codes — count, first-seen, one sample print, nightly ledger
  line. (The runtime tripwire half already exists; first-seen sample + ledger line
  pend.)

**Proposed metrics (trim at mini-spec time, unchanged from the original list):**
classification-quality shares (per 3.3); message-count reconciliation vs vendor totals
as a permanent nightly check; sequence-gap counter; feed liveness/silence per symbol
group; quote staleness at classification; crossed/locked NBBO time (feeds D9); ingest
throughput + queue depth + per-stage latency (feeds 6.2); clock drift; capture-file
integrity + disk headroom; halt detection (LULD — may deserve its own story; touches
3.7 breadth denominators and tile rendering).

**Done-when (unchanged):** agreed metrics visible in the 3.0 dev view on a replayed
session; nightly ledger entry contains the session's data-quality block; replaying the
same capture twice produces identical monitoring numbers.

## Websocket quote re-broadcast hypothesis (~30s cadence) — verify by data, confirm with vendor

**Logged:** 2026-08-15 (1.2 criterion-4 reconciliation). **Lands in:** 1.5 (quote-count
reconciliation + quote-staleness metrics); may also matter to 3.3's quote memory.

Evidence: reconciling the 08-14 capture vs flat files over 12:00–16:00 ET, ~84 symbols
each show almost exactly **480 more quotes in the capture** than in the SIP flat file —
one per 30 seconds over the 4-hour window, concentrated on quiet symbols and sector
ETFs (all XL*, defense names). Arithmetic (84 × 480 ≈ the full −40.5k delta) strongly
suggests the vendor websocket periodically re-broadcasts the current NBBO as a
keep-alive/refresh, and these synthetic records are not SIP quote events. Hypothesis,
not confirmed.

**Verify by data:** pick symbols with delta ≈ −480 (e.g. an XL* fund); align capture
quotes against flat-file quotes for the same window; isolate capture-only records and
check (a) inter-arrival spacing ≈ 30s, (b) whether payloads duplicate the prior NBBO
exactly, and — decisive — (c) whether they carry the same SIP timestamp `t` and
`sequence_number` as the original quote (same seq ⇒ trivial dedupe key exists; fresh
seq ⇒ vendor synthesizes records, nastier). `discovery-scripts/reconcile-capture.py`
already has the parsing needed.

**Ask Massive directly:** whether the stocks websocket re-sends the standing NBBO on an
interval, on what cadence, and whether a field distinguishes a refresh from a true SIP
update.

**Why it matters:** live quote counts run ~0.2% hot vs vendor truth (mechanically, not
loss) — 1.5's reconciliation must expect this or it will cry wolf nightly; and if
refreshes carry fresh timestamps, a naive quote-staleness metric would read a stale
book as fresh. Also carried from the same reconciliation: a residual unexplained
vendor-side surplus on a few active names (CORZ +57, CSCO +25, QQQ +16; ≤0.03% each,
zero reconnects that day) — recheck once the re-broadcast question is settled.
