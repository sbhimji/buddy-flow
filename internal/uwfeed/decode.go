// decode.go — the shared options-frame decoder (mini-spec 7.2). Live's
// Frame hook and replay's StreamCapture both call DecodeFrame; that shared
// path is the determinism contract (the 1.2/1.3 lesson). The decoder
// transcribes wire → struct (R1): no sign conventions, no sweep policy,
// no weights — all of that is optclassify's (7.3).
package uwfeed

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"buddy-flow/internal/optingest"
)

// DecodeStats counts what the decoder saw. Decode failures count, never
// crash (R4): the frame is already safely in capture.
type DecodeStats struct {
	Frames     int64
	Prints     int64 // submitted to the pipeline (pre-dedupe)
	Acks       int64
	Controls   int64 // our _capture records (replay only)
	DecodeErrs int64
	NonTrade   int64 // well-formed tuple on a non-option_trades channel
}

// wirePrint mirrors the option_trades payload, wire-confirmed 2026-08-20
// (data.md §Options tape). Decimal fields arrive as strings; sizes and OI
// as ints; executed_at as epoch ms. Fields we never consume (greeks,
// cumulative side volumes) are not listed — capture retains them (R2).
type wirePrint struct {
	ID           string          `json:"id"`
	Underlying   string          `json:"underlying_symbol"`
	ExecutedAt   int64           `json:"executed_at"`
	OptionSymbol string          `json:"option_symbol"`
	Expiry       string          `json:"expiry"`
	OptionType   string          `json:"option_type"`
	Strike       string          `json:"strike"`
	Size         int64           `json:"size"`
	Price        string          `json:"price"`
	Premium      string          `json:"premium"`
	OpenInterest int64           `json:"open_interest"`
	UPrice       string          `json:"underlying_price"`
	NbboBid      string          `json:"nbbo_bid"`
	NbboAsk      string          `json:"nbbo_ask"`
	TradeCode    string          `json:"trade_code"`
	Tags         json.RawMessage `json:"tags"`
}

// DecodeFrame parses one captured frame and submits any option print to
// the pipeline. Returns silently on non-print frames (acks, controls,
// other channels) — they are counted, not errors.
func DecodeFrame(frame []byte, p *optingest.Pipeline, stats *DecodeStats) {
	stats.Frames++
	var tuple []json.RawMessage
	if json.Unmarshal(frame, &tuple) != nil || len(tuple) != 2 {
		// Not a data tuple. Control records from our own capture are
		// 1-element arrays like [{"detail":…,"ev":"_capture","event":…}] —
		// field order varies, so search the whole frame, not a prefix (a
		// 40-byte sniff misclassified all 38 controls of the first live
		// session as decode errors). This path only runs on non-tuple
		// frames, so the scan costs nothing on the print hot path.
		if bytes.Contains(frame, []byte(`"_capture"`)) {
			stats.Controls++
		} else {
			stats.DecodeErrs++
		}
		return
	}
	var ch string
	if json.Unmarshal(tuple[0], &ch) != nil {
		stats.DecodeErrs++
		return
	}
	if !strings.HasPrefix(ch, "option_trades:") {
		stats.NonTrade++
		return
	}
	// Ack bodies carry "status"; prints never do.
	var probe struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(tuple[1], &probe) == nil && probe.Status != "" {
		stats.Acks++
		return
	}
	var w wirePrint
	if err := json.Unmarshal(tuple[1], &w); err != nil {
		stats.DecodeErrs++
		return
	}
	t, ok := normalizePrint(&w)
	if !ok {
		stats.DecodeErrs++
		return
	}
	p.Submit(t)
	stats.Prints++
}

// normalizePrint converts a wire payload to the typed print. All-or-
// nothing (R5): any unparseable required field fails the whole print —
// no partial structs, no silent zeros.
func normalizePrint(w *wirePrint) (optingest.OptionTrade, bool) {
	var t optingest.OptionTrade
	id, ok := parseUUID(w.ID)
	if !ok || w.Underlying == "" || w.ExecutedAt <= 0 || w.Expiry == "" || w.Size < 0 {
		return t, false
	}
	switch w.OptionType {
	case "call":
		t.IsCall = true
	case "put":
		t.IsCall = false
	default:
		return t, false
	}
	var perr bool
	num := func(s string) float64 {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			perr = true
		}
		return v
	}
	t.Strike = num(w.Strike)
	t.Price = num(w.Price)
	t.Premium = num(w.Premium)
	t.NbboBid = num(w.NbboBid)
	t.NbboAsk = num(w.NbboAsk)
	if perr {
		return t, false
	}
	// underlying_price is "" on index names (D17) — a defined absence,
	// never a decode error and never a zero-as-measurement.
	if w.UPrice != "" {
		t.UPrice = num(w.UPrice)
		if perr {
			return t, false
		}
		t.UPriceOK = true
	}
	t.ID = id
	t.Underlying = w.Underlying
	t.OptionSymbol = w.OptionSymbol
	t.ExecNs = w.ExecutedAt * 1_000_000 // ms → ns (canonical)
	t.Expiry = w.Expiry
	t.Size = w.Size
	t.OpenInterest = w.OpenInterest
	t.TradeCode = w.TradeCode
	t.SideTag = sideTag(w.Tags)
	return t, true
}

// sideTag extracts the side classification from the tags array — raw
// transcription (R1); the ±/0 sign mapping is optclassify's policy. The
// vendor's interpretation tags (bullish/bearish/…) are deliberately
// dropped here: never consumed, never rendered (scope law).
func sideTag(raw json.RawMessage) string {
	var tags []string
	if json.Unmarshal(raw, &tags) != nil {
		return ""
	}
	for _, tg := range tags {
		switch tg {
		case "ask_side", "bid_side", "mid_side", "no_side":
			return tg
		}
	}
	return ""
}

// parseUUID decodes the canonical 36-char uuid form into 16 bytes.
func parseUUID(s string) ([16]byte, bool) {
	var out [16]byte
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return out, false
	}
	hexOnly := s[:8] + s[9:13] + s[14:18] + s[19:23] + s[24:]
	b, err := hex.DecodeString(hexOnly)
	if err != nil {
		return out, false
	}
	copy(out[:], b)
	return out, true
}
