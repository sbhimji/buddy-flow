# View-verify report — 2026-08-14

View: `go run ./cmd/replay -capture /Users/saimbhimji/repo/buddy-flow/data/capture/2026-08-14/stream.jsonl -view -view-at T` (capture-fed Go path).
Python: `../../../../../../../Users/saimbhimji/repo/buddy-flow/data/buckets/2026-08-14.trades-only.csv` + `/Users/saimbhimji/repo/buddy-flow/data/profiles/<SYM>.csv` (flat-file-fed stdlib path).
Baskets: `/Users/saimbhimji/repo/buddy-flow/docs/foundations/morning-tape-baskets-v2.json` (22 baskets, 164 symbols).

Criteria: base exact string; |rvol view-py| <= 0.01; $last relative delta <= 0.5% (view string parsed back to a number vs raw python sum); gaps must agree.

## Render 12:00:00 ET — $last minute 11:59 (minute_of_day 719)

| basket | view $last | view base | view rvol | py $last | py base | py rvol | $last rel delta | $last | base | rvol |
|---|---|---|---|---|---|---|---|---|---|---|
| consumption_software | 13.2M | 5.81M | 2.27 | 13.2M | 5.81M | 2.27 | +0.2053% | PASS | PASS | PASS |
| critical_minerals | 2.81M | 1.49M | 1.89 | 2.82M | 1.49M | 1.89 | -0.1881% | PASS | PASS | PASS |
| dc_reits | 1.85M | 929k | 2.00 | 1.85M | 929k | 2.00 | -0.1637% | PASS | PASS | PASS |
| defense_primes | 1.62M | 3.39M | 0.48 | 1.63M | 3.39M | 0.48 | -0.3718% | PASS | PASS | PASS |
| defense_tech_growth | 17.7M | 8.61M | 2.06 | 17.7M | 8.61M | 2.06 | -0.2702% | PASS | PASS | PASS |
| epc_labor | 2.22M | 2.91M | 0.76 | 2.22M | 2.91M | 0.77 | -0.1982% | PASS | PASS | PASS |
| fuel_cells_storage | 4.58M | 4.49M | 1.02 | 4.58M | 4.49M | 1.02 | +0.0353% | PASS | PASS | PASS |
| memory_storage | 141M | 92.7M | 1.53 | 141M | 92.7M | 1.53 | -0.3356% | PASS | PASS | PASS |
| neoclouds_dc_builders | 13.4M | 16.2M | 0.83 | 13.4M | 16.2M | 0.83 | +0.0244% | PASS | PASS | PASS |
| networking_connectivity | 32.9M | 21.2M | 1.55 | 32.9M | 21.2M | 1.55 | -0.0915% | PASS | PASS | PASS |
| nuclear_fuel_cycle | 4.50M | 4.30M | 1.05 | 4.50M | 4.30M | 1.05 | -0.0546% | PASS | PASS | PASS |
| packaging_test_substrates | 3.15M | 954k | 3.30 | 3.15M | 954k | 3.30 | +0.0057% | PASS | PASS | PASS |
| photonics_optics | 24.7M | 13.4M | 1.85 | 24.7M | 13.4M | 1.85 | -0.0416% | PASS | PASS | PASS |
| power_equipment | 7.62M | 9.34M | 0.82 | 7.63M | 9.34M | 0.82 | -0.0697% | PASS | PASS | PASS |
| promise_tier_ai | 23.5M | 16.5M | 1.42 | 23.5M | 16.5M | 1.42 | -0.0912% | PASS | PASS | PASS |
| proof_tier_ai_megacap | 83.1M | 129M | 0.65 | 83.1M | 129M | 0.65 | -0.0496% | PASS | PASS | PASS |
| quantum | 2.60M | 1.75M | 1.49 | 2.60M | 1.75M | 1.49 | -0.1389% | PASS | PASS | PASS |
| robotics_av | 24.8M | 18.9M | 1.31 | 24.8M | 18.9M | 1.31 | +0.0893% | PASS | PASS | PASS |
| semicap_frontend | 17.8M | 13.1M | 1.35 | 17.8M | 13.1M | 1.35 | +0.1819% | PASS | PASS | PASS |
| semis_analog_power_auto | 8.04M | 6.54M | 1.23 | 8.04M | 6.54M | 1.23 | -0.0351% | PASS | PASS | PASS |
| semis_compute | 62.6M | 79.4M | 0.79 | 62.7M | 79.4M | 0.79 | -0.0936% | PASS | PASS | PASS |
| space | 19.8M | 16.0M | 1.24 | 19.8M | 16.0M | 1.24 | -0.1383% | PASS | PASS | PASS |

## Render 12:31:00 ET — $last minute 12:30 (minute_of_day 750)

| basket | view $last | view base | view rvol | py $last | py base | py rvol | $last rel delta | $last | base | rvol |
|---|---|---|---|---|---|---|---|---|---|---|
| consumption_software | 2.71M | 4.83M | 0.56 | 2.72M | 4.83M | 0.56 | -0.2181% | PASS | PASS | PASS |
| critical_minerals | 5.43M | 1.74M | 3.11 | 5.43M | 1.74M | 3.11 | +0.0593% | PASS | PASS | PASS |
| dc_reits | 384k | 1.02M | 0.38 | 385k | 1.02M | 0.38 | -0.2655% | PASS | PASS | PASS |
| defense_primes | 1.49M | 3.57M | 0.42 | 1.50M | 3.57M | 0.42 | -0.3967% | PASS | PASS | PASS |
| defense_tech_growth | 4.94M | 9.85M | 0.50 | 4.94M | 9.85M | 0.50 | +0.0118% | PASS | PASS | PASS |
| epc_labor | 6.88M | 3.39M | 2.03 | 6.89M | 3.39M | 2.03 | -0.1362% | PASS | PASS | PASS |
| fuel_cells_storage | 4.27M | 5.24M | 0.81 | 4.27M | 5.24M | 0.81 | +0.0682% | PASS | PASS | PASS |
| memory_storage | 103M | 117M | 0.89 | 103M | 117M | 0.89 | -0.2338% | PASS | PASS | PASS |
| neoclouds_dc_builders | 33.6M | 21.3M | 1.58 | 33.6M | 21.3M | 1.58 | -0.0540% | PASS | PASS | PASS |
| networking_connectivity | 27.1M | 23.9M | 1.13 | 27.1M | 23.9M | 1.13 | +0.0630% | PASS | PASS | PASS |
| nuclear_fuel_cycle | 3.62M | 4.60M | 0.79 | 3.62M | 4.60M | 0.79 | +0.0373% | PASS | PASS | PASS |
| packaging_test_substrates | 1.43M | 1.63M | 0.88 | 1.43M | 1.63M | 0.88 | -0.1508% | PASS | PASS | PASS |
| photonics_optics | 16.0M | 14.0M | 1.14 | 16.0M | 14.0M | 1.14 | -0.0892% | PASS | PASS | PASS |
| power_equipment | 7.08M | 13.1M | 0.54 | 7.09M | 13.1M | 0.54 | -0.1492% | PASS | PASS | PASS |
| promise_tier_ai | 8.54M | 18.1M | 0.47 | 8.54M | 18.1M | 0.47 | -0.0019% | PASS | PASS | PASS |
| proof_tier_ai_megacap | 188M | 134M | 1.40 | 188M | 134M | 1.40 | +0.0384% | PASS | PASS | PASS |
| quantum | 4.34M | 2.00M | 2.17 | 4.34M | 2.00M | 2.17 | -0.0771% | PASS | PASS | PASS |
| robotics_av | 26.2M | 22.1M | 1.18 | 26.2M | 22.1M | 1.18 | +0.1376% | PASS | PASS | PASS |
| semicap_frontend | 16.8M | 16.6M | 1.01 | 16.8M | 16.6M | 1.01 | +0.1338% | PASS | PASS | PASS |
| semis_analog_power_auto | 6.66M | 7.61M | 0.88 | 6.66M | 7.61M | 0.88 | -0.0252% | PASS | PASS | PASS |
| semis_compute | 158M | 105M | 1.51 | 158M | 105M | 1.51 | -0.2614% | PASS | PASS | PASS |
| space | 17.0M | 20.7M | 0.82 | 17.0M | 20.7M | 0.82 | +0.0857% | PASS | PASS | PASS |

## Render 13:01:00 ET — $last minute 13:00 (minute_of_day 780)

| basket | view $last | view base | view rvol | py $last | py base | py rvol | $last rel delta | $last | base | rvol |
|---|---|---|---|---|---|---|---|---|---|---|
| consumption_software | 3.81M | 4.00M | 0.95 | 3.81M | 4.00M | 0.95 | -0.0657% | PASS | PASS | PASS |
| critical_minerals | 4.07M | 1.50M | 2.71 | 4.07M | 1.50M | 2.71 | +0.0041% | PASS | PASS | PASS |
| dc_reits | 383k | 844k | 0.45 | 384k | 844k | 0.45 | -0.2383% | PASS | PASS | PASS |
| defense_primes | 3.57M | 2.90M | 1.23 | 3.57M | 2.90M | 1.23 | +0.0547% | PASS | PASS | PASS |
| defense_tech_growth | 4.92M | 9.29M | 0.53 | 4.92M | 9.29M | 0.53 | -0.0483% | PASS | PASS | PASS |
| epc_labor | 3.58M | 2.78M | 1.29 | 3.59M | 2.78M | 1.29 | -0.2282% | PASS | PASS | PASS |
| fuel_cells_storage | 4.65M | 4.91M | 0.95 | 4.65M | 4.91M | 0.95 | -0.0731% | PASS | PASS | PASS |
| memory_storage | 53.3M | 79.3M | 0.67 | 53.4M | 79.3M | 0.67 | -0.1992% | PASS | PASS | PASS |
| neoclouds_dc_builders | 56.8M | 14.9M | 3.80 | 56.8M | 14.9M | 3.80 | +0.0339% | PASS | PASS | PASS |
| networking_connectivity | 18.2M | 18.5M | 0.98 | 18.2M | 18.5M | 0.98 | +0.1559% | PASS | PASS | PASS |
| nuclear_fuel_cycle | 3.44M | 3.70M | 0.93 | 3.44M | 3.70M | 0.93 | +0.0534% | PASS | PASS | PASS |
| packaging_test_substrates | 1.21M | 1.43M | 0.84 | 1.21M | 1.43M | 0.84 | +0.3740% | PASS | PASS | PASS |
| photonics_optics | 9.25M | 12.3M | 0.75 | 9.26M | 12.3M | 0.75 | -0.0810% | PASS | PASS | PASS |
| power_equipment | 2.47M | 10.9M | 0.23 | 2.47M | 10.9M | 0.23 | +0.0153% | PASS | PASS | PASS |
| promise_tier_ai | 9.47M | 17.1M | 0.55 | 9.47M | 17.1M | 0.55 | -0.0141% | PASS | PASS | PASS |
| proof_tier_ai_megacap | 73.5M | 121M | 0.61 | 73.6M | 121M | 0.61 | -0.0899% | PASS | PASS | PASS |
| quantum | 3.69M | 2.08M | 1.78 | 3.70M | 2.08M | 1.78 | -0.1429% | PASS | PASS | PASS |
| robotics_av | 32.4M | 19.7M | 1.65 | 32.4M | 19.7M | 1.65 | +0.0257% | PASS | PASS | PASS |
| semicap_frontend | 14.6M | 12.7M | 1.15 | 14.6M | 12.7M | 1.15 | +0.0843% | PASS | PASS | PASS |
| semis_analog_power_auto | 3.64M | 6.53M | 0.56 | 3.64M | 6.53M | 0.56 | -0.0975% | PASS | PASS | PASS |
| semis_compute | 65.8M | 78.2M | 0.84 | 65.8M | 78.2M | 0.84 | -0.0488% | PASS | PASS | PASS |
| space | 15.2M | 18.5M | 0.82 | 15.2M | 18.5M | 0.82 | -0.2486% | PASS | PASS | PASS |

## Render 14:00:00 ET — $last minute 13:59 (minute_of_day 839)

| basket | view $last | view base | view rvol | py $last | py base | py rvol | $last rel delta | $last | base | rvol |
|---|---|---|---|---|---|---|---|---|---|---|
| consumption_software | 4.04M | 4.68M | 0.86 | 4.04M | 4.68M | 0.86 | -0.0055% | PASS | PASS | PASS |
| critical_minerals | 970k | 1.27M | 0.76 | 971k | 1.27M | 0.76 | -0.0900% | PASS | PASS | PASS |
| dc_reits | 1000k | 604k | 1.65 | 1.00M | 604k | 1.66 | -0.0024% | PASS | PASS | PASS |
| defense_primes | 1.29M | 2.89M | 0.45 | 1.29M | 2.89M | 0.45 | +0.0354% | PASS | PASS | PASS |
| defense_tech_growth | 4.24M | 7.37M | 0.57 | 4.24M | 7.37M | 0.58 | +0.0370% | PASS | PASS | PASS |
| epc_labor | 2.21M | 2.15M | 1.03 | 2.22M | 2.15M | 1.03 | -0.2360% | PASS | PASS | PASS |
| fuel_cells_storage | 2.50M | 4.05M | 0.62 | 2.50M | 4.05M | 0.62 | +0.0674% | PASS | PASS | PASS |
| memory_storage | 50.0M | 58.0M | 0.86 | 50.0M | 58.0M | 0.86 | -0.0408% | PASS | PASS | PASS |
| neoclouds_dc_builders | 9.36M | 11.9M | 0.79 | 9.37M | 11.9M | 0.79 | -0.0684% | PASS | PASS | PASS |
| networking_connectivity | 36.2M | 14.2M | 2.55 | 36.2M | 14.2M | 2.55 | -0.0110% | PASS | PASS | PASS |
| nuclear_fuel_cycle | 4.78M | 3.27M | 1.46 | 4.79M | 3.27M | 1.47 | -0.1622% | PASS | PASS | PASS |
| packaging_test_substrates | 446k | 630k | 0.71 | 446k | 630k | 0.71 | -0.1068% | PASS | PASS | PASS |
| photonics_optics | 8.21M | 10.1M | 0.82 | 8.21M | 10.1M | 0.82 | -0.0501% | PASS | PASS | PASS |
| power_equipment | 6.60M | 7.87M | 0.84 | 6.60M | 7.87M | 0.84 | -0.0136% | PASS | PASS | PASS |
| promise_tier_ai | 6.82M | 15.8M | 0.43 | 6.82M | 15.8M | 0.43 | -0.0128% | PASS | PASS | PASS |
| proof_tier_ai_megacap | 69.5M | 89.4M | 0.78 | 69.6M | 89.4M | 0.78 | -0.1725% | PASS | PASS | PASS |
| quantum | 2.84M | 1.21M | 2.36 | 2.84M | 1.21M | 2.36 | -0.0882% | PASS | PASS | PASS |
| robotics_av | 19.0M | 12.1M | 1.57 | 19.0M | 12.1M | 1.57 | +0.1387% | PASS | PASS | PASS |
| semicap_frontend | 9.79M | 11.1M | 0.88 | 9.81M | 11.1M | 0.88 | -0.1536% | PASS | PASS | PASS |
| semis_analog_power_auto | 4.58M | 5.56M | 0.82 | 4.58M | 5.56M | 0.82 | +0.0803% | PASS | PASS | PASS |
| semis_compute | 59.1M | 54.4M | 1.09 | 59.1M | 54.4M | 1.09 | -0.0423% | PASS | PASS | PASS |
| space | 33.4M | 15.1M | 2.21 | 33.4M | 15.1M | 2.21 | -0.0348% | PASS | PASS | PASS |

## Render 15:30:00 ET — $last minute 15:29 (minute_of_day 929)

| basket | view $last | view base | view rvol | py $last | py base | py rvol | $last rel delta | $last | base | rvol |
|---|---|---|---|---|---|---|---|---|---|---|
| consumption_software | 7.46M | 9.58M | 0.78 | 7.47M | 9.58M | 0.78 | -0.0730% | PASS | PASS | PASS |
| critical_minerals | 3.72M | 2.35M | 1.58 | 3.72M | 2.35M | 1.58 | +0.0405% | PASS | PASS | PASS |
| dc_reits | 771k | 1.74M | 0.44 | 771k | 1.74M | 0.44 | -0.0511% | PASS | PASS | PASS |
| defense_primes | 5.00M | 6.10M | 0.82 | 5.00M | 6.10M | 0.82 | -0.0749% | PASS | PASS | PASS |
| defense_tech_growth | 11.5M | 10.8M | 1.07 | 11.5M | 10.8M | 1.07 | +0.2258% | PASS | PASS | PASS |
| epc_labor | 4.02M | 5.67M | 0.71 | 4.02M | 5.67M | 0.71 | +0.0599% | PASS | PASS | PASS |
| fuel_cells_storage | 4.73M | 6.78M | 0.70 | 4.73M | 6.78M | 0.70 | -0.0928% | PASS | PASS | PASS |
| memory_storage | 92.3M | 126M | 0.73 | 92.4M | 126M | 0.73 | -0.0614% | PASS | PASS | PASS |
| neoclouds_dc_builders | 31.1M | 19.7M | 1.58 | 31.1M | 19.7M | 1.58 | -0.0267% | PASS | PASS | PASS |
| networking_connectivity | 38.0M | 30.6M | 1.24 | 38.0M | 30.6M | 1.24 | +0.0201% | PASS | PASS | PASS |
| nuclear_fuel_cycle | 9.79M | 6.89M | 1.42 | 9.79M | 6.89M | 1.42 | +0.0271% | PASS | PASS | PASS |
| packaging_test_substrates | 2.64M | 2.21M | 1.20 | 2.64M | 2.21M | 1.20 | -0.1159% | PASS | PASS | PASS |
| photonics_optics | 28.0M | 17.7M | 1.59 | 28.0M | 17.7M | 1.59 | -0.1392% | PASS | PASS | PASS |
| power_equipment | 10.00M | 15.2M | 0.66 | 10.0M | 15.2M | 0.66 | -0.0251% | PASS | PASS | PASS |
| promise_tier_ai | 15.8M | 23.5M | 0.67 | 15.8M | 23.5M | 0.67 | -0.2728% | PASS | PASS | PASS |
| proof_tier_ai_megacap | 148M | 160M | 0.92 | 148M | 160M | 0.92 | +0.0625% | PASS | PASS | PASS |
| quantum | 4.15M | 2.95M | 1.41 | 4.15M | 2.95M | 1.41 | +0.1053% | PASS | PASS | PASS |
| robotics_av | 27.1M | 23.1M | 1.18 | 27.2M | 23.1M | 1.18 | -0.2063% | PASS | PASS | PASS |
| semicap_frontend | 22.8M | 26.6M | 0.86 | 22.8M | 26.6M | 0.86 | -0.0349% | PASS | PASS | PASS |
| semis_analog_power_auto | 11.1M | 15.8M | 0.70 | 11.1M | 15.8M | 0.70 | +0.0724% | PASS | PASS | PASS |
| semis_compute | 122M | 101M | 1.20 | 122M | 101M | 1.20 | +0.2858% | PASS | PASS | PASS |
| space | 23.6M | 23.4M | 1.01 | 23.6M | 23.4M | 1.01 | -0.0096% | PASS | PASS | PASS |

## Render 15:59:00 ET — $last minute 15:58 (minute_of_day 958)

| basket | view $last | view base | view rvol | py $last | py base | py rvol | $last rel delta | $last | base | rvol |
|---|---|---|---|---|---|---|---|---|---|---|
| consumption_software | 77.5M | 78.8M | 0.98 | 77.5M | 78.8M | 0.98 | +0.0293% | PASS | PASS | PASS |
| critical_minerals | 21.5M | 17.1M | 1.26 | 21.5M | 17.1M | 1.26 | -0.1399% | PASS | PASS | PASS |
| dc_reits | 15.9M | 20.0M | 0.80 | 15.9M | 20.0M | 0.80 | +0.0191% | PASS | PASS | PASS |
| defense_primes | 54.7M | 63.9M | 0.86 | 54.7M | 63.9M | 0.86 | -0.0040% | PASS | PASS | PASS |
| defense_tech_growth | 53.2M | 41.8M | 1.27 | 53.2M | 41.8M | 1.27 | +0.0859% | PASS | PASS | PASS |
| epc_labor | 48.4M | 51.8M | 0.93 | 48.4M | 51.8M | 0.93 | -0.0480% | PASS | PASS | PASS |
| fuel_cells_storage | 36.6M | 40.5M | 0.90 | 36.6M | 40.5M | 0.90 | -0.0125% | PASS | PASS | PASS |
| memory_storage | 481M | 408M | 1.18 | 481M | 408M | 1.18 | -0.0791% | PASS | PASS | PASS |
| neoclouds_dc_builders | 139M | 105M | 1.33 | 139M | 105M | 1.33 | -0.0736% | PASS | PASS | PASS |
| networking_connectivity | 246M | 211M | 1.17 | 246M | 211M | 1.17 | -0.1507% | PASS | PASS | PASS |
| nuclear_fuel_cycle | 56.2M | 60.8M | 0.92 | 56.2M | 60.8M | 0.92 | -0.0452% | PASS | PASS | PASS |
| packaging_test_substrates | 18.6M | 18.8M | 0.99 | 18.6M | 18.8M | 0.99 | +0.1156% | PASS | PASS | PASS |
| photonics_optics | 147M | 119M | 1.23 | 147M | 119M | 1.23 | +0.0699% | PASS | PASS | PASS |
| power_equipment | 133M | 142M | 0.94 | 133M | 142M | 0.94 | +0.2623% | PASS | PASS | PASS |
| promise_tier_ai | 100M | 110M | 0.91 | 100M | 110M | 0.91 | -0.2563% | PASS | PASS | PASS |
| proof_tier_ai_megacap | 715M | 894M | 0.80 | 715M | 894M | 0.80 | -0.0001% | PASS | PASS | PASS |
| quantum | 12.6M | 13.7M | 0.92 | 12.6M | 13.7M | 0.92 | -0.2920% | PASS | PASS | PASS |
| robotics_av | 77.4M | 88.7M | 0.87 | 77.4M | 88.7M | 0.87 | -0.0330% | PASS | PASS | PASS |
| semicap_frontend | 171M | 180M | 0.95 | 171M | 180M | 0.95 | -0.2063% | PASS | PASS | PASS |
| semis_analog_power_auto | 99.0M | 108M | 0.92 | 99.0M | 108M | 0.92 | -0.0287% | PASS | PASS | PASS |
| semis_compute | 390M | 453M | 0.86 | 390M | 453M | 0.86 | +0.1072% | PASS | PASS | PASS |
| space | 123M | 120M | 1.02 | 123M | 120M | 1.02 | +0.3644% | PASS | PASS | PASS |

## Summary

```
== Summary ==
checks: 132 basket-minutes (22 baskets x 6 minutes)
base  (exact string): 132/132 pass
rvol  (<= 0.01):      132/132 pass
$last (<= 0.5%):      132/132 pass
$last string equality (informational): 106/132 (80.3%)
worst $last relative delta: -0.3967% (defense_primes @ 12:31:00)
mean signed $last relative delta: -0.0418%
RESULT: all criteria pass
```
