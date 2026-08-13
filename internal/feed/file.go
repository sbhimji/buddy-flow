// Package feed contains the message sources that drive the ingest core.
//
// file.go is the flat-file/capture feeder — the replay side of the
// dual-source design (mini-spec 1.1). The live websocket feeder arrives
// with story 1.2 (no live run before capture exists).
//
// Ordering note: Massive daily flat files are sorted by TICKER, not time —
// rows within one symbol are chronological, but cross-symbol interleaving is
// not. That is sufficient for 1.1 (counts, throughput, NBBO are all
// per-symbol state); true time-ordered cross-symbol replay is story 1.3's
// job and will operate on our own (naturally time-ordered) capture files.
package feed

import (
	"compress/gzip"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	"buddy-flow/internal/ingest"
)

// Options controls one file stream.
type Options struct {
	// Full disables the universe filter (stress mode). The filter is the
	// replay analog of the live subscription list, so it lives here in the
	// feeder — the core never sees a filter. Full mode is for throughput
	// stress only (whole-market load ≈ 5× universe), never replay fidelity.
	Full bool
	// FromNs/ToNs bound sip_timestamp (epoch ns UTC), half-open [From, To).
	// Zero values mean unbounded. Used to replay the 09:29–09:35 burst.
	FromNs, ToNs int64
}

// Stats reports what one stream did.
type Stats struct {
	Rows      int64 // data rows read from the file
	Submitted int64 // rows submitted to the pipeline
	Filtered  int64 // rows dropped by the universe filter
	Windowed  int64 // rows dropped by the time window
	Malformed int64 // non-empty numeric fields that failed to parse (became 0)
}

// StreamFile streams one flat file (plain or .gz) of the given kind into the
// pipeline. Blocks until the file is exhausted (Submit applies backpressure).
func StreamFile(path string, kind ingest.Kind, p *ingest.Pipeline, opt Options) (Stats, error) {
	var stats Stats
	f, err := os.Open(path)
	if err != nil {
		return stats, err
	}
	defer f.Close()

	var reader io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return stats, fmt.Errorf("gzip %s: %w", path, err)
		}
		defer gz.Close()
		reader = gz
	}

	cr := csv.NewReader(reader)
	cr.ReuseRecord = true

	header, err := cr.Read()
	if err != nil {
		return stats, fmt.Errorf("read header: %w", err)
	}
	col := map[string]int{}
	for i, name := range header {
		col[strings.TrimSpace(strings.ToLower(name))] = i
	}
	// Column order is not guaranteed (handoff) — resolve by name, fail loudly.
	need := []string{"ticker", "sip_timestamp", "sequence_number", "conditions"}
	if kind == ingest.KindTrade {
		need = append(need, "price", "size", "exchange", "tape", "participant_timestamp")
	} else {
		need = append(need, "bid_price", "bid_size", "bid_exchange", "ask_price", "ask_size", "ask_exchange")
	}
	idx := map[string]int{}
	for _, name := range need {
		i, ok := col[name]
		if !ok {
			return stats, fmt.Errorf("%s: column %q not found in header %v", path, name, header)
		}
		idx[name] = i
	}

	table := p.Table()
	var msg ingest.Msg
	msg.Kind = kind

	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return stats, fmt.Errorf("%s row %d: %w", path, stats.Rows+2, err)
		}
		stats.Rows++

		ts := parseInt(rec[idx["sip_timestamp"]], &stats.Malformed)
		if (opt.FromNs != 0 && ts < opt.FromNs) || (opt.ToNs != 0 && ts >= opt.ToNs) {
			stats.Windowed++
			continue
		}

		sym := rec[idx["ticker"]]
		var st *ingest.SymbolState
		if opt.Full {
			st = table.LookupOrCreate(sym)
		} else if st = table.Lookup(sym); st == nil {
			stats.Filtered++
			continue
		}

		if kind == ingest.KindTrade {
			t := &msg.Trade
			t.State = st
			t.SipTs = ts
			t.PartTs = parseInt(rec[idx["participant_timestamp"]], &stats.Malformed)
			t.Seq = parseInt(rec[idx["sequence_number"]], &stats.Malformed)
			t.Price = parseFloat(rec[idx["price"]], &stats.Malformed)
			t.Size = parseFloat(rec[idx["size"]], &stats.Malformed)
			t.Exchange = int32(parseInt(rec[idx["exchange"]], &stats.Malformed))
			t.Tape = int32(parseInt(rec[idx["tape"]], &stats.Malformed))
			t.NCond = parseConditions(rec[idx["conditions"]], &t.Cond, &p.CondOverflow)
		} else {
			q := &msg.Quote
			q.State = st
			q.SipTs = ts
			q.Seq = parseInt(rec[idx["sequence_number"]], &stats.Malformed)
			q.BidPrice = parseFloat(rec[idx["bid_price"]], &stats.Malformed)
			q.AskPrice = parseFloat(rec[idx["ask_price"]], &stats.Malformed)
			q.BidSize = int64(parseFloat(rec[idx["bid_size"]], &stats.Malformed))
			q.AskSize = int64(parseFloat(rec[idx["ask_size"]], &stats.Malformed))
			q.BidExch = int32(parseInt(rec[idx["bid_exchange"]], &stats.Malformed))
			q.AskExch = int32(parseInt(rec[idx["ask_exchange"]], &stats.Malformed))
			q.NCond = parseConditions(rec[idx["conditions"]], &q.Cond, &p.CondOverflow)
		}
		p.Submit(msg)
		stats.Submitted++
	}
	return stats, nil
}

// parseInt reads a decimal int64. Empty is legal (the flat files leave
// optional fields empty rather than writing sentinels) and becomes 0
// silently; a non-empty field that fails to parse also becomes 0 but is
// counted on malformed — zero is a meaningful wire value (zeroed pre-market
// books), so a silent fallback would forge legal data from garbage.
func parseInt(s string, malformed *int64) int64 {
	if s == "" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		*malformed++
		return 0
	}
	return n
}

// parseFloat mirrors parseInt for float fields (sizes are float strings in
// the flat files: "152.000000").
func parseFloat(s string, malformed *int64) float64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		*malformed++
		return 0
	}
	return v
}

// parseConditions fills the fixed-size condition array without allocating.
// The flat files encode conditions as comma-separated numeric IDs
// ("14,12,37,41"); an empty field means a regular sale (no codes).
// Malformed tokens become sentinel -1 (surfaces downstream as UNKNOWN
// rather than vanishing — same convention as internal/classify). A message
// whose IDs exceed the array capacity increments the overflow counter once
// (per message, matching how the counter is reported), keeping the first
// MaxConditions IDs. Note internal/classify's acceptance parser is uncapped,
// so a >MaxConditions print passes acceptance yet arrives here truncated —
// recorded for story 3.3 (docs/backlog.md); observed session max is 4.
func parseConditions(field string, out *[ingest.MaxConditions]int32, overflow *atomic.Int64) int {
	if field == "" {
		return 0
	}
	n := 0
	overflowed := false
	start := -1
	flush := func(end int) {
		if start < 0 {
			return
		}
		tok := field[start:end]
		start = -1
		id, err := strconv.Atoi(tok)
		if err != nil {
			id = -1
		}
		if n == len(out) {
			if !overflowed {
				overflowed = true
				overflow.Add(1)
			}
			return
		}
		out[n] = int32(id)
		n++
	}
	for i := 0; i < len(field); i++ {
		switch field[i] {
		case ',', ' ', ';':
			flush(i)
		default:
			if start < 0 {
				start = i
			}
		}
	}
	flush(len(field))
	return n
}
