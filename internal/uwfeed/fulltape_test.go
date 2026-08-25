package uwfeed

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"buddy-flow/internal/optingest"
)

func TestParsePGTimestampMs(t *testing.T) {
	cases := map[string]int64{
		"2026-08-17 13:30:00.00714+00":  1786973400007, // 2026-08-17T13:30:00.007Z
		"2026-08-17 13:30:00+00":        1786973400000,
		"2026-08-17T13:30:00.007140Z":   1786973400007,
		"2026-08-17 13:30:00.999999+00": 1786973400999,
	}
	for in, want := range cases {
		got, ok := parsePGTimestampMs(in)
		if !ok || got != want {
			t.Errorf("parsePGTimestampMs(%q) = %d ok=%v, want %d", in, got, ok, want)
		}
	}
	if _, ok := parsePGTimestampMs("not a time"); ok {
		t.Error("garbage timestamp accepted")
	}
}

func TestPGArrayToJSON(t *testing.T) {
	cases := map[string]string{
		"{ask_side,bullish}": `["ask_side","bullish"]`,
		"{}":                 `[]`,
		"":                   `[]`,
		`{"no_side"}`:        `["no_side"]`,
	}
	for in, want := range cases {
		if got := string(pgArrayToJSON(in)); got != want {
			t.Errorf("pgArrayToJSON(%q) = %s, want %s", in, got, want)
		}
	}
}

// A synthetic 3-row zip: one non-universe row, one canceled universe row,
// one good universe row with the real CSV column layout.
func TestStreamFullTapeSyntheticZip(t *testing.T) {
	hdr := "id,underlying_symbol,executed_at,nbbo_bid,nbbo_ask,size,price,option_chain_id,alert_score,created_at,report_flags,tags,expiry,option_type,open_interest,strike,premium,aggregated_trade_id,volume,underlying_price,ewma_nbbo_ask,ewma_nbbo_bid,implied_volatility,delta,theta,gamma,vega,rho,theo,upstream_condition_detail,market_center_locate,canceled,trade_id,exchange,ask_vol,bid_vol,no_side_vol,mid_vol,multi_vol,stock_multi_vol\n"
	rows := hdr +
		"01a00fea-2bfa-7fa2-8e44-be414b76be74,ZZZZ,2026-08-17 13:30:00.00714+00,5.70,5.95,1,5.95,ZZZZ280121C00015000,,2026-08-17 13:30:00.05863+00,{},\"{ask_side,bullish}\",2028-01-21,call,5613,15,595.00,,1,17.44,5.95,5.70,0.5,0.7,-0.004,0.02,0.06,0.09,5.95,auto,8,f,0,XPHO,1,0,0,0,0,0\n" +
		"01a00fea-2bfa-7fa2-8e44-be414b76be75,QQQ,2026-08-17 13:30:01.00000+00,0.61,0.62,1,0.61,QQQ260820P00710000,,2026-08-17 13:30:01.05+00,{},\"{bid_side,bearish,etf}\",2026-08-20,put,5861,710,61.00,,1,710.71,0.62,0.61,0.2,-0.3,-5,0.1,0.04,-0.0006,0.61,auto,8,t,0,AMXO,1,0,0,0,0,0\n" +
		"01a00fea-2bfa-7fa2-8e44-be414b76be76,QQQ,2026-08-17 13:30:02.50000+00,0.61,0.62,3,0.61,QQQ260820P00710000,,2026-08-17 13:30:02.55+00,{},\"{bid_side,bearish,etf}\",2026-08-20,put,5861,710,183.00,,4,710.71,0.62,0.61,0.2,-0.3,-5,0.1,0.04,-0.0006,0.61,\"slan,isoi\",8,f,0,AMXO,1,0,0,0,0,0\n"

	dir := t.TempDir()
	zipPath := filepath.Join(dir, "2026-08-17.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	entry, _ := zw.Create("2026-08-17-option_trades.csv")
	entry.Write([]byte(rows))
	zw.Close()
	f.Close()

	p := optingest.NewPipeline(16)
	obs := &capturingObserver{}
	p.SetObserver(obs)
	done := make(chan struct{})
	go func() { p.Run(); close(done) }()
	st, err := StreamFullTape(zipPath, map[string]bool{"QQQ": true}, p)
	p.Close()
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if st.Rows != 3 || st.Universe != 2 || st.Canceled != 1 || st.OtherSymbol != 1 || st.DecodeErrs != 0 {
		t.Errorf("stats = %+v", st)
	}
	if len(obs.got) != 1 {
		t.Fatalf("observed %d prints, want 1", len(obs.got))
	}
	tr := obs.got[0]
	if tr.Underlying != "QQQ" || tr.OptionSymbol != "QQQ260820P00710000" || tr.IsCall || tr.Size != 3 ||
		tr.Premium != 183 || tr.Strike != 710 || tr.OpenInterest != 5861 || !tr.UPriceOK || tr.UPrice != 710.71 ||
		tr.SideTag != "bid_side" || tr.TradeCode != "slan,isoi" || tr.Expiry != "2026-08-20" {
		t.Errorf("print = %+v", tr)
	}
	if tr.ExecNs != 1786973402500*1_000_000 {
		t.Errorf("ExecNs = %d (Postgres timestamp → ns wrong)", tr.ExecNs)
	}
}
