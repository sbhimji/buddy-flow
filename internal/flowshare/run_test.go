package flowshare

import (
	"strings"
	"testing"

	"buddy-flow/internal/bucket"
	"buddy-flow/internal/devview"
	"buddy-flow/internal/ingest"
	"buddy-flow/internal/profile"
	"buddy-flow/internal/session"
)

// runSynth: union {A, B, C}, basket x = {A, B}, open 09:30 on 2026-08-14.
// Every minute the union prints $1000 (Counted, no crosses); the basket's
// share per minute is basketShare[i]. Baseline: median share 0.25, σ 0
// floored at 0.0625 → flow_share_z = (share − 0.25)/0.0625; cumulative
// median 0.25, floor 0.125 → cum_share_z = (cum share − 0.25)/0.125.
// days[i] (nil = all 20) lets a minute's baseline be withheld.
func runSynth(t *testing.T, basketShare []float64, days []int) (*Run, *devview.RowCtx, int64) {
	t.Helper()
	table := ingest.NewTable([]string{"A", "B", "C"})
	s := bucket.NewStore()
	open, err := session.BucketStart("2026-08-14", session.OpenMinute)
	if err != nil {
		t.Fatal(err)
	}
	for i, sh := range basketShare {
		sec := open + int64(i)*60 + 10
		if sh > 0 {
			s.ObserveTrade(&ingest.Trade{State: table.Lookup("A"), Price: 1, Size: 1000 * sh, SipTs: sec * 1e9})
		}
		if sh < 1 {
			s.ObserveTrade(&ingest.Trade{State: table.Lookup("C"), Price: 1, Size: 1000 * (1 - sh), SipTs: sec * 1e9})
		}
	}
	prof := &profile.ShareProfile{Basket: "x", Rows: make([]profile.ShareRow, session.MinutesPerSession)}
	floors := &profile.Floors{Rows: make([]profile.FloorRow, session.MinutesPerSession)}
	for i := range prof.Rows {
		d := 20
		if days != nil && i < len(days) {
			d = days[i]
		}
		prof.Rows[i] = profile.ShareRow{MinuteOfDay: session.OpenMinute + i,
			Days: d, MedianShare: 0.25, CumDays: d, MedianCumShare: 0.25}
		floors.Rows[i] = profile.FloorRow{MinuteOfDay: session.OpenMinute + i,
			SigmaFloorFlowShare: 0.0625, SigmaFloorCumShare: 0.125}
	}
	r := NewRun(s, states(table, "A", "B", "C"), map[string]*profile.ShareProfile{"x": prof}, floors)
	rc := &devview.RowCtx{Basket: &devview.BasketRow{Name: "x", States: states(table, "A", "B")}}
	return r, rc, open
}

func at(rc *devview.RowCtx, sec int64) *devview.RowCtx {
	return &devview.RowCtx{Basket: rc.Basket, AtSec: sec}
}

// TestRunWindowBoundary: 5m_z gaps at four completed minutes and renders
// at five (09:35:00 is the first render); the value is the mean of the
// five one-minute z's, in minute order.
func TestRunWindowBoundary(t *testing.T) {
	// shares 0.5 (z +4), 0.5, 0.25 (z 0), 0.375 (z +2), 0.5, 0.25
	r, rc, open := runSynth(t, []float64{0.5, 0.5, 0.25, 0.375, 0.5, 0.25}, nil)
	if _, ok := r.FiveMinuteZ(at(rc, open+4*60+30)); ok {
		t.Error("5m_z rendered on 4 completed minutes")
	}
	z, ok := r.FiveMinuteZ(at(rc, open+5*60))
	if !ok || z != (4+4+0+2+4)/5.0 {
		t.Errorf("5m_z at 09:35:00 = %v, %v; want %v", z, ok, (4+4+0+2+4)/5.0)
	}
	// One minute later the window slides: minutes 1..5.
	z, ok = r.FiveMinuteZ(at(rc, open+6*60+5))
	if !ok || z != (4+0+2+4+0)/5.0 {
		t.Errorf("5m_z at 09:36:05 = %v, %v; want %v", z, ok, (4+0+2+4+0)/5.0)
	}
	// 15m_z needs fifteen.
	if _, ok := r.WindowZ(at(rc, open+6*60+5), RunWindowLong); ok {
		t.Error("15m_z rendered on 6 completed minutes")
	}
	cols := r.Columns()
	if cols[1].Name != "5m_z" || cols[1].Cell(at(rc, open+5*60)) != "+2.8" {
		t.Errorf("5m_z cell = %q, want +2.8", cols[1].Cell(at(rc, open+5*60)))
	}
	if got := cols[1].Style(at(rc, open+5*60)); got != sgrGreen {
		t.Errorf("5m_z style = %q, want green at +2.8", got)
	}
	if lit, pos, ok := r.RunFlag(at(rc, open+5*60)); !lit || !pos || !ok {
		t.Errorf("RunFlag = %v %v %v; want lit positive ok", lit, pos, ok)
	}
	// Slid to +2.0 exactly: at the threshold is lit (≥).
	if lit, _, _ := r.RunFlag(at(rc, open+6*60+5)); !lit {
		t.Error("RunFlag unlit at exactly +2.0")
	}
}

// TestRunWindowGapInside: a minute without a baseline inside the window
// gaps the mean — never a mean over fewer; once it leaves the window the
// mean returns.
func TestRunWindowGapInside(t *testing.T) {
	days := []int{20, 20, 0, 20, 20, 20, 20, 20}
	r, rc, open := runSynth(t, []float64{0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5}, days)
	if _, ok := r.FiveMinuteZ(at(rc, open+6*60)); ok {
		t.Error("5m_z rendered over a window containing an un-baselined minute")
	}
	if z, ok := r.FiveMinuteZ(at(rc, open+8*60)); !ok || z != 4 {
		t.Errorf("5m_z after the gap left the window = %v, %v; want +4", z, ok)
	}
	if got := r.Columns()[1].Cell(at(rc, open+6*60)); got != gap {
		t.Errorf("gapped cell = %q, want %q", got, gap)
	}
	if got := r.Columns()[1].Style(at(rc, open+6*60)); got != "" {
		t.Errorf("gapped cell styled %q", got)
	}
	// A 0/0 minute (no union prints) gaps too.
	r2, _, open2 := runSynth(t, []float64{0.5}, nil)
	table := ingest.NewTable([]string{"A", "B", "C"})
	s := bucket.NewStore()
	for i, sh := range []float64{0.5, 0.5, -1, 0.5, 0.5, 0.5} { // minute 2 silent
		if sh < 0 {
			continue
		}
		sec := open2 + int64(i)*60 + 10
		s.ObserveTrade(&ingest.Trade{State: table.Lookup("A"), Price: 1, Size: 500, SipTs: sec * 1e9})
		s.ObserveTrade(&ingest.Trade{State: table.Lookup("C"), Price: 1, Size: 500, SipTs: sec * 1e9})
	}
	r3 := NewRun(s, states(table, "A", "B", "C"), r2.shares, r2.floors)
	rc3 := &devview.RowCtx{Basket: &devview.BasketRow{Name: "x", States: states(table, "A", "B")}, AtSec: open2 + 6*60}
	if _, ok := r3.FiveMinuteZ(rc3); ok {
		t.Error("5m_z rendered over a window containing a 0/0 minute")
	}
}

// TestRunSince: the first |cum_share_z| ≥ 2 minute is the record — kept
// after the cumulative falls back; blank (measured, never crossed) when
// no minute reached it; the gap when no minute had a defined z.
func TestRunSince(t *testing.T) {
	// cum shares: 0.25, 0.25, (250+250+1000)/3000 = 0.5 → z +2.0 at 09:32,
	// then 1250/4000 = 0.3125 (z +0.5), 1250/5000 = 0.25 (z 0).
	r, rc, open := runSynth(t, []float64{0.25, 0.25, 1, 0, 0}, nil)
	if hhmm, crossed, measured := r.Since(at(rc, open+2*60+5)); hhmm != "" || crossed || !measured {
		t.Errorf("since before the crossing = %q %v %v; want blank, measured", hhmm, crossed, measured)
	}
	if hhmm, crossed, _ := r.Since(at(rc, open+3*60)); hhmm != "09:32" || !crossed {
		t.Errorf("since at the crossing = %q %v; want 09:32", hhmm, crossed)
	}
	if hhmm, _, _ := r.Since(at(rc, open+5*60+30)); hhmm != "09:32" {
		t.Errorf("since after falling back = %q; want 09:32 (the record)", hhmm)
	}
	col := r.Columns()[0]
	if col.Name != "since" || col.Cell(at(rc, open+5*60+30)) != "09:32" {
		t.Errorf("since cell = %q", col.Cell(at(rc, open+5*60+30)))
	}
	if got := col.Cell(at(rc, open+2*60+5)); got != "" {
		t.Errorf("never-crossed since cell = %q, want blank", got)
	}
	// Negative crossing counts: basket silent while the union prints →
	// cum share 0 → z −2.0.
	rn, rcn, openn := runSynth(t, []float64{0, 0}, nil)
	if hhmm, _, _ := rn.Since(at(rcn, openn+60)); hhmm != "09:30" {
		t.Errorf("negative crossing since = %q, want 09:30", hhmm)
	}
	// Pre-open / no completed minute: nothing measured → the gap glyph.
	if got := col.Cell(at(rc, open-5)); got != gap {
		t.Errorf("pre-open since = %q, want %q", got, gap)
	}
	// Unknown basket (no baseline): the gap, never blank.
	rcU := &devview.RowCtx{Basket: &devview.BasketRow{Name: "nope", States: rc.Basket.States}, AtSec: open + 5*60}
	if got := col.Cell(rcU); got != gap {
		t.Errorf("no-baseline since = %q, want %q", got, gap)
	}
}

// TestRunSeriesMatchesCells: the last minute of the run series is the
// SAME number the flow_share_z / cum_share_z cells print (one atom, two
// readers) — on a store with crosses, so both slices are exercised.
func TestRunSeriesMatchesCells(t *testing.T) {
	r, rc, open := runSynth(t, []float64{0.5, 0.375, 0.5, 0.25, 0.5, 0.375}, nil)
	// A $200 opening-cross print in minute 0: excluded from the per-minute
	// family, included in the cumulative one.
	r.store.ObserveTrade(&ingest.Trade{State: rc.Basket.States[0], Price: 1, Size: 200, SipTs: (open + 1) * 1e9,
		Cond: [ingest.MaxConditions]int32{18}, NCond: 1})
	c := &cells{store: r.store, union: r.union, shares: r.shares, floors: r.floors}
	cols := Columns(r.store, r.union, r.shares, r.floors)
	cell := map[string]devview.Column{}
	for _, c := range cols {
		cell[c.Name] = c
	}
	for _, sec := range []int64{open + 60 + 5, open + 4*60, open + 6*60 + 59} {
		rcs := at(rc, sec)
		_, bs := r.basket(rcs)
		n := len(bs.z)
		// Float values, not rendered digits: the cells' own operands.
		sh, shOK := c.prevShare(rcs)
		z, zOK := ShareZ(sh, shOK, r.shares["x"], r.floors, session.OpenMinute+n-1)
		if z != bs.z[n-1] || zOK != bs.zOK[n-1] {
			t.Errorf("at +%ds flow_share_z cell %v,%v vs series %v,%v", sec-open, z, zOK, bs.z[n-1], bs.zOK[n-1])
		}
		cz, czOK := c.cumZ(rcs)
		if cz != bs.cumZ[n-1] || czOK != bs.cumZOK[n-1] {
			t.Errorf("at +%ds cum_share_z cell %v,%v vs series %v,%v", sec-open, cz, czOK, bs.cumZ[n-1], bs.cumZOK[n-1])
		}
		if got := cell["cum_share_z"].Cell(rcs); got != fmtZ(bs.cumZ[n-1], bs.cumZOK[n-1]) {
			t.Errorf("at +%ds cum_share_z rendered %q vs series %q", sec-open, got, fmtZ(bs.cumZ[n-1], bs.cumZOK[n-1]))
		}
	}
}

// TestRunDeterminism: two renders of the same store state, same bytes;
// a fresh second invalidates the memo (no stale series).
func TestRunDeterminism(t *testing.T) {
	r, rc, open := runSynth(t, []float64{0.5, 0.5, 0.25, 0.375, 0.5, 0.25, 1}, nil)
	render := func(sec int64) string {
		var sb strings.Builder
		for _, c := range r.DevColumns() {
			sb.WriteString(c.Cell(at(rc, sec)) + "|")
		}
		return sb.String()
	}
	a, b := render(open+6*60), render(open+6*60)
	if a != b {
		t.Errorf("renders differ: %q vs %q", a, b)
	}
	if c := render(open + 7*60); c == a {
		t.Errorf("render did not advance with the second: %q", c)
	}
	if !strings.HasPrefix(a, "09:30|") { // cum share 0.5 → +2.0 at the first minute
		t.Errorf("dev render = %q", a)
	}
}

// TestRunExtendTrader: since/5m_z land right after cum_share_z, the
// footer block before the gap-glyph line, both anchors loud on a miss;
// the footer passes the scope-law scanner.
func TestRunExtendTrader(t *testing.T) {
	r, _, _ := runSynth(t, []float64{0.5}, nil)
	cols := []devview.Column{{Name: "cum_share"}, {Name: "cum_share_typ"}, {Name: "cum_share_z"}, {Name: "breadth"}}
	out, footer := r.ExtendTrader(cols, TraderFooter)
	names := make([]string, len(out))
	for i, c := range out {
		names[i] = c.Name
	}
	if got := strings.Join(names, " "); got != "cum_share cum_share_typ cum_share_z since 5m_z breadth" {
		t.Errorf("order = %s", got)
	}
	if !strings.Contains(footer, "\n5m_z ") || strings.Index(footer, "\n5m_z ") > strings.Index(footer, "\n·  ") {
		t.Error("run footer block not before the gap-glyph line")
	}
	for _, banned := range []string{"buy", "sell", "Buy", "Sell", "bullish", "bearish", "signal", "chase"} {
		if strings.Contains(RunFooter, banned) {
			t.Errorf("run footer contains %q — scope law", banned)
		}
	}
	if !strings.Contains(RunFooter, "±2.0σ") || !strings.Contains(RunFooter, "last 5 completed") {
		t.Errorf("run footer thresholds not built from the constants:\n%s", RunFooter)
	}
	mustPanic := func(name string, f func()) {
		defer func() {
			if recover() == nil {
				t.Errorf("%s: no panic", name)
			}
		}()
		f()
	}
	mustPanic("missing cum_share_z", func() { r.ExtendTrader([]devview.Column{{Name: "breadth"}}, TraderFooter) })
	mustPanic("missing gap line", func() { r.ExtendTrader(cols, "no gap line here\n") })
}
