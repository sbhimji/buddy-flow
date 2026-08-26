package uwfeed

import (
	"testing"

	"buddy-flow/internal/optingest"
)

// Verbatim data frame from the first live capture (2026-08-20 ~14:00 ET) —
// the real-wire fixture: 0DTE QQQ put, bid_side, string decimals, empty
// report_flags, greeks present-but-unconsumed.
const realDataFrame = `["option_trades:QQQ",{"id":"01a02054-442d-7622-ade3-9aed1c92a583","underlying_symbol":"QQQ","executed_at":1787248788491,"nbbo_bid":"0.61","nbbo_ask":"0.62","size":1,"price":"0.61","option_symbol":"QQQ260820P00710000","created_at":1787248788525,"report_flags":[],"tags":["bid_side","bullish","etf"],"expiry":"2026-08-20","option_type":"put","open_interest":5861,"strike":"710","premium":"61.00","volume":327784,"underlying_price":"710.71","ewma_nbbo_ask":"0.62","ewma_nbbo_bid":"0.61","implied_volatility":"0.215667012566822","delta":"-0.3780011023260482","theta":"-5.25021938409143","gamma":"0.1639994763049258","vega":"0.04085640474697583","rho":"-0.000615771526727251","theo":"0.609999999999957","trade_code":"auto","exchange":"AMXO","ask_vol":146534,"bid_vol":156678,"no_side_vol":11,"mid_vol":24561,"multi_vol":6984,"stock_multi_vol":0}]`

// capturingObserver records every observed print for assertions.
type capturingObserver struct{ got []optingest.OptionTrade }

func (c *capturingObserver) ObserveOptionTrade(t *optingest.OptionTrade) {
	c.got = append(c.got, *t)
}

func runPipeline(t *testing.T, frames []string) (*capturingObserver, *optingest.Pipeline, DecodeStats) {
	t.Helper()
	p := optingest.NewPipeline(1024)
	obs := &capturingObserver{}
	p.SetObserver(obs)
	done := make(chan struct{})
	go func() { p.Run(); close(done) }()
	var stats DecodeStats
	for _, f := range frames {
		DecodeFrame([]byte(f), p, &stats)
	}
	p.Close()
	<-done
	return obs, p, stats
}

func TestDecodeRealWireFrame(t *testing.T) {
	obs, _, stats := runPipeline(t, []string{realDataFrame})
	if stats.Prints != 1 || stats.DecodeErrs != 0 {
		t.Fatalf("stats = %+v", stats)
	}
	tr := obs.got[0]
	if tr.Underlying != "QQQ" || tr.OptionSymbol != "QQQ260820P00710000" {
		t.Errorf("identity = %q %q", tr.Underlying, tr.OptionSymbol)
	}
	if tr.IsCall {
		t.Error("put decoded as call")
	}
	if tr.ExecNs != 1787248788491*1_000_000 {
		t.Errorf("ExecNs = %d (ms→ns wrong)", tr.ExecNs)
	}
	if tr.Strike != 710 || tr.Price != 0.61 || tr.Premium != 61.00 {
		t.Errorf("numbers = strike %v price %v premium %v", tr.Strike, tr.Price, tr.Premium)
	}
	if tr.Size != 1 || tr.OpenInterest != 5861 {
		t.Errorf("ints = size %d oi %d", tr.Size, tr.OpenInterest)
	}
	if !tr.UPriceOK || tr.UPrice != 710.71 {
		t.Errorf("underlying = %v ok=%v", tr.UPrice, tr.UPriceOK)
	}
	if tr.NbboBid != 0.61 || tr.NbboAsk != 0.62 {
		t.Errorf("nbbo = %v/%v", tr.NbboBid, tr.NbboAsk)
	}
	if tr.SideTag != "bid_side" || tr.TradeCode != "auto" || tr.Expiry != "2026-08-20" {
		t.Errorf("tags/code/expiry = %q %q %q", tr.SideTag, tr.TradeCode, tr.Expiry)
	}
}

func TestDecodeIndexEmptyUnderlyingPrice(t *testing.T) {
	frame := `["option_trades:SPX",{"id":"01a02054-442d-7622-ade3-9aed1c92a584","underlying_symbol":"SPX","executed_at":1787248788491,"nbbo_bid":"1.00","nbbo_ask":"1.10","size":2,"price":"1.05","option_symbol":"SPXW260820C06000000","tags":["ask_side","index"],"expiry":"2026-08-20","option_type":"call","open_interest":100,"strike":"6000","premium":"210.00","underlying_price":"","trade_code":"slan"}]`
	obs, _, stats := runPipeline(t, []string{frame})
	if stats.Prints != 1 || stats.DecodeErrs != 0 {
		t.Fatalf("stats = %+v", stats)
	}
	tr := obs.got[0]
	if tr.UPriceOK || tr.UPrice != 0 {
		t.Errorf("empty underlying_price must be a defined absence, got %v ok=%v", tr.UPrice, tr.UPriceOK)
	}
	if !tr.IsCall || tr.SideTag != "ask_side" || tr.TradeCode != "slan" {
		t.Errorf("fields = call=%v side=%q code=%q", tr.IsCall, tr.SideTag, tr.TradeCode)
	}
}

func TestDecodeErrorsAndNonPrints(t *testing.T) {
	badDecimal := `["option_trades:QQQ",{"id":"01a02054-442d-7622-ade3-9aed1c92a585","underlying_symbol":"QQQ","executed_at":1787248788491,"size":1,"price":"abc","option_symbol":"X","tags":[],"expiry":"2026-08-20","option_type":"put","open_interest":1,"strike":"710","premium":"61.00","nbbo_bid":"0.61","nbbo_ask":"0.62"}]`
	badType := `["option_trades:QQQ",{"id":"01a02054-442d-7622-ade3-9aed1c92a586","underlying_symbol":"QQQ","executed_at":1787248788491,"size":1,"price":"1","option_type":"straddle","expiry":"2026-08-20","strike":"710","premium":"61.00","nbbo_bid":"0.61","nbbo_ask":"0.62"}]`
	badID := `["option_trades:QQQ",{"id":"nope","underlying_symbol":"QQQ","executed_at":1787248788491,"size":1,"price":"1","option_type":"put","expiry":"2026-08-20","strike":"710","premium":"61.00","nbbo_bid":"0.61","nbbo_ask":"0.62"}]`
	frames := []string{
		realAck,                         // ack → Acks
		`["price:QQQ",{"close":"710"}]`, // other channel → NonTrade
		// Verbatim control-record forms from the 2026-08-20 live capture:
		// 1-element arrays, detail field FIRST — the shape that exposed the
		// 40-byte-prefix sniff bug (all 38 session controls misclassified).
		`[{"detail":"universe=189 until=17:30:00","ev":"_capture","event":"start"}]`,
		`[{"detail":"wss://api.unusualwhales.com/socket?token=REDACTED","ev":"_capture","event":"connect"}]`,
		badDecimal, badType, badID, // → DecodeErrs
		`garbage`, // → DecodeErrs
	}
	_, _, stats := runPipeline(t, frames)
	want := DecodeStats{Frames: 8, Prints: 0, Acks: 1, Controls: 2, NonTrade: 1, DecodeErrs: 4}
	if stats != want {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}
}

func TestDedupeWindow(t *testing.T) {
	// The same print id submitted twice observes once and counts one dupe.
	obs, p, stats := runPipeline(t, []string{realDataFrame, realDataFrame})
	if stats.Prints != 2 { // decode-level count is pre-dedupe
		t.Fatalf("prints = %d", stats.Prints)
	}
	if len(obs.got) != 1 || p.Dupes.Load() != 1 || p.Processed.Load() != 1 {
		t.Errorf("observed=%d dupes=%d processed=%d, want 1/1/1", len(obs.got), p.Dupes.Load(), p.Processed.Load())
	}
}

func TestParseUUID(t *testing.T) {
	id, ok := parseUUID("01a02054-442d-7622-ade3-9aed1c92a583")
	if !ok || id[0] != 0x01 || id[15] != 0x83 {
		t.Errorf("parseUUID = %x ok=%v", id, ok)
	}
	for _, bad := range []string{"", "nope", "01a02054442d7622ade39aed1c92a583", "01a02054-442d-7622-ade3-9aed1c92a58g"} {
		if _, ok := parseUUID(bad); ok {
			t.Errorf("parseUUID(%q) accepted", bad)
		}
	}
}
