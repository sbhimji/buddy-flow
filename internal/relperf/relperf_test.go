package relperf

import (
	"strings"
	"testing"

	"buddy-flow/internal/breadth"
	"buddy-flow/internal/bucket"
	"buddy-flow/internal/devview"
	"buddy-flow/internal/ingest"
	"buddy-flow/internal/session"
)

func print(s *bucket.Store, st *ingest.SymbolState, sec int64, price float64) {
	s.ObserveTrade(&ingest.Trade{State: st, Price: price, Size: 1, SipTs: sec * 1e9})
}

// synth: open 09:30 on 2026-08-14. SPY 100 → 101 (+1.00%); A 100 → 103
// (+3.00%); B 100 → 100 (0.00%); C never trades (unmeasured); D 100 →
// 100.5 (+0.50%), first print in minute 1 (unmeasured in the first
// completed minute, measured after).
func synth(t *testing.T) (*Calc, *ingest.Table, int64) {
	t.Helper()
	table := ingest.NewTable([]string{"SPY", "A", "B", "C", "D"})
	s := bucket.NewStore()
	open, err := session.BucketStart("2026-08-14", session.OpenMinute)
	if err != nil {
		t.Fatal(err)
	}
	print(s, table.Lookup("SPY"), open, 100)
	print(s, table.Lookup("SPY"), open+30, 101)
	print(s, table.Lookup("A"), open, 100)
	print(s, table.Lookup("A"), open+40, 103)
	print(s, table.Lookup("B"), open, 100)
	print(s, table.Lookup("B"), open+45, 100)
	print(s, table.Lookup("D"), open+70, 100)
	print(s, table.Lookup("D"), open+80, 100.5)
	bc, err := breadth.New(s, table)
	if err != nil {
		t.Fatal(err)
	}
	return New(bc), table, open
}

func row(table *ingest.Table, atSec int64, syms ...string) *devview.RowCtx {
	states := make([]*ingest.SymbolState, len(syms))
	for i, s := range syms {
		states[i] = table.Lookup(s)
	}
	return &devview.RowCtx{Basket: &devview.BasketRow{Name: "b", States: states}, AtSec: atSec}
}

// TestEqualWeightExcludesUnmeasured: three-member basket, one without an
// anchor — the mean is over the two measured, equal-weighted, and the
// count says so.
func TestEqualWeightExcludesUnmeasured(t *testing.T) {
	c, table, open := synth(t)
	rc := row(table, open+60+5, "A", "B", "C") // completed minute 09:30
	v := c.At(rc)
	// ((0.03 − 0.01) + (0 − 0.01)) / 2 = 0.005
	if !v.OK || v.Measured != 2 || v.Members != 3 {
		t.Fatalf("cell = %+v", v)
	}
	if got := Fmt(v); got != "+0.50%" {
		t.Errorf("vs_SPY = %q, want +0.50%%", got)
	}
	col := c.Column(true)
	if col.Name != "vs_SPY" || col.Cell(rc) != "+0.50%" {
		t.Errorf("column %s = %q", col.Name, col.Cell(rc))
	}
	if got := col.Style(rc); got != sgrGreen {
		t.Errorf("style = %q, want green beyond +0.10%%", got)
	}
	dev := c.DevColumns()
	if got := dev[1].Cell(rc); dev[1].Name != "vs_SPY_n" || got != "2/3" {
		t.Errorf("vs_SPY_n = %q, want 2/3", got)
	}
	// D joins once it has an anchor (second completed minute): mean over
	// A, B, D = (0.02 − 0.01 − 0.005)/3 = 0.005/3.
	rc2 := row(table, open+2*60, "A", "B", "C", "D")
	v2 := c.At(rc2)
	if !v2.OK || v2.Measured != 3 || Fmt(v2) != "+0.17%" {
		t.Errorf("cell with D = %+v %q", v2, Fmt(v2))
	}
}

// TestDeadBand: inside ±10 bps no colour; below −10 bps red.
func TestDeadBand(t *testing.T) {
	c, table, open := synth(t)
	col := c.Column(true)
	// B alone: 0 − 1% = −1.00% → red.
	rcB := row(table, open+60+5, "B")
	if got, sgr := col.Cell(rcB), col.Style(rcB); got != "-1.00%" || sgr != sgrRed {
		t.Errorf("B = %q %q", got, sgr)
	}
	// A, B, B: (0.02 − 0.01 − 0.01)/3 = 0 → inside the band, no colour.
	rc0 := row(table, open+60+5, "A", "B", "B")
	if got, sgr := col.Cell(rc0), col.Style(rc0); got != "+0.00%" || sgr != "" {
		t.Errorf("flat = %q %q", got, sgr)
	}
	if lit, _, ok := c.Flag(rc0); lit || !ok {
		t.Errorf("Flag inside the band = lit %v ok %v", lit, ok)
	}
}

// TestGaps: before the first completed minute; SPY unmeasurable; no
// member measured — all the gap, never a number, never a style.
func TestGaps(t *testing.T) {
	c, table, open := synth(t)
	col := c.Column(true)
	for name, rc := range map[string]*devview.RowCtx{
		"pre-first-minute":   row(table, open+30, "A", "B"),
		"no member measured": row(table, open+60+5, "C"),
	} {
		if got := col.Cell(rc); got != gap {
			t.Errorf("%s: %q, want %q", name, got, gap)
		}
		if got := col.Style(rc); got != "" {
			t.Errorf("%s: styled %q", name, got)
		}
		if _, _, ok := c.Flag(rc); ok {
			t.Errorf("%s: Flag ok", name)
		}
	}
	// SPY silent: the whole column gaps even with measured members.
	table2 := ingest.NewTable([]string{"SPY", "A"})
	s := bucket.NewStore()
	print(s, table2.Lookup("A"), open, 100)
	print(s, table2.Lookup("A"), open+10, 101)
	bc, err := breadth.New(s, table2)
	if err != nil {
		t.Fatal(err)
	}
	if got := New(bc).Column(true).Cell(row(table2, open+60+5, "A")); got != gap {
		t.Errorf("SPY unmeasurable: %q, want %q", got, gap)
	}
}

// TestDeterminism: same store state and second → same bytes; the memo
// turns over with the second.
func TestDeterminism(t *testing.T) {
	c, table, open := synth(t)
	col := c.Column(true)
	rc := row(table, open+2*60+5, "A", "B", "C", "D")
	a := col.Style(rc) + col.Cell(rc)
	if b := col.Style(rc) + col.Cell(rc); a != b {
		t.Errorf("%q vs %q", a, b)
	}
	if v := c.At(row(table, open+60+5, "A", "B", "C", "D")); v.Measured != 2 {
		t.Errorf("memo did not turn over with the second: %+v", v)
	}
}

// TestExtendTrader: vs_SPY lands right after 5m_z; the footer block goes
// before the gap-glyph line; anchors are loud; the footer passes the
// scope-law scanner.
func TestExtendTrader(t *testing.T) {
	c, _, _ := synth(t)
	cols := []devview.Column{{Name: "cum_share_z"}, {Name: "since"}, {Name: "5m_z"}, {Name: "breadth"}}
	footerIn := "since = x\n·                 = gap\n"
	out, footer := c.ExtendTrader(cols, footerIn)
	names := make([]string, len(out))
	for i, col := range out {
		names[i] = col.Name
	}
	if got := strings.Join(names, " "); got != "cum_share_z since 5m_z vs_SPY breadth" {
		t.Errorf("order = %s", got)
	}
	if strings.Index(footer, "\nvs_SPY ") > strings.Index(footer, "\n·  ") {
		t.Error("vs_SPY footer block not before the gap-glyph line")
	}
	for _, banned := range []string{"buy", "sell", "Buy", "Sell", "bullish", "bearish", "signal", "chase"} {
		if strings.Contains(Footer, banned) {
			t.Errorf("footer contains %q — scope law", banned)
		}
	}
	if !strings.Contains(Footer, "+0.10%") || !strings.Contains(Footer, "since 09:41") {
		t.Errorf("footer lacks the band or the reading guide:\n%s", Footer)
	}
	mustPanic := func(name string, f func()) {
		defer func() {
			if recover() == nil {
				t.Errorf("%s: no panic", name)
			}
		}()
		f()
	}
	mustPanic("missing 5m_z", func() { c.ExtendTrader([]devview.Column{{Name: "breadth"}}, footerIn) })
	mustPanic("missing gap line", func() { c.ExtendTrader(cols, "no gap line\n") })
}
