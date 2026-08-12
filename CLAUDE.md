# MORNING TAPE

Real-time money-flow observation instrument. One screen showing where dollar volume is
concentrating across a ~191-symbol universe (164 basket members + benchmarks) grouped into
22 baskets, whether it is rotating, and whether the buying is broad and aggressive or thin
and passive. Primary user moment: 09:30–10:30 ET.

**Status: pre-code.** The repo currently contains only `docs/`. Read these before doing
anything substantive:

- `docs/INITIAL-PROJECT.md` — the build spec: what the product is; §2 is the math and *is* the product.
- `docs/DEV-PLAN.md` — the operative plan. Phases 0–6, stories, the 19-item
  fix list (Appendix A), the trader Decision Sheet (Appendix B), post-review amendments
  (Appendix C).
- `docs/foundations/baskets.md` — basket/benchmark universe, human-readable, with ⚠ engineering
  annotations. Machine-readable source of truth: `docs/foundations/morning-tape-baskets-v2.json`
  (the engine reads the JSON; the md regenerates from it; PM edits config, never code —
  hot-reload required). Trader-confirmed 2026-08-09. Universe selection criteria and review
  cadence are the trader's, not engineering's — don't ask for or invent them.
- `docs/foundations/data.md` — data source of truth: vendor (Massive Stocks Advanced),
  live channels and fields, historical endpoints, cadence, deferred items.

When the two disagree, **the dev plan wins** — it exists to correct the spec. Appendix A maps
each correction to its home story.

## Scope law — non-negotiable

The prior bot (APEX) failed at the decision layer. This product deletes that layer. Do not
propose, and do not implement:

- Trade-idea generation, buy/sell recommendations, or any signal-to-order automation.
- Buy/sell language anywhere in the UI. Metric names like `NetDelta` are fine internally;
  rendered strings must be statements of measurement.
- News/NLP in v1. (v2 may add a headline *timestamp flag* for ledger grading only.)
- ML or modeling on the collected data. Store everything; model nothing.
- Sub-second infrastructure, colocation, latency racing. Latency budget is print→pixel ≤5s.
- Options premium as a primary driver — it is a secondary brightness overlay, Phase 6, gated
  on the trader confirming daily use of v1.

Header sentences are **correlational, never causal**. Say "MEMORY share collapsing (−1.8σ)
while DEFENSIVES surging (+2.1σ), began 09:41" — not "out of MEMORY into DEFENSIVES." The
de-grossing check runs *before* any rotation sentence.

## Invariants that are easy to break

- **Matched-bucket baselines only.** Every relative-volume and share metric compares against
  the same time-of-day 1-minute bucket in the 20-day profile. Comparing to a daily average
  makes every open look like a spike. This single detail separates the instrument from a
  christmas tree.
- **1-second storage all day**, for the whole session. 1-minute and rolling views are *derived*.
  Never introduce a second storage resolution — a 10:00 seam would distort CUSUM and slopes.
- **Profiles are stored per ticker**, never per basket. Basket baselines are sums over member
  profiles computed at read time, so editing basket membership never corrupts history.
- **Basket membership is effective-date stamped** so historical replays use historical membership.
- **σ guard before every z.** `σ_used = max(σ_measured, floor)`. Applies to FlowShareZ, bond
  velocity z, and any future DeltaRatio z. Literal σ=0 yields a defined null, never an exception.
- **Glow inputs are normalized to a common signed scale before weighting.** Equal weights are
  meaningless otherwise.
- **Cross-basket narrative computes on disjoint sets** for overlapping basket pairs (A6, dev plan
  story 3.7). Shared members (e.g., NVDA in two baskets) would otherwise make a "flow shift"
  between them substantially one ticker arguing with itself. Disjoint series get disjoint-set
  baselines summed from per-ticker profiles; a rump remainder (<3 members or <~30% of avg $vol)
  suppresses that pair's narrative — a false silence beats a false story. Display/tiles/glow/
  breadth keep full membership.
- **Zero-sum holds only inside the tracked universe.** Flow leaving the universe entirely
  redistributes shares misleadingly; that is what the de-grossing precedence rule mitigates.
  Note this caveat wherever FlowShare is explained.

## Method

Spec-driven development at **story granularity**. Each story gets a half-page mini-spec (what,
acceptance criteria, decisions consumed) written immediately before implementing it — never
the whole project at once. A story closes only when its acceptance criteria pass **on replayed
recorded data**, not on live market data.

**Replay is the development environment.** The market is open 09:30–16:00 ET weekdays only;
deterministic replay of captured sessions is what makes all other work possible. Phase 1
(capture + replay harness) gates everything after it. Capture is unconditional from the first
live run — recorded sessions are the test corpus, and the §6 reference days (SNDK guide-night
open, VIAV→optics morning, a known de-grossing day) can only be recorded when they occur.

Build the crude developer view (story 3.0) before any math. Every metric becomes visible the
day it is written; that is how intuition for the numbers gets acquired.

## Decisions belong to the trader

D1–D12 in dev-plan Appendix B are the trader's, not ours. Defaults are listed there and apply
until overridden — proceed on the default and flag it rather than blocking, except D1 (the
universe and basket map), which genuinely blocks Phase 0. The nightly ledger review (story 6.5)
is where defaults get tuned; it is not a code change every time.

## Honest claims

The product's real differentiator is the **cross-basket share view with time-matched baselines**.
Do not write copy or comments claiming "delta no retail tool has" (fix F1). Related honesty
requirements already logged: Lee-Ready degrades at the open (F2), the dark-share dot is a
*maybe* because retail flow is also heavily off-exchange (F3), and breadth is correlation-blind
because ETF arbitrage moves members mechanically (F18).

## Timeline

4–6 weeks solo to the §6 acceptance bar, assuming partial reuse of APEX ingestion plumbing.
The spec's 2-week figure reaches a demo, not acceptance (F10). Phases 0–1 are the schedule risk.
