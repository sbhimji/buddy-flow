package flowshare

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"buddy-flow/internal/bucket"
	"buddy-flow/internal/devview"
	"buddy-flow/internal/feed"
	"buddy-flow/internal/ingest"
	"buddy-flow/internal/profile"
	"buddy-flow/internal/session"
	"buddy-flow/internal/universe"
)

// runCells renders the three run cells (since, 5m_z, 15m_z) for a row —
// the bytes the incremental series must reproduce exactly.
func runCells(r *Run, rc *devview.RowCtx) string {
	var sb strings.Builder
	for _, c := range r.DevColumns() {
		sb.WriteString(c.Cell(rc) + "|")
	}
	return sb.String()
}

// TestRunPostCloseClamp: after the close the series stops at 15:59 — n at
// 16:05 equals n at 16:00 and the cells are the same bytes.
func TestRunPostCloseClamp(t *testing.T) {
	r, rc, open := runSynth(t, []float64{0.5, 0.5, 0.5, 0.5, 0.5, 0.5}, nil)
	closeSec := open + 390*60
	at16 := runCells(r, at(rc, closeSec))
	n16 := r.prime(closeSec).n
	at1605 := runCells(r, at(rc, closeSec+5*60))
	if n := r.prime(closeSec + 5*60).n; n != n16 || n != 390 {
		t.Errorf("n at 16:05 = %d, at 16:00 = %d; want 390 both", n, n16)
	}
	if at16 != at1605 {
		t.Errorf("post-close cells differ: %q vs %q", at16, at1605)
	}
	// A fresh Run at 16:05 agrees byte for byte.
	fresh := NewRun(r.store, r.union, r.shares, r.floors)
	if got := runCells(fresh, at(rc, closeSec+5*60)); got != at1605 {
		t.Errorf("fresh %q vs incremental %q", got, at1605)
	}
}

// TestRunSinceMeasuredFlips: the first minutes have no cumulative
// baseline (gap glyph), the baseline then appears (blank: measured, not
// crossed), then the cumulative crosses (the record).
func TestRunSinceMeasuredFlips(t *testing.T) {
	days := []int{0, 0, 20, 20, 20, 20}
	// cum shares: 0.25, 0.25, 0.25, (750+1000)/4000 = 0.4375 → z +1.5,
	// (1750+1000)/5000 = 0.55 → z +2.4 at minute 4 (09:34).
	r, rc, open := runSynth(t, []float64{0.25, 0.25, 0.25, 1, 1, 0.25}, days)
	col := r.Columns()[0]
	if got := col.Cell(at(rc, open+2*60)); got != gap {
		t.Errorf("unmeasured minutes: %q, want %q", got, gap)
	}
	if got := col.Cell(at(rc, open+3*60)); got != "" {
		t.Errorf("measured, not crossed: %q, want blank", got)
	}
	if got := col.Cell(at(rc, open+5*60)); got != "09:34" {
		t.Errorf("crossed: %q, want 09:34", got)
	}
	if _, crossed, measured := r.Since(at(rc, open+3*60)); crossed || !measured {
		t.Errorf("Since at 09:33 = crossed %v measured %v", crossed, measured)
	}
}

// TestRunIncremental: a Run advanced second by second over a store that
// grows minute by minute renders the same bytes as a fresh Run at every
// step — through a late print into an earlier minute (amendment ring),
// more late prints than the ring holds (wrap → rebuild), a replaced
// membership slice (recomputed), and a new date (new series).
func TestRunIncremental(t *testing.T) {
	table := ingest.NewTable([]string{"A", "B", "C"})
	s := bucket.NewStore()
	open, err := session.BucketStart("2026-08-14", session.OpenMinute)
	if err != nil {
		t.Fatal(err)
	}
	prof := &profile.ShareProfile{Basket: "x", Rows: make([]profile.ShareRow, session.MinutesPerSession)}
	floors := &profile.Floors{Rows: make([]profile.FloorRow, session.MinutesPerSession)}
	for i := range prof.Rows {
		prof.Rows[i] = profile.ShareRow{MinuteOfDay: session.OpenMinute + i, Days: 20, MedianShare: 0.25, CumDays: 20, MedianCumShare: 0.25}
		floors.Rows[i] = profile.FloorRow{MinuteOfDay: session.OpenMinute + i, SigmaFloorFlowShare: 0.0625, SigmaFloorCumShare: 0.125}
	}
	shares := map[string]*profile.ShareProfile{"x": prof}
	union := states(table, "A", "B", "C")
	inc := NewRun(s, union, shares, floors)
	row := &devview.BasketRow{Name: "x", States: states(table, "A", "B")}
	check := func(sec int64, what string) {
		t.Helper()
		rc := &devview.RowCtx{Basket: row, AtSec: sec}
		got := runCells(inc, rc)
		want := runCells(NewRun(s, union, shares, floors), rc)
		if got != want {
			t.Errorf("%s at +%ds: incremental %q vs rebuild %q", what, sec-open, got, want)
		}
	}
	trade := func(sym string, sec int64, dollars float64) {
		s.ObserveTrade(&ingest.Trade{State: table.Lookup(sym), Price: 1, Size: dollars, SipTs: sec * 1e9})
	}
	// Twenty minutes, prints at :10, renders at :05 and :35 of each.
	for i := int64(0); i < 20; i++ {
		trade("A", open+i*60+10, 500+float64(i%3)*100)
		trade("C", open+i*60+10, 500)
		check(open+i*60+5, "growing")
		check(open+i*60+35, "growing")
	}
	// A late print into minute 3 (behind the store's latest second).
	trade("A", open+3*60+40, 900)
	check(open+20*60+5, "late print")
	// Then into minute 0 and minute 17 in one render gap: truncation to
	// the earliest.
	trade("B", open+17*60+1, 300)
	trade("B", open+1, 2000)
	check(open+20*60+6, "two late prints")
	// More late prints than the ring holds: wrap → full rebuild.
	for i := 0; i < bucket.AmendRing+10; i++ {
		trade("C", open+5*60+2, 1)
	}
	check(open+20*60+7, "ring wrap")
	// Membership slice replaced (hot-reload): recomputed from the open.
	row.States = states(table, "A", "B", "C")
	check(open+20*60+8, "membership replaced")
	row.States = states(table, "B")
	check(open+20*60+9, "membership replaced again")
	// A new date: fresh series, no carry-over.
	open2, err := session.BucketStart("2026-08-17", session.OpenMinute)
	if err != nil {
		t.Fatal(err)
	}
	trade("A", open2+10, 400)
	trade("C", open2+10, 400)
	check(open2+65, "new date")
	// Back on the first date (a replay -view-at into the past): rebuilt.
	check(open+20*60+9, "back to the first date")
}

// TestRunIncrementalMatchesRebuildFixture replays a captured session
// (MO8_FIXTURE, with MO8_PROFILES and MO8_BASKETS) through the live
// decoder and, at every new session second from 09:30:00 to 10:00:00,
// asserts the incremental Run's three cells equal a fresh Run's for every
// basket. Late prints in the capture exercise the amendment ring for
// real. Skipped without the env (the fixture lives outside the repo).
func TestRunIncrementalMatchesRebuildFixture(t *testing.T) {
	fixture, profDir, basketsPath := os.Getenv("MO8_FIXTURE"), os.Getenv("MO8_PROFILES"), os.Getenv("MO8_BASKETS")
	if fixture == "" || profDir == "" || basketsPath == "" {
		t.Skip("set MO8_FIXTURE, MO8_PROFILES, MO8_BASKETS to run the replay comparison")
	}
	syms, err := universe.Load(basketsPath)
	if err != nil {
		t.Fatal(err)
	}
	bks, err := universe.LoadBaskets(basketsPath)
	if err != nil {
		t.Fatal(err)
	}
	table := ingest.NewTable(syms)
	store := bucket.NewTimeOrderedStore()
	union, err := Union(bks, table)
	if err != nil {
		t.Fatal(err)
	}
	shares, floors, err := LoadBaselines(profDir, bks)
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]*devview.BasketRow, 0, len(bks))
	for _, b := range bks {
		row := &devview.BasketRow{Name: b.Name}
		for _, m := range b.Members {
			row.States = append(row.States, table.Lookup(m))
		}
		rows = append(rows, row)
	}
	obs := &compareObserver{store: store, inc: NewRun(store, union, shares, floors),
		fresh: func() *Run { return NewRun(store, union, shares, floors) }, rows: rows}
	p := ingest.NewPipeline(table, 0)
	p.SetObserver(obs)
	done := make(chan struct{})
	go func() { p.Run(); close(done) }()
	if _, err := feed.StreamCapture(fixture, p, feed.ReplayOptions{}); err != nil {
		t.Fatal(err)
	}
	p.Close()
	<-done
	if obs.compared == 0 {
		t.Fatal("no session seconds compared")
	}
	t.Logf("compared %d render seconds × %d baskets; amendments seen: %d", obs.compared, len(rows), obs.amendments)
	for _, m := range obs.mismatches {
		t.Error(m)
	}
}

// compareObserver feeds the store and, on every new session second the
// clock reaches inside the window, compares incremental vs rebuild —
// synchronously on the pipeline goroutine, the same thread that writes.
type compareObserver struct {
	store *bucket.Store
	inc   *Run
	fresh func() *Run
	rows  []*devview.BasketRow

	clockSec   int64
	from, to   int64
	gen        uint64
	compared   int
	amendments int
	mismatches []string
}

func (o *compareObserver) ObserveQuote(q *ingest.Quote) { o.store.ObserveQuote(q) }

func (o *compareObserver) ObserveTrade(tr *ingest.Trade) {
	o.store.ObserveTrade(tr)
	sec := tr.SipTs / 1e9
	if sec <= o.clockSec {
		return
	}
	o.clockSec = sec
	if o.from == 0 {
		date := session.Date(sec * 1e9)
		open, err1 := session.BucketStart(date, session.OpenMinute)
		if err1 != nil {
			return
		}
		o.from, o.to = open, open+30*60
	}
	if sec < o.from || sec > o.to {
		return
	}
	if _, gen, _ := o.store.AmendedSince(0); gen != o.gen {
		o.amendments += int(gen - o.gen)
		o.gen = gen
	}
	o.compared++
	fresh := o.fresh()
	for _, row := range o.rows {
		rc := &devview.RowCtx{Basket: row, AtSec: sec}
		got, want := runCells(o.inc, rc), runCells(fresh, rc)
		if got != want && len(o.mismatches) < 20 {
			o.mismatches = append(o.mismatches, fmt.Sprintf("%s %s: incremental %q vs rebuild %q",
				time.Unix(sec, 0).In(session.ET()).Format("15:04:05"), row.Name, got, want))
		}
	}
}

// BenchmarkRunPrime measures one render's series work on a dense tape
// (164 members printing every second): a rebuild from the open versus
// the incremental step (one new minute) at n = 60 and n = 389.
func BenchmarkRunPrime(b *testing.B) {
	syms := make([]string, 164)
	for i := range syms {
		syms[i] = fmt.Sprintf("S%03d", i)
	}
	table := ingest.NewTable(syms)
	s := bucket.NewStore()
	open, _ := session.BucketStart("2026-08-14", session.OpenMinute)
	for _, sym := range syms {
		st := table.Lookup(sym)
		for sec := int64(0); sec < 390*60; sec++ {
			s.ObserveTrade(&ingest.Trade{State: st, Price: 100, Size: 100, SipTs: (open + sec) * 1e9})
		}
	}
	union := make([]*ingest.SymbolState, len(syms))
	for i, sym := range syms {
		union[i] = table.Lookup(sym)
	}
	shares, floors := map[string]*profile.ShareProfile{}, &profile.Floors{Rows: make([]profile.FloorRow, session.MinutesPerSession)}
	for _, n := range []int{60, 389} {
		at := open + int64(n)*60 + 5
		b.Run(fmt.Sprintf("rebuild/n=%d", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				NewRun(s, union, shares, floors).prime(at)
			}
		})
		b.Run(fmt.Sprintf("incremental/n=%d", n), func(b *testing.B) {
			r := NewRun(s, union, shares, floors)
			r.prime(at)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r.extend(r.series, n-1, n) // one new minute: the per-minute step
			}
		})
	}
}
