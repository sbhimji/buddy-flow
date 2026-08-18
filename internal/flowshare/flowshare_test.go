package flowshare

import (
	"fmt"
	"strings"
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
	sym, frac, ok := Concentration(store, states(table, "A", "B", "C"), profile.Counted, start, start+60)
	if !ok || sym != "C" || frac != 0.6 {
		t.Errorf("conc = %s %v %v; want C 0.6 true", sym, frac, ok)
	}
	// Zero-volume basket: gap, never "D 0%".
	if _, _, ok := Concentration(store, states(table, "D"), profile.Counted, start, start+60); ok {
		t.Error("zero-volume basket reported a concentration")
	}
	// Auction-inclusive slice: a $600 cross on A (condition 18) flips the
	// leader under CountedWithAuctions but not under Counted.
	store.ObserveTrade(&ingest.Trade{State: table.Lookup("A"), Price: 1, Size: 600, SipTs: start * 1e9,
		Cond: [ingest.MaxConditions]int32{18}, NCond: 1})
	sym, _, _ = Concentration(store, states(table, "A", "B", "C"), profile.Counted, start, start+60)
	if sym != "C" {
		t.Errorf("counted-slice leader = %s, want C (cross must not count)", sym)
	}
	sym, frac, ok = Concentration(store, states(table, "A", "B", "C"), profile.CountedWithAuctions, start, start+60)
	if !ok || sym != "A" || frac != 900.0/1600.0 {
		t.Errorf("auction-slice conc = %s %v %v; want A %v true", sym, frac, ok, 900.0/1600.0)
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
	sym, frac, ok := Concentration(s, states(table, "A", "B"), profile.Counted, start, start+60)
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
	// One CROSS print (condition 18 → CROSS_REOPEN) inside the completed
	// 13:00 minute: A crosses $100 at 13:00:30. The same print must be
	// EXCLUDED from every per-minute column (Counted slice) and INCLUDED
	// in the cumulative columns (auction-inclusive slice, D7 amendment).
	store.ObserveTrade(&ingest.Trade{State: table2.Lookup("A"), Price: 1, Size: 100, SipTs: (start + 30) * 1e9,
		Cond: [ingest.MaxConditions]int32{18}, NCond: 1})

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
	// flow_share_z: completed 13:00 COUNTED share = 400/1000 = 0.4 (the
	// $100 cross excluded) against median 0.25 at minute 780, σ 0 floored
	// to 0.0625 → z = +2.4.
	if got["flow_share_z"] != "+2.4" {
		t.Errorf("flow_share_z = %q, want +2.4", got["flow_share_z"])
	}
	// concentration: completed minute, Counted slice → A $300 of $400
	// (A's cross does not lift it to 400/500).
	if got["concentration"] != "A 75%" {
		t.Errorf("concentration = %q, want A 75%%", got["concentration"])
	}
	// D7 cumulative columns, live cadence: the DISPLAYED cum_share runs
	// through this second — [09:30, 13:01:06) INCLUDES the 13:01:02 $50
	// print and the cross → (400+100+50)/(1000+100+50). The z stays frozen
	// on the completed-minute share [09:30, 13:01) = 500/1100, keyed to
	// minute 780 (a wrong key hits CumDays=0 and gaps) — live share and z
	// diverging intra-minute is the live-cadence design, so the test pins
	// BOTH operands. Same-path float vars, never folded constants.
	liveShare, cmShare := 550.0/1150.0, 500.0/1100.0
	if want := fmt.Sprintf("%.2f%%", 100*liveShare); got["cum_share"] != want {
		t.Errorf("cum_share = %q, want %s", got["cum_share"], want)
	}
	if got["cum_share_typ"] != "25.00%" {
		t.Errorf("cum_share_typ = %q, want 25.00%%", got["cum_share_typ"])
	}
	med, floor := 0.25, 0.125
	if want := fmt.Sprintf("%+.1f", (cmShare-med)/floor); got["cum_share_z"] != want {
		t.Errorf("cum_share_z = %q, want %s", got["cum_share_z"], want)
	}
	// concentration_day: same live window and slice as cum_share — basket
	// {A,B}, A carries $300 counted + $100 cross + $50 in-progress = $450
	// of $550.
	if got["concentration_day"] != "A 82%" {
		t.Errorf("concentration_day = %q, want A 82%%", got["concentration_day"])
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
		case "flow_share", "concentration", "cum_share", "cum_share_typ", "cum_share_z", "concentration_day":
			if out != "·" {
				t.Errorf("%s on empty window = %q, want gap", c.Name, out)
			}
		}
	}
}

// TestLiveCadence pins the live-cadence acceptance criteria directly
// (trader-view-live-cadence mini-spec): during the FIRST session minute
// cum_share and concentration_day render from the delta window alone
// (auction included) while typ and z gap — no completed minute exists yet
// — and within a later minute, z/typ hold identical values at every
// second while cum_share moves.
func TestLiveCadence(t *testing.T) {
	table := ingest.NewTable([]string{"A", "B"})
	store := bucket.NewStore()
	open, err := session.BucketStart("2026-08-14", session.OpenMinute)
	if err != nil {
		t.Fatal(err)
	}
	// Opening cross $300 on A at 09:30:00, then $100 on B at 09:30:10.
	store.ObserveTrade(&ingest.Trade{State: table.Lookup("A"), Price: 1, Size: 300, SipTs: open * 1e9,
		Cond: [ingest.MaxConditions]int32{18}, NCond: 1})
	store.ObserveTrade(&ingest.Trade{State: table.Lookup("B"), Price: 1, Size: 100, SipTs: (open + 10) * 1e9})

	prof := &profile.ShareProfile{Basket: "x", Rows: make([]profile.ShareRow, session.MinutesPerSession)}
	floors := &profile.Floors{Rows: make([]profile.FloorRow, session.MinutesPerSession)}
	for i := range prof.Rows {
		prof.Rows[i] = profile.ShareRow{MinuteOfDay: session.OpenMinute + i,
			CumDays: 20, MedianCumShare: 0.25, SigmaCumShare: 0.125}
		floors.Rows[i] = profile.FloorRow{MinuteOfDay: session.OpenMinute + i, SigmaFloorCumShare: 0.0625}
	}
	cols := Columns(store, states(table, "A", "B"), map[string]*profile.ShareProfile{"x": prof}, floors)
	cell := func(name string, atSec int64) string {
		rc := &devview.RowCtx{Basket: &devview.BasketRow{Name: "x", States: states(table, "A")}, AtSec: atSec}
		for _, c := range cols {
			if c.Name == name {
				return c.Cell(rc)
			}
		}
		t.Fatalf("no column %s", name)
		return ""
	}
	// 09:30:05 — first minute in progress: cum_share = A's cross $300 of
	// $300 (B's print is 5s in the future), typ/z gap.
	if got := cell("cum_share", open+5); got != "100.00%" {
		t.Errorf("first-minute cum_share = %q, want 100.00%%", got)
	}
	if got := cell("concentration_day", open+5); got != "A 100%" {
		t.Errorf("first-minute concentration_day = %q, want A 100%%", got)
	}
	for _, name := range []string{"cum_share_typ", "cum_share_z"} {
		if got := cell(name, open+5); got != "·" {
			t.Errorf("first-minute %s = %q, want gap", name, got)
		}
	}
	// 09:30:10 — B's print lands: live share moves within the minute.
	if got := cell("cum_share", open+10); got != "75.00%" {
		t.Errorf("intra-minute cum_share = %q, want 75.00%%", got)
	}
	// 09:31:05 vs 09:31:59 — same completed minute, so z and typ are
	// second-invariant; cum_share is the column allowed to move.
	for _, name := range []string{"cum_share_typ", "cum_share_z"} {
		a, b := cell(name, open+65), cell(name, open+119)
		if a != b || a == "·" {
			t.Errorf("%s within one minute = %q then %q; want identical non-gaps", name, a, b)
		}
	}
	// z operand is the completed 09:30 minute (share 0.75 vs median 0.25,
	// σ 0.125): (0.75−0.25)/0.125 = +4.0.
	if got := cell("cum_share_z", open+65); got != "+4.0" {
		t.Errorf("cum_share_z = %q, want +4.0", got)
	}
}

// TestTraderColumns: the trader-view-v0 set — column order, the |z| ≥ 2
// highlight (green positive), and the rank function.
func TestTraderColumns(t *testing.T) {
	store, table, start := synthStore(t)
	prof := &profile.ShareProfile{Basket: "x", Rows: make([]profile.ShareRow, session.MinutesPerSession)}
	floors := &profile.Floors{Rows: make([]profile.FloorRow, session.MinutesPerSession)}
	for i := range prof.Rows {
		// Cum baseline 0.1 with σ 0, floor 0.125: cum share 0.4 → z = +2.4,
		// beyond SignificantZ → green highlight.
		prof.Rows[i] = profile.ShareRow{MinuteOfDay: session.OpenMinute + i,
			CumDays: 20, MedianCumShare: 0.1, SigmaCumShare: 0}
		floors.Rows[i] = profile.FloorRow{MinuteOfDay: session.OpenMinute + i, SigmaFloorCumShare: 0.125}
	}
	// Stub breadth column (the real one is composed by the command from
	// internal/breadth); its Legend must be cleared by TraderColumns.
	breadthStub := devview.Column{Name: "breadth", Width: 7, Legend: "should be cleared",
		Cell: func(rc *devview.RowCtx) string { return "1/2" }}
	cols, rank, footer := TraderColumns(store, states(table, "A", "B", "C"), map[string]*profile.ShareProfile{"x": prof}, floors, breadthStub)
	wantOrder := []string{"cum_share", "cum_share_typ", "cum_share_z", "relative_vol", "breadth", "concentration", "concentration_day"}
	for i, w := range wantOrder {
		if cols[i].Name != w {
			t.Fatalf("column %d = %s, want %s", i, cols[i].Name, w)
		}
		// The footer defines every rendered column (plus the gap glyph);
		// the clock-line legends moved there — columns carry none.
		if !strings.Contains(footer, w+" ") {
			t.Errorf("footer does not define column %s", w)
		}
		if cols[i].Legend != "" {
			t.Errorf("column %s carries a clock-line legend %q; the footer explains columns now", w, cols[i].Legend)
		}
	}
	if !strings.Contains(footer, "·") {
		t.Error("footer does not define the gap glyph")
	}
	if !strings.Contains(footer, "SPY (top line)") {
		t.Error("footer does not define the clock-line SPY status")
	}
	// Scope law: statements of measurement only — spot-guard the obvious
	// violations in rendered trader-facing text.
	for _, banned := range []string{"buy", "sell", "Buy", "Sell"} {
		if strings.Contains(footer, banned) {
			t.Errorf("footer contains %q — scope law bans buy/sell language", banned)
		}
	}
	rc := &devview.RowCtx{Basket: &devview.BasketRow{Name: "x", States: states(table, "A", "B")}, AtSec: start + 65}
	zc := cols[2]
	if got := zc.Cell(rc); got != "+2.4" {
		t.Errorf("cum_share_z = %q, want +2.4", got)
	}
	if got := zc.Style(rc); got != sgrGreen {
		t.Errorf("style = %q, want green (z beyond +%v)", got, SignificantZ)
	}
	// Same-path float expectation via float64 VARIABLES — a constant
	// expression would fold at infinite precision and miss the runtime
	// rounding zguard actually performs.
	share, med, floor := 400.0/1000.0, 0.1, 0.125
	if r, ok := rank(rc); !ok || r != (share-med)/floor {
		t.Errorf("rank = %v, %v; want %v, true", r, ok, (share-med)/floor)
	}
	// Unknown basket: no z → no style, rank gap (sorts last).
	rcU := &devview.RowCtx{Basket: &devview.BasketRow{Name: "nope", States: states(table, "D")}, AtSec: start + 65}
	if got := zc.Style(rcU); got != "" {
		t.Errorf("gap style = %q, want none", got)
	}
	if _, ok := rank(rcU); ok {
		t.Error("gap rank reported ok")
	}
	// Below-threshold z: value renders, no highlight. Median 0.25 → z 1.2.
	for i := range prof.Rows {
		prof.Rows[i].MedianCumShare = 0.25
	}
	if got := zc.Cell(rc); got != "+1.2" {
		t.Errorf("cum_share_z = %q, want +1.2", got)
	}
	if got := zc.Style(rc); got != "" {
		t.Errorf("sub-threshold style = %q, want none", got)
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
