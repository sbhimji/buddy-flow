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
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"buddy-flow/internal/archive"
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
		fromArchive = flag.Bool("archive", false, "before day discovery, fetch the last -days bucket days from the S3 archive into -buckets-dir when absent locally (ARCHIVE_* env; mini-spec 8.1)")
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

	if *fromArchive {
		n, err := fetchArchived(*bucketsDir, *days, *through)
		if err != nil {
			fatal(3, err)
		}
		fmt.Printf("archive: %d bucket files fetched into %s\n", n, *bucketsDir)
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

// fetchArchived makes the cache whole for a build: the most recent n
// covered days (<date>.csv only — the DiscoverDays rule) <= through from
// the archived class, EnsureLocal'd into the buckets dir. Discovery and the
// stamp refusals are unchanged and stay local.
func fetchArchived(dir string, n int, through string) (int, error) {
	cfg, err := archive.ConfigFromEnv(".env")
	if err != nil {
		return 0, err
	}
	s, err := archive.Open(cfg)
	if err != nil {
		return 0, err
	}
	ctx := context.Background()
	objs, err := s.List(ctx, archive.BucketsOptions)
	if err != nil {
		return 0, err
	}
	var names []string
	for _, o := range objs {
		d := strings.TrimSuffix(o.Name, ".csv")
		if len(d) != 10 || strings.Contains(o.Name, ".partial.") || strings.Contains(o.Name, "/") {
			continue
		}
		if _, err := time.Parse("2006-01-02", d); err != nil {
			continue
		}
		if through == "" || d <= through {
			names = append(names, o.Name)
		}
	}
	sort.Strings(names)
	if len(names) > n {
		names = names[len(names)-n:]
	}
	fetched := 0
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			continue
		}
		if _, err := archive.EnsureLocal(ctx, s, archive.BucketsOptions, name, dir); err != nil {
			return fetched, err
		}
		fetched++
	}
	return fetched, nil
}

func fatal(code int, err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(code)
}
