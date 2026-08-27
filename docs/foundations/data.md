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

## Options tape — Unusual Whales (Phase 7, added 2026-08-18)

**Unusual Whales** (unusualwhales.com) — existing license, WebSocket tier confirmed live
2026-08-18. API key: `UNUSUAL_WHALES_API_KEY` in `.env`. REST auth: `Authorization:
Bearer`. Spec: `docs/foundations/openapi.yaml` (official OpenAPI dump); observed facts
below are from the 7.0 probes (`discovery-scripts/verify-uw/`, findings 2026-08-18,
full-tape day 2026-08-17).

### Live — websocket `wss://api.unusualwhales.com/socket?token=<key>`

Phoenix-style protocol. Join: `{"channel":"option_trades:<SYM>","msg_type":"join"}`.
Every frame is a 2-element JSON array `[channel, payload]`; join ack payload is
`{"response":{},"status":"ok"}` (observed verbatim). Per-print payload fields (spec,
to be wire-confirmed on a market-hours run): `id` (uuid, dedupe key),
`underlying_symbol`, `executed_at` (epoch **ms**), `option_symbol` (OSI), `expiry`,
`option_type`, `strike`/`price`/`premium`/`underlying_price`/`nbbo_bid`/`nbbo_ask`
(decimal **strings**), `size`/`open_interest`/`volume` (ints), greeks, `exchange`,
`trade_code` (OPRA condition), `report_flags`, `tags[]` (side:
`ask_side`/`bid_side`/`mid_side`/`no_side`, plus vendor interpretation tags —
`bullish`/`bearish` are decoded but never rendered or used).

Observed 2026-08-18 (off-hours):
- **Join cap: none at universe scale** — 189/189 `option_trades:` joins acked ok on one
  connection.
- **Heartbeat: not required** — a fully idle connection (zero client writes after joins)
  survived >3 minutes. Caveat: gorilla/websocket auto-answers protocol-level pings while
  a reader loop runs; the feeder must always be reading (it is).
- Connect time ~0.5s.

Market-hours findings (recorded 2026-08-20, first live session):
- **Wire payload confirmed** against a 2,000-frame live sample: every documented field
  present, zero undocumented fields. `executed_at` = epoch ms int; live `tags` is a
  JSON array (`["bid_side","bullish","etf"]`) — the full-tape CSV's Postgres array
  literal remains a normalizer difference.
- **Skew** recv−`executed_at`: p50 72ms, p95 112ms, p99 132ms, max 303ms — negligible
  vs the ≤5s budget.
- **Message rate** (from the 08-17 full tape, confirmed live): open hour avg ~500
  prints/s, p99 second 1,059/s, peak 5,649/s at 09:30:01; ~130/s midday; 6.4–6.5M
  universe prints/day. First live session: 4.31M frames by 14:00 ET, on track.

### Historical — REST `https://api.unusualwhales.com`

| Endpoint | Use | Notes (observed) |
|---|---|---|
| `/api/option-trades/full-tape/{date}` | 20-day baseline backfill + replay corpus | One zip per day, single CSV inside (`<date>-option_trades.csv`). 2026-08-17: 1.4 GB zipped / 4.1 GB raw, 10.54M rows, ~30s download, no rate-limit headers returned. Spans 09:30:00–16:59:58 ET (post-16:00 prints exist — index/ETF options trade to 16:15 + late reports; metrics filter to the regular session, capture keeps all). **Published same evening** — the 2026-08-19 tape was complete and downloadable by 18:55 ET that day, so the nightly roll can include the just-ended session and a failed capture day is recoverable same-night. |
| `/api/stock/{ticker}/net-prem-ticks` | independent per-minute cross-check (7.8) | vendor-defined ask−bid netting; never a baseline source |
| `/api/option-trades` | gap-fill after WS disconnects (backlogged) | latest day only; `newer_than`/`older_than` cursors; documented 502 |

**Full-tape ↔ WS field parity (the 7.0 keystone — answered, no contingency needed):**
side `tags` **are present** in the full-tape CSV, so backfilled baselines sign prints by
the same vendor-tags rule as live. Universe rows on 2026-08-17: 6.41M of 10.54M total
(60.8% — the universe is heavily-optioned); side split ask 2.85M / bid 3.00M / mid 566k /
no_side 343. Naming differences vs WS: `upstream_condition_detail` (= WS `trade_code`),
`option_chain_id` (= WS `option_symbol`); full-tape extras: `alert_score`,
`aggregated_trade_id`, `canceled`, `trade_id`, `market_center_locate`. Format
differences the normalizer must own: `executed_at` is a Postgres-style timestamp
(`2026-08-17 13:30:00.00714+00`, space separator, not RFC3339); `tags`/`report_flags`
are Postgres array literals (`{ask_side,bullish}`, `{}`).

**Premium multiplier:** every 2026-08-17 universe row is consistent with
`premium = size × price × 100` — no NANOS/XSP-class contracts in the universe. The
vendor `premium` field is still taken verbatim, never recomputed.

**Live-vs-tape parity (7.5, measured 2026-08-24 on 08-20):** identical outside the
opening minute (780,736/780,894 rows byte-identical; gross premium within 0.04%; the
tape scrubs canceled prints the wire delivered — 5 that day). The live capture lost
all of its 0.18% deficit in the 09:30 minute (18–23% of opening-minute prints on
08-20/21/24) to pre-open reconnect cycling — fixed in the feeder's deadline policy on
2026-08-24, **verified 2026-08-25**: opening minute 55,532 = 55,532 prints live vs tape;
whole-day deficit 157 prints (0.003%), all in one vendor-side `close 1006` reset at
14:07:22 ET (≈1.3 s; 08-21 had one at 13:07:39 — roughly every other day). Full-tape
zips remain the baseline source for every day (they scrub cancels and cover reconnect
gaps); live capture serves the real-time path.

**Open interest caveat:** per-print `open_interest` is prior-close (OCC nightly); the
w_size weight (D16) reads it as such. Intraday OI does not exist anywhere.

## Cadence — three clocks

1. **Ingestion**: per message, continuous (trades + quotes websocket).
2. **Aggregation**: 1-second buckets stored all session; coarser views derived. Baselines
   are per-ticker per-1-minute bucket, 20-day, time-of-day matched.
3. **Display**: 5-second refresh; print→pixel budget ≤5s.

There is no separate price feed: current price = last trade; opening price = the opening
auction cross print (identified via condition codes).

## Storage & retention (mini-spec 8.1, 2026-08-25)

S3 is the archive; the Studio's `data/` is a cache. One job, `bin/nightly`
(`com.buddyflow.nightly`, 22:00 CT weekdays), owns every roll: options
full-tape → `buckets-options` → `profiles-options`; equity `buckets` →
`profiles`; capture compression; upload; local retention; disk report.
Re-run a night by hand: `bin/nightly -date YYYY-MM-DD` (`-dry-run` logs the
plan and does nothing; `-skip step,…` for partial runs). Log: `data/nightly.log`.

**S3 layout** — `s3://<bucket>/<class>/<name>`, name = path relative to the
class directory under `data/`. Bucket versioning on.

| Class | Local | Archived name | Storage class | Local retention |
|---|---|---|---|---|
| `capture` | `data/capture/<date>/` | `<date>/stream.jsonl.gz`, `<date>/manifest.json` | STANDARD_IA | 5 most recent days |
| `capture-options` | `data/capture-options/<date>/` | same | STANDARD_IA | 3 days |
| `full-tape` | `data/full-tape/<date>.zip` | `<date>.zip` | STANDARD_IA | 0 (deleted once uploaded) |
| `buckets` | `data/buckets/` | `<date>.csv`, `<date>.trades-only.csv`, `<date>.partial.csv` | STANDARD | 25 days |
| `buckets-options` | `data/buckets-options/` | `<date>.csv` (`.partial.csv`) | STANDARD | 25 days |
| `profiles` | `data/profiles/` | `<date>/…` (whole dir under the build's through-date) | STANDARD | overwritten nightly, no retention |
| `profiles-options` | `data/profiles-options/` | `<date>/…` | STANDARD | same |

Retention rules, all local-only (S3 keeps everything): a file is deleted only
when it exists in S3 with the same size; a file dated today is never touched
(a live agent may hold it open); a raw `stream.jsonl` is never deleted by
retention (compression owns it); dates listed in
`docs/foundations/reference-days.json` (`{"dates": [...]}`, owner-maintained —
the §6 reference days) are never deleted locally. `data/live.log` is truncated
at each `com.buddyflow.live` start and by nightly beyond 200 MB (frames are
reproducible from replay); same for `live-options.log`.

Vendor flat files (`data/flat-files/`, the 2.1 bootstrap inputs) are **not
archived** (owner, 2026-08-25): the vendor keeps them and `tools/download-trades.py`
re-fetches on demand; the derived `*.trades-only.csv` bucket files are archived like
any other bucket day.

**Capture compression.** Writers stay uncompressed (a torn gzip would make
follow/resume harder); nightly gzips every closed capture (manifest present,
writer's flock free) at BestCompression (~10% of raw) and unlinks the raw only
after two proofs: the gunzipped stream hashes to the raw's sha256, and a
replay of the `.gz` through `cmd/replay` / `cmd/replay-options` yields a
bucket file byte-identical to the reference — for equity the file `cmd/live`
wrote, otherwise a replay of the raw. Any mismatch keeps the raw and fails
the step.

**Schemas and stamps of the archived classes** — the 8.3 seam: builders read
`buckets*/<date>.csv` and write `profiles*/<date>/`; the view reads profiles.
Capture line format: 1.2 (`<recv_ns> <raw frame>`) + `manifest.json`. Equity
bucket CSV: 1.4 (`second,symbol,…`; coverage stated by the filename suffix).
Options bucket CSV: 7.4, header carries the weights stamp `<version>@<hash>`;
readers refuse a mismatch. Equity profiles: 2.1/2.2/3.1 (`<SYM>.csv`,
`shares/`, `_floors.csv`, inputs line). Options profiles: 7.6 (ticker,
`baskets/`, `_floors.csv`, stamp + inputs). Full-tape zip: vendor CSV, 7.5.

**Credentials.** The archive reads only `ARCHIVE_S3_BUCKET`,
`ARCHIVE_S3_REGION`, `ARCHIVE_AWS_ACCESS_KEY_ID`, `ARCHIVE_AWS_SECRET_ACCESS_KEY`
(env, then `.env`). The `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY` pair in
`.env` belongs to the **vendor's** flat-file endpoint (`files.massive.com`,
`tools/download*.py`) and is never used by the archive; neither pair is logged.

**Rebuilding from the archive.** `bin/profiles -archive` /
`bin/profiles-options -archive` fetch the last N bucket days into the local
buckets dir before discovery, so an empty Studio or a laptop builds from S3
alone. No S3 code lives in `internal/profile`, `optprofile`, `flowshare`,
`conviction`.

## Deferred / backlogged (not procured)

- **Failover feed** — dual-feed requirement deferred; intended second feed is Databento
  `EQUS.SIP` (consolidated SIP, vendor ETA late Q3/Q4 2026). Revisit at story 6.1.
- **Bond tape (ZN/ZB futures)** — backlogged; v1 header ships without it (TLT in universe
  is the free interim duration proxy).
- **EOD ETF creation/redemption scrape** (etf.com / Farside) — Phase 4 nightly grading.
- **Market calendar** (holidays, half days) — needed by Phase 2 baselines; static file.
  Extended 2026-08-12 (D4): also carries macro event dates (FOMC, CPI, PPI, NFP, OpEx
  class) so baseline days can be flagged and the display can annotate "today is CPI."
