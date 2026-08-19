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
	if got := cell(t, c, "pre_conc", row(table, at, "A", "B", "C")); got != "C 60%" {
		t.Errorf("pre_conc = %q, want C 60%%", got)
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
	if len(cols) != 3 || cols[0].Name != "pre_vol" || cols[1].Name != "pre_share" || cols[2].Name != "pre_conc" {
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
	for _, s := range []string{"base\n", "pre_vol", "pre_share", "pre_conc", "premarket caveat"} {
		if !strings.Contains(footer, s) {
			t.Errorf("footer missing %q", s)
		}
	}
	for _, banned := range []string{"buy", "sell", "Buy", "Sell"} {
		if strings.Contains(footer, banned) {
			t.Errorf("footer contains %q — scope law", banned)
		}
	}
}
