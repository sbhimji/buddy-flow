package tickerview

import (
	"strings"
	"testing"

	"buddy-flow/internal/bucket"
	"buddy-flow/internal/devview"
	"buddy-flow/internal/ingest"
	"buddy-flow/internal/profile"
	"buddy-flow/internal/session"
	"buddy-flow/internal/universe"
)

const condOpeningCross = 17 // Market Center Opening Trade → CROSS_OPEN

// synthProfiles hand-builds per-ticker profiles: every minute has 20 days,
// median 100 sh / $100 per minute (σ$ 10), and a typical cum_$ of
// $1000 × (minutes completed) with σ 0 — so the cum floor alone (200)
// sets the z scale and every z is hand-computable.
func synthProfiles(syms []string) (map[string]*profile.Profile, *profile.Floors) {
	profs := map[string]*profile.Profile{}
	for _, s := range syms {
		p := &profile.Profile{Symbol: s, Rows: make([]profile.Row, session.MinutesPerSession), HasCumDollars: true}
		for i := range p.Rows {
			p.Rows[i] = profile.Row{MinuteOfDay: session.OpenMinute + i, Days: 20,
				MedianShares: 100, MedianDollars: 100, SigmaDollars: 10,
				MedianCumDollars: 1000 * float64(i+1)}
		}
		profs[s] = p
	}
	fl := &profile.Floors{Rows: make([]profile.FloorRow, session.MinutesPerSession), HasCumDollars: true}
	for i := range fl.Rows {
		fl.Rows[i] = profile.FloorRow{MinuteOfDay: session.OpenMinute + i, SigmaFloorDollars: 10, SigmaFloorCumDollars: 200}
	}
	return profs, fl
}

// synth: in the 09:30 minute A prints an opening cross 100 @ $10 then
// 50 @ $12 continuous (cum $1600); B 20 @ $10 ($200); C 100 @ $10 ($1000);
// D silent. Nothing prints after 09:30.
func synth(t *testing.T) (*Calc, *bucket.Store, *ingest.Table, int64) {
	t.Helper()
	syms := []string{"A", "B", "C", "D"}
	table := ingest.NewTable(syms)
	store := bucket.NewStore()
	open, err := session.BucketStart("2026-08-14", session.OpenMinute)
	if err != nil {
		t.Fatal(err)
	}
	tr := func(sym string, price, size float64, sec int64, cond ...int32) {
		tt := &ingest.Trade{State: table.Lookup(sym), Price: price, Size: size, SipTs: sec * 1e9}
		for i, c := range cond {
			tt.Cond[i] = c
		}
		tt.NCond = len(cond)
		store.ObserveTrade(tt)
	}
	tr("A", 10, 100, open, condOpeningCross)
	tr("A", 12, 50, open+30)
	tr("B", 10, 20, open+10)
	tr("C", 10, 100, open+20)
	profs, fl := synthProfiles(syms)
	baskets := []universe.Basket{{Name: "one", Members: []string{"A", "B", "C"}}, {Name: "two", Members: []string{"C", "D"}}}
	c, err := New(store, table, baskets, profs, fl, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c, store, table, open
}

func TestRowsAndDetail(t *testing.T) {
	c, _, _, open := synth(t)
	at := open + 65 // 09:31:05: 09:30 is the completed minute
	rows := c.Rows("one", at)
	// V1/T1 sort: cum_$_z desc — A +3.0, C 0.0, B −4.0.
	if got := []string{rows[0].Symbol, rows[1].Symbol, rows[2].Symbol}; strings.Join(got, "") != "ACB" {
		t.Fatalf("order = %v", got)
	}
	a := rows[0]
	if !a.ZOK || a.Z != 3 || a.Cum != 1600 || !a.TypOK || a.Typ != 1000 {
		t.Errorf("A cum family = %+v", a)
	}
	if !a.LastOK || a.Last != 12 || !a.OpenOK || a.OpenPct != 20 {
		t.Errorf("A last/open%% = %+v", a)
	}
	if !a.RVolOK || a.RVol != 0.5 || !a.DollarOK || a.DollarZ != 50 {
		t.Errorf("A rvol/$_z = %+v (counted slice excludes the cross)", a)
	}
	if !a.OfOK || a.OfBasket != 1600.0/2800.0 || a.Since != "09:30" {
		t.Errorf("A of_basket/since = %+v", a)
	}
	if rows[1].Since != "" || rows[2].Since != "09:30" {
		t.Errorf("since: C=%q B=%q", rows[1].Since, rows[2].Since)
	}
	// Silent D: no last, no open%, cum 0 with a real typical → z −5.0; rvol 0.
	d := c.Rows("two", at)[1]
	if d.Symbol != "D" || d.LastOK || d.OpenOK || !d.ZOK || d.Z != -5 || !d.RVolOK || d.RVol != 0 {
		t.Errorf("D = %+v", d)
	}

	got := c.Detail("one", at)
	want := "" +
		"    TICKER      last    open%     cum_$  cum_$_typ  cum_$_z  rvol_sh     $_z  of_basket  since\n" +
		"    A          12.00  +20.00%     1.60k      1.00k  \x1b[1;32m   +3.0\x1b[0m     0.50   +50.0        57%  09:30\n" +
		"    C          10.00        ·     1.00k      1.00k     +0.0     1.00   +90.0        36%       \n" +
		"    B          10.00        ·       200      1.00k  \x1b[1;31m   -4.0\x1b[0m     0.20   +10.0         7%  09:30\n"
	if got != want {
		t.Errorf("detail:\n%s\nwant:\n%s", got, want)
	}
	if got != c.Detail("one", at) {
		t.Error("two renders differ")
	}
	if c.Detail("nope", at) != "" {
		t.Error("unknown basket rendered something")
	}
}

func TestStripTimeOrderAndFallback(t *testing.T) {
	c, _, _, open := synth(t)
	at := open + 65
	got := c.Strip(at)
	want := "" +
		"crossed +2.0σ  09:30 A      \x1b[1;32m+3.0σ\x1b[0m  cum_$ 1.60k (typ 1.00k)  one  conv_z —\n" +
		"crossed −2.0σ  09:30 B      \x1b[1;31m-4.0σ\x1b[0m  cum_$ 200 (typ 1.00k)  one  conv_z —\n" +
		"               09:30 D      \x1b[1;31m-5.0σ\x1b[0m  cum_$ 0 (typ 1.00k)  two  conv_z —\n"
	if got != want {
		t.Errorf("strip:\n%s\nwant:\n%s", got, want)
	}
	// One minute later nothing has printed: typicals are $2000 by 09:31.
	// A: 1600 → −2.0 (run began 09:31); C: 1000 → −5.0 (09:31); B: −9.0
	// (since 09:30); D −10.0 (09:30). Newest run first, ties by symbol —
	// B's −9.0 sorts LAST: time order, never z order. A left the positive
	// list when its |z| fell back; its `since` stays 09:30.
	at2 := open + 125
	got = c.Strip(at2)
	want = "" +
		"crossed −2.0σ  09:31 A      \x1b[1;31m-2.0σ\x1b[0m  cum_$ 1.60k (typ 2.00k)  one      conv_z —\n" +
		"               09:31 C      \x1b[1;31m-5.0σ\x1b[0m  cum_$ 1.00k (typ 2.00k)  one,two  conv_z —\n" +
		"               09:30 B      \x1b[1;31m-9.0σ\x1b[0m  cum_$ 200 (typ 2.00k)  one      conv_z —\n" +
		"               09:30 D      \x1b[1;31m-10.0σ\x1b[0m  cum_$ 0 (typ 2.00k)  two      conv_z —\n"
	if got != want {
		t.Errorf("strip at 09:32:\n%s\nwant:\n%s", got, want)
	}
	if a := c.Rows("one", at2)[0]; a.Symbol != "A" || a.Since != "09:30" {
		t.Errorf("A after fallback = %+v", a)
	}
	// Pre-open / before the first completed minute: one honest line.
	if got := c.Strip(open + 30); got != "crossed ±2.0σ  ·\n" {
		t.Errorf("pre-first-minute strip = %q", got)
	}
}

func TestStripCap(t *testing.T) {
	syms := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		syms = append(syms, string(rune('A'+i))+"X")
	}
	table := ingest.NewTable(syms)
	store := bucket.NewStore()
	open, _ := session.BucketStart("2026-08-14", session.OpenMinute)
	profs, fl := synthProfiles(syms)
	c, err := New(store, table, []universe.Basket{{Name: "all", Members: syms}}, profs, fl, nil)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(c.Strip(open+65), "\n"), "\n")
	if len(lines) != StripCap+1 || lines[len(lines)-1] != "               +4 more (broad)" {
		t.Errorf("capped strip = %q", lines)
	}
}

// TestFrame: the strip sits between the basket table and the footer, the
// drill-down directly under its basket's row, and the frame is
// byte-identical across renders (devview seams).
func TestFrame(t *testing.T) {
	c, store, table, open := synth(t)
	dir := t.TempDir()
	profs, _ := synthProfiles([]string{"A", "B", "C", "D"})
	var list []profile.Profile
	for _, s := range []string{"A", "B", "C", "D"} {
		list = append(list, *profs[s])
	}
	if err := profile.Write(dir, list); err != nil {
		t.Fatal(err)
	}
	v, err := devview.New(store, table, []universe.Basket{{Name: "one", Members: []string{"A", "B", "C"}}, {Name: "two", Members: []string{"C", "D"}}}, dir)
	if err != nil {
		t.Fatal(err)
	}
	v.SetDetail(c.DetailFor("two"))
	v.SetTrailer(c.Strip)
	v.SetFooter(Footer)
	out := v.Render(open + 65)
	if out != v.Render(open+65) {
		t.Error("frame differs across renders")
	}
	iOne, iTwo := strings.Index(out, "\none "), strings.Index(out, "\ntwo ")
	iDetail, iStrip, iFooter := strings.Index(out, "    TICKER"), strings.Index(out, "crossed +2.0σ"), strings.Index(out, "--- ticker rows")
	if !(iOne < iTwo && iTwo < iDetail && iDetail < iStrip && iStrip < iFooter) {
		t.Errorf("frame order wrong (one=%d two=%d detail=%d strip=%d footer=%d):\n%s", iOne, iTwo, iDetail, iStrip, iFooter, out)
	}
	if strings.Count(out, "    TICKER") != 1 {
		t.Error("detail rendered for more than the selected basket")
	}
	all := c.RenderAll(open + 65)
	if strings.Count(all, "## basket ") != 2 || strings.Count(all, "    TICKER") != 2 {
		t.Errorf("RenderAll:\n%s", all)
	}
}

// Scope law: rendered trader-facing text is measurement only.
func TestFooterScopeLaw(t *testing.T) {
	for _, banned := range []string{"buy", "sell", "Buy", "Sell", "bullish", "bearish", "signal"} {
		if strings.Contains(Footer, banned) {
			t.Errorf("footer contains %q", banned)
		}
	}
	for _, col := range []string{"last ", "open% ", "cum_$ ", "cum_$_typ ", "cum_$_z ", "rvol_sh ", "$_z ", "of_basket ", "conv_z", "since ", "crossed"} {
		if !strings.Contains(Footer, col) {
			t.Errorf("footer does not define %q", col)
		}
	}
	// MO-1: the shares ratio is named for what it is; the old name is gone.
	if strings.Contains(Footer, "rvol ") {
		t.Error("footer still defines rvol (renamed rvol_sh)")
	}
}

// Old-format baselines (no cum family) must render gaps, never $0 typicals
// or unguarded zs.
func TestNoCumFamilyGaps(t *testing.T) {
	syms := []string{"A"}
	table := ingest.NewTable(syms)
	store := bucket.NewStore()
	open, _ := session.BucketStart("2026-08-14", session.OpenMinute)
	store.ObserveTrade(&ingest.Trade{State: table.Lookup("A"), Price: 10, Size: 10, SipTs: open * 1e9})
	profs, fl := synthProfiles(syms)
	profs["A"].HasCumDollars = false
	c, _ := New(store, table, []universe.Basket{{Name: "one", Members: syms}}, profs, fl, nil)
	r := c.Rows("one", open+65)[0]
	if r.TypOK || r.ZOK || r.Since != "" || !r.CumOK || r.Cum != 100 {
		t.Errorf("row = %+v", r)
	}
	profs["A"].HasCumDollars, fl.HasCumDollars = true, false
	c, _ = New(store, table, []universe.Basket{{Name: "one", Members: syms}}, profs, fl, nil)
	if r := c.Rows("one", open+65)[0]; r.ZOK || !r.TypOK {
		t.Errorf("old floors: row = %+v", r)
	}
}
