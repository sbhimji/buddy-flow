package breadth

import (
	"strings"
	"testing"

	"buddy-flow/internal/bucket"
	"buddy-flow/internal/ingest"
	"buddy-flow/internal/profile"
	"buddy-flow/internal/session"
)

// printSz is print with an explicit size — volume tests steer $vol.
func printSz(s *bucket.Store, st *ingest.SymbolState, sec int64, price, size float64) {
	s.ObserveTrade(&ingest.Trade{State: st, Price: price, Size: size, SipTs: sec * 1e9})
}

// flatProfile builds a profile with the same MedianDollars every session
// minute — matched-minute lookups become hand-computable constants.
func flatProfile(sym string, medDollars float64) *profile.Profile {
	p := &profile.Profile{Symbol: sym, Rows: make([]profile.Row, session.MinutesPerSession)}
	for i := range p.Rows {
		p.Rows[i] = profile.Row{MinuteOfDay: session.OpenMinute + i, Days: 20, MedianDollars: medDollars}
	}
	return p
}

// volSynth: open 09:30 on 2026-08-14, SPY flat at 100, every profiled
// median $100/minute (threshold = VolK×100 = $150, strict). Hand-computed
// at a 09:34:05 render (price windows end 09:34/09:33/09:32; volume
// minutes 09:31/09:32/09:33):
//
//	V: 101 from 09:31, $202 every minute        — ↑ AND unusual
//	W: 101 from 09:31, $101 then silence        — ↑, normal volume
//	X: 100.05 (in-band), $300 every minute      — in-line, unusual
//	B: flat, exactly $150 every minute          — boundary: normal (strict >)
//	L: flat, $202 in 09:31 only                 — one hot minute: normal
//	T: median 0 profile                          — thin: unmeasured
//	M: no profile in the map                     — unmeasured
func volSynth(t *testing.T) (*VolCalc, *ingest.Table, int64) {
	t.Helper()
	table := ingest.NewTable([]string{"SPY", "V", "W", "X", "B", "L", "T", "M"})
	s := bucket.NewStore()
	open := synthOpen(t)
	printSz(s, table.Lookup("SPY"), open, 100, 1)
	for _, sym := range []string{"V", "W", "X", "B", "L", "T", "M"} {
		printSz(s, table.Lookup(sym), open, 100, 1)
	}
	for min := int64(1); min <= 3; min++ {
		at := open + min*60 + 10
		printSz(s, table.Lookup("V"), at, 101, 2)    // $202 > 150
		printSz(s, table.Lookup("X"), at, 100.05, 3) // $300, +5bps in-band
		printSz(s, table.Lookup("B"), at, 100, 1.5)  // $150 exactly
	}
	printSz(s, table.Lookup("W"), open+70, 101, 1)    // $101 in 09:31 only
	printSz(s, table.Lookup("L"), open+70, 100, 2.02) // $202 in 09:31 only, price flat

	profiles := map[string]*profile.Profile{
		"V": flatProfile("V", 100), "W": flatProfile("W", 100),
		"X": flatProfile("X", 100), "B": flatProfile("B", 100),
		"L": flatProfile("L", 100), "T": flatProfile("T", 0),
		// M deliberately absent.
	}
	bc, err := New(s, table)
	if err != nil {
		t.Fatal(err)
	}
	return NewVol(bc, s, profiles), table, open
}

func TestVolumeBreadthHandComputed(t *testing.T) {
	vc, table, open := volSynth(t)
	at := open + 4*60 + 5 // 09:34:05

	// {V, W, X}: breadth ↑ = {V, W}; of those on volume = {V} → 1/2.
	if got := vc.UpOnVolColumn().Cell(row(table, at, "V", "W", "X")); got != "1/2" {
		t.Errorf("up_on_vol = %q, want 1/2", got)
	}
	// Unusual = {V, X}; unmeasured = {T, M}.
	if got := vc.VolDetailColumn().Cell(row(table, at, "V", "W", "X", "T", "M")); got != "2$ 2·" {
		t.Errorf("vol_detail = %q, want 2$ 2·", got)
	}
	// Boundary: exactly VolK×median is NOT unusual; one hot minute fails
	// persistence. Both in-band → up_on_vol 0/0 (a true statement).
	if got := vc.VolDetailColumn().Cell(row(table, at, "B", "L")); got != "0$ 0·" {
		t.Errorf("boundary/persistence = %q, want 0$ 0·", got)
	}
	if got := vc.UpOnVolColumn().Cell(row(table, at, "B", "L")); got != "0/0" {
		t.Errorf("no-up basket = %q, want 0/0", got)
	}
	// Determinism: same state, same bytes.
	if a, b := vc.UpOnVolColumn().Cell(row(table, at, "V", "W", "X")), vc.UpOnVolColumn().Cell(row(table, at, "V", "W", "X")); a != b {
		t.Errorf("nondeterministic: %q vs %q", a, b)
	}
}

// TestVolumeUnmeasuredUpMember: a member persistently ↑ on price whose
// volume has no basis counts in up_on_vol's denominator, never as
// on-volume (VB4). The basket needs one volume-MEASURED sibling — with
// zero volume-measured members the whole cell gaps (TestVolumeGaps).
func TestVolumeUnmeasuredUpMember(t *testing.T) {
	vc, table, at := unmeasuredUpSynth(t, true)
	if got := vc.UpOnVolColumn().Cell(row(table, at, "U", "N")); got != "0/1" {
		t.Errorf("unmeasured-up = %q, want 0/1", got)
	}
	if got := vc.VolDetailColumn().Cell(row(table, at, "U", "N")); got != "0$ 1·" {
		t.Errorf("detail = %q, want 0$ 1·", got)
	}
}

func TestVolumeGaps(t *testing.T) {
	vc, table, open := volSynth(t)
	// Before the persistence horizon: both columns gap.
	early := open + 2*60 + 5
	if got := vc.UpOnVolColumn().Cell(row(table, early, "V")); got != gap {
		t.Errorf("pre-horizon up_on_vol = %q, want gap", got)
	}
	if got := vc.VolDetailColumn().Cell(row(table, early, "V")); got != gap {
		t.Errorf("pre-horizon vol_detail = %q, want gap", got)
	}
	// All-unmeasured basket: both gap ("0$ 2·" over nothing measured
	// would be a fake statement).
	at := open + 4*60 + 5
	if got := vc.UpOnVolColumn().Cell(row(table, at, "T", "M")); got != gap {
		t.Errorf("all-unmeasured up_on_vol = %q, want gap", got)
	}
	if got := vc.VolDetailColumn().Cell(row(table, at, "T", "M")); got != gap {
		t.Errorf("all-unmeasured vol_detail = %q, want gap", got)
	}
}

// TestVolumeSPYMissing: no SPY prints in the needed windows — the price
// side gaps, so up_on_vol gaps; vol_detail needs no SPY and still renders.
func TestVolumeSPYMissing(t *testing.T) {
	vc, table, at := spyMissingSynth(t)
	if got := vc.UpOnVolColumn().Cell(row(table, at, "V")); got != gap {
		t.Errorf("SPY-missing up_on_vol = %q, want gap", got)
	}
	if got := vc.VolDetailColumn().Cell(row(table, at, "V")); got != "1$ 0·" {
		t.Errorf("SPY-missing vol_detail = %q, want 1$ 0·", got)
	}
}

// unmeasuredUpSynth: U is persistently ↑ on price but its volume has no
// basis (median-0 profile); N is flat, quiet, and (when measuredSibling)
// volume-measured. Render second 09:34:05. Shared by the up_on_vol and
// merged-cell tests.
func unmeasuredUpSynth(t *testing.T, measuredSibling bool) (*VolCalc, *ingest.Table, int64) {
	t.Helper()
	table := ingest.NewTable([]string{"SPY", "U", "N"})
	s := bucket.NewStore()
	open := synthOpen(t)
	printSz(s, table.Lookup("SPY"), open, 100, 1)
	printSz(s, table.Lookup("U"), open, 100, 1)
	printSz(s, table.Lookup("U"), open+70, 101, 5) // ↑ persistent; volume has no basis
	printSz(s, table.Lookup("N"), open, 100, 1)    // flat, quiet
	bc, err := New(s, table)
	if err != nil {
		t.Fatal(err)
	}
	profiles := map[string]*profile.Profile{"U": flatProfile("U", 0)}
	if measuredSibling {
		profiles["N"] = flatProfile("N", 100)
	}
	return NewVol(bc, s, profiles), table, open + 4*60 + 5
}

// spyMissingSynth: SPY first prints 09:35, so every price window through
// the 09:34:05 render is unmeasurable; V is flat on $200/min (unusual vs a
// $100 median). Shared by the up_on_vol and merged-cell tests.
func spyMissingSynth(t *testing.T) (*VolCalc, *ingest.Table, int64) {
	t.Helper()
	table := ingest.NewTable([]string{"SPY", "V"})
	s := bucket.NewStore()
	open := synthOpen(t)
	printSz(s, table.Lookup("SPY"), open+300, 100, 1) // SPY first prints 09:35
	printSz(s, table.Lookup("V"), open, 100, 1)
	for min := int64(1); min <= 3; min++ {
		printSz(s, table.Lookup("V"), open+min*60+10, 100, 2) // $200/min, flat
	}
	bc, err := New(s, table)
	if err != nil {
		t.Fatal(err)
	}
	return NewVol(bc, s, map[string]*profile.Profile{"V": flatProfile("V", 100)}), table, open + 4*60 + 5
}

// TestBreadthMerged: the MO-1 trader cell `x/y z$` — 3.2 breadth over full
// membership, then the ↑ members also on unusual volume — fixed-width so
// the slash and $ align, with the VB4 gap rules preserved: price-side gap →
// whole cell gaps; volume side uncountable → `x/y ·$`; no ↑ member → no $
// term.
func TestBreadthMerged(t *testing.T) {
	vc, table, open := volSynth(t)
	at := open + 4*60 + 5 // 09:34:05
	col := vc.BreadthColumn(true)
	if col.Name != "breadth" || col.Width != 9 || col.Style == nil {
		t.Fatalf("column = %+v", col)
	}
	// {V, W, X}: ↑ = {V, W} of 3; on volume among them = {V} → 2/3 1$.
	if got := col.Cell(row(table, at, "V", "W", "X")); got != " 2/3   1$" {
		t.Errorf("merged = %q, want \" 2/3   1$\"", got)
	}
	// Fixed columns: a two-digit denominator and count keep the slash and
	// $ in place — every cell is exactly Width bytes wide.
	for _, c := range []string{col.Cell(row(table, at, "V", "W", "X")), col.Cell(row(table, at, "B", "L"))} {
		if len([]rune(c)) != 9 {
			t.Errorf("cell %q is %d runes wide, want 9", c, len([]rune(c)))
		}
	}
	// No ↑ member: nothing to count on the volume side — `0/2`, no $ term,
	// padded so the slash stays aligned.
	if got := col.Cell(row(table, at, "B", "L")); got != " 0/2     " {
		t.Errorf("no-up = %q, want \" 0/2     \"", got)
	}
	// Price side gaps before the persistence horizon: whole cell gaps.
	if got := col.Cell(row(table, open+2*60+5, "V", "W")); got != gap {
		t.Errorf("pre-horizon = %q, want gap", got)
	}
	// Whole-cell highlight follows the price fraction alone (T7): 2/2 ↑ →
	// green; a mixed basket → none.
	if got := col.Style(row(table, at, "V", "W")); got != sgrGreen {
		t.Errorf("style = %q, want green", got)
	}
	if got := col.Style(row(table, at, "V", "W", "X", "B")); got != "" {
		t.Errorf("style = %q, want none", got)
	}
	// Unstyled variant carries no Style.
	if vc.BreadthColumn(false).Style != nil {
		t.Error("unstyled column carries a Style")
	}
	// Determinism.
	if a, b := col.Cell(row(table, at, "V", "W", "X")), col.Cell(row(table, at, "V", "W", "X")); a != b {
		t.Errorf("nondeterministic: %q vs %q", a, b)
	}

	// Volume side wholly unmeasurable: U ↑ on price with a median-0 profile,
	// N has no profile — zero volume-measured members, price measured →
	// `1/2 ·$`, never `1/2 0$`.
	vc2, table2, at2 := unmeasuredUpSynth(t, false)
	if got := vc2.BreadthColumn(true).Cell(row(table2, at2, "U", "N")); got != " 1/2   ·$" {
		t.Errorf("volume-unmeasured = %q, want \" 1/2   ·$\"", got)
	}
	// Partial unmeasurement: N IS volume-measured but the ↑ member U has no
	// basis — the $ count would be over a partially unknown set, so it is
	// still `·$` (the dev up_on_vol cell prints 0/1 for the same state).
	vc3, table3, at3 := unmeasuredUpSynth(t, true)
	if got := vc3.BreadthColumn(true).Cell(row(table3, at3, "U", "N")); got != " 1/2   ·$" {
		t.Errorf("partially-unmeasured = %q, want \" 1/2   ·$\"", got)
	}
	// SPY missing: price side gaps → whole cell gaps even though V's
	// volume is measurable.
	vc4, table4, at4 := spyMissingSynth(t)
	if got := vc4.BreadthColumn(true).Cell(row(table4, at4, "V")); got != gap {
		t.Errorf("SPY-missing = %q, want gap", got)
	}
}

// TestVolumeLegends: the columns document themselves and carry no
// buy/sell language (measurement statements only — scope law).
func TestVolumeLegends(t *testing.T) {
	vc, _, _ := volSynth(t)
	for _, col := range []string{vc.UpOnVolColumn().Legend, vc.VolDetailColumn().Legend} {
		if col == "" {
			t.Error("column missing legend")
		}
		lower := strings.ToLower(col)
		for _, banned := range []string{"buy", "sell", "bullish", "bearish"} {
			if strings.Contains(lower, banned) {
				t.Errorf("legend %q contains %q", col, banned)
			}
		}
	}
}
