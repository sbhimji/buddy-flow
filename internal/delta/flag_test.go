package delta

import (
	"testing"

	"buddy-flow/internal/devview"
	"buddy-flow/internal/ingest"
)

// TestFlagThreshold: the δ glyph predicate is the Δ5 highlight — lit at
// exactly ±DeltaHighlight, not at ±(DeltaHighlight−0.001); gap never lit;
// the thin-basket rule turns the flag off (ok=false) and Style follows.
func TestFlagThreshold(t *testing.T) {
	cases := []struct {
		v                  float64
		ok                 bool
		lit, positive, ok2 bool
	}{
		{DeltaHighlight, true, true, true, true},
		{-DeltaHighlight, true, true, false, true},
		{DeltaHighlight - 0.001, true, false, true, true},
		{-DeltaHighlight + 0.001, true, false, false, true},
		{1, false, false, false, false},
	}
	for _, c := range cases {
		lit, pos, ok := Flag(c.v, c.ok)
		if lit != c.lit || pos != c.positive || ok != c.ok2 {
			t.Errorf("Flag(%v,%v) = %v %v %v, want %v %v %v", c.v, c.ok, lit, pos, ok, c.lit, c.positive, c.ok2)
		}
		if Style(c.v, c.ok) != sgr(lit, pos, ok) {
			t.Errorf("Style(%v) != sgr(Flag)", c.v)
		}
	}
	m := Minute{Delta: 0.5, DeltaOK: true, Counted: DeltaMinDollars, Prints: 100}
	if lit, pos, ok := BasketFlag(m, DeltaMinMembers); !lit || !pos || !ok {
		t.Errorf("at both floors = %v %v %v, want lit positive ok", lit, pos, ok)
	}
	if lit, _, ok := BasketFlag(m, DeltaMinMembers-1); lit || ok {
		t.Errorf("thin basket = %v %v, want unlit ok=false", lit, ok)
	}
	thin := m
	thin.Counted = DeltaMinDollars - 1
	if lit, _, ok := BasketFlag(thin, 10); lit || ok {
		t.Errorf("under $5M = %v %v, want unlit ok=false", lit, ok)
	}
	if BasketStyle(thin, 10) != "" || BasketStyle(m, DeltaMinMembers) != sgrGreen {
		t.Error("BasketStyle disagrees with BasketFlag")
	}
}

// TestCalcFlagIsCellPredicate: Calc.Flag through a RowCtx equals the
// trader δ column's Style on the same row (G1 on the real Calc).
func TestCalcFlagIsCellPredicate(t *testing.T) {
	fs := newFake(true)
	table := ingest.NewTable([]string{"A", "B", "C"})
	open := openSec(t)
	sts := states(table, "A", "B", "C")
	for sec := open + 5*60; sec < open+10*60; sec += 60 {
		fs.put(sts[0], sec, 3e6, "ask", true)
		fs.put(sts[1], sec, 1e6, "ask", true)
		fs.put(sts[2], sec, 1e6, "bid", true)
	}
	c := New(fs)
	rowAt := func(s []*ingest.SymbolState, at int64) *devview.RowCtx {
		return &devview.RowCtx{Basket: &devview.BasketRow{Name: "x", States: s}, AtSec: at}
	}
	rc := rowAt(sts, open+10*60)
	col := c.Columns()[0]
	lit, pos, ok := c.Flag(rc)
	if !lit || !pos || !ok {
		t.Fatalf("Flag = %v %v %v; cell %s", lit, pos, ok, col.Cell(rc))
	}
	if col.Style(rc) != sgr(lit, pos, ok) {
		t.Errorf("Style %q != sgr(Flag)", col.Style(rc))
	}
	// Two members: thin-basket → ok=false and no style, cell still renders.
	rc2 := rowAt(sts[:2], open+10*60)
	if _, _, ok := c.Flag(rc2); ok || col.Style(rc2) != "" || col.Cell(rc2) == "·" {
		t.Errorf("thin: Flag ok=%v style=%q cell=%q", ok, col.Style(rc2), col.Cell(rc2))
	}
}
