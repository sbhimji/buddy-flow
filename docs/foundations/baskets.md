# MORNING TAPE — Baskets & Benchmarks v2.0 (EXPANDED)

> Full sector coverage + high-volume names per PM directive. Companion to Build Spec v2.0.
> Machine-readable source of truth: `morning-tape-baskets-v2.json` (identical contents; engine reads the JSON).
>
> **Engineering annotations** from spec review are marked ⚠ — see docs/DEV-PLAN.md fix list.

## 1. Benchmark / ETF Universe

| Group | Tickers | Role |
| --- | --- | --- |
| Broad | SPY, QQQ, IWM, RSP | Relative-performance denominators; regime breadth |
| Semis | SMH, SOXX | Benchmark for all semi/photonics/memory baskets |
| Sector SPDRs | XLK XLF XLE XLV XLP XLU XLI XLB XLY XLC XLRE | Classic rotation grid; each rendered as its own tile |
| Thematic | URA, NLR, ITA, IGV, HACK | Benchmarks for nuclear, defense, software baskets |
| Cash proxy | BIL, SGOV | CRITICAL: inflows here = de-grossing detector input |
| Duration | TLT | Equity-visible bond behavior |
| Bond gate | ZN, ZB futures (Databento) | 10Y/30Y yield VELOCITY z-score overlay ⚠ separate Databento dataset (CME futures) — not covered by the equities feed in Build Spec §4; add to data stack & budget |

## 2. Custom Baskets — 22 Baskets, Full Membership

Equal-weight v1. Each basket's ratio computes against its ASSIGNED benchmark below; FlowShare computes against the entire tracked universe. Duplicated tickers across baskets are intentional — which crowd a name ignites with is itself information.

⚠ Multi-basket membership means basket flow shares do NOT sum to 100%; rotation narratives between overlapping baskets must flag shared members (dev plan fix #5/#14).

| Basket | Tickers (equal-weight members) | Bench | Operational note |
| --- | --- | --- | --- |
| proof_tier_ai_megacap | MSFT, META, AMZN, GOOGL, AAPL, NVDA, AVGO, ORCL, IBM, CRM, NOW | QQQ | Mega-cap AI with current earnings proof |
| promise_tier_ai | PLTR, CRDO, ALAB, APP, SOUN, BBAI, AI, PATH, TEM, RBRK | QQQ | High-multiple / story-priced AI incl. high-volume retail names; most rate-sensitive basket |
| semis_compute | NVDA, AMD, ARM, INTC, TSM, QCOM | SMH | Core compute silicon. NVDA double-counted with proof-tier intentionally |
| networking_connectivity | ANET, AVGO, MRVL, ALAB, CRDO, CSCO, CIEN | SMH | Switching, custom ASIC, DSPs, retimers, optical systems |
| semis_analog_power_auto | TXN, ADI, NXPI, MCHP, ON, STM, SWKS, QRVO | SOXX | Analog/auto/industrial semis — the cyclical recovery gauge |
| memory_storage | MU, SNDK, WDC, STX, SIMO, PENG, NTAP, P | SMH | DRAM/NAND/HDD + controllers + modules + storage systems. SK Hynix untrackable on US feed (optional EWY overlay) |
| photonics_optics | AAOI, COHR, LITE, VIAV, FN, MTSI, SMTC, CIEN, GLW, POET | SMH | SHARED TAIL: China InP export-permit headlines move whole basket — treat basket-wide gaps as ONE event, suppress per-member divergence alerts 30min |
| semicap_frontend | AMAT, LRCX, KLAC, ASML, TER, ENTG, MKSI, AEIS, ACMR, UCTT | SMH | Wafer fab equipment + subsystems |
| packaging_test_substrates | CAMT, ONTO, KLIC, FORM, AXTI | SMH | HBM/advanced packaging inspection, bonding, probe + compound-semi substrates (AXTI = InP/GaAs, China-located fabs — dual-flag) |
| defense_primes | LMT, RTX, NOC, GD, LHX, HII, LDOS, BAH | ITA | Large-cap defense |
| defense_tech_growth | KTOS, AVAV, PLTR, RCAT, ONDS | ITA | Drones/software/counter-UAS high-beta tier. PLTR triple-listed intentionally |
| space | RKLB, LUNR, ASTS, RDW, PL, SPCX | ITA | Launch/satellites. SPCX added — SpaceX IPO'd June 12, 2026, Nasdaq: SPCX (verified) |
| critical_minerals | MP, USAR, UUUU, TMC, ALB, LAC, CLF | XLB | Rare earths, uranium-adjacent, lithium, electrical steel. Moves on DC headlines. UUUU double-counted with nuclear |
| neoclouds_dc_builders | IREN, APLD, NBIS, CRWV, CIFR, WULF, CORZ, RIOT, CLSK, HUT, GLXY | QQQ | GPU landlords + converted miners. Most credit-sensitive basket — read against bond gate; ignition during yield spikes = squeeze artifact |
| dc_reits | DLR, EQIX | XLRE | 2-member basket: only 2/2 breadth = ignition |
| quantum | IONQ, RGTI, QBTS, QUBT, ARQQ | QQQ | Froth/risk-appetite gauge incl. high-volume retail names |
| power_equipment | VRT, ETN, HUBB, POWL, ENS, VICR, NVT, AMSC, SPXC, GEV, CAT, EMR | XLI | Transformers, switchgear, turbines, power conversion — the invoice-now layer |
| fuel_cells_storage | BE, PLUG, FCEL, BLDP, FLNC, EOSE | XLI | On-site generation + grid batteries. High-beta retail tier of the power trade |
| nuclear_fuel_cycle | CEG, VST, TLN, NRG, OKLO, SMR, NNE, LEU, CCJ, UEC, DNN, UUUU, BWXT, CW, FLR | URA | Operators + SMR developers + enrichment/miners + components/engineering. Split from power_equipment: different rate-sensitivity and revenue horizon |
| epc_labor | IESC, MYRG, EME, FIX, PWR, DY, STRL, PRIM | XLI | Electrical/data-center contractors — the labor bottleneck; uncrowded layer. Early ignition = institutional discovery signal |
| consumption_software | DDOG, MDB, SNOW, NET, ESTC, TWLO | IGV | Usage-billed software — lagged derivative of cloud consumption |
| robotics_av | TSLA, ISRG, SYM, SERV, PONY | QQQ | The rotation destination the crowd is entering per PM read — track for ignition confirmation |

## 3. Engine Rules Bound to This Config

- Ignition = triple gate: ratio z > ±1.5 AND matched-bucket RVOL > 1.3x AND options-premium confirmation, gated by regime classifier. ⚠ CONFLICT with Build Spec §7 ("options premium is an overlay, never the primary driver") — requires trader reconciliation before implementation; "ignition" must be defined as a display-emphasis state, not an alert/signal.
- Breadth as fraction (e.g. 8/11). For 2–3 member baskets (dc_reits, quantum small tier, critical pairs): only FULL participation = ignition.
- photonics_optics SHARED TAIL: China InP headlines gap the whole basket = ONE event; suppress per-member divergence alerts 30 min.
- neoclouds_dc_builders + promise_tier_ai render adjacent to bond-gate state; ignition during yield spikes = squeeze artifact, label it.
- space basket: SPCX included (IPO June 12, 2026). ⚠ New listing — <20 trading days of history until mid-July 2026 baselines mature; excluded from z-scores until ≥10 profiled days (dev plan fix #11).
- ADR wide-spread filter: STM, ASML, TSM. China-located flag: AXTI. Min price $3.00. Hot-reload of JSON required — PM edits config, never code.
- Opening window: 1-second bars 09:30:00–10:00:00 ET, 1-minute after. Baselines: 20-day rolling, time-of-day matched, per ticker.
- ⚠ "Ratio z vs assigned benchmark" is not defined in Build Spec §2 (which defines RelPerf vs SPY only). Assigned-benchmark version supersedes for basket tiles; SPY-relative retained for regime header. Reconcile in dev plan story 3.6.

## 4. Maintenance

**2026-08-10 (trader-confirmed):** PSTG → P (Pure Storage renamed Everpure, NYSE: P, March 2026); CFLT removed from consumption_software (IBM acquisition closed 2026-03-17, delisted — no replacement named).

**2026-08-11 (trader decision):** BESIY dropped from packaging_test_substrates — OTC ADR; story 0.2 probe showed it does not stream on the Massive real-time websocket and no NBBO exists at the vendor (so no Lee-Ready). No replacement named. Universe: 164 unique members + benchmarks ≈ 191 symbols (189 equity symbols subscribed, ZN/ZB backlogged).

PM reviews membership after every earnings cycle and on any thesis change. All edits flow through `morning-tape-baskets-v2.json`; this document regenerates from it. Total tracked universe: ~170 symbols incl. benchmarks — well within a single Databento websocket subscription.

⚠ Universe is ~170 symbols, not the ~60 in Build Spec §8 — ingest throughput floor in dev plan story 1.1 raised to ≥50k msgs/sec through the 09:30 burst; verify Databento tier pricing at this symbol count. 