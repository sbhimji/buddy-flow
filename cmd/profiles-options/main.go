// Command profiles-options rolls the options baselines (mini-spec 7.6):
// per-ticker and per-basket 20-day matched-minute profiles of
// net_conviction and net_notional, plus σ floors. Inputs are the 7.4/7.5
// bucket files; every input and output carries the weights stamp.
//
//	go run ./cmd/profiles-options -days 20 -through 2026-08-24
//
// Exit codes: 0 ok, 2 flag/config, 3 input refused or unreadable, 4 write.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"buddy-flow/internal/optbucket"
	"buddy-flow/internal/optclassify"
	"buddy-flow/internal/optprofile"
	"buddy-flow/internal/universe"
)

func main() {
	var (
		days        = flag.Int("days", 20, "lookback: the N most recent covered days")
		through     = flag.String("through", "", "last date to include (YYYY-MM-DD; default: latest on disk)")
		bucketsDir  = flag.String("buckets-dir", "data/buckets-options", "options bucket files")
		outDir      = flag.String("out", "data/profiles-options", "profile output directory")
		frac        = flag.Float64("sigma-floor-frac", 0.25, "σ floor as a fraction of the universe-median σ per minute (D18)")
		basketsPath = flag.String("baskets", "docs/foundations/morning-tape-baskets-v2.json", "trader-owned basket config")
		weightsPath = flag.String("weights", "docs/foundations/options-weights-v1.json", "conviction weights config")
	)
	flag.Parse()

	w, hash, err := optclassify.LoadWeights(*weightsPath)
	if err != nil {
		fatal(2, err)
	}
	stamp := w.Version + "@" + hash
	syms, err := universe.Load(*basketsPath)
	if err != nil {
		fatal(2, err)
	}
	baskets, err := universe.LoadBaskets(*basketsPath)
	if err != nil {
		fatal(2, err)
	}

	dates, err := optprofile.DiscoverDays(*bucketsDir, *through, *days)
	if err != nil {
		fatal(3, err)
	}
	if len(dates) == 0 {
		fatal(3, fmt.Errorf("no covered days in %s (through %q)", *bucketsDir, *through))
	}
	start := time.Now()
	var lookback []optprofile.Day
	for _, d := range dates {
		sess, err := optbucket.ReadCSV(optbucket.Path(*bucketsDir, d), stamp)
		if err != nil {
			fatal(3, err)
		}
		lookback = append(lookback, optprofile.Day{Date: d, Session: sess})
	}
	fmt.Printf("read %d days (%s .. %s) in %s\n", len(dates), dates[0], dates[len(dates)-1], time.Since(start).Round(time.Second))
	if len(dates) < optprofile.MinProfiledDays {
		fmt.Printf("note: %d days < MinProfiledDays %d — profiles build but z-scores stay null until the gate is met\n", len(dates), optprofile.MinProfiledDays)
	}

	inputs := fmt.Sprintf("built from %d symbols x %d days (%s .. %s)", len(syms), len(dates), dates[0], dates[len(dates)-1])
	tickers, err := optprofile.Build(syms, lookback)
	if err != nil {
		fatal(3, err)
	}
	bprofs, err := optprofile.BuildBaskets(baskets, lookback)
	if err != nil {
		fatal(3, err)
	}
	fl := optprofile.ComputeFloors(tickers, bprofs, *frac)

	if err := optprofile.Write(*outDir, tickers, stamp, inputs); err != nil {
		fatal(4, err)
	}
	if err := optprofile.WriteBaskets(*outDir, bprofs, stamp, inputs); err != nil {
		fatal(4, err)
	}
	if err := optprofile.WriteFloors(*outDir, fl, *frac, stamp, inputs); err != nil {
		fatal(4, err)
	}
	fmt.Printf("wrote %d ticker profiles, %d basket profiles, %s -> %s (%s) in %s\n",
		len(tickers), len(bprofs), optprofile.FloorsFile, *outDir, stamp, time.Since(start).Round(time.Second))
	fmt.Printf("days: %s\n", strings.Join(dates, " "))
}

func fatal(code int, err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(code)
}
