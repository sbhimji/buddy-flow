// Command profiles builds the 20-day per-ticker volume profiles from bucket
// files (mini-spec 2.1 PR 2: bucket files in, profiles out) plus the σ floor
// table _floors.csv (mini-spec 2.2, -sigma-floor-frac), and runs the RVOL
// sanity check against a session's bucket file.
//
//	go run ./cmd/profiles -days 20 -through 2026-08-13
//	go run ./cmd/profiles -rvol-check data/buckets/2026-08-14.csv -rvol-minute 13:00
//
// Day discovery: <buckets-dir>/<date>.csv or <date>.trades-only.csv (full
// session files preferred when both exist). The nightly roll is this same
// command run after each session close — most recent -days days win.
package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"buddy-flow/internal/bucket"
	"buddy-flow/internal/profile"
	"buddy-flow/internal/session"
	"buddy-flow/internal/universe"
)

// checkDate leniently extracts the session date from any bucket filename
// (including .partial.csv) — the -rvol-check target is human-chosen, not
// discovered, so partial files are acceptable there when the checked minute
// lies inside their span.
var checkDate = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})`)

// classifyDayFile decides whether a filename is eligible for BASELINES and
// how it ranks: full-session files (<date>.csv, rank 0) beat trades-only
// bootstrap files (<date>.trades-only.csv, rank 1); .gz variants of either
// are accepted (D4 gzips after session close). Everything else — notably
// <date>.partial.csv, capture-derived files that don't span the session —
// is deliberately not discovered: a partial day would feed zero mornings
// into the profile.
func classifyDayFile(name string) (date string, rank int, ok bool) {
	name = strings.TrimSuffix(name, ".gz")
	switch {
	case strings.HasSuffix(name, bucket.TradesOnlySuffix):
		date, rank = strings.TrimSuffix(name, bucket.TradesOnlySuffix), 1
	case strings.HasSuffix(name, bucket.PartialSuffix):
		return "", 0, false
	case strings.HasSuffix(name, ".csv"):
		date, rank = strings.TrimSuffix(name, ".csv"), 0
	default:
		return "", 0, false
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return "", 0, false
	}
	return date, rank, true
}

func main() {
	var (
		basketsPath = flag.String("baskets", "docs/foundations/morning-tape-baskets-v2.json", "trader-owned basket config (universe source of truth)")
		bucketsDir  = flag.String("buckets-dir", "data/buckets", "bucket-file directory to build from")
		outDir      = flag.String("out", "data/profiles", "profile output directory")
		days        = flag.Int("days", 20, "lookback: the most recent N available days")
		through     = flag.String("through", "", "use only days <= this date (YYYY-MM-DD); the check-day must be excluded from its own baseline")
		rvolCheck   = flag.String("rvol-check", "", "session bucket file to RVOL-check against existing profiles (skips building)")
		rvolMinute  = flag.String("rvol-minute", "13:00", "ET minute for the RVOL check, HH:MM")
		floorFrac   = flag.Float64("sigma-floor-frac", 0.25, "σ floor fraction (2.2 D2): floor = frac × universe median of sigma_family per minute; recorded in _floors.csv")
	)
	flag.Parse()
	if *days < 1 {
		fmt.Fprintf(os.Stderr, "-days must be >= 1 (got %d)\n", *days)
		os.Exit(2)
	}
	if math.IsNaN(*floorFrac) || math.IsInf(*floorFrac, 0) || *floorFrac < 0 {
		fmt.Fprintf(os.Stderr, "-sigma-floor-frac must be a nonnegative number (got %v)\n", *floorFrac)
		os.Exit(2)
	}
	if *through != "" {
		if _, err := time.Parse("2006-01-02", *through); err != nil {
			fmt.Fprintf(os.Stderr, "-through must be YYYY-MM-DD: %v\n", err)
			os.Exit(2)
		}
	}

	syms, err := universe.Load(*basketsPath)
	if err != nil {
		fatal(err)
	}

	if *rvolCheck != "" {
		if err := checkRVOL(*outDir, *rvolCheck, *rvolMinute, syms); err != nil {
			fatal(err)
		}
		return
	}

	daysIn, err := discoverDays(*bucketsDir, *days, *through)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("building profiles: %d symbols x %d days (%s .. %s)\n",
		len(syms), len(daysIn), daysIn[0].Date, daysIn[len(daysIn)-1].Date)
	if len(daysIn) < 10 {
		fmt.Printf("!! only %d days — below the >=10-day rule; profiles will build but no ticker may contribute to z (2.1)\n", len(daysIn))
	}
	profiles, err := profile.Build(syms, daysIn)
	if err != nil {
		fatal(err)
	}
	if err := profile.Write(*outDir, profiles); err != nil {
		fatal(err)
	}
	fmt.Printf("wrote %d profiles -> %s\n", len(profiles), *outDir)

	// Share profiles build in the same run (3.1 D2): per basket per minute,
	// each day's FlowShare first, then median/MAD over days.
	baskets, err := universe.LoadBaskets(*basketsPath)
	if err != nil {
		fatal(err)
	}
	shares, skipped, err := profile.BuildShares(baskets, daysIn)
	if err != nil {
		fatal(err)
	}
	for _, s := range skipped {
		fmt.Printf("!! share profiles: day %s skipped for ALL baskets — uncovered members (zero prints all session): %v\n", s.Date, s.Uncovered)
	}
	inputs := fmt.Sprintf("%d symbols x %d days (%s .. %s)",
		len(syms), len(daysIn), daysIn[0].Date, daysIn[len(daysIn)-1].Date)
	if err := profile.WriteShares(*outDir, shares, inputs); err != nil {
		fatal(err)
	}
	fmt.Printf("wrote %d share profiles -> %s\n", len(shares), filepath.Join(*outDir, profile.SharesDir))

	// Floors build in the same run as every profile build (2.2 D2; 3.1 D4
	// adds the flowshare family).
	floors, err := profile.ComputeFloors(profiles, *floorFrac)
	if err != nil {
		fatal(err)
	}
	shareFloors, err := profile.FlowShareFloors(shares, *floorFrac)
	if err != nil {
		fatal(err)
	}
	cumFloors, err := profile.CumShareFloors(shares, *floorFrac)
	if err != nil {
		fatal(err)
	}
	for i := range floors.Rows {
		floors.Rows[i].SigmaFloorFlowShare = shareFloors[i]
		floors.Rows[i].SigmaFloorCumShare = cumFloors[i]
	}
	if err := profile.WriteFloors(*outDir, floors, *floorFrac, inputs); err != nil {
		fatal(err)
	}
	fmt.Printf("wrote floors -> %s\n", filepath.Join(*outDir, profile.FloorsFile))
	// Spot sanity (2.2 done-when #4; reported, not gated): open-minute
	// floors should be visibly larger than midday — volume σ scales with
	// volume. FlowShare floors have no such physics (shares are ratios);
	// printed for eyeballing only.
	open, _ := floors.Minute(9*60 + 35)
	mid, _ := floors.Minute(13 * 60)
	fmt.Printf("σ floors (frac=%v): shares 09:35=%.4g 13:00=%.4g | dollars 09:35=%.4g 13:00=%.4g (expect 09:35 larger) | flowshare 09:35=%.4g 13:00=%.4g | cumshare 09:35=%.4g 13:00=%.4g (expect narrowing)\n",
		*floorFrac, open.SigmaFloorShares, mid.SigmaFloorShares, open.SigmaFloorDollars, mid.SigmaFloorDollars,
		open.SigmaFloorFlowShare, mid.SigmaFloorFlowShare, open.SigmaFloorCumShare, mid.SigmaFloorCumShare)
}

// discoverDays lists bucket files, prefers full-session files over
// trades-only for the same date, and returns the most recent n (dates <=
// through when set), ascending.
func discoverDays(dir string, n int, through string) ([]profile.Day, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	type cand struct {
		path string
		rank int
		gz   bool
	}
	best := map[string]cand{} // date -> best candidate (lower rank wins; plain beats .gz)
	for _, e := range entries {
		date, rank, ok := classifyDayFile(e.Name())
		if !ok {
			continue
		}
		if through != "" && date > through {
			continue
		}
		c := cand{filepath.Join(dir, e.Name()), rank, strings.HasSuffix(e.Name(), ".gz")}
		if prev, seen := best[date]; !seen || c.rank < prev.rank || (c.rank == prev.rank && prev.gz && !c.gz) {
			best[date] = c
		}
	}
	if len(best) == 0 {
		return nil, fmt.Errorf("no bucket files in %s (through=%q)", dir, through)
	}
	dates := make([]string, 0, len(best))
	for d := range best {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	if len(dates) > n {
		dates = dates[len(dates)-n:]
	}
	out := make([]profile.Day, 0, len(dates))
	for _, d := range dates {
		out = append(out, profile.Day{Date: d, Path: best[d].path})
	}
	return out, nil
}

// checkRVOL is the 2.1 sanity criterion: on an average day, counted volume
// in a matched minute divided by the profile median should sit near 1.0
// across the universe. Reported, not gated — the human reads the
// distribution.
func checkRVOL(profDir, bucketPath, hhmm string, syms []string) error {
	m := checkDate.FindStringSubmatch(filepath.Base(bucketPath))
	if m == nil {
		return fmt.Errorf("cannot parse session date from %q (want a YYYY-MM-DD... filename)", filepath.Base(bucketPath))
	}
	date := m[1]
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return fmt.Errorf("bad -rvol-minute %q (want HH:MM): %w", hhmm, err)
	}
	minute := t.Hour()*60 + t.Minute()
	if !session.InRegular(minute) {
		return fmt.Errorf("minute %s is outside the regular session", hhmm)
	}
	start, err := session.BucketStart(date, minute)
	if err != nil {
		return err
	}
	sess, err := bucket.ReadCSV(bucketPath)
	if err != nil {
		return err
	}
	// Partial files are allowed here only when the checked minute lies
	// inside their data span — outside it, zeros would be lies, not data.
	minSec, maxSec, ok := sess.Bounds()
	if !ok {
		return fmt.Errorf("%s is empty", bucketPath)
	}
	if start+59 < minSec || start > maxSec {
		et := func(sec int64) string { return time.Unix(sec, 0).In(session.ET()).Format("15:04:05") }
		return fmt.Errorf("minute %s is outside the file's data span (%s..%s ET)", hhmm, et(minSec), et(maxSec))
	}

	var rvols []float64
	skippedThin := 0
	for _, sym := range syms {
		p, err := profile.Read(profDir, sym)
		if err != nil {
			return fmt.Errorf("profile %s: %w (build profiles first?)", sym, err)
		}
		row, ok := p.Minute(minute)
		if !ok {
			return fmt.Errorf("minute %d not in profile", minute)
		}
		if row.MedianShares == 0 {
			skippedThin++ // 0/0 territory — no baseline to compare against
			continue
		}
		min, err := sess.DeriveMinute(sym, start)
		if err != nil {
			return err
		}
		shares, _ := profile.Counted(min)
		rvols = append(rvols, shares/row.MedianShares)
	}
	if len(rvols) == 0 {
		return fmt.Errorf("no tickers with a nonzero baseline at %s", hhmm)
	}
	sort.Float64s(rvols)
	q := func(p float64) float64 { return rvols[int(p*float64(len(rvols)-1))] }
	within := 0
	for _, r := range rvols {
		if r >= 0.5 && r <= 2.0 {
			within++
		}
	}
	fmt.Printf("RVOL @ %s ET on %s (matched minute %d, counted shares, n=%d tickers, %d thin-skipped):\n",
		hhmm, date, minute, len(rvols), skippedThin)
	fmt.Printf("  p10=%.2f  p25=%.2f  median=%.2f  p75=%.2f  p90=%.2f   in[0.5,2.0]=%d/%d (%.0f%%)\n",
		q(0.10), q(0.25), q(0.50), q(0.75), q(0.90), within, len(rvols), 100*float64(within)/float64(len(rvols)))
	fmt.Printf("  sanity target: median ≈ 1.0 on an average day (2.1 done-when #3; reported, not gated)\n")
	return nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
