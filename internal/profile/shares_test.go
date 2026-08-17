package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"buddy-flow/internal/bucket"
	"buddy-flow/internal/ingest"
	"buddy-flow/internal/session"
	"buddy-flow/internal/universe"
)

// shareDay writes a bucket file where each symbol trades `dollars[sym]` at
// $1 in the 09:35 minute, plus (for coverage) 1 share at $1 in 09:36 for
// every symbol listed in covered. Each symbol in `crosses` additionally
// prints an opening cross (condition 25 → CROSS_OPEN) of that many dollars
// in the 09:35 minute, and a $500 CLOSING cross (condition 8 → CROSS_CLOSE)
// at 16:00:00 — the closing print sits past the [open, close) window, so if
// the clamp ever leaked it into the cumulative, the hand-computed
// expectations below would break.
func shareDay(t *testing.T, dir, date string, dollars, crosses map[string]float64, covered []string) Day {
	t.Helper()
	table := ingest.NewTable([]string{"A", "B", "C"})
	s := bucket.NewStore()
	m575, err := session.BucketStart(date, 575)
	if err != nil {
		t.Fatal(err)
	}
	closeSec, err := session.BucketStart(date, session.CloseMinute)
	if err != nil {
		t.Fatal(err)
	}
	m576 := m575 + 60
	for sym, d := range dollars {
		if d > 0 {
			s.ObserveTrade(&ingest.Trade{State: table.LookupOrCreate(sym), Price: 1, Size: d, SipTs: m575 * 1e9})
		}
	}
	for sym, d := range crosses {
		st := table.LookupOrCreate(sym)
		s.ObserveTrade(&ingest.Trade{State: st, Price: 1, Size: d, SipTs: m575 * 1e9, Cond: [ingest.MaxConditions]int32{25}, NCond: 1})
		s.ObserveTrade(&ingest.Trade{State: st, Price: 1, Size: 500, SipTs: closeSec * 1e9, Cond: [ingest.MaxConditions]int32{8}, NCond: 1})
	}
	for _, sym := range covered {
		s.ObserveTrade(&ingest.Trade{State: table.LookupOrCreate(sym), Price: 1, Size: 1, SipTs: m576 * 1e9})
	}
	path := filepath.Join(dir, date+".trades-only.csv")
	if _, err := s.WriteCSV(path); err != nil {
		t.Fatal(err)
	}
	return Day{Date: date, Path: path}
}

var testBaskets = []universe.Basket{
	{Name: "flat", Members: []string{"B"}},   // trades $0 at 09:35 every day: share 0, MAD 0 — the F13 basket
	{Name: "x", Members: []string{"A", "B"}}, // shares .3 .4 .5 .6 .7 at 09:35
	{Name: "y", Members: []string{"C"}},      // shares .7 .6 .5 .4 .3
}

func buildTestShares(t *testing.T) ([]ShareProfile, []SkippedDay) {
	t.Helper()
	dir := t.TempDir()
	all := []string{"A", "B", "C"}
	// Every trading day also prints a $10 opening cross on A (and a closing
	// cross, structurally excluded): per-minute samples must NOT move, the
	// cumulative family must count it (D7 amendment).
	crossA := map[string]float64{"A": 10}
	days := []Day{
		// (a, c) dollars at 09:35; b always 0 there. Day 2 doubles day 1's
		// tape shape at other shares — co-movement is the point of shares.
		shareDay(t, dir, "2026-08-03", map[string]float64{"A": 30, "C": 70}, crossA, all),
		shareDay(t, dir, "2026-08-04", map[string]float64{"A": 40, "C": 60}, crossA, all),
		shareDay(t, dir, "2026-08-05", map[string]float64{"A": 50, "C": 50}, crossA, all),
		shareDay(t, dir, "2026-08-06", map[string]float64{"A": 60, "C": 40}, crossA, all),
		shareDay(t, dir, "2026-08-07", map[string]float64{"A": 70, "C": 30}, crossA, all),
		// 0/0 minute: everyone silent at 09:35 (covered via 09:36) — the
		// minute contributes NO sample, not a zero share (D3). No crosses:
		// this day also keeps the cum-denominator-still-zero null path.
		shareDay(t, dir, "2026-08-10", nil, nil, all),
		// Uncovered day: C prints nothing all session — whole day skipped
		// for every basket (D2b).
		shareDay(t, dir, "2026-08-11", map[string]float64{"A": 99}, nil, []string{"A", "B"}),
	}
	profiles, skipped, err := BuildShares(testBaskets, days)
	if err != nil {
		t.Fatal(err)
	}
	return profiles, skipped
}

func TestBuildSharesHandComputed(t *testing.T) {
	profiles, skipped := buildTestShares(t)
	if len(skipped) != 1 || skipped[0].Date != "2026-08-11" || len(skipped[0].Uncovered) != 1 || skipped[0].Uncovered[0] != "C" {
		t.Fatalf("skipped = %+v, want day 2026-08-11 for C", skipped)
	}
	byName := map[string]*ShareProfile{}
	for i := range profiles {
		byName[profiles[i].Basket] = &profiles[i]
	}
	// x @575: samples {.3,.4,.5,.6,.7} — median .5, MAD .1 → σ .1×1.4826.
	// Days = 5: the 0/0 day contributed no sample; the uncovered day is out.
	// The $10 cross on A moves NOTHING here — per-minute shares count the
	// Counted slice only (crosses excluded).
	// Expectations run through robust() on the same float ratios the build
	// produces — binary rounding must match, not just decimal intent.
	wantMed, wantSig := robust([]float64{30.0 / 100, 40.0 / 100, 50.0 / 100, 60.0 / 100, 70.0 / 100})
	x, _ := byName["x"].Minute(575)
	if x.Days != 5 || x.MedianShare != wantMed || x.SigmaShare != wantSig {
		t.Errorf("x@575 = %+v, want med %v sig %v", x, wantMed, wantSig)
	}
	// y mirrors x by construction.
	y, _ := byName["y"].Minute(575)
	if y.Days != 5 || y.MedianShare != wantMed || y.SigmaShare != wantSig {
		t.Errorf("y@575 = %+v", y)
	}
	// flat (B only) never trades at 09:35: share 0 on every sampled day —
	// median 0, MAD 0, stored honestly (the floor handles it at z time).
	fl, _ := byName["flat"].Minute(575)
	if fl.Days != 5 || fl.MedianShare != 0 || fl.SigmaShare != 0 {
		t.Errorf("flat@575 = %+v", fl)
	}
	// 09:36 (the coverage minute): every symbol traded $1 → x share = 2/3.
	// Days = 6 here: the 09:35-silent day (2026-08-10) DID trade at 09:36,
	// so it contributes — 0/0 skips are per minute, not per day.
	x6, _ := byName["x"].Minute(576)
	if x6.Days != 6 || x6.MedianShare != 2.0/3.0 {
		t.Errorf("x@576 = %+v", x6)
	}
	// A minute nobody ever traded: zero samples.
	if q, _ := byName["x"].Minute(600); q.Days != 0 {
		t.Errorf("x@600 = %+v, want 0 samples", q)
	}

	// D7 cumulative family — the AUCTION-INCLUSIVE slice: A's $10 opening
	// cross enters numerator and denominator (per-minute samples above
	// were unmoved by it). Through 575: x = (A+10)/110, and the 0/0 day
	// (no crosses either) has no cumulative denominator yet → CumDays 5.
	wantCumMed575, wantCumSig575 := robust([]float64{40.0 / 110, 50.0 / 110, 60.0 / 110, 70.0 / 110, 80.0 / 110})
	if x.CumDays != 5 || x.MedianCumShare != wantCumMed575 || x.SigmaCumShare != wantCumSig575 {
		t.Errorf("x cum@575 = %+v, want med %v sig %v", x, wantCumMed575, wantCumSig575)
	}
	// Through 576: each day adds $1 A + $1 B to x and $3 to the universe;
	// the 09:35-silent day now samples too (2/3). Same-path expectations.
	wantCumMed, wantCumSig := robust([]float64{42.0 / 113, 52.0 / 113, 62.0 / 113, 72.0 / 113, 82.0 / 113, 2.0 / 3.0})
	if x6.CumDays != 6 || x6.MedianCumShare != wantCumMed || x6.SigmaCumShare != wantCumSig {
		t.Errorf("x cum@576 = %+v, want med %v sig %v", x6, wantCumMed, wantCumSig)
	}
	// After the last trade of the day the cumulative freezes: minute 600
	// has no per-minute samples but carries the same cumulative as 576.
	// The $500 CLOSING cross froze nothing open — its 16:00:00 SIP ts is
	// past the [open, close) window, so it appears in NO minute's cum.
	q, _ := byName["x"].Minute(600)
	if q.CumDays != 6 || q.MedianCumShare != wantCumMed || q.SigmaCumShare != wantCumSig {
		t.Errorf("x cum@600 = %+v, want frozen cum from 576 (closing cross excluded)", q)
	}
}

func TestFlowShareFloors(t *testing.T) {
	profiles, _ := buildTestShares(t)
	floors, err := FlowShareFloors(profiles, 0.25)
	if err != nil {
		t.Fatal(err)
	}
	// σ_share at 575 across baskets: {0 (flat), σx, σy} with σx = σy —
	// median = σx → floor 0.25 × σx (same-path computation as the build).
	_, sigX := robust([]float64{30.0 / 100, 40.0 / 100, 50.0 / 100, 60.0 / 100, 70.0 / 100})
	if got, want := floors[575-session.OpenMinute], 0.25*sigX; got != want {
		t.Errorf("floor@575 = %v, want %v", got, want)
	}
	// A minute with zero samples everywhere: σ undefined for every basket →
	// floor 0 (a defined gap downstream, never an invented floor).
	if got := floors[600-session.OpenMinute]; got != 0 {
		t.Errorf("floor@600 = %v, want 0", got)
	}
	// The cumulative family floors independently, on its auction-inclusive
	// samples: σ_cum@575 across baskets = {0 (flat), σx_cum, σy_cum} with
	// the cum sample sets from the hand-computed test; at 600 the
	// per-minute floor is 0 but the cumulative one is defined (cum
	// freezes, CumDays 6).
	cumFloors, err := CumShareFloors(profiles, 0.25)
	if err != nil {
		t.Fatal(err)
	}
	_, sigXCum := robust([]float64{40.0 / 110, 50.0 / 110, 60.0 / 110, 70.0 / 110, 80.0 / 110})
	_, sigYCum := robust([]float64{70.0 / 110, 60.0 / 110, 50.0 / 110, 40.0 / 110, 30.0 / 110})
	if got, want := cumFloors[575-session.OpenMinute], 0.25*median([]float64{0, sigXCum, sigYCum}); got != want {
		t.Errorf("cum floor@575 = %v, want %v", got, want)
	}
	if cumFloors[600-session.OpenMinute] <= 0 {
		t.Errorf("cum floor@600 = %v, want >0 (cumulative family is defined where per-minute is not)",
			cumFloors[600-session.OpenMinute])
	}
}

func TestBuildSharesRejectsDuplicateDates(t *testing.T) {
	dir := t.TempDir()
	d := shareDay(t, dir, "2026-08-03", map[string]float64{"A": 30, "C": 70}, nil, []string{"A", "B", "C"})
	if _, _, err := BuildShares(testBaskets, []Day{d, d}); err == nil {
		t.Error("duplicate dates accepted")
	}
}

func TestSharesWriteReadRoundTripAndDeterminism(t *testing.T) {
	// Two INDEPENDENT builds from identically-constructed inputs, written
	// separately — catches nondeterminism in BuildShares itself, not just
	// in the writer.
	profiles, _ := buildTestShares(t)
	profiles2, _ := buildTestShares(t)
	dir := t.TempDir()
	out1, out2 := filepath.Join(dir, "p1"), filepath.Join(dir, "p2")
	inputs := "3 symbols x 7 days (2026-08-03 .. 2026-08-11)"
	if err := WriteShares(out1, profiles, inputs); err != nil {
		t.Fatal(err)
	}
	if err := WriteShares(out2, profiles2, inputs); err != nil {
		t.Fatal(err)
	}
	b1, _ := os.ReadFile(filepath.Join(out1, SharesDir, "x.csv"))
	b2, _ := os.ReadFile(filepath.Join(out2, SharesDir, "x.csv"))
	if string(b1) != string(b2) {
		t.Error("two writes differ")
	}
	first := strings.SplitN(string(b1), "\n", 2)[0]
	if !strings.Contains(first, "members=A B;") || !strings.Contains(first, inputs) {
		t.Errorf("header comment = %q", first)
	}
	got, err := ReadShares(out1, "x", []string{"A", "B"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != session.MinutesPerSession {
		t.Fatalf("read %d rows", len(got.Rows))
	}
	var want *ShareProfile
	for i := range profiles {
		if profiles[i].Basket == "x" {
			want = &profiles[i]
		}
	}
	for i, r := range got.Rows {
		if r != want.Rows[i] {
			t.Fatalf("row %d: read %+v != built %+v", i, r, want.Rows[i])
		}
	}
}

// TestReadSharesRejectsStaleMembership is the D2a contract: a basket edit
// must fail the read loudly, naming the basket — never serve a stale
// baseline.
func TestReadSharesRejectsStaleMembership(t *testing.T) {
	profiles, _ := buildTestShares(t)
	dir := t.TempDir()
	if err := WriteShares(dir, profiles, "test"); err != nil {
		t.Fatal(err)
	}
	_, err := ReadShares(dir, "x", []string{"A", "B", "NVDA"})
	if err == nil {
		t.Fatal("stale membership accepted")
	}
	if !strings.Contains(err.Error(), "x") || !strings.Contains(err.Error(), "stale") {
		t.Errorf("error should name the basket and staleness: %v", err)
	}
	// Unchanged membership still reads.
	if _, err := ReadShares(dir, "x", []string{"A", "B"}); err != nil {
		t.Errorf("fresh read failed: %v", err)
	}
}

func TestReadSharesRejectsMalformed(t *testing.T) {
	profiles, _ := buildTestShares(t)
	dir := t.TempDir()
	if err := WriteShares(dir, profiles, "test"); err != nil {
		t.Fatal(err)
	}
	good, err := os.ReadFile(filepath.Join(dir, SharesDir, "x.csv"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(good), "\n")
	corrupt := func(name, content string) {
		t.Helper()
		sub := t.TempDir()
		if err := os.MkdirAll(filepath.Join(sub, SharesDir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, SharesDir, "x.csv"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadShares(sub, "x", []string{"A", "B"}); err == nil {
			t.Errorf("%s: malformed file accepted", name)
		}
	}
	corrupt("no-stamp", "# a comment without the stamp\n"+strings.Join(lines[1:], "\n"))
	corrupt("truncated", strings.Join(lines[:5], "\n")+"\n")
	swapped := append([]string{}, lines...)
	swapped[2], swapped[3] = lines[3], lines[2]
	corrupt("non-contiguous", strings.Join(swapped, "\n"))
}
