package delta

import (
	"strings"
	"testing"

	"buddy-flow/internal/bucket"
	"buddy-flow/internal/classify"
	"buddy-flow/internal/devview"
	"buddy-flow/internal/flowshare"
	"buddy-flow/internal/ingest"
	"buddy-flow/internal/profile"
	"buddy-flow/internal/session"
)

// fakeStore is a hand-built 1-second store: buckets keyed by (state, sec),
// Window summing the fields Compute reads. Signed is carried when the
// source recorded it (recorded == true), else nil — the bucket.Store
// posture (recording is per source, not per bucket).
type fakeStore struct {
	recorded bool
	rows     map[*ingest.SymbolState]map[int64]bucket.Bucket
}

func newFake(recorded bool) *fakeStore {
	return &fakeStore{recorded: recorded, rows: map[*ingest.SymbolState]map[int64]bucket.Bucket{}}
}

// put adds one continuous print's dollars at sec: counted always; strong
// (quote/midpoint) or tick-rule ask/bid dollars per the flags; "" leaves
// it unclassified.
func (f *fakeStore) put(st *ingest.SymbolState, sec int64, dollars float64, side string, strong bool) {
	m := f.rows[st]
	if m == nil {
		m = map[int64]bucket.Bucket{}
		f.rows[st] = m
	}
	b := m[sec]
	if b.Signed == nil {
		b.Signed = &bucket.SignedAgg{}
	}
	b.Dollars += dollars
	b.Class[classify.Continuous].Dollars += dollars
	add := func(c *bucket.ClassAgg) { c.Trades++; c.Dollars += dollars }
	switch side {
	case "ask":
		add(&b.Signed.AskSide)
		if strong {
			add(&b.Signed.QuoteAsk)
		} else {
			b.Signed.TickRule++
		}
	case "bid":
		add(&b.Signed.BidSide)
		if strong {
			add(&b.Signed.QuoteBid)
		} else {
			b.Signed.TickRule++
		}
	}
	m[sec] = b
}

func (f *fakeStore) Window(st *ingest.SymbolState, fromSec, toSec int64) bucket.Bucket {
	var out bucket.Bucket
	if f.recorded {
		out.Signed = &bucket.SignedAgg{}
	}
	for sec := fromSec; sec < toSec; sec++ {
		b, ok := f.rows[st][sec]
		if !ok {
			continue
		}
		out.Dollars += b.Dollars
		for i := range out.Class {
			out.Class[i].Dollars += b.Class[i].Dollars
		}
		if out.Signed != nil && b.Signed != nil {
			s, o := out.Signed, b.Signed
			s.AskSide.Dollars += o.AskSide.Dollars
			s.BidSide.Dollars += o.BidSide.Dollars
			s.QuoteAsk.Dollars += o.QuoteAsk.Dollars
			s.QuoteAsk.Trades += o.QuoteAsk.Trades
			s.QuoteBid.Dollars += o.QuoteBid.Dollars
			s.QuoteBid.Trades += o.QuoteBid.Trades
			s.TickRule += o.TickRule
		}
	}
	return out
}

func openSec(t *testing.T) int64 {
	t.Helper()
	open, err := session.BucketStart("2026-08-24", session.OpenMinute)
	if err != nil {
		t.Fatal(err)
	}
	return open
}

func states(table *ingest.Table, syms ...string) []*ingest.SymbolState {
	out := make([]*ingest.SymbolState, 0, len(syms))
	for _, s := range syms {
		out = append(out, table.Lookup(s))
	}
	return out
}

// TestKnownValues: minute 09:35 over A+B. A: 600 strong ask, 100 strong
// bid, 100 tick ask; B: 200 strong bid. Counted 1000 →
// delta = (600−300)/1000 = +0.30, class = 900/1000 = 90%,
// delta_all = (700−300)/1000 = +0.40; 3 strong prints.
func TestKnownValues(t *testing.T) {
	table := ingest.NewTable([]string{"A", "B"})
	f := newFake(true)
	from := openSec(t) + 5*60
	a, b := table.Lookup("A"), table.Lookup("B")
	f.put(a, from+1, 600, "ask", true)
	f.put(a, from+2, 100, "bid", true)
	f.put(a, from+3, 100, "ask", false)
	f.put(b, from+59, 200, "bid", true)
	f.put(b, from+60, 5000, "ask", true) // next minute: excluded
	m := Compute(f, states(table, "A", "B"), from, from+60)
	if !m.DeltaOK || !m.ClassOK || !m.DeltaAllOK || m.Counted != 1000 || m.Prints != 3 || m.Span != 60 {
		t.Fatalf("minute = %+v", m)
	}
	if got := FmtDelta(m.Delta, m.DeltaOK); got != "+0.30" {
		t.Errorf("delta = %s, want +0.30", got)
	}
	if got := FmtClass(m.Class, m.ClassOK); got != "90%" {
		t.Errorf("class = %s, want 90%%", got)
	}
	if got := FmtDelta(m.DeltaAll, m.DeltaAllOK); got != "+0.40" {
		t.Errorf("delta_all = %s, want +0.40", got)
	}
	// Bid-heavy sign.
	f2 := newFake(true)
	f2.put(a, from+1, 100, "ask", true)
	f2.put(a, from+2, 400, "bid", true)
	m2 := Compute(f2, states(table, "A"), from, from+60)
	if got := FmtDelta(m2.Delta, m2.DeltaOK); got != "-0.60" {
		t.Errorf("delta = %s, want -0.60", got)
	}
}

// TestD9Boundary: a print at 09:30:29 is outside the 09:30 window (gap); a
// print at 09:30:30 is inside (renders). Δ2 applies to the prints; the
// 09:31 minute is untouched by the clamp.
func TestD9Boundary(t *testing.T) {
	table := ingest.NewTable([]string{"A"})
	open := openSec(t)
	st := states(table, "A")
	f := newFake(true)
	f.put(table.Lookup("A"), open+29, 100, "ask", true)
	if m := Compute(f, st, open, open+60); m.DeltaOK || m.ClassOK || m.Span != 30 {
		t.Errorf("09:30:29 print counted: %+v", m)
	}
	f.put(table.Lookup("A"), open+30, 100, "bid", true)
	m := Compute(f, st, open, open+60)
	if got := FmtDelta(m.Delta, m.DeltaOK); got != "-1.00" {
		t.Errorf("09:30:30 print: delta = %s, want -1.00 (only the 09:30:30 print counts)", got)
	}
	if got := FmtClass(m.Class, m.ClassOK); got != "100%" {
		t.Errorf("class = %s, want 100%%", got)
	}
	f.put(table.Lookup("A"), open+61, 100, "ask", true)
	if m := Compute(f, st, open+60, open+120); FmtDelta(m.Delta, m.DeltaOK) != "+1.00" || m.Span != 60 {
		t.Errorf("09:31 minute clamped: %+v", m)
	}
}

// TestTrailingWindow: the trader cell gaps until the D9-clamped trailing
// span reaches MinWindow — at 09:32:59 the window is [09:30:30, 09:32) =
// 90 s (gap, even with prints in it); at 09:33:00 it is [09:30:30, 09:33)
// = 150 s and renders; a full window at 09:40 is a ratio of sums (the
// 09:35 minute alone is −1.00; over 09:35–09:39 the sums give −0.80).
func TestTrailingWindow(t *testing.T) {
	table := ingest.NewTable([]string{"A"})
	open := openSec(t)
	a := table.Lookup("A")
	st := states(table, "A")
	f := newFake(true)
	f.put(a, open+45, 100, "ask", true)
	if m := Trailing(f, st, open+2*60+59); m.DeltaOK || m.ClassOK || m.Span != 90 {
		t.Errorf("09:32:59 rendered: %+v", m)
	}
	m := Trailing(f, st, open+3*60)
	if m.Span != 150 || FmtDelta(m.Delta, m.DeltaOK) != "+1.00" || FmtClass(m.Class, m.ClassOK) != "100%" {
		t.Errorf("09:33:00 = %+v", m)
	}
	f.put(a, open+4*60+1, 100, "ask", true)
	f.put(a, open+5*60+1, 900, "bid", true)
	m = Trailing(f, st, open+10*60+10) // 09:40:10 → [09:35, 09:40)
	if m.Span != 300 || FmtDelta(m.Delta, m.DeltaOK) != "-1.00" {
		t.Errorf("09:40 = %+v", m)
	}
	m = Trailing(f, st, open+9*60+10) // 09:39:10 → [09:34, 09:39): (100−900)/1000
	if FmtDelta(m.Delta, m.DeltaOK) != "-0.80" {
		t.Errorf("09:39 ratio of sums = %+v", m)
	}
}

// TestGaps covers every Δ4 path: Signed not recorded (fake and the real
// non-time-ordered bucket.Store, whose counted dollars are non-zero —
// proving the gap comes from the nil Signed, not an empty window),
// counted 0, and class 0% with delta gap.
func TestGaps(t *testing.T) {
	table := ingest.NewTable([]string{"A"})
	from := openSec(t) + 5*60
	a := table.Lookup("A")
	st := states(table, "A")

	unrec := newFake(false)
	unrec.put(a, from+1, 100, "ask", true)
	if m := Compute(unrec, st, from, from+60); m.DeltaOK || m.ClassOK || m.DeltaAllOK {
		t.Errorf("unrecorded store rendered: %+v", m)
	}
	real := bucket.NewStore() // not time-ordered: Signed nil on every Window
	real.ObserveTrade(&ingest.Trade{State: a, Price: 10, Size: 10, SipTs: (from + 1) * 1e9})
	if _, d := profile.Counted(real.Window(a, from, from+60)); d != 100 {
		t.Fatalf("real store counted = %v, want 100", d)
	}
	if m := Compute(real, st, from, from+60); m.DeltaOK || m.ClassOK || m.Counted != 0 {
		t.Errorf("bucket.NewStore (Signed nil) rendered: %+v", m)
	}
	if m := Compute(newFake(true), st, from, from+60); m.DeltaOK || m.ClassOK {
		t.Errorf("counted 0 rendered: %+v", m)
	}
	// Counted > 0, nothing strong-classified: class 0% is a measurement;
	// delta has no numerator → gap. Tick-only classification feeds
	// delta_all but not delta.
	tick := newFake(true)
	tick.put(a, from+1, 100, "ask", false)
	m := Compute(tick, st, from, from+60)
	if got := FmtClass(m.Class, m.ClassOK); got != "0%" {
		t.Errorf("class = %s, want 0%%", got)
	}
	if got := FmtDelta(m.Delta, m.DeltaOK); got != gap {
		t.Errorf("delta = %s, want gap", got)
	}
	if got := FmtDelta(m.DeltaAll, m.DeltaAllOK); got != "+1.00" {
		t.Errorf("delta_all = %s, want +1.00", got)
	}
	unclass := newFake(true)
	unclass.put(a, from+1, 100, "", true)
	m = Compute(unclass, st, from, from+60)
	if got := FmtClass(m.Class, m.ClassOK); got != "0%" || m.DeltaOK || m.DeltaAllOK {
		t.Errorf("unclassified only: class %s delta %v delta_all %v", got, m.DeltaOK, m.DeltaAllOK)
	}
}

func TestHighlightThreshold(t *testing.T) {
	if DeltaHighlight != 0.20 {
		t.Errorf("DeltaHighlight = %v, want 0.20 (ledger default, 2026-08-27)", DeltaHighlight)
	}
	cases := []struct {
		v    float64
		ok   bool
		want string
	}{
		{0.20, true, sgrGreen}, {-0.20, true, sgrRed},
		{0.199, true, ""}, {-0.199, true, ""},
		{1, true, sgrGreen}, {-1, true, sgrRed},
		{0.9, false, ""}, {0, true, ""},
	}
	for _, c := range cases {
		if got := Style(c.v, c.ok); got != c.want {
			t.Errorf("Style(%v, %v) = %q, want %q", c.v, c.ok, got, c.want)
		}
	}
}

// TestHighlightGates: the thin-basket rule (members, dollars) and the
// per-ticker floor (dollars, strong prints) unstyle a delta that Style
// alone would light; the cell text is unchanged.
func TestHighlightGates(t *testing.T) {
	lit := Minute{Delta: 0.5, DeltaOK: true, Counted: DeltaMinDollars, Prints: TickerDeltaMinPrints}
	if BasketStyle(lit, DeltaMinMembers) != sgrGreen {
		t.Error("basket at both floors not lit")
	}
	if BasketStyle(lit, DeltaMinMembers-1) != "" {
		t.Error("2-member basket lit")
	}
	thin := lit
	thin.Counted = DeltaMinDollars - 1
	if BasketStyle(thin, 10) != "" {
		t.Error("basket under $5M lit")
	}
	if TickerStyle(lit) != sgrGreen {
		t.Error("ticker at both floors not lit")
	}
	few := lit
	few.Prints = TickerDeltaMinPrints - 1
	if TickerStyle(few) != "" {
		t.Error("ticker with 19 placed prints lit")
	}
	small := lit
	small.Counted = TickerDeltaMinDollars - 1
	if TickerStyle(small) != "" {
		t.Error("ticker under $500k lit")
	}
	if neg := (Minute{Delta: -0.5, DeltaOK: true, Counted: 1e7, Prints: 100}); BasketStyle(neg, 5) != sgrRed || TickerStyle(neg) != sgrRed {
		t.Error("negative delta above floors not red")
	}
}

// TestColumnsAndDeterminism: the trader pair through RowCtx renders the
// same bytes on repeated renders and across Calc instances; the dev set
// adds delta_1m and delta_all. Three members and $10M so the basket
// gate passes; the 09:40 window is [09:35, 09:40): A −1.00 on $9M, B/C
// +1.00 on $1M → −0.80; the 09:39 minute alone is +1.00 (delta_1m).
func TestColumnsAndDeterminism(t *testing.T) {
	table := ingest.NewTable([]string{"A", "B", "C"})
	open := openSec(t)
	f := newFake(true)
	f.put(table.Lookup("A"), open+5*60+1, 9e6, "bid", true)
	f.put(table.Lookup("B"), open+9*60+1, 5e5, "ask", true)
	f.put(table.Lookup("C"), open+9*60+2, 5e5, "ask", true)
	row := &devview.BasketRow{Name: "x", States: states(table, "A", "B", "C")}
	render := func(c *Calc, cols []devview.Column) string {
		rc := &devview.RowCtx{Basket: row, AtSec: open + 10*60 + 10} // 09:40:10
		var sb strings.Builder
		for _, col := range cols {
			sb.WriteString(col.Cell(rc))
			if col.Style != nil {
				sb.WriteString(col.Style(rc))
			}
			sb.WriteByte('|')
		}
		return sb.String()
	}
	c := New(f)
	first := render(c, c.DevColumns())
	if want := "-0.80" + sgrRed + "|100%|+1.00|+1.00|"; first != want {
		t.Errorf("dev cells = %q, want %q", first, want)
	}
	if again := render(c, c.DevColumns()); again != first {
		t.Errorf("second render differs: %q vs %q", again, first)
	}
	if other := render(New(f), New(f).DevColumns()); other != first {
		t.Errorf("fresh Calc differs: %q vs %q", other, first)
	}
	names := []string{}
	for _, col := range c.DevColumns() {
		names = append(names, col.Name)
	}
	if got := strings.Join(names, " "); got != "delta class% delta_1m delta_all" {
		t.Errorf("dev columns = %s", got)
	}
	if got := render(c, c.Columns()); got != "-0.80"+sgrRed+"|100%|" {
		t.Errorf("trader cells = %q", got)
	}
	// Same numbers on a 2-member basket: rendered, not styled.
	thin := &devview.BasketRow{Name: "y", States: states(table, "A", "B")}
	rc := &devview.RowCtx{Basket: thin, AtSec: open + 10*60 + 10}
	if cell, sgr := c.Columns()[0].Cell(rc), c.Columns()[0].Style(rc); cell != "-0.89" || sgr != "" {
		t.Errorf("thin basket = %q %q", cell, sgr)
	}
}

// TestExtendTraderAndFooter: the pair lands after concentration_day and
// before pre_share (README order); the footer defines both, before the
// gap-glyph line, and carries no buy/sell language (scope law).
func TestExtendTraderAndFooter(t *testing.T) {
	base := []devview.Column{{Name: "cum_share"}, {Name: "breadth"}, {Name: "concentration_day"}, {Name: "pre_share"}}
	footer := "concentration_day = x\n·                 = no basis for comparison — never a zero\npre_share         = y\n"
	cols, out := New(newFake(true)).ExtendTrader(base, footer)
	names := []string{}
	for _, col := range cols {
		names = append(names, col.Name)
	}
	if got := strings.Join(names, " "); got != "cum_share breadth concentration_day delta class% pre_share" {
		t.Errorf("order = %s", got)
	}
	lines := strings.Split(out, "\n")
	if !strings.HasPrefix(lines[1], "delta ") || !strings.HasPrefix(lines[2], "class% ") || !strings.HasPrefix(lines[3], "·") {
		t.Errorf("footer order wrong:\n%s", out)
	}
	for _, banned := range []string{"buy", "sell", "Buy", "Sell", "bullish", "bearish", "signal"} {
		if strings.Contains(Footer, banned) {
			t.Errorf("footer contains %q — scope law bans it", banned)
		}
	}
	for _, want := range []string{"trailing 5 completed minutes", "±0.20", "under 3 members or $5M", "from 09:33", "same 5-minute window"} {
		if !strings.Contains(Footer, want) {
			t.Errorf("footer lacks %q", want)
		}
	}
	// A missing anchor is a wiring error: loud, not appended.
	for _, bad := range []struct {
		cols   []devview.Column
		footer string
	}{{[]devview.Column{{Name: "a"}}, footer}, {base, "a = b\n"}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("ExtendTrader(%v, %q) did not panic on a missing anchor", bad.cols, bad.footer)
				}
			}()
			New(newFake(true)).ExtendTrader(bad.cols, bad.footer)
		}()
	}
}

// TestExtendRealTrader extends the REAL flowshare trader set and footer:
// the pair sits right after concentration_day (the last column before
// premarket appends pre_share), and the footer block lands between the
// concentration_day clause and the gap-glyph line.
func TestExtendRealTrader(t *testing.T) {
	stub := devview.Column{Name: "breadth", Width: 9, Cell: func(rc *devview.RowCtx) string { return "" }}
	cols, _, footer := flowshare.TraderColumns(bucket.NewStore(), nil, nil, nil, stub)
	cols, footer = New(newFake(true)).ExtendTrader(cols, footer)
	names := []string{}
	for _, col := range cols {
		names = append(names, col.Name)
	}
	if got := strings.Join(names, " "); got != "cum_share cum_share_typ cum_share_z breadth concentration_day delta class%" {
		t.Errorf("order = %s", got)
	}
	lines := strings.Split(footer, "\n")
	at := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "concentration_day ") {
			at = i
		}
	}
	if at < 0 || !strings.HasPrefix(lines[at+1], "delta ") || !strings.HasPrefix(lines[at+2], "class% ") || !strings.HasPrefix(lines[at+3], "·") {
		t.Errorf("footer block misplaced:\n%s", footer)
	}
}
