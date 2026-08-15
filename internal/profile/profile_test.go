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
