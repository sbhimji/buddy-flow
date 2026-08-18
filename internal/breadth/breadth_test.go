package breadth

import (
	"testing"

	"buddy-flow/internal/bucket"
	"buddy-flow/internal/devview"
	"buddy-flow/internal/ingest"
	"buddy-flow/internal/session"
)

func print(s *bucket.Store, st *ingest.SymbolState, sec int64, price float64) {
	printSz(s, st, sec, price, 1) // printSz (volume_test.go) with the price tests' unit size
}

// synth: open 09:30 on 2026-08-14. SPY flat at 100. Hand-computed states at
// a 09:34:05 render (windows end 09:34/09:33/09:32, all vs since-open):
//
//	A: 100 → 101 at 09:31:30            — +100bps every window → persistent ↑
//	B: 100 → 99  at 09:30:30            — −100bps every window → persistent ↓
//	C: 100 → 100.05 at 09:30:30         — +5bps, inside ±10bps → in-line
//	D: 100 → 101 at 09:30:10, 99 at 09:33:30 — ↑↑ then ↓ → flip, counts ·
//	E: never trades                      — unmeasured → folds to ·
//	L: first trades 09:33:10 (100 → 102 by 09:33:40) — ↑ in the newest
//	   window only, UNMEASURABLE in the ones ending 09:33/09:32:
//	   persistence never demonstrated → the middle, never a partial ↑
func synth(t *testing.T) (*Calc, *ingest.Table, int64) {
	t.Helper()
	table := ingest.NewTable([]string{"SPY", "A", "B", "C", "D", "E", "L"})
	s := bucket.NewStore()
	open, err := session.BucketStart("2026-08-14", session.OpenMinute)
	if err != nil {
		t.Fatal(err)
	}
	print(s, table.Lookup("SPY"), open, 100)
	print(s, table.Lookup("A"), open, 100)
	print(s, table.Lookup("A"), open+90, 101)
	print(s, table.Lookup("B"), open, 100)
	print(s, table.Lookup("B"), open+30, 99)
	print(s, table.Lookup("C"), open, 100)
	print(s, table.Lookup("C"), open+30, 100.05)
	print(s, table.Lookup("D"), open, 100)
	print(s, table.Lookup("D"), open+10, 101)
	print(s, table.Lookup("D"), open+3*60+30, 99)
	print(s, table.Lookup("L"), open+3*60+10, 100)
	print(s, table.Lookup("L"), open+3*60+40, 102)
	c, err := New(s, table)
	if err != nil {
		t.Fatal(err)
	}
	return c, table, open
}

func row(table *ingest.Table, atSec int64, syms ...string) *devview.RowCtx {
	states := make([]*ingest.SymbolState, len(syms))
	for i, s := range syms {
		states[i] = table.Lookup(s)
	}
	return &devview.RowCtx{Basket: &devview.BasketRow{Name: "b", States: states}, AtSec: atSec}
}

func TestBreadthHandComputed(t *testing.T) {
	c, table, open := synth(t)
	at := open + 4*60 + 5 // 09:34:05
	rc := row(table, at, "A", "B", "C", "D", "E")
	if got := c.Column(false).Cell(rc); got != "1/5" {
		t.Errorf("breadth = %q, want 1/5", got)
	}
	if got := c.DetailColumn().Cell(rc); got != "1↑ 3· 1↓" {
		t.Errorf("detail = %q, want 1↑ 3· 1↓", got)
	}
	// Two renders of the same state: identical bytes.
	if a, b := c.Column(false).Cell(row(table, at, "A", "B")), c.Column(false).Cell(row(table, at, "A", "B")); a != b {
		t.Errorf("nondeterministic: %q vs %q", a, b)
	}
	// Down-side basket: persistence works for underperformers too.
	if got := c.DetailColumn().Cell(row(table, at, "B")); got != "0↑ 0· 1↓" {
		t.Errorf("down basket = %q", got)
	}
	// Late starter: L is ↑ in the newest window but unmeasurable in the
	// older ones — persistence never demonstrated folds to the middle,
	// never a partial ↑ (the doc rule, pinned).
	if got := c.DetailColumn().Cell(row(table, at, "A", "L")); got != "1↑ 1· 0↓" {
		t.Errorf("late-start member = %q, want 1↑ 1· 0↓", got)
	}
}

// TestBreadthHighlight covers the HighlightFrac styling: green past the
// threshold on the up side, red on the down side, none in between or on a
// gap; the unstyled (dev) column carries no Style at all.
func TestBreadthHighlight(t *testing.T) {
	c, table, open := synth(t)
	at := open + 4*60 + 5
	col := c.Column(true)
	// {A}: 1/1 up = 100% > 70% → green.
	if got := col.Style(row(table, at, "A")); got != sgrGreen {
		t.Errorf("all-up style = %q, want green", got)
	}
	// {B}: 1/1 down → red.
	if got := col.Style(row(table, at, "B")); got != sgrRed {
		t.Errorf("all-down style = %q, want red", got)
	}
	// {A,B,C,D,E}: 1/5 up, 1/5 down — neither side past 70% → no style.
	if got := col.Style(row(table, at, "A", "B", "C", "D", "E")); got != "" {
		t.Errorf("mixed style = %q, want none", got)
	}
	// Gap (pre-persistence): no style.
	if got := col.Style(row(table, open+2*60+5, "A")); got != "" {
		t.Errorf("gap style = %q, want none", got)
	}
	// Dev variant renders plain.
	if c.Column(false).Style != nil {
		t.Error("unstyled column carries a Style func")
	}
}

func TestBreadthGaps(t *testing.T) {
	c, table, open := synth(t)
	// Before PersistMinutes completed session minutes: gap, never 0/N.
	if got := c.Column(false).Cell(row(table, open+2*60+5, "A", "B")); got != gap {
		t.Errorf("pre-persistence = %q, want gap", got)
	}
	// All-unmeasured basket: gap ("0/1" would be a fake statement).
	if got := c.Column(false).Cell(row(table, open+4*60+5, "E")); got != gap {
		t.Errorf("unmeasured basket = %q, want gap", got)
	}
}

func TestBreadthSPYMissing(t *testing.T) {
	table := ingest.NewTable([]string{"SPY", "A"})
	s := bucket.NewStore()
	open, err := session.BucketStart("2026-08-14", session.OpenMinute)
	if err != nil {
		t.Fatal(err)
	}
	print(s, table.Lookup("A"), open, 100)
	print(s, table.Lookup("SPY"), open+300, 100) // SPY first prints 09:35
	c, err := New(s, table)
	if err != nil {
		t.Fatal(err)
	}
	// 09:34:05: the persistence windows predate any SPY print → gap.
	if got := c.Column(false).Cell(row(table, open+4*60+5, "A")); got != gap {
		t.Errorf("SPY-missing = %q, want gap", got)
	}
}

func TestNewRejectsMissingSPY(t *testing.T) {
	if _, err := New(bucket.NewStore(), ingest.NewTable([]string{"A"})); err == nil {
		t.Error("missing SPY accepted")
	}
}

// TestStatus covers the T9 clock-line SPY read: color by the ±DeadBand
// (green up / yellow flat / red down), the one-completed-minute gate, and
// the gap posture. The synth store's SPY is flat at 100 → yellow +0.00%.
func TestStatus(t *testing.T) {
	c, table, open := synth(t)
	if got, want := c.Status(open+4*60+5), "SPY "+sgrYellow+"+0.00%"+sgrReset+" since open"; got != want {
		t.Errorf("flat status = %q, want %q", got, want)
	}
	// Available from the FIRST completed minute — before breadth is.
	if got := c.Status(open + 60 + 5); got != "SPY "+sgrYellow+"+0.00%"+sgrReset+" since open" {
		t.Errorf("09:31 status = %q", got)
	}
	// Before any completed minute: gap, never a fake 0.00%.
	if got := c.Status(open + 5); got != "SPY · since open" {
		t.Errorf("pre-minute status = %q", got)
	}
	// Determinism: same state, same bytes.
	if c.Status(open+4*60+5) != c.Status(open+4*60+5) {
		t.Error("status not deterministic")
	}
	_ = table

	// Up and down beyond the band, hand-computed on fresh stores.
	for _, tc := range []struct {
		last float64
		want string
	}{
		{101, sgrGreen + "+1.00%"},  // +100bps > band
		{99, sgrRed + "-1.00%"},     // −100bps < −band
		{100.05, sgrYellow + "+0.05%"}, // +5bps inside band
	} {
		tbl := ingest.NewTable([]string{"SPY"})
		s := bucket.NewStore()
		print(s, tbl.Lookup("SPY"), synthOpen(t), 100)
		print(s, tbl.Lookup("SPY"), synthOpen(t)+70, tc.last)
		cc, err := New(s, tbl)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := cc.Status(synthOpen(t)+2*60+5), "SPY "+tc.want+sgrReset+" since open"; got != want {
			t.Errorf("status(last=%v) = %q, want %q", tc.last, got, want)
		}
	}
}

func synthOpen(t *testing.T) int64 {
	t.Helper()
	open, err := session.BucketStart("2026-08-14", session.OpenMinute)
	if err != nil {
		t.Fatal(err)
	}
	return open
}
