package bucket

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"buddy-flow/internal/ingest"
)

// signedStore drives a quote and prints through the real pipeline so the
// classification happens against the book as of arrival, the way live and
// capture replay do. Contents (one symbol X, book 10.00/10.20 at sec 99,
// mid 10.10):
//
//	sec 100: 100@10.20 (at ask)              → AskSide, quote
//	sec 100: 50@10.00 (at bid)               → BidSide, quote
//	sec 100: 10@10.12 (inside, above mid)    → AskSide, midpoint
//	sec 101: 20@10.10 (at mid, ref 10.12)    → BidSide, tick
//	sec 101: 30@10.10 (at mid, ref 10.12)    → BidSide, tick (look-back)
//	sec 101: 40@10.20 late (PartTs=0)        → Late, unclassified
//	sec 101: 60@10.20 NON_PRICE_FORMING      → ineligible
//	sec 101: 70@10.20 BLOCK (9)              → ineligible (never signed)
func signedStore(t *testing.T, timeOrdered bool) (*Store, *ingest.SymbolState) {
	t.Helper()
	table := ingest.NewTable([]string{"X"})
	st := table.Lookup("X")
	s := NewStore()
	if timeOrdered {
		s = NewTimeOrderedStore()
	}
	p := ingest.NewPipeline(table, 64)
	p.SetObserver(s)
	done := make(chan struct{})
	go func() { p.Run(); close(done) }()
	q := ingest.Msg{Kind: ingest.KindQuote}
	q.Quote = ingest.Quote{State: st, BidPrice: 10.00, AskPrice: 10.20, SipTs: 99_000_000_000}
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
	tr(100_300_000_000, 10.12, 10, true)
	tr(101_100_000_000, 10.10, 20, true)
	tr(101_200_000_000, 10.10, 30, true)
	tr(101_300_000_000, 10.20, 40, false)
	tr(101_400_000_000, 10.20, 60, true, 22)
	tr(101_500_000_000, 10.20, 70, true, 9)
	p.Close()
	<-done
	return s, st
}

func TestSignedAggregationAndWindow(t *testing.T) {
	s, st := signedStore(t, true)
	b100 := s.Window(st, 100, 101)
	if b100.Signed == nil {
		t.Fatal("time-ordered store: Window must carry Signed")
	}
	sg := b100.Signed
	// Expected dollars accumulate the way the store does: a runtime
	// float64(price*size) per print, then +=. (A constant expression would
	// be folded exactly and differ in the last bit.)
	mul := func(price, size float64) float64 { return float64(price * size) }
	askDollars := mul(10.20, 100)
	askDollars += mul(10.12, 10)
	if sg.AskSide != (ClassAgg{Trades: 2, Shares: 110, Dollars: askDollars}) {
		t.Errorf("sec 100 AskSide = %+v", sg.AskSide)
	}
	if sg.QuoteAsk != sg.AskSide {
		t.Errorf("sec 100 QuoteAsk = %+v, want = AskSide (quote + midpoint rules)", sg.QuoteAsk)
	}
	if sg.BidSide != (ClassAgg{Trades: 1, Shares: 50, Dollars: mul(10.00, 50)}) || sg.QuoteBid != sg.BidSide {
		t.Errorf("sec 100 BidSide = %+v QuoteBid = %+v", sg.BidSide, sg.QuoteBid)
	}
	if sg.TickRule != 0 || sg.Late != 0 {
		t.Errorf("sec 100 tick=%d late=%d", sg.TickRule, sg.Late)
	}
	b101 := s.Window(st, 101, 102)
	sg = b101.Signed
	if sg.AskSide != (ClassAgg{}) || sg.QuoteAsk != (ClassAgg{}) {
		t.Errorf("sec 101 AskSide = %+v (BLOCK must not be signed)", sg.AskSide)
	}
	bidDollars := mul(10.10, 20)
	bidDollars += mul(10.10, 30)
	if sg.BidSide != (ClassAgg{Trades: 2, Shares: 50, Dollars: bidDollars}) {
		t.Errorf("sec 101 BidSide = %+v", sg.BidSide)
	}
	if sg.QuoteBid != (ClassAgg{}) {
		t.Errorf("sec 101 QuoteBid = %+v, want zero (tick-rule prints are not quote-ruled)", sg.QuoteBid)
	}
	if sg.TickRule != 2 || sg.Late != 1 {
		t.Errorf("sec 101 tick=%d late=%d", sg.TickRule, sg.Late)
	}
	// Window sums the signed family across seconds.
	w := s.Window(st, 100, 102)
	sg = w.Signed
	if sg.AskSide.Trades != 2 || sg.BidSide.Trades != 3 || sg.QuoteAsk.Trades != 2 || sg.QuoteBid.Trades != 1 || sg.TickRule != 2 || sg.Late != 1 {
		t.Errorf("window: %+v", *sg)
	}
	// Eligible = CONTINUOUS only: 6 prints (not the NPF, not the BLOCK).
	e := w.Eligible()
	if e.Trades != 6 || e.Shares != 250 {
		t.Errorf("eligible: %+v", e)
	}
	u, ok := w.Unclassified()
	if !ok || u.Trades != 1 || u.Shares != 40 || u.Dollars != mul(10.20, 40) {
		t.Errorf("unclassified: %+v ok=%v", u, ok)
	}
	if u.Trades != e.Trades-sg.AskSide.Trades-sg.BidSide.Trades {
		t.Errorf("unclassified identity broken")
	}
	// Top-level and class totals are untouched by classification: 8 prints.
	if w.Trades != 8 || w.Shares != 380 {
		t.Errorf("totals: trades=%d shares=%v", w.Trades, w.Shares)
	}
	st2, ok := s.Aggressor()
	if !ok || st2.Eligible != 6 || st2.Ask != 2 || st2.Bid != 3 || st2.Quote != 3 || st2.Tick != 2 || st2.Late != 1 || st2.Unclassified() != 1 || st2.NoBook() != 0 {
		t.Errorf("Aggressor() = %+v ok=%v", st2, ok)
	}
}

// TestNotTimeOrderedStoreRecordsNothing (review B1): a store fed by a
// non-time-ordered source never classifies, carries no Signed, writes no
// signed columns, and reports no split.
func TestNotTimeOrderedStoreRecordsNothing(t *testing.T) {
	s, st := signedStore(t, false)
	w := s.Window(st, 100, 102)
	if w.Signed != nil {
		t.Fatalf("Window on a non-time-ordered store carried Signed: %+v", *w.Signed)
	}
	if w.Trades != 8 {
		t.Errorf("base aggregation must be unaffected: trades=%d", w.Trades)
	}
	if _, ok := w.Unclassified(); ok {
		t.Error("Unclassified must report ok=false without Signed")
	}
	if _, ok := s.Aggressor(); ok {
		t.Error("Aggressor must report ok=false")
	}
	path := writeStore(t, s, "flat.csv")
	raw, _ := os.ReadFile(path)
	head := strings.SplitN(string(raw), "\n", 2)[0]
	if strings.Contains(head, "ask_") || strings.Contains(head, "tick_rule") {
		t.Errorf("non-time-ordered file must not carry signed columns: %s", head)
	}
	sess, err := ReadCSV(path)
	if err != nil {
		t.Fatal(err)
	}
	if sess.HasSigned {
		t.Error("HasSigned must be false")
	}
	var buf strings.Builder
	Report(&buf, s, 0)
	if !strings.Contains(buf.String(), "not time-ordered") {
		t.Errorf("Report: %q", buf.String())
	}
}

func TestSignedCSVRoundTripAndHeader(t *testing.T) {
	s, _ := signedStore(t, true)
	path := writeStore(t, s, "signed.csv")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	head := strings.SplitN(string(raw), "\n", 2)[0]
	for _, col := range signedHeader() {
		if !strings.Contains(","+head+",", ","+col+",") {
			t.Errorf("header missing %q: %s", col, head)
		}
	}
	if n := len(signedHeader()); n != 14 {
		t.Errorf("signed family has %d columns, want 14", n)
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
		if got := sess.Get(r.Symbol, r.Sec); !reflect.DeepEqual(got, r.Bucket) {
			t.Errorf("(%s,%d): read %+v != stored %+v", r.Symbol, r.Sec, got, r.Bucket)
		}
	}
	m, err := sess.DeriveMinute("X", 60)
	if err != nil {
		t.Fatal(err)
	}
	if m.Signed == nil || m.Signed.AskSide.Trades != 2 || m.Signed.BidSide.Trades != 3 || m.Signed.TickRule != 2 || m.Signed.Late != 1 {
		t.Errorf("DeriveMinute signed fields: %+v", m.Signed)
	}
	var buf strings.Builder
	Report(&buf, s, 0)
	if !strings.HasPrefix(buf.String(), "aggressor: eligible=6 ") {
		t.Errorf("Report: %q", buf.String())
	}
}

// TestOldCSVLoadsWithoutSigned: a pre-MO-2 bucket file (1.4 columns only)
// loads with HasSigned=false, every 1.4 field intact, and Signed nil on
// every bucket and every derived minute — absence propagates, it never sums
// as zero (review S7; column-compatibility contract, docs/backlog.md).
func TestOldCSVLoadsWithoutSigned(t *testing.T) {
	s, _ := signedStore(t, true)
	path := writeStore(t, s, "new.csv")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Project the file onto the 1.4 columns to produce an old-format file.
	base := len(baseHeader())
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	project := func(n int) string {
		var out []string
		for _, line := range lines {
			f := strings.Split(line, ",")
			out = append(out, strings.Join(f[:n], ","))
		}
		return strings.Join(out, "\n") + "\n"
	}
	oldPath := filepath.Join(t.TempDir(), "old.csv")
	if err := os.WriteFile(oldPath, []byte(project(base)), 0o644); err != nil {
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
		want.Signed = nil
		if !reflect.DeepEqual(got, want) {
			t.Errorf("(%s,%d): read %+v != base %+v", r.Symbol, r.Sec, got, want)
		}
		if got.Signed != nil {
			t.Errorf("(%s,%d): Signed must be nil on an old file", r.Symbol, r.Sec)
		}
	}
	m, err := sess.DeriveMinute("X", 60)
	if err != nil {
		t.Fatal(err)
	}
	if m.Signed != nil {
		t.Errorf("DeriveMinute on an old file must propagate absence, got %+v", *m.Signed)
	}
	if _, ok := m.Unclassified(); ok {
		t.Error("Unclassified must be ok=false on an old file")
	}
	if m.Trades != 8 {
		t.Errorf("base fields still sum: trades=%d", m.Trades)
	}
	// A partial signed set is a corrupt header, not an old file.
	partialPath := filepath.Join(t.TempDir(), "partial.csv")
	if err := os.WriteFile(partialPath, []byte(project(base+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCSV(partialPath); err == nil {
		t.Error("partial signed column set must be an error")
	}
}
