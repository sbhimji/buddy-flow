package optclassify

import (
	"os"
	"path/filepath"
	"testing"

	"buddy-flow/internal/optingest"
)

const weightsPath = "../../docs/foundations/options-weights-v1.json"

func defaults(t *testing.T) Weights {
	t.Helper()
	w, hash, err := LoadWeights(weightsPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(hash) != 12 {
		t.Fatalf("hash = %q, want 12 hex chars", hash)
	}
	return w
}

func classifier(t *testing.T) *Classifier {
	t.Helper()
	c, err := NewClassifier(defaults(t), "2026-08-24")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// trade builds a baseline print: ask-side call, ATM-avoiding strike (OTM
// near = weight 1.0), long-dated, small vs OI, not a sweep — every factor
// neutral except what a test overrides.
func trade(mod func(*optingest.OptionTrade)) *optingest.OptionTrade {
	tr := &optingest.OptionTrade{
		Underlying: "QQQ", ExecNs: 1, Expiry: "2027-01-15", IsCall: true,
		Strike: 110, Size: 1, Price: 1, Premium: 100, OpenInterest: 1000,
		UPrice: 100, UPriceOK: true, SideTag: "ask_side", TradeCode: "auto",
	}
	if mod != nil {
		mod(tr)
	}
	return tr
}

func TestSignAndSignedNotional(t *testing.T) {
	c := classifier(t)
	cases := []struct {
		side   string
		isCall bool
		want   float64
	}{
		{"ask_side", true, +100}, {"bid_side", true, -100},
		{"ask_side", false, -100}, {"bid_side", false, +100},
		{"mid_side", true, 0}, {"no_side", true, 0}, {"", true, 0},
	}
	for _, cse := range cases {
		p, ok := c.Classify(trade(func(tr *optingest.OptionTrade) {
			tr.SideTag = cse.side
			tr.IsCall = cse.isCall
		}))
		if !ok || p.SignedNotional != cse.want {
			t.Errorf("side=%q call=%v: signed_notional = %v ok=%v, want %v", cse.side, cse.isCall, p.SignedNotional, ok, cse.want)
		}
	}
}

// The vendor premium is authoritative — never recomputed as size×price×100.
func TestPremiumVerbatim(t *testing.T) {
	c := classifier(t)
	p, _ := c.Classify(trade(func(tr *optingest.OptionTrade) {
		tr.Premium, tr.Size, tr.Price = 10, 4, 5 // size×price×100 = 2000 ≠ 10
	}))
	if p.SignedNotional != 10 {
		t.Errorf("signed_notional = %v, want the verbatim premium 10", p.SignedNotional)
	}
}

func TestMoneynessBands(t *testing.T) {
	c := classifier(t)
	cases := []struct {
		name   string
		isCall bool
		strike float64 // UPrice fixed at 100
		want   float64
	}{
		{"call ATM upper edge f=+0.05", true, 105, 0.5},
		{"call ATM lower edge f=-0.05", true, 95, 0.5},
		{"call OTM near f=0.10", true, 110, 1.0},
		{"call OTM edge f=0.15", true, 115, 1.0},
		{"call OTM far f=0.16", true, 116, 0.5},
		{"call ITM near f=-0.10", true, 90, 0.5},
		{"call ITM edge f=-0.15", true, 85, 0.5},
		{"call deep ITM f=-0.16", true, 84, 0.1},
		{"put OTM near (strike below spot)", false, 90, 1.0},
		{"put deep ITM (strike far above spot)", false, 120, 0.1},
		{"put ATM", false, 104, 0.5},
	}
	for _, cse := range cases {
		p, ok := c.Classify(trade(func(tr *optingest.OptionTrade) {
			tr.IsCall = cse.isCall
			tr.Strike = cse.strike
		}))
		if !ok || p.ConvictionWeight != cse.want {
			t.Errorf("%s: weight = %v ok=%v, want %v", cse.name, p.ConvictionWeight, ok, cse.want)
		}
	}
}

func TestMoneynessNoUnderlyingPrice(t *testing.T) {
	c := classifier(t)
	p, ok := c.Classify(trade(func(tr *optingest.OptionTrade) {
		tr.UPriceOK = false // index name (D17)
		tr.Strike = 84      // would be deep ITM if measurable
	}))
	if !ok || p.ConvictionWeight != 1.0 || !p.NoUPrice {
		t.Errorf("no-uprice: weight = %v NoUPrice=%v, want 1.0/true", p.ConvictionWeight, p.NoUPrice)
	}
}

func TestDTEWeight(t *testing.T) {
	c := classifier(t) // session 2026-08-24
	cases := []struct {
		expiry string
		want   float64
	}{
		{"2026-08-24", 1.25}, // 0DTE
		{"2026-10-08", 1.25}, // exactly 45 days
		{"2026-10-09", 1.0},  // 46 days
	}
	for _, cse := range cases {
		p, ok := c.Classify(trade(func(tr *optingest.OptionTrade) { tr.Expiry = cse.expiry }))
		if !ok || p.ConvictionWeight != cse.want {
			t.Errorf("expiry %s: weight = %v ok=%v, want %v", cse.expiry, p.ConvictionWeight, ok, cse.want)
		}
	}
	if _, ok := c.Classify(trade(func(tr *optingest.OptionTrade) { tr.Expiry = "garbage" })); ok {
		t.Error("unparseable expiry must be unclassifiable (K3), got ok=true")
	}
}

func TestSizeOIWeight(t *testing.T) {
	c := classifier(t)
	cases := []struct {
		size, oi int64
		want     float64
		zeroOI   bool
	}{
		{25, 100, 1.0, false}, // exactly frac×OI: strict inequality → no boost
		{26, 100, 1.5, false},
		{1, 0, 1.0, true}, // D16: OI=0 → neutral + flag
	}
	for _, cse := range cases {
		p, ok := c.Classify(trade(func(tr *optingest.OptionTrade) {
			tr.Size, tr.OpenInterest = cse.size, cse.oi
		}))
		if !ok || p.ConvictionWeight != cse.want || p.ZeroOI != cse.zeroOI {
			t.Errorf("size=%d oi=%d: weight=%v zeroOI=%v ok=%v, want %v/%v", cse.size, cse.oi, p.ConvictionWeight, p.ZeroOI, ok, cse.want, cse.zeroOI)
		}
	}
}

func TestSweepCodes(t *testing.T) {
	c := classifier(t)
	cases := map[string]bool{
		"slan": true, "slai": true, "slcn": true, "slci": true, "slft": true,
		"slan,isoi": true, "isoi,slan": true, "auto": false, "isoi": false, "": false,
		"mlet,mlat": false,
	}
	for code, want := range cases {
		p, _ := c.Classify(trade(func(tr *optingest.OptionTrade) { tr.TradeCode = code }))
		if p.IsSweep != want {
			t.Errorf("trade_code %q: IsSweep = %v, want %v", code, p.IsSweep, want)
		}
		wantW := 1.0
		if want {
			wantW = 1.5
		}
		if p.ConvictionWeight != wantW {
			t.Errorf("trade_code %q: weight = %v, want %v", code, p.ConvictionWeight, wantW)
		}
	}
}

// The SECTOR §3.1 acceptance fixture: a deep-ITM-heavy day produces
// near-zero conviction despite huge premium — the taxonomy's whole point.
func TestDeepITMHeavyDayNearZeroConviction(t *testing.T) {
	c := classifier(t)
	var notional, weighted float64
	for i := 0; i < 100; i++ {
		p, ok := c.Classify(trade(func(tr *optingest.OptionTrade) {
			tr.Strike = 50 // f = -0.5: deep ITM synthetic-stock plumbing
			tr.Premium = 1_000_000
		}))
		if !ok {
			t.Fatal("fixture print unclassifiable")
		}
		notional += p.SignedNotional
		weighted += p.Weighted
	}
	if notional != 100_000_000 {
		t.Fatalf("notional = %v", notional)
	}
	if ratio := weighted / notional; ratio > 0.15 {
		t.Errorf("deep-ITM day conviction ratio = %v, want near-zero (≤0.15)", ratio)
	}
}

func TestConvictionWeightComposes(t *testing.T) {
	c := classifier(t)
	// Sweep(1.5) × OTM-near(1.0) × 0DTE(1.25) × size>frac·OI(1.5) = 2.8125
	p, ok := c.Classify(trade(func(tr *optingest.OptionTrade) {
		tr.TradeCode = "slan"
		tr.Expiry = "2026-08-24"
		tr.Size, tr.OpenInterest = 26, 100
	}))
	if !ok || p.ConvictionWeight != 1.5*1.0*1.25*1.5 {
		t.Errorf("composed weight = %v ok=%v, want 2.8125", p.ConvictionWeight, ok)
	}
	if p.Weighted != p.SignedNotional*p.ConvictionWeight {
		t.Errorf("Weighted = %v, want SignedNotional×ConvictionWeight", p.Weighted)
	}
}

func TestHashChangesWithAnyByte(t *testing.T) {
	raw, err := os.ReadFile(weightsPath)
	if err != nil {
		t.Fatal(err)
	}
	_, h1, err := LoadWeights(weightsPath)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	mutated := filepath.Join(dir, "w.json")
	// Whitespace-only change: semantically identical JSON, different bytes —
	// the stamp is over bytes, so even this forces a rebuild (deliberate).
	if err := os.WriteFile(mutated, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	_, h2, err := LoadWeights(mutated)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Error("hash unchanged after byte change")
	}
}

func TestLoadWeightsRefusals(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) string {
		p := filepath.Join(dir, "w.json")
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := map[string]string{
		"missing version": `{"sweep_weight":1.5,"moneyness":{"atm_band_pct":0.05,"atm_weight":0.5,"otm_near_max_pct":0.15,"otm_near_weight":1,"otm_far_weight":0.5,"itm_near_weight":0.5,"deep_itm_min_pct":0.15,"deep_itm_weight":0.1,"no_underlying_price_weight":1},"dte":{"max_days":45,"weight":1.25},"size_oi":{"frac":0.25,"weight":1.5,"zero_oi_weight":1}}`,
		"zero weight":     `{"version":"v","sweep_weight":0,"moneyness":{"atm_band_pct":0.05,"atm_weight":0.5,"otm_near_max_pct":0.15,"otm_near_weight":1,"otm_far_weight":0.5,"itm_near_weight":0.5,"deep_itm_min_pct":0.15,"deep_itm_weight":0.1,"no_underlying_price_weight":1},"dte":{"max_days":45,"weight":1.25},"size_oi":{"frac":0.25,"weight":1.5,"zero_oi_weight":1}}`,
		"band order":      `{"version":"v","sweep_weight":1.5,"moneyness":{"atm_band_pct":0.2,"atm_weight":0.5,"otm_near_max_pct":0.15,"otm_near_weight":1,"otm_far_weight":0.5,"itm_near_weight":0.5,"deep_itm_min_pct":0.15,"deep_itm_weight":0.1,"no_underlying_price_weight":1},"dte":{"max_days":45,"weight":1.25},"size_oi":{"frac":0.25,"weight":1.5,"zero_oi_weight":1}}`,
		"not json":        `hello`,
	}
	for name, cfg := range cases {
		if _, _, err := LoadWeights(write(cfg)); err == nil {
			t.Errorf("%s: accepted, want refusal", name)
		}
	}
}
