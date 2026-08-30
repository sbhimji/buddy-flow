package premarket

import (
	"strings"
	"testing"

	"buddy-flow/internal/bucket"
	"buddy-flow/internal/devview"
	"buddy-flow/internal/ingest"
	"buddy-flow/internal/session"
)

// formT observes an extended-hours print (condition 12 → NON_FLOW).
func formT(s *bucket.Store, st *ingest.SymbolState, sec int64, dollars float64) {
	s.ObserveTrade(&ingest.Trade{State: st, Price: 1, Size: dollars, SipTs: sec * 1e9,
		Cond: [ingest.MaxConditions]int32{12}, NCond: 1})
}

// synth premarket tape on 2026-08-14: B $100 at 04:00:00, C $600 at 07:00,
// A $300 at 08:00; C also prints $500 REGULAR at 09:31 and $50 Form T at
// 09:45 — both outside the premarket lens (wrong window / clamp).
func synth(t *testing.T) (*Calc, *ingest.Table, int64, int64) {
	t.Helper()
	table := ingest.NewTable([]string{"A", "B", "C", "D"})
	s := bucket.NewStore()
	pre, err := session.BucketStart("2026-08-14", PreOpenMinute)
	if err != nil {
		t.Fatal(err)
	}
	open, err := session.BucketStart("2026-08-14", session.OpenMinute)
	if err != nil {
		t.Fatal(err)
	}
	formT(s, table.Lookup("B"), pre, 100)
	formT(s, table.Lookup("C"), pre+3*3600, 600)
	formT(s, table.Lookup("A"), pre+4*3600, 300)
	s.ObserveTrade(&ingest.Trade{State: table.Lookup("C"), Price: 1, Size: 500, SipTs: (open + 60) * 1e9})
	formT(s, table.Lookup("C"), open+15*60, 50)
	c := New(s, states(table, "A", "B", "C", "D"))
	return c, table, pre, open
}

func states(table *ingest.Table, syms ...string) []*ingest.SymbolState {
	out := make([]*ingest.SymbolState, len(syms))
	for i, s := range syms {
		out[i] = table.Lookup(s)
	}
	return out
}

func row(table *ingest.Table, atSec int64, syms ...string) *devview.RowCtx {
	return &devview.RowCtx{Basket: &devview.BasketRow{Name: "b", States: states(table, syms...)}, AtSec: atSec}
}

func cell(t *testing.T, c *Calc, name string, rc *devview.RowCtx) string {
	t.Helper()
	for _, col := range c.Columns() {
		if col.Name == name {
			return col.Cell(rc)
		}
	}
	t.Fatalf("no column %s", name)
	return ""
}

func TestPremarketHandComputed(t *testing.T) {
	c, table, pre, open := synth(t)
	at := pre + 4*3600 + 5 // 08:00:05 — all three prints in window
	// {A,B}: $400 of $1000 universe.
	rc := row(table, at, "A", "B")
	if got := cell(t, c, "pre_vol", rc); got != "$400" {
		t.Errorf("pre_vol = %q, want $400", got)
	}
	if got := cell(t, c, "pre_share", rc); got != "40.0%" {
		t.Errorf("pre_share = %q, want 40.0%%", got)
	}
	if got := cell(t, c, "pre_conc", row(table, at, "A", "B", "C")); got != "C $600 60%" {
		t.Errorf("pre_conc = %q, want C $600 60%%", got)
	}
	// D traded nothing premarket: $0 is a TRUE measurement (window open),
	// share 0.0%, but concentration gaps (no top member of nothing).
	rcD := row(table, at, "D")
	if got := cell(t, c, "pre_vol", rcD); got != "$0" {
		t.Errorf("quiet pre_vol = %q, want $0", got)
	}
	if got := cell(t, c, "pre_conc", rcD); got != gap {
		t.Errorf("quiet pre_conc = %q, want gap", got)
	}
	// Mid-premarket windowing: at 06:00 only B's 04:00 print exists.
	if got := cell(t, c, "pre_share", row(table, pre+2*3600, "B")); got != "100.0%" {
		t.Errorf("06:00 share = %q, want 100.0%%", got)
	}
	// Before 04:00: no window, all gap.
	for _, name := range []string{"pre_vol", "pre_share", "pre_conc"} {
		if got := cell(t, c, name, row(table, pre-1, "A", "B")); got != gap {
			t.Errorf("pre-04:00 %s = %q, want gap", name, got)
		}
	}
	// Frozen after the open: identical at 09:29:59, 10:00, and 15:00 —
	// the 09:31 regular print and the 09:45 Form T never enter the lens.
	want := cell(t, c, "pre_vol", row(table, open-1, "A", "B", "C"))
	for _, at := range []int64{open + 30*60, open + 330*60} {
		if got := cell(t, c, "pre_vol", row(table, at, "A", "B", "C")); got != want {
			t.Errorf("post-open pre_vol = %q, want frozen %q", got, want)
		}
	}
	if want != "$1K" { // 100+600+300 = $1000 → $1K (whole-K compact form)
		t.Errorf("premarket total = %q, want $1K", want)
	}
}

func TestExtendTraderRank(t *testing.T) {
	c, table, pre, open := synth(t)
	sentinel := func(rc *devview.RowCtx) (float64, bool) { return 42, true }
	cols, rank, footer := c.ExtendTrader(nil, sentinel, "base\n")
	// MO-1: only pre_share rides on the session table; pre_vol/pre_conc
	// return on the premarket tab (MO-7).
	if len(cols) != 1 || cols[0].Name != "pre_share" {
		t.Fatalf("columns = %v", cols)
	}
	// Pre-open (08:00): rank = pre_share, the premarket money story.
	if r, ok := rank(row(table, pre+4*3600+5, "A", "B")); !ok || r != 0.4 {
		t.Errorf("pre-open rank = %v, %v; want 0.4, true", r, ok)
	}
	// 09:30:30 — open but no completed minute yet: still premarket rank.
	if r, ok := rank(row(table, open+30, "A", "B")); !ok || r != 0.4 {
		t.Errorf("09:30:30 rank = %v, %v; want 0.4, true", r, ok)
	}
	// 09:31:05 — first completed minute exists: delegates to the cum rank.
	if r, ok := rank(row(table, open+65, "A", "B")); !ok || r != 42 {
		t.Errorf("post-open rank = %v, %v; want sentinel 42", r, ok)
	}
	// Footer carries the base block plus every premarket definition.
	for _, s := range []string{"base\n", "pre_share", "premarket caveat"} {
		if !strings.Contains(footer, s) {
			t.Errorf("footer missing %q", s)
		}
	}
	for _, s := range []string{"pre_vol", "pre_conc"} {
		if strings.Contains(footer, s) {
			t.Errorf("footer defines %q, which left the session table (MO-1)", s)
		}
	}
	scanFooter(t, footer)
}

// scanFooter is the scope-law scanner every footer in this package goes
// through (the same banned-substring scan flowshare's / conviction's /
// delta's footers use).
func scanFooter(t *testing.T, footer string) {
	t.Helper()
	for _, banned := range []string{"buy", "sell", "Buy", "Sell", "bullish", "bearish", "signal"} {
		if strings.Contains(footer, banned) {
			t.Errorf("footer contains %q — scope law bans it", banned)
		}
	}
}

// MO-7: the premarket tab's own composition — three columns, pre_share
// rank that never hands over to the cum z, full footer.
func TestTab(t *testing.T) {
	c, table, pre, open := synth(t)
	cols, rank, footer := c.Tab()
	if len(cols) != 3 || cols[0].Name != "pre_vol" || cols[1].Name != "pre_share" || cols[2].Name != "pre_conc" {
		t.Fatalf("columns = %v", cols)
	}
	// Rank is pre_share before AND after the open (frozen, never the cum z).
	for _, at := range []int64{pre + 4*3600 + 5, open + 65, open + 330*60} {
		if r, ok := rank(row(table, at, "A", "B")); !ok || r != 0.4 {
			t.Errorf("rank at %d = %v, %v; want 0.4, true", at, r, ok)
		}
	}
	// Before 04:00 the rank gaps (sorts last), as the cells do.
	if _, ok := rank(row(table, pre-1, "A", "B")); ok {
		t.Error("pre-04:00 rank defined; want gap")
	}
	for _, s := range []string{"pre_vol", "pre_share", "pre_conc", "premarket caveat", "no typical yet", "off-exchange"} {
		if !strings.Contains(footer, s) {
			t.Errorf("tab footer missing %q", s)
		}
	}
	scanFooter(t, footer)
}

// MO-7 review #5: the premarket frame is frozen after the open and
// deterministic — the replay-path contract (-view-mode premarket at
// 08:00 twice, and at 09:30 vs 09:45) at the cell level.
func TestTabFrozenAndDeterministic(t *testing.T) {
	c, table, pre, open := synth(t)
	cols, _, _ := c.Tab()
	render := func(calc *Calc, at int64) string {
		var sb strings.Builder
		for _, col := range calc.Columns() {
			sb.WriteString(col.Cell(row(table, at, "A", "B", "C", "D")) + "|")
		}
		return sb.String()
	}
	_ = cols
	frozen := render(c, open+65)
	if got := render(c, open+330*60); got != frozen {
		t.Errorf("15:00 cells %q != 09:31:05 cells %q (must be frozen)", got, frozen)
	}
	if got := render(c, open-1); got != frozen {
		t.Errorf("09:29:59 cells %q != post-open cells %q (window already complete)", got, frozen)
	}
	// Determinism: a fresh Calc over the same store renders the same bytes.
	c2 := New(c.store, c.union)
	if a, b := render(c, pre+4*3600), render(c2, pre+4*3600); a != b {
		t.Errorf("08:00 render differs across instances: %q vs %q", a, b)
	}
	if a := render(c, pre+4*3600); a != render(c, pre+4*3600) {
		t.Error("08:00 render differs across calls")
	}
}

// MO-7 review #3: the clock-line status names the window and whether it
// still moves.
func TestStatus(t *testing.T) {
	c, _, pre, open := synth(t)
	cases := map[int64]string{
		pre - 1:      "premarket window 04:00–09:30 not yet open",
		pre + 4*3600: "premarket window 04:00–08:00:00, live",
		open - 1:     "premarket window 04:00–09:29:59, live",
		open:         "premarket window 04:00–09:30 frozen",
		open + 65:    "premarket window 04:00–09:30 frozen",
	}
	for at, want := range cases {
		if got := c.Status(at); got != want {
			t.Errorf("Status(%d) = %q, want %q", at, got, want)
		}
	}
	if Frozen(open-1) || !Frozen(open) {
		t.Error("Frozen boundary is 09:30:00")
	}
	scanFooter(t, c.Status(open))
}
