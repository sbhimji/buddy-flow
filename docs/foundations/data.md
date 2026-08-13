# Data — source of truth

What we pull, from where, at what cadence. Decided 2026-08-10. Working notes and the
per-metric derivation live in DEV-PLAN Phase 3 stories; this file is just the data.

## Vendor

**Massive** (massive.com, formerly Polygon.io) — **Stocks Advanced** plan ($199/mo,
real-time SIP consolidated, all US stock tickers, 20+ years history).
API key: `MASSIVE_API_KEY` in `.env` at repo root (gitignored). Auth: `Authorization: Bearer`.
Go client: local copy at `../client-go` (`github.com/massive-com/client-go/v3`).

## Universe

`docs/foundations/morning-tape-baskets-v2.json` — the machine-readable basket config
(trader-owned). 164 basket members + benchmarks, minus the backlogged ZN/ZB futures
= **189 equity symbols** subscribed/queried. (BESIY dropped 2026-08-11 by trader
decision: OTC, does not stream on the real-time websocket, no NBBO at the vendor.)

## Live — websocket `wss://socket.massive.com/stocks`

| Channel | Topic | Fields we rely on |
|---|---|---|
| Trades | `T.<sym>` | `sym`, `p` price, `s` size, `x` exchange ID, `z` tape, `c[]` condition codes, `t` SIP ts (ms), `pt` participant ts (ms), `q` sequence, `trfi` TRF ID, `trft` TRF receipt ts |
| Quotes (NBBO) | `Q.<sym>` | `sym`, `bp/bs/bx` bid, `ap/as/ax` ask, `c` quote condition, `i[]` indicators, `t` SIP ts, `q` sequence, `z` tape |
| Halts | `LULD.<sym>` | limit-up/limit-down state per ticker |
| Auction imbalances | `NOI.<sym>` | net order imbalance events (not consumed in v1; available) |

Notes: `pt` is in the docs but absent from the Go client's struct — consume raw JSON or
patch the model; confirm on the wire. TRF prints are identified by `trfi` presence /
FINRA exchange ID. All timestamps are Unix ms.

## Historical — REST `https://api.massive.com`

| Endpoint | Use | Cadence |
|---|---|---|
| `/v2/aggs/ticker/{sym}/range/1/minute/{from}/{to}` | 1-minute bars: seed + roll the 20-day baseline profiles. **Includes extended hours — filter to 09:30–16:00 ET.** | one-time backfill, then nightly |
| `/v3/reference/tickers` | symbol existence/active/market checks | nightly sanity |
| `/v3/reference/conditions?asset_class=stocks` | condition-code table with `update_rules` | refresh on demand; snapshot committed at `docs/foundations/massive-conditions.json` (94 conditions: 40 sale, 33 quote, misc.) |
| Flat files (tick-level trades + quotes) | full-day condition/TRF analysis for the 0.3 print-inclusion policy; historical reference days for the §6 replay corpus | on demand |

## Cadence — three clocks

1. **Ingestion**: per message, continuous (trades + quotes websocket).
2. **Aggregation**: 1-second buckets stored all session; coarser views derived. Baselines
   are per-ticker per-1-minute bucket, 20-day, time-of-day matched.
3. **Display**: 5-second refresh; print→pixel budget ≤5s.

There is no separate price feed: current price = last trade; opening price = the opening
auction cross print (identified via condition codes).

## Deferred / backlogged (not procured)

- **Failover feed** — dual-feed requirement deferred; intended second feed is Databento
  `EQUS.SIP` (consolidated SIP, vendor ETA late Q3/Q4 2026). Revisit at story 6.1.
- **Bond tape (ZN/ZB futures)** — backlogged; v1 header ships without it (TLT in universe
  is the free interim duration proxy).
- **EOD ETF creation/redemption scrape** (etf.com / Farside) — Phase 4 nightly grading.
- **Market calendar** (holidays, half days) — needed by Phase 2 baselines; static file.
  Extended 2026-08-12 (D4): also carries macro event dates (FOMC, CPI, PPI, NFP, OpEx
  class) so baseline days can be flagged and the display can annotate "today is CPI."
