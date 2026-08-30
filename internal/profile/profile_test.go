package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"buddy-flow/internal/bucket"
	"buddy-flow/internal/ingest"
	"buddy-flow/internal/session"
)

// synthDay writes a bucket file for date where NVDA trades `shares` at $10
// in the 09:35 minute (if shares > 0), plus fixed cross/duplicate prints
// that must never enter profiles.
func synthDay(t *testing.T, dir, date string, shares float64) Day {
	t.Helper()
	table := ingest.NewTable([]string{"NVDA", "SPY"})
	nvda := table.Lookup("NVDA")
	s := bucket.NewStore()
	start, err := session.BucketStart(date, 575) // 09:35
	if err != nil {
		t.Fatal(err)
	}
	if shares > 0 {
		s.ObserveTrade(&ingest.Trade{State: nvda, Price: 10, Size: shares, SipTs: start*1e9 + 5e8})
	}
	// Noise that the $vol slice must exclude: opening cross (cond 25) and an
	// average-price duplicate (cond 2) in the same minute.
	cross := &ingest.Trade{State: nvda, Price: 10, Size: 999999, SipTs: start * 1e9}
	cross.Cond[0], cross.NCond = 25, 1
	s.ObserveTrade(cross)
	dup := &ingest.Trade{State: nvda, Price: 10, Size: 888888, SipTs: start*1e9 + 1e8}
	dup.Cond[0], dup.NCond = 2, 1
	s.ObserveTrade(dup)

	path := filepath.Join(dir, date+".trades-only.csv")
	if _, err := s.WriteCSV(path); err != nil {
		t.Fatal(err)
	}
	return Day{Date: date, Path: path}
}

func TestBuildMedianWithZerosAndSliceExclusion(t *testing.T) {
	dir := t.TempDir()
	days := []Day{
		synthDay(t, dir, "2026-08-10", 100),
		synthDay(t, dir, "2026-08-11", 200),
		synthDay(t, dir, "2026-08-12", 300),
		synthDay(t, dir, "2026-08-13", 0), // silent 09:35 — a true zero
		synthDay(t, dir, "2026-08-14", 0),
	}
	profiles, err := Build([]string{"NVDA", "SPY"}, days)
	if err != nil {
		t.Fatal(err)
	}
	var nvda, spy *Profile
	for i := range profiles {
		switch profiles[i].Symbol {
		case "NVDA":
			nvda = &profiles[i]
		case "SPY":
			spy = &profiles[i]
		}
	}
	r, ok := nvda.Minute(575)
	if !ok {
		t.Fatal("minute 575 missing")
	}
	// Sample = {0, 0, 100, 200, 300}: median 100; |dev| = {100,100,0,100,200},
	// MAD = 100 → sigma 148.26. Cross/duplicate sizes must not appear.
	if r.Days != 5 || r.MedianShares != 100 || r.SigmaShares != 100*MADConsistency {
		t.Errorf("NVDA@575 = %+v", r)
	}
	if r.MedianDollars != 1000 {
		t.Errorf("median dollars = %v, want 1000 (slice leaked cross/dup?)", r.MedianDollars)
	}
	// SPY never traded: all-zero profile, still 390 rows of true zeros.
	if len(spy.Rows) != session.MinutesPerSession {
		t.Fatalf("SPY rows = %d", len(spy.Rows))
	}
	if r2, _ := spy.Minute(575); r2.MedianShares != 0 || r2.SigmaShares != 0 || r2.Days != 5 {
		t.Errorf("SPY@575 = %+v, want zeros with Days=5", r2)
	}
	// A quiet minute for NVDA (09:36) is also all-zero.
	if r3, _ := nvda.Minute(576); r3.MedianShares != 0 {
		t.Errorf("NVDA@576 = %+v, want zero", r3)
	}
}

// TestBuildRejectsMisdatedFile: a file whose data lies on a different ET
// date than its Day claims would otherwise contribute a full session of
// zeros to every symbol.
func TestBuildRejectsMisdatedFile(t *testing.T) {
	dir := t.TempDir()
	d := synthDay(t, dir, "2026-08-10", 100)
	d.Date = "2026-08-11" // lie about the date
	if _, err := Build([]string{"NVDA"}, []Day{d}); err == nil {
		t.Error("misdated file accepted")
	}
}

func TestReadRejectsMalformedFiles(t *testing.T) {
	dir := t.TempDir()
	days := []Day{synthDay(t, dir, "2026-08-10", 100)}
	profiles, err := Build([]string{"NVDA"}, days)
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, profiles); err != nil {
		t.Fatal(err)
	}
	good, err := os.ReadFile(filepath.Join(dir, "NVDA.csv"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(good), "\n")

	corrupt := func(name string, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name+".csv"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(dir, name); err == nil {
			t.Errorf("%s: malformed file accepted", name)
		}
	}
	// Short row: parse error with file:line, not an index panic.
	corrupt("SHORTROW", lines[0]+"\n570,1\n")
	// Truncated file: fewer than 390 rows.
	corrupt("TRUNC", strings.Join(lines[:5], "\n")+"\n")
	// Non-contiguous minutes: positional Minute() lookup would silently
	// return the wrong bucket.
	bad := append([]string{}, lines...)
	bad[1], bad[2] = lines[2], lines[1]
	corrupt("SWAPPED", strings.Join(bad, "\n"))
}

func TestBuildRejectsDuplicateDates(t *testing.T) {
	dir := t.TempDir()
	d := synthDay(t, dir, "2026-08-10", 100)
	if _, err := Build([]string{"NVDA"}, []Day{d, d}); err == nil {
		t.Error("duplicate dates accepted")
	}
}

func TestWriteReadRoundTripAndDeterminism(t *testing.T) {
	dir := t.TempDir()
	days := []Day{
		synthDay(t, dir, "2026-08-10", 100),
		synthDay(t, dir, "2026-08-11", 250),
	}
	profiles, err := Build([]string{"NVDA"}, days)
	if err != nil {
		t.Fatal(err)
	}
	out1, out2 := filepath.Join(dir, "p1"), filepath.Join(dir, "p2")
	if err := Write(out1, profiles); err != nil {
		t.Fatal(err)
	}
	if err := Write(out2, profiles); err != nil {
		t.Fatal(err)
	}
	b1, _ := os.ReadFile(filepath.Join(out1, "NVDA.csv"))
	b2, _ := os.ReadFile(filepath.Join(out2, "NVDA.csv"))
	if string(b1) != string(b2) {
		t.Error("two writes differ")
	}
	got, err := Read(out1, "NVDA")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != session.MinutesPerSession {
		t.Fatalf("read %d rows", len(got.Rows))
	}
	for i, r := range got.Rows {
		if r != profiles[0].Rows[i] {
			t.Fatalf("row %d: read %+v != built %+v", i, r, profiles[0].Rows[i])
		}
	}
}

func TestMedianEvenCount(t *testing.T) {
	if m := median([]float64{1, 2, 3, 4}); m != 2.5 {
		t.Errorf("median = %v, want 2.5", m)
	}
	if m, sig := robust([]float64{5, 5, 5, 5}); m != 5 || sig != 0 {
		t.Errorf("constant series: med=%v sig=%v, want 5, 0 (MAD=0 stored, floor is 2.2's job)", m, sig)
	}
}

// TestCumDollarsFamily: the ticker-view-v0 cum-dollar family is the
// median/MAD over days of the day's CUMULATIVE auction-inclusive dollars
// through each minute — hand-computable on three synthetic days, and
// visibly not the running sum of per-minute medians.
func TestCumDollarsFamily(t *testing.T) {
	dir := t.TempDir()
	syms := []string{"X"}
	// Day d: opening cross 100 sh @ $1 at 09:30 (auction-inclusive only),
	// then continuous prints of (day-specific) dollars at 09:30 and 09:31.
	//   day1: cross 100, cont 10 @09:30, 20 @09:31
	//   day2: cross 100, cont 30 @09:30, 40 @09:31
	//   day3: cross 100, cont 50 @09:30, 0  @09:31
	cont := [][2]float64{{10, 20}, {30, 40}, {50, 0}}
	days := []Day{}
	for i, date := range []string{"2026-08-11", "2026-08-12", "2026-08-13"} {
		tbl := ingest.NewTable(syms)
		s := bucket.NewStore()
		open, err := session.BucketStart(date, session.OpenMinute)
		if err != nil {
			t.Fatal(err)
		}
		st := tbl.Lookup("X")
		s.ObserveTrade(&ingest.Trade{State: st, Price: 1, Size: 100, SipTs: open * 1e9})
		s.ObserveTrade(&ingest.Trade{State: st, Price: 1, Size: cont[i][0], SipTs: (open + 5) * 1e9})
		if cont[i][1] > 0 {
			s.ObserveTrade(&ingest.Trade{State: st, Price: 1, Size: cont[i][1], SipTs: (open + 65) * 1e9})
		}
		path := dir + "/" + date + ".trades-only.csv"
		if _, err := s.WriteCSV(path); err != nil {
			t.Fatal(err)
		}
		days = append(days, Day{Date: date, Path: path})
	}
	profs, err := Build(syms, days)
	if err != nil {
		t.Fatal(err)
	}
	p := profs[0]
	if !p.HasCumDollars {
		t.Fatal("Build must mark the cum family present")
	}
	r30, _ := p.Minute(session.OpenMinute)
	r31, _ := p.Minute(session.OpenMinute + 1)
	// Whether the 100-share print counted as a cross or continuous, it is in
	// the auction-inclusive slice either way: cum@09:30 = {110,130,150},
	// cum@09:31 = {130,170,150}. Medians 130 and 150; MADs: |{110,130,150}-130|
	// = {20,0,20} → 20; |{130,170,150}-150| = {20,20,0} → 20.
	if r30.MedianCumDollars != 130 || r31.MedianCumDollars != 150 {
		t.Errorf("cum medians = %v, %v; want 130, 150", r30.MedianCumDollars, r31.MedianCumDollars)
	}
	mad := 20.0 // a variable: a constant product would fold at infinite precision
	if want := mad * MADConsistency; r30.SigmaCumDollars != want || r31.SigmaCumDollars != want {
		t.Errorf("cum sigmas = %v, %v; want %v", r30.SigmaCumDollars, r31.SigmaCumDollars, want)
	}
	// Sum-of-medians is NOT the family: per-minute medians of the counted
	// slice at 09:31 = median{20,40,0} = 20, plus 09:30's median — never 150.
	if r31.MedianDollars != 20 {
		t.Errorf("per-minute median at 09:31 = %v, want 20", r31.MedianDollars)
	}

	// Round trip: written columns read back; an old-format file (no cum
	// columns) loads with HasCumDollars=false and zero cum fields.
	if err := Write(dir, profs); err != nil {
		t.Fatal(err)
	}
	back, err := Read(dir, "X")
	if err != nil {
		t.Fatal(err)
	}
	if !back.HasCumDollars || back.Rows[1].MedianCumDollars != 150 || back.Rows[1].SigmaCumDollars != r31.SigmaCumDollars {
		t.Errorf("round trip lost the cum family: %+v", back.Rows[1])
	}
	raw, err := os.ReadFile(dir + "/X.csv")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	var old []string
	for _, l := range lines {
		f := strings.Split(l, ",")
		old = append(old, strings.Join(f[:6], ","))
	}
	if err := os.WriteFile(dir+"/OLD.csv", []byte(strings.Join(old, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldP, err := Read(dir, "OLD")
	if err != nil {
		t.Fatalf("old-format profile must still read: %v", err)
	}
	if oldP.HasCumDollars || oldP.Rows[1].MedianCumDollars != 0 {
		t.Errorf("old-format profile claims a cum family: %+v", oldP.Rows[1])
	}
	if oldP.Rows[1].MedianDollars != 20 {
		t.Errorf("old-format per-minute columns changed: %+v", oldP.Rows[1])
	}

	// Floors: sigma_floor_cumdollars = frac × median across tickers of the
	// cum σ, written and read back; an old floors file reads with
	// HasCumDollars=false.
	fl, err := ComputeFloors(profs, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if got := fl.Rows[1].SigmaFloorCumDollars; got != 0.5*r31.SigmaCumDollars {
		t.Errorf("cum floor = %v, want %v", got, 0.5*r31.SigmaCumDollars)
	}
	fdir := t.TempDir()
	if err := WriteFloors(fdir, fl, 0.5, "test"); err != nil {
		t.Fatal(err)
	}
	rf, err := ReadFloors(fdir)
	if err != nil {
		t.Fatal(err)
	}
	if !rf.HasCumDollars || rf.Rows[1].SigmaFloorCumDollars != fl.Rows[1].SigmaFloorCumDollars {
		t.Errorf("floors round trip lost cumdollars: %+v", rf.Rows[1])
	}
	rawF, _ := os.ReadFile(fdir + "/" + FloorsFile)
	var oldF []string
	for _, l := range strings.Split(strings.TrimRight(string(rawF), "\n"), "\n") {
		if strings.HasPrefix(l, "#") {
			oldF = append(oldF, l)
			continue
		}
		f := strings.Split(l, ",")
		oldF = append(oldF, strings.Join(f[:5], ","))
	}
	odir := t.TempDir()
	if err := os.WriteFile(odir+"/"+FloorsFile, []byte(strings.Join(oldF, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	of, err := ReadFloors(odir)
	if err != nil {
		t.Fatalf("old-format floors must still read: %v", err)
	}
	if of.HasCumDollars || of.Rows[1].SigmaFloorCumDollars != 0 {
		t.Errorf("old-format floors claim a cumdollars family")
	}
}
