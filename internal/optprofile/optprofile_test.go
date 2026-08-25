package optprofile

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"buddy-flow/internal/optbucket"
	"buddy-flow/internal/session"
	"buddy-flow/internal/universe"
)

// synthDay builds a session where symbol A prints net_conviction v at
// 09:30 (spread over three seconds) and symbol B prints -v/2 at 09:30;
// every other minute is silent. Dates are real trading days.
func synthDay(t *testing.T, date string, v float64) Day {
	t.Helper()
	sec, err := session.BucketStart(date, session.OpenMinute)
	if err != nil {
		t.Fatal(err)
	}
	s := &optbucket.Session{Buckets: map[string]map[int64]*optbucket.Bucket{
		"A": {
			sec:     {Prints: 1, NetConviction: v / 2, PremCallAsk: v},
			sec + 1: {Prints: 1, NetConviction: v / 4},
			sec + 2: {Prints: 1, NetConviction: v / 4},
		},
		"B": {sec + 30: {Prints: 1, NetConviction: -v / 2, PremPutAsk: v / 2}},
	}}
	return Day{Date: date, Session: s}
}

var dates = []string{
	"2026-07-20", "2026-07-21", "2026-07-22", "2026-07-23", "2026-07-24",
	"2026-07-27", "2026-07-28", "2026-07-29", "2026-07-30", "2026-07-31",
	"2026-08-03", "2026-08-04",
}

func synthDays(t *testing.T) []Day {
	// values 1..11 plus one silent day (v=0): median of {0,1,..,11} = 5.5
	var days []Day
	for i, d := range dates {
		days = append(days, synthDay(t, d, float64(i)))
	}
	return days
}

func TestBuildTickerProfiles(t *testing.T) {
	profs, err := Build([]string{"B", "A", "C"}, synthDays(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(profs) != 3 || profs[0].Name != "A" || profs[2].Name != "C" {
		t.Fatalf("profiles = %v", profs)
	}
	a := profs[0].Rows[0] // 09:30
	// A's 09:30 net_conviction per day = v (v/2+v/4+v/4), v = 0..11 → median 5.5;
	// MAD: |v-5.5| = {5.5,4.5,...,0.5,0.5,...,5.5} → median 3 → σ = 3×1.4826.
	if a.Days != 12 || a.MedianNetConv != 5.5 || a.SigmaNetConv != 3*1.4826 {
		t.Errorf("A 09:30 = %+v", a)
	}
	// net_notional at 09:30 for A = PremCallAsk = v → same stats.
	if a.MedianNetNotional != 5.5 {
		t.Errorf("A net_notional median = %v", a.MedianNetNotional)
	}
	// Silent minutes are true zeros with full day counts (P1/F13).
	if r := profs[0].Rows[1]; r.Days != 12 || r.MedianNetConv != 0 || r.SigmaNetConv != 0 {
		t.Errorf("A 09:31 silent = %+v", r)
	}
	// C never prints: all-zero profile, still 12 days.
	if r := profs[2].Rows[0]; r.Days != 12 || r.MedianNetConv != 0 {
		t.Errorf("C = %+v", r)
	}
	// B's negative median round-trips as negative.
	if r := profs[1].Rows[0]; r.MedianNetConv != -2.75 {
		t.Errorf("B 09:30 median = %v, want -2.75", r.MedianNetConv)
	}
	if len(profs[0].Rows) != session.MinutesPerSession || profs[0].Rows[389].MinuteOfDay != 959 {
		t.Errorf("row layout wrong")
	}
}

func TestBuildBasketsDayLevel(t *testing.T) {
	bks := []universe.Basket{{Name: "ab", Members: []string{"B", "A"}}}
	profs, err := BuildBaskets(bks, synthDays(t))
	if err != nil {
		t.Fatal(err)
	}
	p := profs[0]
	if p.Name != "ab" || len(p.Members) != 2 || p.Members[0] != "A" {
		t.Fatalf("basket profile = %+v", p)
	}
	// Day-level sum at 09:30: A (v) + B (−v/2) = v/2 per day → median 2.75.
	// (Sum-of-medians would also give 2.75 here, but MAD must be of the
	// SUMMED series: |v/2 − 2.75| median = 1.5 → σ = 1.5×1.4826, not the
	// sum of member MADs (3 + 1.5)×1.4826.)
	r := p.Rows[0]
	if r.MedianNetConv != 2.75 || r.SigmaNetConv != 1.5*1.4826 {
		t.Errorf("basket 09:30 = %+v (want median 2.75, sigma %v)", r, 1.5*1.4826)
	}
}

func TestFloorsAndGate(t *testing.T) {
	days := synthDays(t)
	tickers, _ := Build([]string{"A", "B"}, days)
	baskets, _ := BuildBaskets([]universe.Basket{{Name: "ab", Members: []string{"A", "B"}}}, days)
	fl := ComputeFloors(tickers, baskets, 0.25)
	// Ticker σ at 09:30: A = 3×1.4826, B = 1.5×1.4826 → median 2.25×1.4826 → floor ×0.25.
	if want := 0.25 * 2.25 * 1.4826; math.Abs(fl.NetConv[0]-want) > 1e-12 {
		t.Errorf("floor netconv 09:30 = %v, want %v", fl.NetConv[0], want)
	}
	if math.Abs(fl.BasketNetConv[0]-0.25*1.5*1.4826) > 1e-12 {
		t.Errorf("basket floor = %v", fl.BasketNetConv[0])
	}
	// Below MinProfiledDays nothing qualifies → floor 0.
	few, _ := Build([]string{"A"}, days[:5])
	if fl2 := ComputeFloors(few, nil, 0.25); fl2.NetConv[0] != 0 {
		t.Errorf("floor with 5 days = %v, want 0 (gate)", fl2.NetConv[0])
	}
}

func TestWriteReadRoundTripStampsAndDeterminism(t *testing.T) {
	days := synthDays(t)
	tickers, _ := Build([]string{"A", "B"}, days)
	bks := []universe.Basket{{Name: "ab", Members: []string{"A", "B"}}}
	baskets, _ := BuildBaskets(bks, days)
	fl := ComputeFloors(tickers, baskets, 0.25)
	stamp := "options-weights-v1@abcdef012345"

	d1, d2 := t.TempDir(), t.TempDir()
	for _, dir := range []string{d1, d2} {
		if err := Write(dir, tickers, stamp, "test"); err != nil {
			t.Fatal(err)
		}
		if err := WriteBaskets(dir, baskets, stamp, "test"); err != nil {
			t.Fatal(err)
		}
		if err := WriteFloors(dir, fl, 0.25, stamp, "test"); err != nil {
			t.Fatal(err)
		}
	}
	for _, rel := range []string{"A.csv", "baskets/ab.csv", FloorsFile} {
		b1, _ := os.ReadFile(filepath.Join(d1, rel))
		b2, _ := os.ReadFile(filepath.Join(d2, rel))
		if string(b1) != string(b2) {
			t.Errorf("%s: two builds differ", rel)
		}
	}

	a, err := Read(d1, "A", stamp)
	if err != nil {
		t.Fatal(err)
	}
	if a.Rows[0] != tickers[0].Rows[0] {
		t.Errorf("ticker round-trip = %+v, want %+v", a.Rows[0], tickers[0].Rows[0])
	}
	bp, err := ReadBasket(d1, "ab", []string{"B", "A"}, stamp)
	if err != nil {
		t.Fatal(err)
	}
	if bp.Rows[0] != baskets[0].Rows[0] {
		t.Errorf("basket round-trip = %+v", bp.Rows[0])
	}
	rf, err := ReadFloors(d1, stamp)
	if err != nil {
		t.Fatal(err)
	}
	if rf.NetConv[0] != fl.NetConv[0] || rf.BasketNetNotional[5] != fl.BasketNetNotional[5] {
		t.Errorf("floors round-trip mismatch")
	}

	// Refusals: stale weights, changed membership.
	if _, err := Read(d1, "A", "options-weights-v1@000000000000"); err == nil {
		t.Error("stale weights stamp accepted for ticker profile")
	}
	if _, err := ReadBasket(d1, "ab", []string{"A", "B", "C"}, stamp); err == nil {
		t.Error("changed membership accepted for basket profile")
	}
	if _, err := ReadFloors(d1, "x@y"); err == nil {
		t.Error("stale weights stamp accepted for floors")
	}
}

func TestDiscoverDays(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"2026-08-01.csv", "2026-08-02.csv", "2026-08-03.partial.csv", "2026-08-04.csv", "junk.csv"} {
		os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644)
	}
	got, err := DiscoverDays(dir, "2026-08-04", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "2026-08-02" || got[1] != "2026-08-04" {
		t.Errorf("DiscoverDays = %v (partial excluded, most recent 2)", got)
	}
}
