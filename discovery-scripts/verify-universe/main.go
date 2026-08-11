// verify-universe: no-market-hours verification against the Massive REST API.
//
// Checks:
//  1. Every symbol in our universe (basket members + benchmarks, minus backlogged
//     bond futures) exists and is active in Massive's reference data — and flags
//     any whose market is not "stocks" (e.g. OTC ADRs like BESIY, which the
//     reference API may know but the NMS SIP websocket will not carry).
//  2. Dumps the full stocks condition-code table (with update_rules) to a JSON
//     file — the machine-readable seed for the 0.3 print-inclusion policy.
//  3. Pulls one day of 1-minute bars for a probe ticker to confirm historical
//     aggregates entitlement (the 20-day baseline backfill path).
//
// Usage:
//   cd discovery-scripts/verify-universe && go run . \
//     [-config ../../docs/foundations/morning-tape-baskets-v2.json] \
//     [-out ../../docs/foundations/massive-conditions.json] [-probe NVDA]
//   (MASSIVE_API_KEY from env or ../../.env)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

const baseURL = "https://api.massive.com"

var apiKey string

func get(path string, params url.Values) (map[string]any, error) {
	u := baseURL + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < 5 {
			time.Sleep(15 * time.Second) // free-tier rate limit; paid plans shouldn't hit this
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s -> HTTP %d: %.200s", path, resp.StatusCode, body)
		}
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			return nil, fmt.Errorf("%s: bad JSON: %w", path, err)
		}
		return m, nil
	}
}

func universeFromConfig(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg struct {
		Baskets map[string]struct {
			Members []string `json:"members"`
		} `json:"baskets"`
		Benchmarks map[string][]string `json:"benchmarks"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, b := range cfg.Baskets {
		for _, t := range b.Members {
			set[t] = true
		}
	}
	for group, tickers := range cfg.Benchmarks {
		if group == "bond_gate_futures" { // ZN/ZB: backlogged, not equities
			continue
		}
		for _, t := range tickers {
			set[t] = true
		}
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out, nil
}

func checkTickers(symbols []string) (missing, nonStocks []string) {
	fmt.Printf("Checking %d symbols against %s/v3/reference/tickers ...\n", len(symbols), baseURL)
	for i, sym := range symbols {
		params := url.Values{"ticker": {sym}}
		doc, err := get("/v3/reference/tickers", params)
		if err != nil {
			fmt.Printf("  %s: ERROR %v\n", sym, err)
			missing = append(missing, sym)
			continue
		}
		results, _ := doc["results"].([]any)
		found := false
		for _, r := range results {
			rm, _ := r.(map[string]any)
			if rm["ticker"] == sym {
				found = true
				market, _ := rm["market"].(string)
				active, _ := rm["active"].(bool)
				if market != "stocks" {
					nonStocks = append(nonStocks, fmt.Sprintf("%s (market=%s)", sym, market))
				}
				if !active {
					nonStocks = append(nonStocks, fmt.Sprintf("%s (INACTIVE)", sym))
				}
			}
		}
		if !found {
			missing = append(missing, sym)
			fmt.Printf("  %s: NOT FOUND\n", sym)
		}
		if (i+1)%25 == 0 {
			fmt.Printf("  ... %d/%d\n", i+1, len(symbols))
		}
	}
	return missing, nonStocks
}

func dumpConditions(outPath string) error {
	fmt.Println("\nDumping stocks condition codes (with update_rules) ...")
	var all []any
	params := url.Values{"asset_class": {"stocks"}, "limit": {"1000"}}
	path := "/v3/reference/conditions"
	for {
		doc, err := get(path, params)
		if err != nil {
			return err
		}
		results, _ := doc["results"].([]any)
		all = append(all, results...)
		next, _ := doc["next_url"].(string)
		if next == "" {
			break
		}
		u, err := url.Parse(next)
		if err != nil {
			break
		}
		path, params = u.Path, u.Query()
	}
	byType := map[string]int{}
	for _, c := range all {
		cm, _ := c.(map[string]any)
		t, _ := cm["type"].(string)
		byType[t]++
	}
	fmt.Printf("  %d conditions; by type: %v\n", len(all), byType)
	out := map[string]any{"results": all, "fetched": time.Now().Format(time.RFC3339)}
	data, _ := json.MarshalIndent(out, "", "  ")
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		return err
	}
	fmt.Printf("  wrote %s (seed for the 0.3 print-inclusion table)\n", outPath)
	return nil
}

func checkBars(probe string) error {
	day := time.Now().AddDate(0, 0, -1)
	for day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
		day = day.AddDate(0, 0, -1)
	}
	d := day.Format("2006-01-02")
	fmt.Printf("\nHistorical probe: 1-min bars for %s on %s ...\n", probe, d)
	doc, err := get(fmt.Sprintf("/v2/aggs/ticker/%s/range/1/minute/%s/%s", probe, d, d),
		url.Values{"limit": {"50000"}})
	if err != nil {
		return err
	}
	results, _ := doc["results"].([]any)
	fmt.Printf("  %d bars returned (regular session alone = 390)\n", len(results))
	if len(results) == 0 {
		return fmt.Errorf("no bars — check entitlement/date")
	}
	return nil
}

func main() {
	config := flag.String("config", "../../docs/foundations/morning-tape-baskets-v2.json", "baskets config JSON")
	out := flag.String("out", "../../docs/foundations/massive-conditions.json", "where to write the conditions dump")
	probe := flag.String("probe", "NVDA", "ticker for the historical-bars probe")
	envFile := flag.String("env", "../../.env", "path to .env file with MASSIVE_API_KEY")
	flag.Parse()

	_ = godotenv.Load(*envFile) // fills MASSIVE_API_KEY; existing env vars win; missing file is fine
	apiKey = os.Getenv("MASSIVE_API_KEY")
	if apiKey == "" {
		fmt.Fprintf(os.Stderr, "MASSIVE_API_KEY not set and not found in %s\n", *envFile)
		os.Exit(2)
	}

	symbols, err := universeFromConfig(*config)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}

	missing, flagged := checkTickers(symbols)
	condErr := dumpConditions(*out)
	barsErr := checkBars(*probe)

	fmt.Println("\n===== SUMMARY =====")
	fmt.Printf("Symbols resolved: %d/%d\n", len(symbols)-len(missing), len(symbols))
	if len(missing) > 0 {
		fmt.Println("MISSING:", strings.Join(missing, ", "))
	}
	if len(flagged) > 0 {
		fmt.Println("FLAGGED (non-stocks market or inactive — verify these stream on the SIP websocket):")
		fmt.Println(" ", strings.Join(flagged, ", "))
	}
	if condErr != nil {
		fmt.Println("Conditions dump FAILED:", condErr)
	}
	if barsErr != nil {
		fmt.Println("Historical probe FAILED:", barsErr)
	}
	if len(missing) == 0 && condErr == nil && barsErr == nil {
		fmt.Println("RESULT: OK")
		return
	}
	fmt.Println("RESULT: GAPS FOUND — see above")
	os.Exit(1)
}
