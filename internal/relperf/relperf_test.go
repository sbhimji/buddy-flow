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
	rc := row(table, open+60+5, "A", "B", "C", "C") // completed minute 09:30; C twice, never trades
	v := c.At(rc)
	// A, B measured of four: excluded members counted, and under the
	// floor (max(min(3,4), 2) = 3) the mean gaps — TestMinMeasured has
	// both sides; here the exclusion and the count.
	if v.OK || v.Measured != 2 || v.Members != 4 {
		t.Fatalf("cell = %+v", v)
	}
	rc = row(table, open+60+5, "A", "B")
	v = c.At(rc)
	// ((0.03 − 0.01) + (0 − 0.01)) / 2 = 0.005
	if !v.OK || v.Measured != 2 || v.Members != 2 || Fmt(v) != "+0.50%" {
		t.Errorf("vs_SPY = %+v %q, want +0.50%%", v, Fmt(v))
	}
	col := c.Column()
	if col.Name != "vs_SPY" || col.Cell(rc) != "+0.50%" {
		t.Errorf("column %s = %q", col.Name, col.Cell(rc))
	}
	if col.Style != nil {
		t.Error("vs_SPY is styled; review 2026-08-27 ships it unstyled")
	}
	if lit, pos, ok := c.Flag(rc); !lit || !pos || !ok {
		t.Errorf("Flag = %v %v %v; want lit positive ok beyond +0.10%%", lit, pos, ok)
	}
	dev := c.DevColumns()
	if got := dev[1].Cell(rc); dev[1].Name != "vs_SPY_n" || got != "2/2" {
		t.Errorf("vs_SPY_n = %q, want 2/2", got)
	}
	// D joins once it has an anchor (second completed minute): mean over
	// A, B, D = (0.02 − 0.01 − 0.005)/3 = 0.005/3.
	rc2 := row(table, open+2*60, "A", "B", "C", "D")
	v2 := c.At(rc2)
	if !v2.OK || v2.Measured != 3 || Fmt(v2) != "+0.17%" {
		t.Errorf("cell with D = %+v %q", v2, Fmt(v2))
	}
}

// TestDeadBand: Flag is unlit inside ±10 bps, lit negative below. The
// baskets list B more than once on purpose — membership is whatever the
// caller passes, and the duplicate weights B's 0% again to land the mean
// exactly where the case needs it (A +2%, B −1%, B −1% → 0).
func TestDeadBand(t *testing.T) {
	c, table, open := synth(t)
	col := c.Column()
	// A, B, B (three measured of three): (0.02 − 0.01 − 0.01)/3 = 0.
	rc0 := row(table, open+60+5, "A", "B", "B")
	if got := col.Cell(rc0); got != "+0.00%" {
		t.Errorf("flat = %q", got)
	}
	if lit, _, ok := c.Flag(rc0); lit || !ok {
		t.Errorf("Flag inside the band = lit %v ok %v", lit, ok)
	}
	// B, B, B: −1.00% → lit, negative.
	rcB := row(table, open+60+5, "B", "B", "B")
	if got := col.Cell(rcB); got != "-1.00%" {
		t.Errorf("B = %q", got)
	}
	if lit, pos, ok := c.Flag(rcB); !lit || pos || !ok {
		t.Errorf("Flag below the band = %v %v %v", lit, pos, ok)
	}
}

// TestMinMeasured: the partial-membership floor, both sides.
func TestMinMeasured(t *testing.T) {
	for n, want := range map[int]int{1: 1, 2: 2, 3: 3, 4: 3, 5: 3, 6: 3, 7: 4, 9: 5, 12: 6, 15: 8} {
		if got := MinMeasured(n); got != want {
			t.Errorf("MinMeasured(%d) = %d, want %d", n, got, want)
		}
	}
	c, table, open := synth(t)
	col := c.Column()
	// A, B, C: 2 of 3 measured < 3 → gap; A, B alone (2 of 2) → renders.
	if got := col.Cell(row(table, open+60+5, "A", "B", "C")); got != gap {
		t.Errorf("2 of 3 measured rendered %q, want %q", got, gap)
	}
	if got := col.Cell(row(table, open+60+5, "A", "B")); got != "+0.50%" {
		t.Errorf("2 of 2 measured = %q, want +0.50%%", got)
	}
	// Four members: at the first minute A, B measured (2 < 3 → gap); from
	// the second minute D has an anchor too (3 ≥ 3 → renders).
	if got := col.Cell(row(table, open+60+5, "A", "B", "C", "D")); got != gap {
		t.Errorf("2 of 4 measured rendered %q, want %q", got, gap)
	}
	if got := col.Cell(row(table, open+2*60, "A", "B", "C", "D")); got != "+0.17%" {
		t.Errorf("3 of 4 measured = %q, want +0.17%%", got)
	}
	if got := c.DevColumns()[1].Cell(row(table, open+60+5, "A", "B", "C", "D")); got != gap {
		t.Errorf("vs_SPY_n on a gapped cell = %q, want %q", got, gap)
	}
}

// TestGaps: before the first completed minute; SPY unmeasurable; no
// member measured — all the gap, never a number, never a style.
func TestGaps(t *testing.T) {
	c, table, open := synth(t)
	col := c.Column()
	for name, rc := range map[string]*devview.RowCtx{
		"pre-first-minute":   row(table, open+30, "A", "B"),
		"no member measured": row(table, open+60+5, "C"),
	} {
		if got := col.Cell(rc); got != gap {
			t.Errorf("%s: %q, want %q", name, got, gap)
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
	if got := New(bc).Column().Cell(row(table2, open+60+5, "A")); got != gap {
		t.Errorf("SPY unmeasurable: %q, want %q", got, gap)
	}
}

// TestDeterminism: same store state and second → same bytes; the memo
// turns over with the second.
func TestDeterminism(t *testing.T) {
	c, table, open := synth(t)
	col := c.Column()
	rc := row(table, open+2*60+5, "A", "B", "C", "D")
	a := col.Cell(rc)
	if b := col.Cell(rc); a != b {
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
	if !strings.Contains(Footer, "at least 3 of them") || !strings.Contains(Footer, "since 09:41") {
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
