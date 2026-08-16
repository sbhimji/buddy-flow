package devview

import (
	"strings"
	"testing"

	"buddy-flow/internal/bucket"
	"buddy-flow/internal/ingest"
	"buddy-flow/internal/profile"
	"buddy-flow/internal/session"
	"buddy-flow/internal/universe"
)

// newTestView builds a two-basket view over a synthetic session: NVDA and
// AMD trade in the 09:35 minute of 2026-08-13; profiles come from a 2-day
// synthetic build so baselines are real profile medians, not hand-planted.
func newTestView(t *testing.T) (*View, int64) {
	t.Helper()
	syms := []string{"NVDA", "AMD", "SPY"}
	dir := t.TempDir()

	// Profile inputs: NVDA prints 100 sh @ $10 at 09:35 both days (median
	// $1000); AMD 50 sh @ $20 on one day only (median $500); SPY silent.
	days := make([]profile.Day, 0, 2)
	for i, date := range []string{"2026-08-11", "2026-08-12"} {
		tbl := ingest.NewTable(syms)
		s := bucket.NewStore()
		start, err := session.BucketStart(date, 575)
		if err != nil {
			t.Fatal(err)
		}
		s.ObserveTrade(&ingest.Trade{State: tbl.Lookup("NVDA"), Price: 10, Size: 100, SipTs: start * 1e9})
		if i == 0 {
			s.ObserveTrade(&ingest.Trade{State: tbl.Lookup("AMD"), Price: 20, Size: 50, SipTs: start * 1e9})
		}
		path := dir + "/" + date + ".trades-only.csv"
		if _, err := s.WriteCSV(path); err != nil {
			t.Fatal(err)
		}
		days = append(days, profile.Day{Date: date, Path: path})
	}
	profiles, err := profile.Build(syms, days)
	if err != nil {
		t.Fatal(err)
	}
	profDir := dir + "/profiles"
	if err := profile.Write(profDir, profiles); err != nil {
		t.Fatal(err)
	}

	// The "session" being viewed: 2026-08-13. NVDA trades $3000 in the
	// completed 09:35 minute, $550 so far in 09:36; AMD is silent.
	table := ingest.NewTable(syms)
	store := bucket.NewStore()
	baskets := []universe.Basket{
		{Name: "chips", Members: []string{"AMD", "NVDA"}},
		{Name: "quiet", Members: []string{"SPY"}},
	}
	v, err := New(store, table, baskets, profDir)
	if err != nil {
		t.Fatal(err)
	}
	m35, err := session.BucketStart("2026-08-13", 575)
	if err != nil {
		t.Fatal(err)
	}
	v.ObserveTrade(&ingest.Trade{State: table.Lookup("NVDA"), Price: 10, Size: 200, SipTs: m35 * 1e9})
	v.ObserveTrade(&ingest.Trade{State: table.Lookup("NVDA"), Price: 10, Size: 100, SipTs: (m35 + 59) * 1e9})
	v.ObserveTrade(&ingest.Trade{State: table.Lookup("NVDA"), Price: 11, Size: 50, SipTs: (m35 + 70) * 1e9})
	return v, m35 + 75 // render at 09:36:15
}

func TestRenderValuesAndDeterminism(t *testing.T) {
	v, atSec := newTestView(t)
	out := v.Render(atSec)
	if out != v.Render(atSec) {
		t.Error("two renders of the same state differ")
	}
	if v.ClockSec() != atSec-5 {
		t.Errorf("clock = %d, want %d", v.ClockSec(), atSec-5)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 { // clock line, header, chips, quiet
		t.Fatalf("got %d lines:\n%s", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "09:36:15 ET  2026-08-13") {
		t.Errorf("clock line = %q", lines[0])
	}
	// chips: completed 09:35 minute = NVDA $2000+$1000 = $3000 counted;
	// baseline = NVDA median $1000 + AMD median $500 = $1500; rvol 2.00;
	// current minute so far = $550.
	chips := strings.Fields(lines[2])
	want := []string{"chips", "2", "3.00k", "1.50k", "2.00", "550"}
	if len(chips) != len(want) {
		t.Fatalf("chips row = %v", chips)
	}
	for i, w := range want {
		if chips[i] != w {
			t.Errorf("chips[%d] = %q, want %q (row %v)", i, chips[i], w, chips)
		}
	}
	// quiet: SPY never traded — zero volume, zero baseline, rvol gap.
	quiet := strings.Fields(lines[3])
	if quiet[2] != "0" || quiet[3] != "0" || quiet[4] != gap {
		t.Errorf("quiet row = %v", quiet)
	}
}

// TestRenderOutsideSession: before the open the completed minute has no
// profiled baseline — base and rvol render gaps, never fake zeros.
func TestRenderOutsideSession(t *testing.T) {
	v, _ := newTestView(t)
	preOpen, err := session.BucketStart("2026-08-13", session.OpenMinute)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(v.Render(preOpen+5), "\n") // 09:30:05 — minute m-1 is 09:29
	chips := strings.Fields(lines[2])
	if chips[3] != gap || chips[4] != gap {
		t.Errorf("pre-open chips row = %v, want base/rvol gaps", chips)
	}
}

// TestRegisterColumn is done-when #4: a new metric column lands via the
// registry alone — no renderer changes.
func TestRegisterColumn(t *testing.T) {
	v, atSec := newTestView(t)
	v.Register(Column{Name: "dummy", Width: 5, Cell: func(rc *RowCtx) string {
		return rc.Basket.Name[:1] + "!"
	}})
	out := v.Render(atSec)
	lines := strings.Split(out, "\n")
	if !strings.HasSuffix(strings.TrimRight(lines[1], " "), "dummy") {
		t.Errorf("header missing dummy column: %q", lines[1])
	}
	if fields := strings.Fields(lines[2]); fields[len(fields)-1] != "c!" {
		t.Errorf("chips dummy cell = %v", fields)
	}
}

// TestNewRejectsMissingProfile: a member without a profile must fail loudly
// at construction, not render fake-zero baselines.
func TestNewRejectsMissingProfile(t *testing.T) {
	table := ingest.NewTable([]string{"NVDA"})
	_, err := New(bucket.NewStore(), table, []universe.Basket{{Name: "b", Members: []string{"NVDA"}}}, t.TempDir())
	if err == nil {
		t.Error("missing profile accepted")
	}
}

func TestFmtDollars(t *testing.T) {
	// Three significant figures per unit — displayed operands must
	// reconcile with displayed ratios.
	cases := map[float64]string{
		0: "0", 999: "999", 1500: "1.50k", 383915: "384k",
		2.5e6: "2.50M", 53.3e6: "53.3M", 370.7e6: "371M", 1.234e9: "1.23B",
	}
	for in, want := range cases {
		if got := fmtDollars(in); got != want {
			t.Errorf("fmtDollars(%v) = %q, want %q", in, got, want)
		}
	}
}

// TestClockRejectsCorruptFutureTimestamp: one far-future SIP ts (feed
// builds these unvalidated) must not pin the max-clock and freeze the view.
func TestClockRejectsCorruptFutureTimestamp(t *testing.T) {
	v, atSec := newTestView(t)
	before := v.ClockSec()
	v.advance((atSec + 48*3600) * 1e9) // corrupt: two days ahead
	if v.ClockSec() != before {
		t.Errorf("clock advanced to corrupt timestamp: %d", v.ClockSec())
	}
	v.advance((atSec + 30) * 1e9) // sane forward step still advances
	if v.ClockSec() != atSec+30 {
		t.Errorf("clock = %d, want %d", v.ClockSec(), atSec+30)
	}
}
