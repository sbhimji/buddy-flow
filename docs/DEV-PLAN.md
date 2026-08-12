# MORNING TAPE — Development Plan v1

Companion to *Build Specification v2.0*. Restructures the build into phases and stories, incorporates the 19 spec amendments identified in review (referenced as **[F#]** throughout, full list in Appendix A), and isolates every decision requiring the trader into one sheet (Appendix B).

**Method:** spec-driven development at story granularity. Each story gets a half-page mini-spec (what, acceptance criteria, decisions consumed) written immediately before implementation — never for the whole project at once. A story is closed only when its acceptance criteria pass on replayed recorded data.

**Timeline honesty [F10]:** the original 2-week estimate reaches a demo, not the §6 acceptance bar. Realistic solo pace: 4–6 weeks to hardened, assuming partial reuse of prior (APEX) ingestion plumbing. Phases 0–1 are the schedule risk; the math core is fast once replay exists.

---

## Phase 0 — Foundations & Decisions

Goal: every ambiguity that can block a later story is resolved or explicitly deferred; vendor access works.

**~~0.1 — Universe & basket definition [F4, F5]~~** ✅ resolved 2026-08-09
- *Status:* universe + basket map delivered and trader-confirmed (`docs/foundations/morning-tape-baskets-v2.json`, D1/D3 answered). Selection criteria and review cadence are trader-owned — out of engineering scope (D2).
- ~~Trader delivers: the universe and basket membership map (Decision Sheet items D1, D3).~~
- ~~Decide single- vs multi-basket membership [F5a]. If multi, document that shares no longer sum to 100% and the zero-sum framing carries a caveat.~~ Multi confirmed; F5a caveat documented in baskets.md and Appendix C.
- ~~Include reference instruments: SPY, BIL, SGOV, bond proxies for the header.~~ In config: broad/cash-proxy/duration benchmarks + ZN/ZB.
- Carried to first code (loader module): effective-date/versioning mechanism; *done when* config loads, validates (no orphan tickers, no empty baskets), and a membership change produces a new version without touching history.

**~~0.2 — Data vendor setup~~** ✅ resolved 2026-08-11
- *Status:* vendor = **Massive Stocks Advanced** (massive.com, the former Polygon.io), real-time SIP consolidated feed — supersedes the Databento/Polygon language below (Databento has no consolidated equities dataset until EQUS.SIP ships). Source of truth: `docs/foundations/data.md`; verification results: `docs/temp/0.2-results.md`.
- ~~Sign up / verify: Databento consolidated trades+NBBO (spine), Polygon Advanced (failover), Unusual Whales (existing license, overlay only — deferred to Phase 6).~~ Massive Stocks Advanced provides SIP consolidated trades, NBBO quotes, condition codes, TRF venue attribution, LULD + imbalance channels, and 20+yr historical. Unusual Whales overlay still deferred to Phase 6.
- ~~Confirm entitlements cover TRF venue codes, condition codes, and auction cross flags.~~ Entitlements verified by scripts 2026-08-11 (`discovery-scripts/verify-universe`, `verify-feed`): 190/190 universe symbols resolve; live websocket entitled and streaming (NVDA 5-min: 42k trades / 33k quotes, latency p95 = 104ms vs 5000ms budget); 94 condition codes dumped to `docs/foundations/massive-conditions.json` for story 0.3.
- **Deferred:**
  - **Failover feed** — spec's dual-feed requirement deliberately deferred: Databento EQUS.SIP (late Q3/Q4 2026) is the intended second feed; revisit at story 6.1.
  - **Bond tape (ZN/ZB)** — backlogged (see 5.2 note).
  - **BESIY** — 2026-08-11 probe: does not stream on the real-time websocket (OTC prints are REST-only; no NBBO exists at the vendor, so no Lee-Ready either). Keep/drop/replace pending trader decision.

**0.3 — Print-inclusion policy [F9]**
- Written policy for which prints count toward flow metrics: handling of out-of-sequence trades, cancels/corrections, average-price and other derivative prints, odd lots, opening/closing crosses (crosses tracked separately per §4, not blended into continuous flow).
- This policy is a dependency of every metric and of replay reproducibility; it is written *before* ingestion code, then encoded as a single classification function with table-driven tests.
- *Done when:* policy doc exists and the classification function passes a test table covering every condition code observed in one recorded session.

**0.4 — Trader Decision Sheet round 1**
- Deliver Appendix B to the trader; collect D1–D6 (the items that block Phases 0–3). D7+ can wait for their phases.

**0.5 — Spec reconciliation [A9] (open item)**
- Obtain the trader's "Layers"-numbered spec version (references seen: "Layer 6" bond gate, "Layer 8" leadership-divergence detector) and reconcile its numbering with Build Spec v2's §-numbering.
- The leadership-divergence detector is not described in any document currently held; capture its definition before scoping it into any phase (3.1's ex-leader flow series is its likely input — see [A8]).

---

## Phase 1 — Capture, Replay & Ingestion

Goal: live data flows in, every session is recorded, and any recorded session can be replayed deterministically. Replay is the development environment for the entire rest of the project (the market is only open 09:30–16:00 ET weekdays; replay is what makes evening/weekend work possible).

**1.1 — Ingest skeleton & throughput floor [F8]**
- Hot path in a compiled/concurrent-safe stack (Go/Rust/Java, or carefully structured async with the hot loop out of pure Python). Stated requirement: sustain ≥ 20k messages/sec (trades+quotes) through the 09:30 burst with zero drops. **Raised to ≥ 50k msg/sec** per baskets-v2 config: the universe is ~192 symbols incl. benchmarks (165 unique members + 27 benchmarks/futures; 2026-08-10 amendment), not the spec's ~60 — verify Databento tier pricing at this symbol count, and note ZN/ZB bond-gate futures are a separate Databento dataset (CME) not covered by the equities feed.
- Maintains in-memory current NBBO per ticker (the Lee-Ready lookup table).
- Evaluate APEX ingestion reuse here: if its feed handlers, reconnect logic, or storage match, port rather than rewrite; reuse is already assumed by the original spec's 60–80% figure.
- *Done when:* one full live session ingested with message counts matching vendor-reported totals.

**1.2 — Capture to disk**
- Every raw message (both feeds) appended to per-session capture files with receive timestamps. Capture is unconditional from the first live run — recorded sessions are the project's test corpus.
- Immediately record: several ordinary sessions, at least one boring low-volatility day, and (as they occur) a macro morning. Obtain/record the §6 reference days: the SNDK guide-night open, the VIAV→optics morning, a known de-grossing day.
- *Done when:* a session capture replays through the ingest path and produces identical message counts and identical bucket outputs twice [§5 Days 1–3 "Done when"].

**1.3 — Replay harness**
- Replays a capture file through the full pipeline at configurable speed (real-time, accelerated, instant), driving the same code paths as live.
- Deterministic: same capture in → bit-identical metrics out.
- *Done when:* every subsequent story's acceptance tests run against replay, not live.

**1.4 — Bucket store [F7]**
- Store at 1-second resolution for the entire session, all day (cheap at 60 tickers); 1-minute and rolling views are *derived*, never a second storage format. This removes the 10:00 resolution seam that would otherwise distort CUSUM and slopes.
- Persist every computed metric per bucket as it comes to exist in later phases (design principle: **store everything, model nothing** — this is the future-ML dataset, the replay scrubber's source, and the grading ledger's input, all for free).
- Zero-trade buckets: FlowShare 0/positive = 0; define 0/0 (whole universe silent) as carry-forward-blank and render as gap [F13 addendum].
- *Done when:* buckets from a replayed session match a hand-computed sample and derived 1-minute bars equal the sum of their 1-second bars.

**1.5 — Ingest data-quality monitoring**
- Counters/distributions computed inside the ingest path, surfaced two ways: live in the 3.0 developer view, and as a nightly data-quality block in the grading ledger. Loud logs only in v1 — 6.3 owns real alerting. Consumes the 0.3 condition-code table and 3.3's NBBO ring-buffer window. These metrics answer one question: *is the data feeding the map trustworthy right now?*
- Agreed metrics:
  - **Late-print lag distribution** — `report_ts − execution_ts` per print, tracked per venue class (exchange / TRF / out-of-sequence). Tunes the 3.3 ring-buffer window from measured reality; detects a venue reporting later than normal. Nightly: p50/p95/p99/max to ledger.
  - **Unaccounted condition codes** — any print whose code combination isn't in the 0.3 table: count, first-seen, one sample print. Default treatment applies (count volume, don't classify, log loudly); nonzero nightly count = a ledger line for a human to classify into the table.
- Proposed metrics (trim at mini-spec time): classification-quality shares (ask/bid/tick-rule/unclassified %, per 3.3); message-count reconciliation vs vendor totals (1.1's done-when made a permanent nightly check); sequence-gap counter (proof of dropped messages; failover trigger input for 6.1); feed liveness/silence per symbol group (distinguishes "quiet ticker" from "symbology break"); quote staleness at classification (p95 spike = delta untrustworthy); crossed/locked NBBO time (feeds D9); ingest throughput + queue depth + per-stage latency (proves the 50k floor; feeds 6.2); clock drift (`receive_ts − exchange_ts`); capture-file integrity + disk headroom (a truncated capture is an unrunnable replay); halt detection (LULD status — a halted ticker prints zero volume and would misread as "abandonment" in 3.7; touches breadth denominators and tile rendering, so may deserve promotion to its own story).
- *Done when:* agreed metrics visible in the 3.0 dev view during a replayed session; nightly ledger entry contains the session's data-quality block; replaying the same capture twice produces identical monitoring numbers (determinism applies to the monitors too).

---

## Phase 2 — Baselines

**2.1 — Nightly profile builder [F6, F11]**
- 20-day rolling volume profiles per **ticker** per 1-minute bucket (per-ticker storage so basket edits never invalidate history; basket baselines are computed as sums over member profiles at read time).
- Calendar policy [F11]: exclude half days; flag-and-configurably-exclude FOMC/CPI-class event days (Decision D6); a ticker contributes to z-scores only after ≥10 profiled days.
- Robustness [F12]: choose one — winsorized mean/σ, or median/MAD — for the per-bucket baseline statistics, so one wild day in the lookback doesn't suppress the next month's z-scores. (Recommendation: median/MAD, simplest to reason about.)
- *Done when:* profiles for the full universe build in the nightly window; sanity check per §6 — matched-bucket RVOL ≈ 1.0 across baskets at 09:35 on an average replayed day.

**2.2 — σ guard [F13]**
- σ floor before any z computation: σ_used = max(σ_measured, floor), floor defined as a fraction of the universe-wide median per-bucket σ (Decision D5 sets the fraction; default 0.25×).
- Applies to every z in the system: FlowShareZ, bond velocity z, and any future DeltaRatio z.
- *Done when:* a synthetic near-zero-σ series produces bounded z; literal σ=0 produces a defined null, never an exception.

---

## Phase 3 — Math Core (one calculation per story, dependency-ordered)

Each story: mini-spec → implement → unit tests → verify on replay → watch it in the developer view (3.0).

**3.0 — Developer view (build first)**
- Crude auto-refreshing table (terminal or single HTML page): one row per basket, one column per metric, fed by live or replay. No design work. This is the learning instrument — every subsequent metric becomes visible the day it's built, which is how a developer without trading background acquires intuition for what the numbers do on real mornings.

**3.1 — FlowShare & FlowShareZ [F4]**
- Per §2.1, z per matched bucket using Phase 2 baselines with σ guard.
- Document the universe-boundary caveat in the metric's mini-spec: zero-sum holds only within the tracked universe; flow leaving the universe entirely redistributes shares misleadingly (mitigated by 3.7's de-grossing precedence).
- Concentration stat **[A8]**: per basket per bucket, compute the top member's share of basket dollar volume (e.g., NVDA 63% of basket $vol). Always computed and persisted; rendered per 5.1. The complementary ex-leader basket flow series is the input 3.7's degeneracy guard uses and is the same series a future leadership-divergence detector needs (see 0.5).

**3.2 — Breadth [F17, F18]**
- Three-state per member vs SPY: outperform / in-line / underperform, with dead-band ±X bps (Decision D7, default 10) and optional persistence filter (member counts only after N consecutive minutes on one side; default 3).
- Reference point: since-open (Decision D8; must match 3.6 RelPerf).
- Display raw fraction (8/9) so small-basket evidence is visibly weaker; three-state detail (6↑ 1↓ 2·) on hover/detail view.
- Mini-spec notes breadth is correlation-blind (ETF arbitrage moves all members mechanically) — one Glow input, never standalone.
- Easiest calc in the project; scheduled early deliberately as the momentum win.

**3.3 — Lee-Ready & signed volume [F2]**
- Aggressor classification against in-memory NBBO: at/above ask = buy, at/below bid = sell, midpoint = tick rule. NetDelta and DeltaRatio per basket per bucket.
- **Initial idea — classification cascade (2026-08-10, revisit at mini-spec time):** Databento's trades schema carries a venue-supplied `side` field (B = buyer-initiated, A = seller-initiated, N = not stated; expect N on TRF/off-exchange prints, which have no lit matching engine). Proposed order of preference: (1) venue `side` A/B — ground truth from the matching engine, use directly; (2) N → quote-relative classification vs NBBO at execution time (ring-buffer lookup); (3) midpoint/ambiguous → tick rule (compare vs last different trade price — weakest classifier, last resort); (4) still nothing (e.g. print older than ring-buffer window) → unclassified. Unclassified volume counts in DeltaRatio's denominator but not the numerator — uncertainty dampens the signal toward 0 rather than biasing it. Cross-check idea for 1.5: score our quote-relative classifier against venue side on A/B prints (off hot path) to get a measured accuracy rate for what we're applying to the N prints.
- Open-degradation policy [F2]: DeltaRatio suppressed or visually flagged for the first N seconds after 09:30:00 while quotes are wide/crossed (Decision D9, default 30s). Document that midpoint/price-improved retail executions fall to the tick rule and that DeltaRatio is an aggregate approximation, not print-level truth.
- *Done when:* replayed classification rates (buy%/sell%/tick-rule%) are stable across two runs and the tick-rule share is reported as a data-quality metric.

**3.4 — VWAP posture**
- Session VWAP per ticker from the same buckets; % of basket members above VWAP. Straightforward; no open items.

**3.5 — Off-exchange share [F3]**
- % of basket dollar volume with TRF venue codes.
- Dot renders only on the composite condition: dark share rising AND price rising AND DeltaRatio > 0 in the same window. Mini-spec documents the false-positive mode (retail frenzies are also heavily off-exchange) — the dot is a *maybe*, never a conclusion.

**3.6 — RelPerf [F19a]**
- Equal-weighted mean of members' returns relative to SPY (equal-weight so the term measures the sector, not its largest member), reference point identical to breadth's (D8).

**3.7 — Flow-shift detector & regime classifier [F14, F15]**
- CUSUM change-point detection on each basket's FlowShareZ series over 5-min and 15-min rolling windows (built from 1-second buckets — no seam, per 1.4).
- Two distinct reportable signatures, rendered with distinct language [F15]:
  - **Selling pressure:** share z elevated + DeltaRatio strongly negative → "SELLING PRESSURE in MEMORY (+2.0σ vol, δ −0.35)".
  - **Abandonment:** share z depressed → "MEMORY GOING QUIET (−1.8σ)".
- Header language is correlational, never causal [F14]: "MEMORY share collapsing (−1.8σ) while DEFENSIVES surging (+2.1σ), began 09:41" — no "out of / into". The mini-spec records the four assumptions the old phrasing hid (common actor, closed universe, activity≈positioning, simultaneity window).
- Precedence rule: the de-grossing check (breadth down broadly + dispersion + BIL/SGOV activity) runs **before** any rotation sentence, so "money leaving the universe entirely" is never misreported as rotation into whichever basket shrank least.
- Define rendering when ≥3 baskets break in one window (list top movers by |z|; no pairwise narrative).
- Overlap handling **[A6]** (accepted stakeholder proposal): (a) at config load and on every hot-reload, compute the basket-pair overlap matrix and auto-generate disjoint comparison sets (each basket minus the pairwise intersection); (b) all cross-basket narrative — CUSUM shift detection, header shift sentences, pairwise rotation claims — computes on disjoint sets **only** for overlapping pairs; non-overlapping pairs unchanged; (c) disjoint-set series use disjoint-set baselines, summed from per-ticker profiles (free given 2.1's per-ticker storage) — never full-basket baselines; (d) degeneracy guard: if a disjoint remainder has <3 members or <~30% of the basket's average dollar volume, suppress that pair's narrative entirely — a false silence beats a false story — and log the suppression to the nightly ledger; (e) header language remains correlational per [F14]. Display, tiles, glow, and breadth keep **full** membership — the which-crowd-did-it-ignite-with read survives intact.
- *Done when:* replay tests A–C pass — with test A's expectation first *verified against the recorded SNDK session* before being enshrined [F15]: confirm empirically whether the post-gap signature is share-spike-with-negative-delta (selling pressure) or share collapse (abandonment), and set the test to match reality.

**3.8 — Glow composite [F19b, F19c]**
- Normalize all four inputs to a common scale before weighting: FlowShareZ already in σ; z-score DeltaRatio and RelPerf against their own matched-bucket histories; re-center breadth (breadth − 0.5, or its own z) so all terms are signed.
- Weights configurable, start equal — meaningful only post-normalization.
- Define the signed Glow → color mapping: warm scale for positive, symmetric cold scale for negative (outflow tiles), intensity ∝ |Glow| (Decision D10 confirms palette).

---

## Phase 4 — Persistence, Ledger & ML-Ready Output

**4.1 — Metric persistence (verify, mostly done by 1.4's principle)**
- Confirm every metric from Phase 3 lands in the bucket store with schema versioning. This store *is* the future-ML dataset; no modeling in this project (scope law §7 — the thing that killed the last bot stays dead).

**4.2 — Nightly EOD grading job**
- Writes the day's final map state, all regime sentences with timestamps, shift events, and (free, EOD) the ETF creation/redemption scrape to the ledger.
- Optional v2 hook per §7: one headline-timestamp flag per basket for ledger grading only — record the socket for it, build nothing.

---

## Phase 5 — Frontend

**5.1 — Tile grid**
- Grid auto-sorted strongest inflow → heaviest outflow; per tile: Glow color/intensity, volume ring = matched-bucket RVOL, breadth fraction, 15-min FlowShareZ sparkline, dark-share dot. 5s refresh.
- Raw-share visibility [F16]: tile *area* ∝ raw FlowShare (or a one-tap toggle to a raw-share treemap) so the PM can distinguish "unusual for its size" from "large in absolute terms" — equal z ≠ equal dollars.
- Concentration stat **[A8]**: render the top member's share of basket dollar volume on the tile when it exceeds 50% (e.g., "NVDA 63%") — a >50% tile is honestly "<leader> + friends" and the PM should read it that way. Computed in 3.1; always available in the detail view.

**5.2 — Header**
- Line 1: mechanical regime sentence from 3.7 (DE-GROSSING / rotation-correlational / RE-GROSSING / CHOP). Line 2: 10Y/30Y level + velocity z (σ guard applies) — **BACKLOG (2026-08-09): bond tape (ZN/ZB, separate CME dataset) deferred; v1 ships header line 1 only.** TLT (in universe) available as interim duration proxy if wanted.
- No buy/sell language anywhere in the UI (§7); audit strings against [F1] — the product's honest claim is the cross-basket share view with matched baselines, not "delta that no retail tool has."

**5.3 — Replay scrubber**
- Drag through the morning in 1-min steps, reading directly from the bucket store (free, per 1.4).
- *Phase done when:* §5's bar — PM reads the 09:33 tape state in one glance, no clicks.

---

## Phase 6 — Hardening & Acceptance

**6.1 — Failover drill** — kill the primary feed mid-replay-burst and mid-live-session; secondary takes over ≤10s with no metric gap (§6).
**6.2 — Latency audit** — print-to-pixel ≤5s sustained through a replayed 09:30 burst; instrument each pipeline stage.
**6.3 — Ops** — market-calendar scheduler (holidays, half days), process supervision, disk monitoring for capture files, alerting on feed silence.
**6.4 — Acceptance suite** — §6 tests A–E as a single command against the recorded corpus; run green two consecutive days.
**6.5 — Live soak** — one full live week including at least one macro morning; nightly grading ledger reviewed with the trader each evening (this review is also the mechanism for tuning weights, dead-bands, and thresholds — D-item defaults are starting points, the ledger is the judge).
**6.6 — Options overlay (v2 gate)** — Unusual Whales net-premium as a secondary brightness input only, added only after the trader confirms daily use of v1 (§4 priority 3, §7 scope law).

---

## Appendix A — Fix List (final, 19 items)

| # | Section | Item | Home story |
|---|---------|------|-----------|
| 1 | §2.3 | "No retail dashboard has this" overstated; honest claim is the cross-basket matched-baseline view | 5.2 |
| 2 | §2.3 | Lee-Ready degraded at the open; suppression/flag policy for first N seconds | 3.3 |
| 3 | §2.6 | Dark-share tell ambiguous (retail flow also off-exchange); composite render condition + documented false-positive | 3.5 |
| 4 | §2.1 | Zero-sum holds only inside the tracked universe; selection criteria + review cadence + blind-spot doc | 0.1, 3.1 |
| 5 | — | Basket membership rules: multi-membership? edit mechanism? | 0.1 |
| 6 | §2.2 | Store profiles per ticker; compute basket baselines as member sums so edits never corrupt history | 2.1 |
| 7 | §4 | 10:00 resolution seam; store 1-second all day, derive coarser views | 1.4 |
| 8 | §5 | Ingest throughput requirement (≥20k msg/s burst) and stack constraint stated | 1.1 |
| 9 | §4/§5 | Print-inclusion policy (condition codes, odd lots, crosses, corrections) written before code | 0.3 |
| 10 | §5 | Timeline: 4–6 weeks to hardened, not 2 | plan-wide |
| 11 | §2.2 | Baseline calendar edge cases: half days, event days, new tickers (≥10-day minimum) | 2.1 |
| 12 | §2.2 | n=20 σ is noisy/fat-tailed; robust estimator (median/MAD or winsorized) | 2.1 |
| 13 | §2.2 | σ floor before any z; defined behavior for zero-trade and 0/0 buckets | 2.2, 1.4 |
| 14 | §2.4/§3 | Header overclaims causation; correlational template + documented assumptions + de-grossing precedence | 3.7 |
| 15 | §2.4/§3/§6 | "Out of X" ambiguous between selling-pressure (high z, δ<0) and abandonment (low z); distinct language; verify test A's signature empirically | 3.7 |
| 16 | §3 | No raw-share view; tile area or treemap toggle so equal z ≠ equal dollars is visible | 5.1 |
| 17 | §2.5 | Breadth underspecified: reference point, dead-band, small-basket display | 3.2 |
| 18 | §2.5 | Breadth three-state upgrade (out/in-line/under) + persistence filter; correlation-blindness noted | 3.2 |
| 19 | §2.7 | RelPerf undefined (equal-weight, matched reference point); Glow inputs normalized to common signed scale; signed color mapping | 3.6, 3.8 |

## Appendix B — Trader Decision Sheet

One pass, answered in writing; defaults apply until overridden; the nightly ledger review (6.5) is where defaults get tuned.

| ID | Decision | Default | Blocks |
|----|----------|---------|--------|
| ~~D1~~ | ~~Final universe + basket map~~ | **ANSWERED 2026-08-09**: baskets-v2 config, trader-confirmed | 0.1 |
| ~~D2~~ | ~~Universe selection criteria & review cadence~~ | **RESOLVED**: trader-owned, out of engineering scope; cadence per baskets.md §4 | 0.1 |
| ~~D3~~ | ~~Multi-basket membership allowed?~~ | **ANSWERED**: yes (7 overlapping members; F5a caveat applies) | 0.1 |
| D4 | Baseline event-day exclusions (FOMC/CPI class) | Flag, include | 2.1 |
| D5 | σ-floor fraction | 0.25× universe median σ | 2.2 |
| D6 | Robust baseline estimator | Median/MAD | 2.1 |
| D7 | Breadth dead-band | ±10 bps vs SPY | 3.2 |
| D8 | Breadth/RelPerf reference point | Since open | 3.2, 3.6 |
| D9 | DeltaRatio open-suppression window | 30 s | 3.3 |
| D10 | Glow palette (signed) & sort tie-break | Warm/cold symmetric | 3.8, 5.1 |
| D11 | Regime-sentence wording sign-off (correlational templates) | Per 3.7 | 3.7, 5.2 |
| D12 | Basket-edit workflow (who, how, versioned config) | Config file, engineer applies | 0.1 |
| D13 | Ignition options-premium confirmation gate (baskets-v2 config; conflicts with §7 "overlay, never driver" — ignition stays a v2-backlog display-emphasis badge, no alert mechanics, until resolved) | Disabled | 6.6 |

## Appendix C — Post-review amendments (2026-08-09)

Accepted stakeholder amendments, numbered per the review discussion (only the items below were delivered as plan changes; the review's other numbered items were not).

| # | Item | Home story |
|---|------|-----------|
| A6 | Overlapping-basket fix: cross-basket narrative (CUSUM, header sentences, pairwise rotation claims) computes on auto-generated disjoint sets with disjoint-set baselines; degeneracy guard (<3 members or <~30% of avg $vol → suppress narrative, log to ledger); display keeps full membership | 3.7 |
| A8 | Per-tile concentration stat: top member's share of basket dollar volume, always computed, rendered when >50% | 3.1, 5.1 |
| A9 | Obtain and reconcile the trader's "Layers"-numbered spec (Layer 6 bond gate, Layer 8 leadership-divergence detector — latter undocumented here) | 0.5 |

Note: A6 presupposes multi-basket membership (e.g., NVDA in both semis_compute and proof_tier), which effectively answers **D3 = multi allowed** — 0.1's [F5a] caveat (shares no longer sum to 100% across baskets) therefore applies and must be documented.
