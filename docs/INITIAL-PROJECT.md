**MORNING TAPE**

*Live Money-Flow Map — Advanced Build Specification v2.0 (supersedes FLOWSEEKER v1 alert-engine framing)*

*Prepared for: CTO  |  Product class: pure observation instrument — NO trade ideas, NO signal-to-trade automation  |  Internal*

# **1. Product Definition — What This Is and Is Not**

A real-time market weather map. One screen that shows, from 09:30:00 onward, where dollar volume is concentrating, whether it is shifting between sectors/baskets, and whether the buying/selling is broad and aggressive or thin and passive. The PM reads it and decides. The machine observes; it never recommends.

- Prior options-flow/news-to-trade-idea bot failed at the decision layer. This product deletes the decision layer entirely — different failure mode, different product class.
- Primary user moment: 09:30–10:30 ET. Secondary: continuous intraday + 15:30–16:00 close window.

# **2. The Core Mathematics (this section IS the product)**

## **2.1 Dollar-Volume Flow Share — the money-flow metric**

Volume alone is noise; SHARE of the tape's total dollar volume is flow. For each basket B in each time bucket t:

`FlowShare(B,t) = Σ dollar_volume(members of B, t) / Σ dollar_volume(entire tracked universe, t)`

`FlowShareZ(B,t) = ( FlowShare(B,t) − μ20d(B, matched bucket t) ) / σ20d(B, matched bucket t)`

Because shares are zero-sum across the tape, a shift is definitionally visible: one basket's share z rising while another's falls IS money rotating, measured — not inferred. This directly answers 'is volume shifting toward a different sector' with a number.

## **2.2 Time-of-Day Matched Baselines (the accuracy layer)**

Intraday volume is U-shaped; comparing 9:35 volume to a daily average makes every open look like a spike. Nightly job builds per-ticker 20-day rolling volume profiles in 1-minute buckets. ALL relative-volume and share metrics compare against the MATCHED bucket only. This one detail separates a real instrument from a christmas tree.

## **2.3 Signed Volume — net buying vs net selling (the alpha layer)**

Classify every trade as buyer- or seller-initiated (aggressor side) using quote-relative classification (Lee-Ready style: trade at/above ask = buy, at/below bid = sell, midpoint = tick rule). Then:

`NetDelta(B,t) = Σ buy_dollar_volume − Σ sell_dollar_volume   (per basket, per bucket)`

`DeltaRatio(B,t) = NetDelta / TotalDollarVolume   ∈ [−1, +1]`

This upgrades the map from 'volume is here' to 'money is BUYING here' — the closest legally available approximation of true directional money flow. Requires trades+quotes feed (see §4). This is the layer no retail dashboard has.

## **2.4 Flow-Shift Detector — the rotation math**

`ShiftSlope(B) = d/dt FlowShareZ(B) over rolling 5-min and 15-min windows`

Run change-point detection (CUSUM) on each basket's share series. When one basket's slope breaks positive and another's breaks negative beyond thresholds in the same window, the header renders a factual sentence: 'FLOW SHIFT: out of MEMORY (−1.8σ) into DEFENSIVES (+2.1σ), began 09:41.' A statement of measurement, not a recommendation.

## **2.5 Breadth & Participation**

`Breadth(B,t) = fraction of members positive vs SPY on the session   |   e.g. 8/9`

Simultaneity is the institutional fingerprint: one stock up is news; nine up on volume is money. Breadth gets equal weight with FlowShareZ in tile intensity.

## **2.6 VWAP Posture & Off-Exchange Share (secondary institutional tells)**

- VWAP posture: % of basket members trading above session VWAP — intraday accumulation proxy.
- TRF/off-exchange share: % of basket volume printing off-exchange (FINRA TRF venue codes in the consolidated feed). Rising dark share + rising price + positive delta = institutional accumulation signature; renders as a small dot on the tile.

## **2.7 Tile Intensity — the composite**

`Glow(B,t) = w1·FlowShareZ + w2·Breadth + w3·DeltaRatio + w4·RelPerf(B vs SPY)   (weights configurable; start equal)`

# **3. The Screen (single view, zero navigation)**

- Grid of basket tiles, auto-sorted: strongest inflow top-left → heaviest outflow bottom-right.
- Each tile: color+intensity = Glow score | volume ring thickness = matched-bucket RVOL | breadth fraction (8/9) | 15-min sparkline of FlowShareZ | small dot = dark-share flag.
- Header line 1 — regime sentence (mechanical): DE-GROSSING / ROTATION (from X into Y) / RE-GROSSING / CHOP, computed from breadth across baskets + dispersion + cash-proxy (BIL/SGOV) behavior.
- Header line 2 — bond tape: 10Y/30Y level + velocity z. Equity flow reads differently when yields are moving; the header keeps the referee visible.
- Open replay scrubber: drag back through the morning's evolution in 1-min steps (stored buckets make this free).

# **4. Data Stack — Fastest & Most Accurate, Honestly Ranked**


| **Feed** | **What it provides** | **Why / Priority** | **Est. cost** |
| --- | --- | --- | --- |
| **Databento US equities consolidated — trades + NBBO quotes (websocket)** | **Every print with venue code (incl. TRF) + quotes for aggressor classification** | **THE spine. Enables §2.1–2.6 fully. Priority 1** | **~$200–400/mo tier** |
| **Opening auction prints (in consolidated feed)** | **9:30 opening cross size/price per member — the day's first institutional footprint** | **Free within feed; parse cross flags. Priority 1** | **included** |
| **Nasdaq TotalView + NYSE imbalance feeds (optional v2)** | **PRE-open order imbalances from ~9:28 and MOC imbalances from 15:50** | **The only data that is literally earlier than the open. Add after v1 proves daily use** | **~$100–300/mo** |
| **Polygon.io Advanced (backup feed)** | **Redundant real-time trades/aggregates** | **Bot must not go blind in the volatile hour it exists for. Priority 2** | **~$200/mo** |
| **Unusual Whales API** | **Options net premium per member → basket overlay** | **Secondary brightness input only — NOT the driver (lesson from prior bot). Priority 3** | **already licensed / small delta** |
| **ETF flow scrape (etf.com/Farside)** | **EOD creation/redemption dollars** | **Nightly grading of the day's map. Free** | **free** |


Latency budget (end-to-end, print → pixel): ≤ 2–5 seconds. Bucket resolution: 1-second bars 09:30–10:00, 1-minute thereafter. Anything faster is HFT cosplay: our horizon is minutes-to-days; accuracy and uptime beat microseconds. Two independent feeds with automatic failover is the real 'speed' purchase.

# **5. Build Plan (≈2 weeks; reuses ~60–80% of prior bot's plumbing)**


| **Phase** | **Deliverable** | **Done when** |
| --- | --- | --- |
| **Days 1–3** | **Ingest: dual-feed websockets, trade classification, TRF tagging, 1s/1m bucket store; nightly 20-day profile builder** | **Replay of a recorded session reproduces identical metrics twice** |
| **Days 4–7** | **Math core: FlowShare, matched-bucket z, NetDelta, breadth, VWAP posture, shift detector (CUSUM), regime + bond header** | **Unit tests pass on recorded rotation days (see §6)** |
| **Days 8–11** | **The screen: tile grid, glow composite, rings, sparklines, sort, replay scrubber; 5s refresh** | **PM reads the 9:33 tape state in one glance, no clicks** |
| **Days 12–14** | **Hardening: failover drill, latency audit, nightly EOD grading job writing the day's map + regime call to the ledger** | **Survives a full live week incl. one macro morning** |


# **6. Acceptance Tests (non-negotiable)**

- Replay test A: the SNDK guide night + following open — memory basket must show share-z collapse and negative delta within the first 15 minutes of the replayed session.
- Replay test B: the VIAV→optics read-through morning — optics tile must dim on breadth before AAOI's own print date.
- Replay test C: a known de-grossing day must classify DE-GROSSING (not rotation) in the header — the map must say 'money leaving, nothing catching it' when that is true.
- Accuracy test: matched-bucket RVOL at 9:35 on an average day ≈ 1.0 across baskets (if every open reads \>2x, baselines are wrong).
- Latency test: print-to-pixel ≤5s sustained through a 9:30 burst; failover completes ≤10s with no metric gap.

# **7. Non-Goals (scope law — what killed the last bot stays dead)**

- NO trade-idea generation, NO buy/sell language anywhere in the UI, NO signal-to-order automation.
- NO news-NLP layer in v1. A single optional headline timestamp flag per basket (did a headline coincide with the shift) may come in v2 for ledger grading only.
- NO sub-second infrastructure, colocation, or latency racing.
- Options premium is an overlay, never the primary driver.

# **8. Sign-off Summary**

One screen. Dollar-volume share with time-matched baselines, signed by aggressor, weighted by breadth, sorted by intensity, headlined by one mechanical regime sentence and the bond tape. Dual-feed, ≤5-second latency, replay-tested against the exact mornings we lived this month. The machine watches sixty tickers a second and draws the weather; the PM keeps every decision. Ship in two weeks; graded nightly by the ledger from day one.
