# Print-Inclusion Policy — story 0.3

**Status: policy complete 2026-08-11** — all 40 sale conditions classified in joint
review; the per-condition table below is normative. Story 0.3 remains open until the
classification function passes the table-driven acceptance test on a full
recorded/flat-file day. Open details: id 55 verification (`docs/backlog.md`).
Cancels/corrections deferred to backlog (low priority) 2026-08-11.

## Mini-spec

- **What:** the written policy for which prints count toward flow metrics, encoded as a
  single classification function `classify(trade) -> class` with table-driven tests.
  Every metric and replay reproducibility depend on it; it is written before ingestion code.
- **Done when:** this doc's table is complete and the classification function passes a
  test table covering every condition code observed in one full recorded day (Massive
  flat files serve until Phase 1 records a live session).
- **Decisions consumed:** none open — crosses-tracked-separately is Build Spec §4;
  delayed-open fallback decided 2026-08-11 (below).

## Classification classes

Output of the single classification function. One class per print.

| Class | Meaning |
|---|---|
| `CONTINUOUS` | Counts toward continuous flow $vol in its 1-second bucket; eligible for Lee-Ready (story 3.3). |
| `CROSS_OPEN` | Opening auction cross. Price anchors RelPerf/breadth ("since open"); $vol goes to the cross ledger, never continuous flow. |
| `CROSS_CLOSE` | Closing auction cross. Same ledger treatment. |
| `CROSS_REOPEN` | Post-halt reopening cross. Cross ledger, flagged as reopening; no z-score (no matched baseline for rare events); never resets the since-open anchor. |
| `NON_PRICE_FORMING` | Real money whose price/time can't be trusted: late reports (5, 32, 33) and contemporaneous-but-non-market-priced prints (10, 21, 22, 52, 53). Counted in $vol at receipt time; excluded from price-forming series (CUSUM, slopes) and from Lee-Ready in v1. Logged (below). |
| `BLOCK` | Negotiated/crossed institutional prints (9 Cross Trade, 24 Rule 127 — decided 2026-08-11). Real dollars at a real price, but no open-market aggression occurred; Rule 127 executes outside the quote, so Lee-Ready would misread it as maximally aggressive. Counted in $vol (FlowShare, RVOL); excluded from every other metric; logged (below) for a possible future block-flow metric. |
| `DUPLICATE` | Dollars already on the tape as individual prints (e.g. Average Price re-reports). Stored, never counted. |
| `NON_FLOW` | Administrative non-trades (official open/close messages), non-regular settlement (7, 20, 29 — decided 2026-08-11), and out-of-session prints. Stored, never counted. |
| `UNKNOWN` | Condition ID not in the table. Stored, ignored by all metrics, tripwired. |

**Multi-condition precedence:** `c[]` is an array; a print can carry several conditions.
Rule: exclusion-dominant, total order — `UNKNOWN` > `NON_FLOW` > `DUPLICATE` >
`NON_PRICE_FORMING` > `CROSS_REOPEN` > `CROSS_OPEN` > `CROSS_CLOSE` > `BLOCK` >
`CONTINUOUS`. (`UNKNOWN` quarantines the whole print — an unrecognized code means its
semantics are unknown. The within-cross order is arbitrary but fixed for determinism.) The function classifies per condition, then takes the highest-precedence
class present.

**Default:** empty `c[]` = Regular Sale (modifier 0) — the majority of the tape —
classifies `CONTINUOUS`.

**Unknown codes (decided 2026-08-11):** a condition ID not in this table (e.g. 40
"Held" and 46 "Correction" appear in vendor web docs but not the REST dump) classifies
`UNKNOWN`: ignored by every metric, stored like everything else, counted in a
data-quality tripwire reviewed nightly. Expected frequency ~zero.

**ID authority (recorded 2026-08-11):** classification keys on the vendor's numeric
condition IDs as returned by `/v3/reference/conditions` — never on SIP characters
(collisions: 6/37 both CTA `I`; 23/24 both `K`; 35/52 both `V`) and never on the IDs
in the vendor's web docs, which drift from the endpoint's. Where the vendor's prose
definitions conflict with `update_rules` (e.g. 23/27 carry copy-pasted Seller's Option
text), `update_rules` win — they are the SIP's machine-consumed behavior.

## Why crosses are a separate series, not blended (recorded 2026-08-11)

Nothing is discarded: 1-second storage keeps every print, and daily totals sum both
ledgers. The separation is at the metric level, for two reasons:

1. **Cross timing jitters across buckets day to day.** Nasdaq crosses complete
   ~09:30:00–:02; NYSE DMM opens land anywhere from 09:30:00 to 09:33+, per name, per
   day. A print worth a large share of the first minute's volume floating across
   different time-of-day buckets injects variance into whichever baseline bucket it
   hits — σ inflates and opening-window z-scores (the primary user moment) degrade.
   Matched-bucket baselines assume the same economic event recurs in the same bucket;
   the cross violates that.
2. **The cross is a different economic object** — a batch clearing of overnight order
   accumulation, not continuous aggression. No prevailing NBBO is hit, so Lee-Ready is
   undefined on it.

The cross ledger gets its own per-ticker 20-day baseline, compared event-to-event
(cross vs prior crosses), not bucket-to-bucket. "Opening cross N× its 20-day average
cross" is a first-class observation; unusual opening auction volume is *more* visible
this way, not less.

**Delayed-open fallback (decided 2026-08-11):** until a ticker's opening cross arrives,
its since-open anchors are **absent** — the ticker contributes nothing to RelPerf or
breadth, and renders as no-data rather than a provisional number seeded from the first
continuous print. A false silence beats a false number.

## Lateness log

Every `NON_PRICE_FORMING` print — **plus Sold Last prints (30, 31), which classify
`CONTINUOUS` because they print in sequence** — is appended to a session log: ticker,
condition codes, SIP timestamp `t`, participant timestamp `pt`, **lateness = t − pt**,
size, notional, and the 1-second bucket it was counted in. Purpose: measure how many
reports are late, how late, and whether the flow is material — evidence for tuning the
policy at the nightly ledger review rather than guessing. If the data shows Sold Last
prints are materially late, they flip to `NON_PRICE_FORMING` with evidence.

## Block log

Every `BLOCK` print (9, 24) is appended to a session log: ticker, timestamps, size,
notional, condition, exchange. Purpose: raw material for a possible future block-flow
metric (decided 2026-08-11) — until then, blocks contribute dollars to FlowShare/RVOL
and nothing else.

## Non-sale codes that ride on trade messages (neutral)

`data_types` in the vendor's conditions table shows 15 non-sale conditions can appear
on trades. All are ticker-state or regulatory flags, not statements about the print's
economics; the classifier ignores them. Listed so the acceptance test's "every observed
code" bar covers them without failing:

| IDs | Type | Treatment |
|---|---|---|
| 41 | trade_thru_exempt | Neutral — regulatory flag |
| 57–60 | short_sale_restriction_indicator | Neutral — ticker SSR state |
| 62–71 | financial_status_indicator | Neutral — ticker financial status |

Quote conditions (33, `bbo`/`nbbo`) are out of 0.3 scope — consumed by story 3.3's
NBBO-validity appendix.

## Cancels & corrections (decided 2026-08-11: deferred)

v1 ignores canceled/corrected trades. The live websocket carries no cancel/correction
events (verified in the vendor client — the `correction` indicator exists only on
REST/flat files), so intraday handling is impossible; busts are rare enough that the
distortion is negligible. Stored sessions reflect the tape as delivered live. The fix,
when warranted, is a nightly REST reconciliation — specced as a low-priority item in
`docs/backlog.md`.

## Backlog (post-MVP)

- **Condition ID 55 (CTA `G`) verification** — provisional `CROSS_OPEN`; double-count
  risk and verification steps detailed in `docs/backlog.md`.

- **Lee-Ready on late reports:** retain a rolling quote-history window (minutes) so
  `NON_PRICE_FORMING` prints can be classified against the NBBO as of their participant
  timestamp `pt`. In v1 they are unclassified (DeltaRatio denominator only, per 3.3).
  Also noted on story 3.3.

## Per-condition table (normative — reviewed 2026-08-11)

Classification keys on the vendor's numeric ID (first column). SIP characters shown
for reference only.

| ID | Name | SIP | Class | Note |
|---|---|---|---|---|
| — | Regular Sale (empty `c[]`) | `@`/none | CONTINUOUS | The majority of the tape |
| 1 | Acquisition | UTP `A` | CONTINUOUS | Legacy, near-zero frequency |
| 2 | Average Price Trade | `B`/`W` | DUPLICATE | Double-counts constituent fills; deliberate deviation from `updates_volume`=✔ |
| 3 | Automatic Execution | CTA `E` | CONTINUOUS | |
| 4 | Bunched Trade | UTP `B` | CONTINUOUS | Contemporaneous aggregate |
| 5 | Bunched Sold Trade | UTP `G` | NON_PRICE_FORMING | Bunched trade reported late |
| 6 | CAP Election | CTA `I` | CONTINUOUS | Legacy; char collides with 37 |
| 7 | Cash Sale | `C` | NON_FLOW | Same-day settlement, non-comparable price |
| 8 | Closing Prints | UTP `6` | CROSS_CLOSE | |
| 9 | Cross Trade | `X` | BLOCK | Crossing-session match; no aggression occurred |
| 10 | Derivatively Priced | `4` | NON_PRICE_FORMING | Price not from quoted market |
| 11 | Distribution | UTP `D` | CONTINUOUS | Gradual block selling, contemporaneous |
| 12 | Form T/Extended Hours | `T` | NON_FLOW | Outside 09:30–16:00 |
| 13 | Extended Hours Sold OOS | `U` | NON_FLOW | Outside 09:30–16:00 |
| 14 | Intermarket Sweep | `F` | CONTINUOUS | Common; aggressive by construction |
| 15 | Official Close | `M` | NON_FLOW | Admin, zero volume |
| 16 | Official Open | `Q` | NON_FLOW | Admin, zero volume |
| 17 | Market Center Opening Trade | CTA `O` | CROSS_OPEN | Anchor from listing exchange print only |
| 18 | Market Center Reopening Trade | CTA `5` | CROSS_REOPEN | |
| 19 | Market Center Closing Trade | CTA `6` | CROSS_CLOSE | |
| 20 | Next Day | `N` | NON_FLOW | Next-day settlement |
| 21 | Price Variation Trade | `H` | NON_PRICE_FORMING | Price far from prevailing market |
| 22 | Prior Reference Price | `P` | NON_PRICE_FORMING | Priced >90s stale by definition |
| 23 | Rule 155 (AMEX) | `K` | CONTINUOUS | Vendor prose is copy-paste error; `update_rules` win |
| 24 | Rule 127 (NYSE) | CTA `K` | BLOCK | Outside-quote negotiated block (≥10k sh / ≥$200k) |
| 25 | Opening Prints | UTP `O` | CROSS_OPEN | The 09:30 anchor print (Nasdaq-listed) |
| 27 | Stopped Stock | UTP `1` | CONTINUOUS | Legacy; prose copy-paste error |
| 28 | Re-Opening Prints | UTP `5` | CROSS_REOPEN | |
| 29 | Seller | `R` | NON_FLOW | Seller's-option settlement |
| 30 | Sold Last | `L` | CONTINUOUS | In-sequence late report; in lateness log |
| 31 | Sold Last + Stopped Stock | UTP `2` | CONTINUOUS | Rides with 30; legacy |
| 32 | Sold (Out of Sequence) | `Z` | NON_PRICE_FORMING | Timestamp unreliable by definition |
| 33 | Sold OOS + Stopped Stock | UTP `3` | NON_PRICE_FORMING | Rides with 32; legacy |
| 34 | Split Trade | UTP `S` | CONTINUOUS | Two-market execution |
| 35 | Stock Option | UTP `V` | CONTINUOUS | Option-MM delta-hedge stock leg; char collides with 52 |
| 36 | Yellow Flag Regular Trade | UTP `Y` | CONTINUOUS | |
| 37 | Odd Lot Trade | `I` | CONTINUOUS | Fully counted — retail flow is core signal; deliberate deviation from SIP price rules |
| 38 | Corrected Consolidated Close | `9` | NON_FLOW | Admin correction, no volume |
| 52 | Contingent Trade | CTA `V` | NON_PRICE_FORMING | Contingency-priced, not market-priced |
| 53 | Qualified Contingent Trade | `7` | NON_PRICE_FORMING | Component-relationship pricing |
| 55 | Opening Reopening Trade Detail | CTA `G` | CROSS_OPEN *(provisional)* | Double-count verification: `docs/backlog.md` |

Non-sale trade-riding codes (41, 57–60, 62–71): neutral, see above. Any other ID:
`UNKNOWN`.

## Empirical notes — 2026-08-11 full session (126.6M trades, acceptance run)

- **Auction crosses print as 17 (open) / 8 (close) / 18 (reopen) on both tapes.**
  Massive normalizes: ids 25, 19, 28 never occurred; NVDA's Nasdaq opening cross
  carried `[17,9,41]`, its closing cross `[8,9,41]`. Anchor logic keys on 17.
- **Crosses also carry id 9** (crossing session) — the cross-classes-above-BLOCK
  precedence is what routes them correctly.
- **Official open/close rows (16/15) duplicate the cross's price AND size** ~1ms
  after the real cross print — `NON_FLOW` on them prevents double-counted auctions
  (their price×size is also why NON_FLOW dollars look inflated in raw stats).
- 22 distinct sale-condition IDs observed; zero `UNKNOWN`; id 55 absent (backlog
  tripwire stands). Odd lots (37) rode on 70% of all prints; Trade Thru Exempt (41)
  on a third. Ids 30/31 (Sold Last) never occurred.
- Flat-file quirk: `size` is encoded as a float string (`152.000000`), unlike the
  websocket's integer.
