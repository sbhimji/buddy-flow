package profile

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"buddy-flow/internal/session"
	"buddy-flow/internal/zguard"
)

// synthUniverse builds a small universe of hand-constructed profiles whose
// per-minute sigmas are constant across the session. CONST is the F13
// ticker: identical volume every day, so robust() yields MAD=0 — asserted
// here rather than assumed.
func synthUniverse(t *testing.T) []Profile {
	t.Helper()
	constDays := []float64{100, 100, 100, 100, 100}
	if med, sig := robust(constDays); med != 100 || sig != 0 {
		t.Fatalf("constant series: med=%v sig=%v, want 100, 0", med, sig)
	}
	flat := func(sym string, sigShares, sigDollars float64) Profile {
		p := Profile{Symbol: sym, Rows: make([]Row, session.MinutesPerSession)}
		for i := range p.Rows {
			p.Rows[i] = Row{MinuteOfDay: session.OpenMinute + i, Days: 5,
				SigmaShares: sigShares, SigmaDollars: sigDollars}
		}
		return p
	}
	return []Profile{
		flat("CONST", 0, 0),
		flat("B", 40, 400),
		flat("C", 80, 800),
		flat("D", 120, 1200),
		flat("E", 160, 1600),
	}
}

// TestFloorBoundsConstantVolumeTicker is 2.2 done-when #1: a MAD=0 ticker's
// z is finite and exact against the universe floor, where an unguarded z
// would divide by zero.
func TestFloorBoundsConstantVolumeTicker(t *testing.T) {
	fl, err := ComputeFloors(synthUniverse(t), 0.25)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := fl.Minute(575)
	if !ok {
		t.Fatal("minute 575 missing")
	}
	// Universe sigmas {0,40,80,120,160}: median 80 → floor 20 (shares);
	// dollars ×10 → floor 200.
	if r.SigmaFloorShares != 20 || r.SigmaFloorDollars != 200 {
		t.Fatalf("floors@575 = %+v, want 20 / 200", r)
	}
	// CONST prints 500 shares against its median 100, σ_measured = 0.
	z, ok := zguard.Z(500, 100, 0, r.SigmaFloorShares)
	if !ok || z != 20 {
		t.Errorf("guarded z = %v, %v; want 20, true", z, ok)
	}
	// Outside the session there is no floor.
	if _, ok := fl.Minute(session.CloseMinute); ok {
		t.Error("minute outside session returned a floor")
	}
}

func TestComputeFloorsRejectsBadInputs(t *testing.T) {
	if _, err := ComputeFloors(nil, 0.25); err == nil {
		t.Error("empty universe accepted")
	}
	u := synthUniverse(t)
	for _, frac := range []float64{-0.25, math.NaN(), math.Inf(1)} {
		if _, err := ComputeFloors(u, frac); err == nil {
			t.Errorf("frac=%v accepted", frac)
		}
	}
	short := append([]Profile{}, u...)
	short[2].Rows = short[2].Rows[:10]
	if _, err := ComputeFloors(short, 0.25); err == nil {
		t.Error("malformed profile accepted")
	}
}

func TestFloorsWriteReadRoundTripAndDeterminism(t *testing.T) {
	fl, err := ComputeFloors(synthUniverse(t), 0.25)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	out1, out2 := filepath.Join(dir, "p1"), filepath.Join(dir, "p2")
	inputs := "5 symbols x 5 days (2026-08-10 .. 2026-08-14)"
	if err := WriteFloors(out1, fl, 0.25, inputs); err != nil {
		t.Fatal(err)
	}
	if err := WriteFloors(out2, fl, 0.25, inputs); err != nil {
		t.Fatal(err)
	}
	b1, _ := os.ReadFile(filepath.Join(out1, FloorsFile))
	b2, _ := os.ReadFile(filepath.Join(out2, FloorsFile))
	if string(b1) != string(b2) {
		t.Error("two writes differ")
	}
	// Self-describing header: the comment records the fraction and inputs.
	first := strings.SplitN(string(b1), "\n", 2)[0]
	if !strings.HasPrefix(first, "#") || !strings.Contains(first, "sigma_floor_frac=0.25") || !strings.Contains(first, inputs) {
		t.Errorf("header comment = %q", first)
	}
	got, err := ReadFloors(out1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != session.MinutesPerSession {
		t.Fatalf("read %d rows", len(got.Rows))
	}
	for i, r := range got.Rows {
		if r != fl.Rows[i] {
			t.Fatalf("row %d: read %+v != computed %+v", i, r, fl.Rows[i])
		}
	}
}

func TestReadFloorsRejectsMalformedFiles(t *testing.T) {
	fl, err := ComputeFloors(synthUniverse(t), 0.25)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	if err := WriteFloors(good, fl, 0.25, "test"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(good, FloorsFile))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")

	corrupt := func(name, content string) {
		t.Helper()
		sub := filepath.Join(dir, name)
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, FloorsFile), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadFloors(sub); err == nil {
			t.Errorf("%s: malformed file accepted", name)
		}
	}
	// Comment only, no header.
	corrupt("noheader", "# only a comment\n")
	// Missing a required column.
	corrupt("nocol", "minute_of_day,sigma_floor_shares\n570,1\n")
	// Short row: parse error with file:line, not an index panic.
	corrupt("shortrow", lines[0]+"\n"+lines[1]+"\n570\n")
	// Truncated: fewer than 390 rows.
	corrupt("trunc", strings.Join(lines[:5], "\n")+"\n")
	// Non-contiguous minutes: positional Minute() would return the wrong bucket.
	bad := append([]string{}, lines...)
	bad[2], bad[3] = lines[3], lines[2]
	corrupt("swapped", strings.Join(bad, "\n"))
}
