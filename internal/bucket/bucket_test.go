package bucket

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"buddy-flow/internal/classify"
	"buddy-flow/internal/ingest"
)

func trade(st *ingest.SymbolState, sipNs int64, price, size float64, conds ...int32) *ingest.Trade {
	t := &ingest.Trade{State: st, Price: price, Size: size, SipTs: sipNs}
	copy(t.Cond[:], conds)
	t.NCond = len(conds)
	return t
}

// twoSymbolStore builds a small store with known contents:
//
//	NVDA sec 100: regular 100@50.00, ISO 200@50.10, avg-price (dup) 1000@50.05
//	NVDA sec 101: sold-last 300@50.20 (continuous, lateness-monitored)
//	SPY  sec 100: regular 10@600.00; 3 quotes
func twoSymbolStore(t *testing.T) (*Store, *ingest.SymbolState, *ingest.SymbolState) {
	t.Helper()
	table := ingest.NewTable([]string{"NVDA", "SPY"})
	nvda, spy := table.Lookup("NVDA"), table.Lookup("SPY")
	s := NewStore()
	s.ObserveTrade(trade(nvda, 100_500_000_000, 50.00, 100))
	s.ObserveTrade(trade(nvda, 100_600_000_000, 50.10, 200, 14))    // ISO: Continuous
	s.ObserveTrade(trade(nvda, 100_700_000_000, 50.05, 1000, 2))    // Average Price: Duplicate
	s.ObserveTrade(trade(nvda, 101_100_000_000, 50.20, 300, 30))    // Sold Last: Continuous
	s.ObserveTrade(trade(spy, 100_999_999_999, 600.00, 10))
	for i := 0; i < 3; i++ {
		s.ObserveQuote(&ingest.Quote{State: spy, SipTs: 100_100_000_000})
	}
	return s, nvda, spy
}

func TestObserveAggregation(t *testing.T) {
	s, _, _ := twoSymbolStore(t)
	rows := s.Rows()
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	// Sorted (second, symbol): (100,NVDA), (100,SPY), (101,NVDA).
	b := rows[0].Bucket
	if rows[0].Sec != 100 || rows[0].Symbol != "NVDA" {
		t.Fatalf("row 0 = (%d,%s)", rows[0].Sec, rows[0].Symbol)
	}
	if b.Trades != 3 || b.Shares != 1300 || b.Quotes != 0 {
		t.Errorf("NVDA@100 totals: trades=%d shares=%v quotes=%d", b.Trades, b.Shares, b.Quotes)
	}
	wantDollars := 100*50.00 + 200*50.10 + 1000*50.05
	if b.Dollars != wantDollars {
		t.Errorf("NVDA@100 dollars = %v, want %v", b.Dollars, wantDollars)
	}
	// Last print in the second is the duplicate at .7s — the tape's last,
	// class-blind by design.
	if b.LastPrice != 50.05 || b.LastSipTs != 100_700_000_000 {
		t.Errorf("NVDA@100 last = %v @ %d", b.LastPrice, b.LastSipTs)
	}
	cont := b.Class[classify.Continuous]
	if cont.Trades != 2 || cont.Shares != 300 || cont.Dollars != 100*50.00+200*50.10 {
		t.Errorf("NVDA@100 continuous = %+v", cont)
	}
	dup := b.Class[classify.Duplicate]
	if dup.Trades != 1 || dup.Shares != 1000 || dup.Dollars != 1000*50.05 {
		t.Errorf("NVDA@100 duplicate = %+v", dup)
	}
	if q := rows[1].Bucket.Quotes; rows[1].Symbol != "SPY" || q != 3 {
		t.Errorf("SPY@100 quotes = %d, want 3", q)
	}
}

func TestUnknownTripwire(t *testing.T) {
	table := ingest.NewTable([]string{"NVDA"})
	nvda := table.Lookup("NVDA")
	s := NewStore()
	s.ObserveTrade(trade(nvda, 1e9, 10, 5, 99))     // unknown ID 99
	s.ObserveTrade(trade(nvda, 2e9, 10, 5, 99, 98)) // two unknown IDs on one print
	prints, ids := s.Unknown()
	if prints != 2 {
		t.Errorf("unknown prints = %d, want 2", prints)
	}
	if len(ids) != 2 || ids[0].ID != 98 || ids[0].Prints != 1 || ids[1].ID != 99 || ids[1].Prints != 2 {
		t.Errorf("unknown ids = %+v", ids)
	}
	// Quarantined: counted in totals and the UNKNOWN class, nowhere else.
	b := s.Rows()[0].Bucket
	if b.Class[classify.Unknown].Trades != 1 || b.Class[classify.Continuous].Trades != 0 {
		t.Errorf("unknown print misclassified: %+v", b.Class)
	}
}

func TestLatePrintAmendsTrueBucket(t *testing.T) {
	table := ingest.NewTable([]string{"NVDA"})
	nvda := table.Lookup("NVDA")
	s := NewStore()
	s.ObserveTrade(trade(nvda, 200_000_000_001, 51, 100))
	// TRF straggler: arrives after, executed earlier — lands in sec 100, and
	// its earlier SipTs must not steal LastPrice from a later print there.
	s.ObserveTrade(trade(nvda, 100_200_000_000, 50, 10))
	s.ObserveTrade(trade(nvda, 100_100_000_000, 49, 10))
	rows := s.Rows()
	if len(rows) != 2 || rows[0].Sec != 100 || rows[0].Bucket.Trades != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if lp := rows[0].Bucket.LastPrice; lp != 50 {
		t.Errorf("sec-100 last price = %v, want 50 (latest SipTs in bucket)", lp)
	}
}

func writeStore(t *testing.T, s *Store, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if _, err := s.WriteCSV(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWriteReadRoundTrip(t *testing.T) {
	s, _, _ := twoSymbolStore(t)
	path := writeStore(t, s, "s.csv")
	sess, err := ReadCSV(path)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Len() != 3 {
		t.Fatalf("read %d rows, want 3", sess.Len())
	}
	for _, r := range s.Rows() {
		if got := sess.Get(r.Symbol, r.Sec); got != r.Bucket {
			t.Errorf("(%s,%d): read %+v != stored %+v", r.Symbol, r.Sec, got, r.Bucket)
		}
	}
	// F13: absent row reads as zero bucket, not an error.
	if z := sess.Get("NVDA", 42); z != (Bucket{}) {
		t.Errorf("absent row = %+v, want zero", z)
	}
	if z := sess.Get("TSLA", 100); z != (Bucket{}) {
		t.Errorf("absent symbol = %+v, want zero", z)
	}
}

// TestReadGzip: D4 gzips session files after close; the reader must read
// them as-is.
func TestReadGzip(t *testing.T) {
	s, _, _ := twoSymbolStore(t)
	plain := writeStore(t, s, "s.csv")
	data, err := os.ReadFile(plain)
	if err != nil {
		t.Fatal(err)
	}
	gzPath := plain + ".gz"
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gzPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	sess, err := ReadCSV(gzPath)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Len() != 3 {
		t.Fatalf("gz read %d rows, want 3", sess.Len())
	}
	for _, r := range s.Rows() {
		if got := sess.Get(r.Symbol, r.Sec); got != r.Bucket {
			t.Errorf("(%s,%d): gz read %+v != stored %+v", r.Symbol, r.Sec, got, r.Bucket)
		}
	}
}

func TestWriterDeterminism(t *testing.T) {
	s, _, _ := twoSymbolStore(t)
	p1 := writeStore(t, s, "a.csv")
	p2 := writeStore(t, s, "b.csv")
	b1, err := os.ReadFile(p1)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := os.ReadFile(p2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b1, b2) {
		t.Error("two writes of the same store differ")
	}
}

// TestDeriveMinute is done-when #2: a derived minute exactly equals the sum
// of its sixty seconds.
func TestDeriveMinute(t *testing.T) {
	table := ingest.NewTable([]string{"NVDA"})
	nvda := table.Lookup("NVDA")
	s := NewStore()
	// Minute [120,180): trades at seconds 120, 137, 179; one outside at 180.
	var wantTrades int64
	var wantShares, wantDollars float64
	for i, sec := range []int64{120, 137, 179} {
		price, size := 50.0+float64(i), 100.0*float64(i+1)
		s.ObserveTrade(trade(nvda, sec*1e9+5e8, price, size))
		wantTrades++
		wantShares += size
		wantDollars += price * size
	}
	s.ObserveTrade(trade(nvda, 180*1e9, 99, 1))
	s.ObserveQuote(&ingest.Quote{State: nvda, SipTs: 145 * 1e9})

	path := writeStore(t, s, "m.csv")
	sess, err := ReadCSV(path)
	if err != nil {
		t.Fatal(err)
	}
	min, err := sess.DeriveMinute("NVDA", 120)
	if err != nil {
		t.Fatal(err)
	}
	if min.Trades != wantTrades || min.Shares != wantShares || min.Dollars != wantDollars || min.Quotes != 1 {
		t.Errorf("minute = %+v", min)
	}
	if min.LastPrice != 52 || min.LastSipTs != 179*1e9+5e8 {
		t.Errorf("minute last = %v @ %d", min.LastPrice, min.LastSipTs)
	}
	// Independent check: sum the sixty Gets.
	var sum Bucket
	for sec := int64(120); sec < 180; sec++ {
		b := sess.Get("NVDA", sec)
		sum.add(&b)
	}
	if sum != min {
		t.Errorf("Σ seconds %+v != DeriveMinute %+v", sum, min)
	}
	if _, err := sess.DeriveMinute("NVDA", 121); err == nil {
		t.Error("non-aligned minuteSec accepted")
	}
}
