// fulltape.go — historical backfill source (mini-spec 7.5): the daily
// full-tape zip (one CSV, whole market) streamed through the SAME
// normalizePrint the wire decoder uses, filtered to the universe. No fake
// websocket frames are synthesized — provenance stays honest; parity with
// live capture is proven empirically on an overlapping day, not assumed.
package uwfeed

import (
	"archive/zip"
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"buddy-flow/internal/optingest"
)

// FullTapeStats counts one day's conversion.
type FullTapeStats struct {
	Rows        int64 // every CSV row
	Universe    int64 // rows for universe underlyings (pre-dedupe submits)
	Canceled    int64 // universe rows flagged canceled='t' — skipped
	DecodeErrs  int64 // universe rows that failed normalization
	OtherSymbol int64 // rows outside the universe — skipped
}

// fullTapeColumns are the REST/CSV names for the fields we consume; the
// wire decoder's names differ for two of them (option_chain_id =
// option_symbol, upstream_condition_detail = trade_code) — 7.0 finding.
var fullTapeColumns = []string{
	"id", "underlying_symbol", "executed_at", "nbbo_bid", "nbbo_ask", "size",
	"price", "option_chain_id", "tags", "expiry", "option_type",
	"open_interest", "strike", "premium", "underlying_price",
	"upstream_condition_detail", "canceled",
}

// StreamFullTape reads one full-tape zip and submits every universe print.
func StreamFullTape(zipPath string, universe map[string]bool, p *optingest.Pipeline) (FullTapeStats, error) {
	var stats FullTapeStats
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return stats, err
	}
	defer zr.Close()
	if len(zr.File) != 1 {
		return stats, fmt.Errorf("%s: expected exactly one CSV entry, found %d", zipPath, len(zr.File))
	}
	rc, err := zr.File[0].Open()
	if err != nil {
		return stats, err
	}
	defer rc.Close()

	cr := csv.NewReader(bufio.NewReaderSize(rc, 4<<20))
	cr.ReuseRecord = true
	hdr, err := cr.Read()
	if err != nil {
		return stats, fmt.Errorf("%s: read header: %w", zipPath, err)
	}
	idx := map[string]int{}
	for i, h := range hdr {
		idx[strings.TrimSpace(h)] = i
	}
	for _, c := range fullTapeColumns {
		if _, ok := idx[c]; !ok {
			return stats, fmt.Errorf("%s: missing column %q", zipPath, c)
		}
	}
	col := func(row []string, name string) string { return row[idx[name]] }

	for {
		row, err := cr.Read()
		if err == io.EOF {
			return stats, nil
		}
		if err != nil {
			return stats, fmt.Errorf("%s: row %d: %w", zipPath, stats.Rows+2, err)
		}
		stats.Rows++
		sym := col(row, "underlying_symbol")
		if !universe[sym] {
			stats.OtherSymbol++
			continue
		}
		stats.Universe++
		if col(row, "canceled") == "t" {
			stats.Canceled++
			continue
		}
		ms, ok := parsePGTimestampMs(col(row, "executed_at"))
		if !ok {
			stats.DecodeErrs++
			continue
		}
		var size, oi int64
		if _, err := fmt.Sscan(col(row, "size"), &size); err != nil {
			stats.DecodeErrs++
			continue
		}
		if s := col(row, "open_interest"); s != "" {
			if _, err := fmt.Sscan(s, &oi); err != nil {
				stats.DecodeErrs++
				continue
			}
		}
		w := wirePrint{
			ID:           col(row, "id"),
			Underlying:   sym,
			ExecutedAt:   ms,
			OptionSymbol: col(row, "option_chain_id"),
			Expiry:       col(row, "expiry"),
			OptionType:   col(row, "option_type"),
			Strike:       col(row, "strike"),
			Size:         size,
			Price:        col(row, "price"),
			Premium:      col(row, "premium"),
			OpenInterest: oi,
			UPrice:       col(row, "underlying_price"),
			NbboBid:      col(row, "nbbo_bid"),
			NbboAsk:      col(row, "nbbo_ask"),
			TradeCode:    col(row, "upstream_condition_detail"),
			Tags:         pgArrayToJSON(col(row, "tags")),
		}
		t, ok := normalizePrint(&w)
		if !ok {
			stats.DecodeErrs++
			continue
		}
		p.Submit(t)
	}
}

// parsePGTimestampMs parses the full-tape's Postgres-style timestamp
// ("2026-08-17 13:30:00.00714+00", microsecond fraction of variable width)
// to epoch milliseconds, matching the wire's executed_at resolution.
func parsePGTimestampMs(s string) (int64, bool) {
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999-07",
		"2006-01-02 15:04:05-07",
		"2006-01-02T15:04:05.999999Z07:00",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UnixNano() / 1_000_000, true
		}
	}
	return 0, false
}

// pgArrayToJSON converts a Postgres array literal ("{ask_side,bullish}")
// into the JSON array the wire decoder's sideTag expects.
func pgArrayToJSON(lit string) json.RawMessage {
	lit = strings.TrimSpace(lit)
	if len(lit) < 2 || lit[0] != '{' || lit[len(lit)-1] != '}' {
		return json.RawMessage("[]")
	}
	inner := lit[1 : len(lit)-1]
	if inner == "" {
		return json.RawMessage("[]")
	}
	parts := strings.Split(inner, ",")
	for i := range parts {
		parts[i] = strings.Trim(strings.TrimSpace(parts[i]), `"`)
	}
	b, _ := json.Marshal(parts)
	return b
}
