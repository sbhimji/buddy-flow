package flowshare

import (
	"testing"

	"buddy-flow/internal/bucket"
	"buddy-flow/internal/devview"
	"buddy-flow/internal/ingest"
	"buddy-flow/internal/profile"
	"buddy-flow/internal/session"
)

// synth: A trades $300, B $100, C $600 in one minute; D silent.
func synthStore(t *testing.T) (*bucket.Store, *ingest.Table, int64) {
	t.Helper()
	table := ingest.NewTable([]string{"A", "B", "C", "D"})
	s := bucket.NewStore()
	start, err := session.BucketStart("2026-08-14", 780) // 13:00
	if err != nil {
		t.Fatal(err)
	}
	for sym, d := range map[string]float64{"A": 300, "B": 100, "C": 600} {
		s.ObserveTrade(&ingest.Trade{State: table.Lookup(sym), Price: 1, Size: d, SipTs: start * 1e9})
	}
	return s, table, start
}

func states(table *ingest.Table, syms ...string) []*ingest.SymbolState {
	out := make([]*ingest.SymbolState, len(syms))
	for i, s := range syms {
		out[i] = table.Lookup(s)
	}
	return out
}

func TestShare(t *testing.T) {
	store, table, start := synthStore(t)
	union := states(table, "A", "B", "C", "D")
	// Basket {A,B}: (300+100)/1000 = 0.4.
	s, ok := Share(store, states(table, "A", "B"), union, start, start+60)
	if !ok || s != 0.4 {
		t.Errorf("share = %v, %v; want 0.4, true", s, ok)
	}
	// Empty window: 0/0 is a gap, never 0%.
	if _, ok := Share(store, states(table, "A"), union, start-60, start); ok {
		t.Error("0/0 share reported ok")
	}
}

func TestConcentration(t *testing.T) {
	store, table, start := synthStore(t)
	// {A,B,C}: C dominates with 600/1000.
	sym, frac, ok := Concentration(store, states(table, "A", "B", "C"), start, start+60)
	if !ok || sym != "C" || frac != 0.6 {
		t.Errorf("conc = %s %v %v; want C 0.6 true", sym, frac, ok)
	}
	// Zero-volume basket: gap, never "D 0%".
	if _, _, ok := Concentration(store, states(table, "D"), start, start+60); ok {
		t.Error("zero-volume basket reported a concentration")
	}
}

// TestConcentrationTieBreak: equal top members resolve alphabetically
// (first-wins over the sorted member order) — snapshot determinism.
func TestConcentrationTieBreak(t *testing.T) {
	table := ingest.NewTable([]string{"A", "B"})
	s := bucket.NewStore()
	start, err := session.BucketStart("2026-08-14", 780)
	if err != nil {
		t.Fatal(err)
	}
	for _, sym := range []string{"B", "A"} { // observe B first: order of prints must not matter
		s.ObserveTrade(&ingest.Trade{State: table.Lookup(sym), Price: 1, Size: 100, SipTs: start * 1e9})
	}
	sym, frac, ok := Concentration(s, states(table, "A", "B"), start, start+60)
	if !ok || sym != "A" || frac != 0.5 {
		t.Errorf("tie = %s %v %v; want A 0.5 true", sym, frac, ok)
	}
}

// TestColumns exercises the cell plumbing end to end: window boundaries,
// the completed minute's matched-bucket key (a cur-instead-of-cur−60 bug
// would silently mismatch baselines by one bucket), gap rendering, and
// snapshot memoization across rows.
func TestColumns(t *testing.T) {
	store, table, start := synthStore(t) // A $300, B $100, C $600 in the 13:00 minute
	// One print in the in-progress 13:01 minute: A trades $50 at 13:01:02.
	table2 := table
	store.ObserveTrade(&ingest.Trade{State: table2.Lookup("A"), Price: 1, Size: 50, SipTs: (start + 62) * 1e9})

	prof := &profile.ShareProfile{Basket: "x", Rows: make([]profile.ShareRow, session.MinutesPerSession)}
	floors := &profile.Floors{Rows: make([]profile.FloorRow, session.MinutesPerSession)}
	for i := range prof.Rows {
		// Only minute 780 (13:00) has a usable baseline; 781 has Days=0 —
		// if a z cell keyed the wrong minute, it would gap instead.
		days := 0
		if session.OpenMinute+i == 780 {
			days = 20
		}
		prof.Rows[i] = profile.ShareRow{MinuteOfDay: session.OpenMinute + i,
			Days: days, MedianShare: 0.25, SigmaShare: 0,
			CumDays: days, MedianCumShare: 0.25, SigmaCumShare: 0}
		floors.Rows[i] = profile.FloorRow{MinuteOfDay: session.OpenMinute + i,
			SigmaFloorFlowShare: 0.0625, SigmaFloorCumShare: 0.125}
	}
	union := states(table, "A", "B", "C")
	cols := Columns(store, union, map[string]*profile.ShareProfile{"x": prof}, floors)
	rc := &devview.RowCtx{
		Basket: &devview.BasketRow{Name: "x", States: states(table, "A", "B")},
		AtSec:  start + 65, // 13:01:05 — completed minute is 13:00
	}
	got := map[string]string{}
	for _, c := range cols {
		got[c.Name] = c.Cell(rc)
	}
	// flow_share: in-progress 13:01 so far = A's $50 of a $50 universe.
	if got["flow_share"] != "100.00%" {
		t.Errorf("flow_share = %q, want 100.00%%", got["flow_share"])
	}
	// flow_share_z: completed 13:00 share = 400/1000 = 0.4 against median
	// 0.25 at minute 780, σ 0 floored to 0.0625 → z = +2.4.
	if got["flow_share_z"] != "+2.4" {
		t.Errorf("flow_share_z = %q, want +2.4", got["flow_share_z"])
	}
	// concentration: completed minute, basket {A,B} → A $300 of $400.
	if got["concentration"] != "A 75%" {
		t.Errorf("concentration = %q, want A 75%%", got["concentration"])
	}
	// D7 cumulative columns: window [09:30, 13:01) EXCLUDES the 13:01:02
	// print — cum share = 400/1000 = 40%, keyed to minute 780 (a wrong key
	// hits CumDays=0 and gaps). z = (0.4 − 0.25) / max(0, 0.125) = +1.2.
	if got["cum_share"] != "40.00%" {
		t.Errorf("cum_share = %q, want 40.00%%", got["cum_share"])
	}
	if got["cum_share_typ"] != "25.00%" {
		t.Errorf("cum_share_typ = %q, want 25.00%%", got["cum_share_typ"])
	}
	if got["cum_share_z"] != "+1.2" {
		t.Errorf("cum_share_z = %q, want +1.2", got["cum_share_z"])
	}
	// A row rendered for an unknown basket gaps the z (no profile).
	rcQuiet := &devview.RowCtx{Basket: &devview.BasketRow{Name: "nope", States: states(table, "D")}, AtSec: start + 65}
	for _, c := range cols {
		if c.Name == "flow_share_z" {
			if out := c.Cell(rcQuiet); out != "·" {
				t.Errorf("unknown-basket z = %q, want gap", out)
			}
		}
	}
	// Empty window: render before any data (noon, nothing traded yet that
	// morning in this store) → share, conc, and all cum columns gap.
	rcPre := &devview.RowCtx{Basket: &devview.BasketRow{Name: "x", States: states(table, "A", "B")}, AtSec: start - 3600}
	for _, c := range cols {
		out := c.Cell(rcPre)
		switch c.Name {
		case "flow_share", "concentration", "cum_share", "cum_share_typ", "cum_share_z":
			if out != "·" {
				t.Errorf("%s on empty window = %q, want gap", c.Name, out)
			}
		}
	}
}

// TestCumShareZ covers the D7 z's null paths directly.
func TestCumShareZ(t *testing.T) {
	prof := &profile.ShareProfile{Basket: "x", Rows: make([]profile.ShareRow, session.MinutesPerSession)}
	floors := &profile.Floors{Rows: make([]profile.FloorRow, session.MinutesPerSession)}
	for i := range prof.Rows {
		prof.Rows[i] = profile.ShareRow{MinuteOfDay: session.OpenMinute + i, CumDays: 20, MedianCumShare: 0.25, SigmaCumShare: 0.125}
		floors.Rows[i] = profile.FloorRow{MinuteOfDay: session.OpenMinute + i, SigmaFloorCumShare: 0.0625}
	}
	if z, ok := CumShareZ(0.75, true, prof, floors, 780); !ok || z != 4 {
		t.Errorf("z = %v, %v; want 4, true", z, ok)
	}
	prof.Rows[780-session.OpenMinute].SigmaCumShare = 0
	if z, ok := CumShareZ(0.75, true, prof, floors, 780); !ok || z != 8 {
		t.Errorf("floored z = %v, %v; want 8, true", z, ok)
	}
	prof.Rows[781-session.OpenMinute].CumDays = 9
	if _, ok := CumShareZ(0.75, true, prof, floors, 781); ok {
		t.Error("z below MinProfiledDays reported ok")
	}
	if _, ok := CumShareZ(0, false, prof, floors, 782); ok {
		t.Error("z on a gapped share reported ok")
	}
	if _, ok := CumShareZ(0.75, true, prof, floors, session.CloseMinute); ok {
		t.Error("z outside session reported ok")
	}
}

func TestShareZ(t *testing.T) {
	prof := &profile.ShareProfile{Basket: "x", Rows: make([]profile.ShareRow, session.MinutesPerSession)}
	floors := &profile.Floors{Rows: make([]profile.FloorRow, session.MinutesPerSession)}
	// Powers of two throughout — every expectation is float-exact.
	for i := range prof.Rows {
		prof.Rows[i] = profile.ShareRow{MinuteOfDay: session.OpenMinute + i, Days: 20, MedianShare: 0.25, SigmaShare: 0.125}
		floors.Rows[i] = profile.FloorRow{MinuteOfDay: session.OpenMinute + i, SigmaFloorFlowShare: 0.0625}
	}
	// Plain: (0.75 − 0.25) / max(0.125, 0.0625) = 4.
	if z, ok := ShareZ(0.75, true, prof, floors, 780); !ok || z != 4 {
		t.Errorf("z = %v, %v; want 4, true", z, ok)
	}
	// MAD=0 basket bounded by the floor: (0.75 − 0.25) / 0.0625 = 8.
	prof.Rows[780-session.OpenMinute].SigmaShare = 0
	if z, ok := ShareZ(0.75, true, prof, floors, 780); !ok || z != 8 {
		t.Errorf("floored z = %v, %v; want 8, true", z, ok)
	}
	// σ and floor both 0: defined null.
	floors.Rows[780-session.OpenMinute].SigmaFloorFlowShare = 0
	if _, ok := ShareZ(0.3, true, prof, floors, 780); ok {
		t.Error("0/0 z reported ok")
	}
	// Below the day gate: gap.
	prof.Rows[781-session.OpenMinute].Days = 9
	if _, ok := ShareZ(0.3, true, prof, floors, 781); ok {
		t.Error("z below MinProfiledDays reported ok")
	}
	// 0/0 share upstream: gap regardless of baseline.
	if _, ok := ShareZ(0, false, prof, floors, 782); ok {
		t.Error("z on a gapped share reported ok")
	}
	// Outside the profiled session: gap.
	if _, ok := ShareZ(0.3, true, prof, floors, session.CloseMinute); ok {
		t.Error("z outside session reported ok")
	}
}
