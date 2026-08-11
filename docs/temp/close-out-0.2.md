# Task: Close out story 0.2 (data vendor setup) — execute Tue 2026-08-11 during market hours

You are executing the final verification for DEV-PLAN story 0.2. Read `CLAUDE.md` and skim
`docs/DEV-PLAN.md` Phase 0 first. Everything below has been decided and discussed — your job
is to run the checks, record results, and close the story if green. Do not redesign anything.

## Context you need

- **Vendor decision (made):** Massive (massive.com, the former Polygon.io) Stocks Advanced,
  real-time SIP consolidated feed. This supersedes the Databento/Polygon language in the
  DEV-PLAN 0.2 story text — Databento has no consolidated equities dataset until their
  EQUS.SIP ships (late Q3/Q4 2026; see Deferred items below).
- **Data source of truth:** `docs/foundations/data.md` — vendor, channels, fields,
  endpoints, cadence, deferred items. If anything below seems ambiguous, that file wins.
- **API key:** `MASSIVE_API_KEY` in `.env` at repo root (gitignored). The scripts load it
  automatically via godotenv.
- **Scripts:** `discovery-scripts/verify-universe/` (REST: symbols, conditions, historical)
  and `discovery-scripts/verify-feed/` (websocket go/no-go: connectivity, entitlement,
  latency). Both run with `go run .` from their own directory.
- **Universe config:** `docs/foundations/morning-tape-baskets-v2.json` — amended 2026-08-10
  (PSTG→P, CFLT removed, trader-confirmed). 165 members + 27 non-futures benchmarks
  = 190 equity symbols to verify (ZN/ZB futures are backlogged, excluded by the script).
- **Known open item:** BESIY is an OTC-traded ADR (`market=otc` in reference data). Whether
  it streams on the real-time websocket is unknown — today's run settles it empirically.

## Steps (times are ET)

1. **Any time before 09:30 — universe re-verification:**
   ```
   cd discovery-scripts/verify-universe && go run .
   ```
   Expected: **190/190 resolved**, BESIY still FLAGGED (market=otc) — that flag alone is
   fine; conditions dump rewritten; historical probe OK. Any MISSING symbol = stop and
   report to the user; do not edit the basket config (membership is trader-owned).

2. **09:31 — main go/no-go (after the open, so the tape is live):**
   ```
   cd discovery-scripts/verify-feed && go run . -ticker NVDA -duration 5m
   ```
   Green means: status messages show successful auth + subscription acks for T.NVDA and
   Q.NVDA; nonzero trades AND quotes; latency p95 well under 5000ms (expect < ~500ms —
   if p95 exceeds ~2000ms, note it as a concern but not a hard fail; if the connection is
   rejected or the feed host redirects to delayed, that is a hard fail = entitlement problem).

3. **~09:40 — BESIY probe:**
   ```
   go run . -ticker BESIY -duration 2m
   ```
   Record trades/quotes counts. BESIY is thinly traded — a few prints or even quote-only
   is "streams"; total silence on BOTH channels for 2 minutes during the open = "does not
   stream." Either way, record the observation; the keep/drop/replace decision is the
   trader's, not yours.

4. **Record results** in `docs/temp/0.2-results.md`:
   ```markdown
   # 0.2 verification results — 2026-08-11
   ## verify-universe: <PASS/FAIL> (190/190; notes)
   ## verify-feed NVDA: <PASS/FAIL> (trades=…, quotes=…, latency p50/p95/p99=…)
   ## BESIY probe: streams / does not stream (trades=…, quotes=…)
   ## Verdict: 0.2 <closed / blocked on: …>
   ```
   Paste the relevant script output under each heading.

5. **If everything above is green, close the story in `docs/DEV-PLAN.md`:** strike through
   story 0.2's heading and delivered bullets in the same style as story 0.1 (`~~…~~` +
   "✅ resolved <date>" + a *Status* line), rewriting its content to reflect reality:
   vendor = Massive Stocks Advanced (SIP consolidated: trades, NBBO quotes, condition
   codes, TRF venue attribution, LULD + imbalance channels, 20+yr historical); entitlements
   verified by scripts on 2026-08-11. Keep unstruck a "Deferred" bullet listing:
   - **Failover feed** — spec's dual-feed requirement deliberately deferred: Databento
     EQUS.SIP (late Q3/Q4 2026) is the intended second feed; revisit at story 6.1.
   - **Bond tape (ZN/ZB)** — backlogged (see 5.2 note).
   - **BESIY** — per today's probe result, pending trader decision.

6. **Commit** all changes (results doc, DEV-PLAN edit) on the current branch with a message
   like `Close out 0.2: Massive feed verified`. Do not push unless asked.

## Guardrails

- Do not purchase, subscribe, or change any account settings.
- Do not edit `docs/foundations/morning-tape-baskets-v2.json` or `baskets.md` — basket
  membership is the trader's (D1); flag findings instead.
- If a script fails to build or run, fix the script, not the requirements.
- If results are mixed (e.g. feed green but latency alarming), record everything, leave 0.2
  open, and summarize what's blocking for the user rather than force-closing.
