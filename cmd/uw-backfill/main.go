// Command uw-backfill converts Unusual Whales full-tape zips into options
// bucket files (mini-spec 7.5): the 20-day baseline corpus. Resumable —
// dates that already have a bucket file are skipped. Downloads are a
// separate (shell) step; this command only converts what is on disk.
//
//	go run ./cmd/uw-backfill -zips data/full-tape -out data/buckets-options
//	go run ./cmd/uw-backfill -zips data/full-tape -out /tmp/scratch -dates 2026-08-20
//
// Live-capture days are NOT converted here (F2): if the output already
// exists it is left alone, whatever produced it.
// Exit codes: 0 ok, 2 flag misuse, 3 conversion failure.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"buddy-flow/internal/optbucket"
	"buddy-flow/internal/optclassify"
	"buddy-flow/internal/optingest"
	"buddy-flow/internal/universe"
	"buddy-flow/internal/uwfeed"
)

func main() {
	var (
		zipsDir     = flag.String("zips", "data/full-tape", "directory of <date>.zip full-tape files")
		outDir      = flag.String("out", "data/buckets-options", "bucket file directory")
		dates       = flag.String("dates", "", "comma-separated dates to convert (default: every zip in -zips)")
		basketsPath = flag.String("baskets", "docs/foundations/morning-tape-baskets-v2.json", "trader-owned basket config")
		weightsPath = flag.String("weights", "docs/foundations/options-weights-v1.json", "conviction weights config")
		force       = flag.Bool("force", false, "convert even when the bucket file exists (overwrites)")
	)
	flag.Parse()

	syms, err := universe.Load(*basketsPath)
	if err != nil {
		fatal(2, err)
	}
	uni := make(map[string]bool, len(syms))
	for _, s := range syms {
		uni[s] = true
	}
	w, hash, err := optclassify.LoadWeights(*weightsPath)
	if err != nil {
		fatal(2, err)
	}
	stamp := w.Version + "@" + hash

	var list []string
	if *dates != "" {
		list = strings.Split(*dates, ",")
	} else {
		entries, err := filepath.Glob(filepath.Join(*zipsDir, "*.zip"))
		if err != nil {
			fatal(2, err)
		}
		for _, e := range entries {
			list = append(list, strings.TrimSuffix(filepath.Base(e), ".zip"))
		}
	}
	sort.Strings(list)
	if len(list) == 0 {
		fatal(2, fmt.Errorf("no zips found in %s", *zipsDir))
	}

	converted, skipped := 0, 0
	for _, date := range list {
		out := optbucket.Path(*outDir, date)
		if !*force {
			if _, err := os.Stat(out); err == nil {
				fmt.Printf("%s: bucket file exists — skipped (F2; -force to overwrite)\n", date)
				skipped++
				continue
			}
		}
		zipPath := filepath.Join(*zipsDir, date+".zip")
		start := time.Now()
		p := optingest.NewPipeline(0)
		store, err := optbucket.NewStore(w, stamp)
		if err != nil {
			fatal(3, err)
		}
		p.SetObserver(store)
		done := make(chan struct{})
		go func() { p.Run(); close(done) }()
		st, err := uwfeed.StreamFullTape(zipPath, uni, p)
		p.Close()
		<-done
		if err != nil {
			fatal(3, fmt.Errorf("%s: %w", date, err))
		}
		minSec, maxSec, ok := store.Bounds()
		if !ok {
			fmt.Printf("%s: no universe prints — nothing written\n", date)
			continue
		}
		writePath := out
		if dataDate, spans, serr := optbucket.SpansRegularSession(minSec, maxSec); serr != nil || !spans || dataDate != date {
			writePath = optbucket.PartialPath(*outDir, date)
			fmt.Printf("%s: does not span the regular session (spans=%v date=%s err=%v) — writing partial name\n", date, spans, dataDate, serr)
		}
		rows, err := store.WriteCSV(writePath)
		if err != nil {
			fatal(3, fmt.Errorf("%s: write: %w", date, err))
		}
		prints, _ := store.Totals()
		fmt.Printf("%s: rows=%d universe=%d canceled=%d decode-errs=%d other=%d dupes=%d unclassifiable=%d -> %d bucket rows (%s) in %s\n",
			date, st.Rows, st.Universe, st.Canceled, st.DecodeErrs, st.OtherSymbol, p.Dupes.Load(), store.Unclassifiable, rows,
			filepath.Base(writePath), time.Since(start).Round(time.Second))
		_ = prints
		converted++
	}
	fmt.Printf("done: converted=%d skipped=%d\n", converted, skipped)
}

func fatal(code int, err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(code)
}
