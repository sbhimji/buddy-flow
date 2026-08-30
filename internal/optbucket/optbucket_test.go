package optbucket

import (
	"os"
	"path/filepath"
	"testing"

	"buddy-flow/internal/optclassify"
	"buddy-flow/internal/optingest"
	"buddy-flow/internal/session"
)

const weightsPath = "../../docs/foundations/options-weights-v1.json"

func newTestStore(t *testing.T) *Store {
	t.Helper()
	w, hash, err := optclassify.LoadWeights(weightsPath)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(w, "options-weights-v1@"+hash)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// anchor returns ns at 09:30:00 ET on 2026-08-24 plus an offset in seconds.
func anchor(t *testing.T, plusSec int64) int64 {
	t.Helper()
	sec, err := session.BucketStart("2026-08-24", session.OpenMinute)
	if err != nil {
		t.Fatal(err)
	}
	return (sec + plusSec) * 1_000_000_000
}

func print(execNs int64, mod func(*optingest.OptionTrade)) *optingest.OptionTrade {
	tr := &optingest.OptionTrade{
		Underlying: "QQQ", ExecNs: execNs, Expiry: "2027-01-15", IsCall: true,
		Strike: 110, Size: 1, Price: 1, Premium: 100, OpenInterest: 1000,
		UPrice: 100, UPriceOK: true, SideTag: "ask_side", TradeCode: "auto",
	}
	if mod != nil {
		mod(tr)
	}
	return tr
}

// Three hand-computed prints in one bucket: an aggressive sweep call, a
// plain put at the bid, a zero-sign mid call.
func fillHandComputed(t *testing.T, s *Store) {
	ns := anchor(t, 0)
	// weight = sweep 1.5 × OTM-near 1.0 × 0DTE 1.25 × size>frac·OI 1.5 = 2.8125
	s.ObserveOptionTrade(print(ns, func(tr *optingest.OptionTrade) {
		tr.TradeCode = "slan"
		tr.Expiry = "2026-08-24"
		tr.Size, tr.OpenInterest = 26, 100
	}))
	// put at the bid: sign −1 × put −1 = +50; every weight neutral = 1.0
	s.ObserveOptionTrade(print(ns, func(tr *optingest.OptionTrade) {
		tr.IsCall = false
		tr.Strike = 90
		tr.SideTag = "bid_side"
		tr.Premium = 50
		tr.Size = 2
	}))
	// mid call: zero sign, premium still sliced
	s.ObserveOptionTrade(print(ns, func(tr *optingest.OptionTrade) {
		tr.SideTag = "mid_side"
		tr.Premium = 30
	}))
}

func TestHandComputedBucket(t *testing.T) {
	s := newTestStore(t)
	fillHandComputed(t, s)
	sec := anchor(t, 0) / 1_000_000_000
	b := s.Get("QQQ", sec)
	if b == nil {
		t.Fatal("bucket missing")
	}
	if b.Prints != 3 || b.Contracts != 29 {
		t.Errorf("prints=%d contracts=%d, want 3/29", b.Prints, b.Contracts)
	}
	if b.PremCallAsk != 100 || b.PremPutBid != 50 || b.PremCallMid != 30 || b.PremSweep != 100 {
		t.Errorf("slices = ca=%v pb=%v cm=%v sweep=%v", b.PremCallAsk, b.PremPutBid, b.PremCallMid, b.PremSweep)
	}
	if want := 100*2.8125 + 50*1.0; b.NetConviction != want {
		t.Errorf("net_conviction = %v, want %v", b.NetConviction, want)
	}
	if b.SignedNotional() != 150 { // (100−0) − (0−50)
		t.Errorf("signed_notional = %v, want 150", b.SignedNotional())
	}
	if tel := s.Telemetry(); tel.SweepPrints != 1 || tel.SidePrints[SideAsk] != 1 || tel.SidePrints[SideBid] != 1 || tel.SidePrints[SideZero] != 1 {
		t.Errorf("telemetry = %+v", s.Telemetry())
	}
}

func TestUnclassifiableCounted(t *testing.T) {
	s := newTestStore(t)
	s.ObserveOptionTrade(print(anchor(t, 0), func(tr *optingest.OptionTrade) { tr.Expiry = "garbage" }))
	if s.Telemetry().Unclassifiable != 1 {
		t.Errorf("unclassifiable = %d, want 1", s.Telemetry().Unclassifiable)
	}
	if p, _ := s.Totals(); p != 0 {
		t.Errorf("prints = %d, want 0 (unclassifiable never buckets)", p)
	}
}

func TestDeriveMinuteAndWindow(t *testing.T) {
	s := newTestStore(t)
	for i := int64(0); i < 60; i++ {
		s.ObserveOptionTrade(print(anchor(t, i), nil))
	}
	s.ObserveOptionTrade(print(anchor(t, 60), nil)) // next minute — excluded
	minSec := anchor(t, 0) / 1_000_000_000
	win := s.Window("QQQ", minSec, minSec+60)
	if win.Prints != 60 || win.PremCallAsk != 6000 {
		t.Errorf("window = %d prints $%v, want 60/$6000", win.Prints, win.PremCallAsk)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "d.csv")
	if _, err := s.WriteCSV(path); err != nil {
		t.Fatal(err)
	}
	sess, err := ReadCSV(path, "")
	if err != nil {
		t.Fatal(err)
	}
	m, err := sess.DeriveMinute("QQQ", minSec)
	if err != nil {
		t.Fatal(err)
	}
	if m.Prints != 60 || m.PremCallAsk != 6000 {
		t.Errorf("derived minute = %d prints $%v, want 60/$6000", m.Prints, m.PremCallAsk)
	}
	if _, err := sess.DeriveMinute("QQQ", minSec+1); err == nil {
		t.Error("unaligned minute start accepted")
	}
}

func TestWriteReadRoundTripAndDeterminism(t *testing.T) {
	s := newTestStore(t)
	fillHandComputed(t, s)
	dir := t.TempDir()
	p1, p2 := filepath.Join(dir, "a.csv"), filepath.Join(dir, "b.csv")
	if _, err := s.WriteCSV(p1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCSV(p2); err != nil {
		t.Fatal(err)
	}
	b1, _ := os.ReadFile(p1)
	b2, _ := os.ReadFile(p2)
	if string(b1) != string(b2) {
		t.Fatal("two writes differ")
	}

	sess, err := ReadCSV(p1, s.weightsName)
	if err != nil {
		t.Fatal(err)
	}
	sec := anchor(t, 0) / 1_000_000_000
	got := sess.Buckets["QQQ"][sec]
	want := s.Get("QQQ", sec)
	if got == nil || *got != *want {
		t.Errorf("round-trip bucket = %+v, want %+v", got, want)
	}
	if sess.WeightsName != s.weightsName {
		t.Errorf("stamp = %q", sess.WeightsName)
	}
}

func TestReadRejectsStaleWeights(t *testing.T) {
	s := newTestStore(t)
	fillHandComputed(t, s)
	path := filepath.Join(t.TempDir(), "d.csv")
	if _, err := s.WriteCSV(path); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCSV(path, "options-weights-v1@000000000000"); err == nil {
		t.Fatal("stale weights stamp accepted — D2a analog violated")
	}
}

func TestReadRejectsMissingColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.csv")
	content := "# options buckets (mini-spec 7.4): weights=x@y; unclassifiable=0\nsecond,symbol,prints\n1,QQQ,1\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCSV(path, ""); err == nil {
		t.Fatal("file missing required columns accepted")
	}
}
