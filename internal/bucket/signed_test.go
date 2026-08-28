package bucket

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"buddy-flow/internal/ingest"
)

// signedStore drives a quote and prints through the real pipeline so the
// classification happens against the book as of arrival, the way live and
// replay do. Contents (one symbol X, book 10.00/10.20 set at sec 100):
//
//	sec 100: 100@10.20 (at ask)            → AskSide
//	sec 100: 50@10.00 (at bid)             → BidSide
//	sec 100: 10@10.10 (inside, ref 10.00)  → tick up → AskSide
//	sec 101: 20@10.05 (inside, ref 10.10)  → tick down → BidSide
//	sec 101: 30@10.05 (inside, ref 10.10)  → tick down → BidSide (look-back)
//	sec 101: 40@10.20 late (PartTs=0)      → Late, unclassified
//	sec 101: 60@10.20 NON_PRICE_FORMING    → ineligible (not in the tally)
//	sec 101: 70@10.20 BLOCK (9)            → eligible → AskSide
func signedStore(t *testing.T) (*Store, *ingest.SymbolState) {
	t.Helper()
	table := ingest.NewTable([]string{"X"})
	st := table.Lookup("X")
	s := NewStore()
	p := ingest.NewPipeline(table, 64)
	p.SetObserver(s)
	done := make(chan struct{})
	go func() { p.Run(); close(done) }()
	q := ingest.Msg{Kind: ingest.KindQuote}
	q.Quote = ingest.Quote{State: st, BidPrice: 10.00, AskPrice: 10.20, SipTs: 100_000_000_000}
	p.Submit(q)
	// onTime: PartTs == SipTs; late: PartTs == 0.
	tr := func(sipNs int64, price, size float64, onTime bool, conds ...int32) {
		m := ingest.Msg{Kind: ingest.KindTrade}
		m.Trade = *trade(st, sipNs, price, size, conds...)
		if onTime {
			m.Trade.PartTs = sipNs
		}
		p.Submit(m)
	}
	tr(100_100_000_000, 10.20, 100, true)
	tr(100_200_000_000, 10.00, 50, true)
	tr(100_300_000_000, 10.10, 10, true)
	tr(101_100_000_000, 10.05, 20, true)
	tr(101_200_000_000, 10.05, 30, true)
	tr(101_300_000_000, 10.20, 40, false)
	tr(101_400_000_000, 10.20, 60, true, 22)
	tr(101_500_000_000, 10.20, 70, true, 9)
	p.Close()
	<-done
	return s, st
}

func TestSignedAggregationAndWindow(t *testing.T) {
	s, st := signedStore(t)
	b100 := s.Window(st, 100, 101)
	if b100.AskSide != (ClassAgg{Trades: 2, Shares: 110, Dollars: float64(100*10.20) + float64(10*10.10)}) {
		t.Errorf("sec 100 AskSide = %+v", b100.AskSide)
	}
	if b100.BidSide != (ClassAgg{Trades: 1, Shares: 50, Dollars: float64(50 * 10.00)}) {
		t.Errorf("sec 100 BidSide = %+v", b100.BidSide)
	}
	if b100.TickRule != 1 || b100.Late != 0 {
		t.Errorf("sec 100 tick=%d late=%d", b100.TickRule, b100.Late)
	}
	b101 := s.Window(st, 101, 102)
	if b101.AskSide != (ClassAgg{Trades: 1, Shares: 70, Dollars: float64(70 * 10.20)}) {
		t.Errorf("sec 101 AskSide = %+v", b101.AskSide)
	}
	if b101.BidSide != (ClassAgg{Trades: 2, Shares: 50, Dollars: float64(20*10.05) + float64(30*10.05)}) {
		t.Errorf("sec 101 BidSide = %+v", b101.BidSide)
	}
	if b101.TickRule != 2 || b101.Late != 1 {
		t.Errorf("sec 101 tick=%d late=%d", b101.TickRule, b101.Late)
	}
	// Window sums the new fields across seconds.
	w := s.Window(st, 100, 102)
	if w.AskSide.Trades != 3 || w.BidSide.Trades != 3 || w.TickRule != 3 || w.Late != 1 {
		t.Errorf("window: %+v", w)
	}
	if w.AskSide.Shares != 180 || w.BidSide.Shares != 100 {
		t.Errorf("window shares: ask=%v bid=%v", w.AskSide.Shares, w.BidSide.Shares)
	}
	// Unclassified derived = Eligible − Ask − Bid: eligible = 7 (all but the
	// NON_PRICE_FORMING print), classified 6, so 1 (the late print).
	e := w.Eligible()
	if e.Trades != 7 || e.Shares != 320 {
		t.Errorf("eligible: %+v", e)
	}
	u := w.Unclassified()
	if u.Trades != 1 || u.Shares != 40 || u.Dollars != float64(40*10.20) {
		t.Errorf("unclassified: %+v", u)
	}
	if u.Trades != e.Trades-w.AskSide.Trades-w.BidSide.Trades {
		t.Errorf("unclassified identity broken")
	}
	// Top-level and class totals are untouched by classification: 8 prints.
	if w.Trades != 8 || w.Shares != 380 {
		t.Errorf("totals: trades=%d shares=%v", w.Trades, w.Shares)
	}
	// Store-wide tally matches.
	if got := s.Aggressor(); got.Eligible != 7 || got.Ask != 3 || got.Bid != 3 || got.Tick != 3 || got.Late != 1 || got.Unclassified() != 1 {
		t.Errorf("Aggressor() = %+v", got)
	}
}

func TestSignedCSVRoundTripAndHeader(t *testing.T) {
	s, _ := signedStore(t)
	path := writeStore(t, s, "signed.csv")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	head := strings.SplitN(string(raw), "\n", 2)[0]
	for _, col := range []string{"ask_trades", "ask_shares", "ask_dollars", "bid_trades", "bid_shares", "bid_dollars", "tick_rule", "late"} {
		if !strings.Contains(","+head+",", ","+col+",") {
			t.Errorf("header missing %q: %s", col, head)
		}
	}
	for _, banned := range []string{"buy", "sell"} {
		if strings.Contains(strings.ToLower(head), banned) {
			t.Errorf("header contains %q — scope law", banned)
		}
	}
	sess, err := ReadCSV(path)
	if err != nil {
		t.Fatal(err)
	}
	if !sess.HasSigned {
		t.Fatal("HasSigned = false on a file written with signed columns")
	}
	for _, r := range s.Rows() {
		if got := sess.Get(r.Symbol, r.Sec); got != r.Bucket {
			t.Errorf("(%s,%d): read %+v != stored %+v", r.Symbol, r.Sec, got, r.Bucket)
		}
	}
	m, err := sess.DeriveMinute("X", 60)
	if err != nil {
		t.Fatal(err)
	}
	if m.AskSide.Trades != 3 || m.BidSide.Trades != 3 || m.TickRule != 3 || m.Late != 1 {
		t.Errorf("DeriveMinute signed fields: %+v", m)
	}
}

// TestOldCSVLoadsWithoutSigned: a pre-MO-2 bucket file (1.4 columns only)
// loads with HasSigned=false and every 1.4 field intact — the
// column-compatibility contract (docs/backlog.md).
func TestOldCSVLoadsWithoutSigned(t *testing.T) {
	s, _ := signedStore(t)
	path := writeStore(t, s, "new.csv")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Project the file onto the 1.4 columns to produce an old-format file.
	base := len(baseHeader())
	var old []string
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		f := strings.Split(line, ",")
		old = append(old, strings.Join(f[:base], ","))
	}
	oldPath := filepath.Join(t.TempDir(), "old.csv")
	if err := os.WriteFile(oldPath, []byte(strings.Join(old, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sess, err := ReadCSV(oldPath)
	if err != nil {
		t.Fatalf("old-format file must load: %v", err)
	}
	if sess.HasSigned {
		t.Error("HasSigned = true on a file with no signed columns")
	}
	for _, r := range s.Rows() {
		got := sess.Get(r.Symbol, r.Sec)
		want := r.Bucket
		want.AskSide, want.BidSide, want.TickRule, want.Late = ClassAgg{}, ClassAgg{}, 0, 0
		if got != want {
			t.Errorf("(%s,%d): read %+v != base %+v", r.Symbol, r.Sec, got, want)
		}
	}
	// A partial signed set is a corrupt header, not an old file.
	partial := make([]string, len(old))
	for i, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		f := strings.Split(line, ",")
		partial[i] = strings.Join(f[:base+1], ",")
	}
	partialPath := filepath.Join(t.TempDir(), "partial.csv")
	if err := os.WriteFile(partialPath, []byte(strings.Join(partial, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCSV(partialPath); err == nil {
		t.Error("partial signed column set must be an error")
	}
}
