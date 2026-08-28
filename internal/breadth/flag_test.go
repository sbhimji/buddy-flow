package breadth

import "testing"

// TestBreadthFlag: the B glyph is the T7 rule — lit positive over
// HighlightFrac ↑, lit negative over HighlightFrac ↓, unlit mixed, ok=false
// on a gap — and Style is its colour.
func TestBreadthFlag(t *testing.T) {
	c, table, open := synth(t)
	at := open + 4*60 + 5
	cases := []struct {
		syms              []string
		atSec             int64
		lit, positive, ok bool
	}{
		{[]string{"A"}, at, true, true, true},
		{[]string{"B"}, at, true, false, true},
		{[]string{"A", "B", "C", "D", "E"}, at, false, false, true},
		{[]string{"A"}, open + 2*60 + 5, false, false, false}, // pre-persistence gap
	}
	for _, cs := range cases {
		rc := row(table, cs.atSec, cs.syms...)
		lit, pos, ok := c.Flag(rc)
		if lit != cs.lit || pos != cs.positive || ok != cs.ok {
			t.Errorf("Flag(%v) = %v %v %v, want %v %v %v", cs.syms, lit, pos, ok, cs.lit, cs.positive, cs.ok)
		}
		want := ""
		if lit && ok {
			want = sgrRed
			if pos {
				want = sgrGreen
			}
		}
		if got := c.Style(rc); got != want {
			t.Errorf("Style(%v) = %q, want %q", cs.syms, got, want)
		}
	}
}

// TestDollarFlag: the $ glyph restates the merged cell's own count — lit
// when k ≥ 1 and 2k ≥ up; ok=false whenever the cell prints no $ count.
func TestDollarFlag(t *testing.T) {
	vc, table, open := volSynth(t)
	at := open + 4*60 + 5
	col := vc.BreadthColumn(true)
	cases := []struct {
		syms    []string
		cell    string
		lit, ok bool
	}{
		{[]string{"V", "W", "X"}, " 2/3   1$", true, true}, // exactly half
		{[]string{"V"}, " 1/1   1$", true, true},           // all
		{[]string{"W", "X"}, " 1/2   0$", false, true},     // none on volume
		{[]string{"B", "L"}, " 0/2     ", false, false},    // no ↑ member: no count
	}
	for _, cs := range cases {
		rc := row(table, at, cs.syms...)
		if got := col.Cell(rc); got != cs.cell {
			t.Fatalf("cell(%v) = %q, want %q", cs.syms, got, cs.cell)
		}
		lit, pos, ok := vc.DollarFlag(rc)
		if lit != cs.lit || ok != cs.ok || (lit && !pos) {
			t.Errorf("DollarFlag(%v) = %v %v %v, want lit=%v ok=%v positive", cs.syms, lit, pos, ok, cs.lit, cs.ok)
		}
	}
	// Pre-horizon gap and a partially unmeasured ↑ set (`·$`): ok=false.
	if lit, _, ok := vc.DollarFlag(row(table, open+2*60+5, "V", "W")); lit || ok {
		t.Errorf("pre-horizon = %v %v, want unlit, ok=false", lit, ok)
	}
	vc3, table3, at3 := unmeasuredUpSynth(t, true)
	if lit, _, ok := vc3.DollarFlag(row(table3, at3, "U", "N")); lit || ok {
		t.Errorf("·$ = %v %v, want unlit, ok=false", lit, ok)
	}
}
