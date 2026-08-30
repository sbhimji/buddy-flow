// Command replay-options replays a captured options session through the
// production decoder and pipeline (mini-spec 7.2), optionally aggregating
// into the 7.4 bucket store. The acceptance instrument for the options
// tape: same capture in → identical counts and bucket bytes out, at any
// speed.
//
//	go run ./cmd/replay-options -capture data/capture-options/2026-08-24/stream.jsonl
//	go run ./cmd/replay-options -capture ... -buckets data/buckets-options/2026-08-24.csv
//	go run ./cmd/replay-options -capture ... -speed 60 -pace-from 09:30:00
//
// -buckets takes a FILE path (the cmd/replay convention). The file name is
// validated against the data: a store that does not span the regular
// session must be written under the .partial.csv name.
// Exit codes: 0 ok, 2 flag misuse, 3 unreadable input, 4 bucket write failed.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"buddy-flow/internal/conviction"
	"buddy-flow/internal/optbucket"
	"buddy-flow/internal/optclassify"
	"buddy-flow/internal/optingest"
	"buddy-flow/internal/session"
	"buddy-flow/internal/universe"
	"buddy-flow/internal/uwfeed"
)

func main() {
	var (
		capturePath = flag.String("capture", "", "options capture stream.jsonl(.gz) to replay (required)")
		bucketsPath = flag.String("buckets", "", "write the 7.4 bucket store to this FILE path (<dir>/<date>.csv)")
		weightsPath = flag.String("weights", "docs/foundations/options-weights-v1.json", "conviction weights config (7.3)")
		viewAt      = flag.String("view-at", "", "ET HH:MM:SS — render the per-basket snapshot table at this time (post-replay)")
		basketsCfg  = flag.String("baskets", "docs/foundations/morning-tape-baskets-v2.json", "trader-owned basket config (for -view-at)")
		profilesDir = flag.String("profiles", "", "options profile dir (7.6) — adds conv_z/net_z columns to -view-at; omit for sums only")
		speed       = flag.Float64("speed", 0, "0 = instant; 1 = real-time; N = xN")
		paceFrom    = flag.String("pace-from", "", "ET HH:MM:SS — replay instantly until here, then pace")
		queue       = flag.Int("queue", 0, "pipeline queue size (0 = default)")
		view        = flag.Bool("view", false, "PACED VIEW: with -speed N (and optionally -pace-from), re-render the basket table every -refresh of event time as the replay unfolds — what the trader would have seen")
		follow      = flag.Bool("follow", false, "FOLLOW MODE: tail a capture still being written, re-rendering the basket table every -refresh of event time; exits when the session's manifest.json appears or on Ctrl-C")
		refresh     = flag.Duration("refresh", 5*time.Second, "follow-mode render cadence (event time)")
	)
	flag.Parse()
	if *capturePath == "" {
		fmt.Fprintln(os.Stderr, "-capture is required")
		os.Exit(2)
	}

	if *follow {
		if err := runFollow(*capturePath, *weightsPath, *basketsCfg, *profilesDir, *refresh); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		return
	}

	p := optingest.NewPipeline(*queue)
	var store *optbucket.Store
	var stamp string
	if *bucketsPath != "" || *viewAt != "" {
		w, hash, err := optclassify.LoadWeights(*weightsPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		stamp = w.Version + "@" + hash
		store, err = optbucket.NewStore(w, stamp)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		p.SetObserver(store) // before Run starts (pipeline contract)
	}
	pipeDone := make(chan struct{})
	go func() { p.Run(); close(pipeDone) }()

	var stats uwfeed.DecodeStats
	var pacedView *viewer
	if *view {
		if store == nil {
			w, hash, err := optclassify.LoadWeights(*weightsPath)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
			stamp = w.Version + "@" + hash
			if store, err = optbucket.NewStore(w, stamp); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
			p.SetObserver(store)
		}
		bks, err := universe.LoadBaskets(*basketsCfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		var base *conviction.Baselines
		if *profilesDir != "" {
			if base, err = conviction.Load(*profilesDir, bks, stamp); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
		}
		progress, drained := pipelineProgress(p, &stats)
		pacedView = &viewer{store: store, progress: progress, drained: drained, bks: bks, base: base, refresh: *refresh,
			header: fmt.Sprintf("REPLAY %s x%g", filepath.Base(filepath.Dir(*capturePath)), *speed)}
		if *speed == 0 {
			fmt.Println("note: -view with -speed 0 renders as fast as the replay runs; use -speed N to watch at N× real time")
		}
	}
	start := time.Now()
	err := uwfeed.StreamCapture(*capturePath, p, &stats, uwfeed.ReplayOptions{
		Speed:    *speed,
		PaceFrom: *paceFrom,
		Log:      func(f string, a ...any) { fmt.Printf(f+"\n", a...) },
		Tick: func(int64) {
			if pacedView != nil {
				pacedView.render(false)
			}
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "replay:", err)
		os.Exit(3)
	}
	p.Close()
	<-pipeDone
	if pacedView != nil {
		pacedView.render(true)
	}

	fmt.Printf("replayed in %s: frames=%d prints=%d acks=%d controls=%d non-trade=%d decode-errs=%d\n",
		time.Since(start).Round(time.Millisecond),
		stats.Frames.Load(), stats.Prints.Load(), stats.Acks.Load(), stats.Controls.Load(), stats.NonTrade.Load(), stats.DecodeErrs.Load())
	fmt.Printf("pipeline: processed=%d dupes=%d queue-max=%d\n",
		p.Processed.Load(), p.Dupes.Load(), p.MaxQueueDepth.Load())
	if stats.DecodeErrs.Load() > 0 {
		fmt.Printf("!! tripwire: %d decode errors — inspect the capture (frames are preserved raw)\n", stats.DecodeErrs.Load())
	}
	if store == nil {
		return
	}

	// Classification-rate summary (7.3 done-when 3): stated as counts and
	// percentages of processed prints.
	prints, contracts := store.Totals()
	pct := func(n int64) float64 {
		if prints == 0 {
			return 0
		}
		return 100 * float64(n) / float64(prints)
	}
	tel := store.Telemetry()
	ask, bid, zero := tel.SidePrints[optbucket.SideAsk], tel.SidePrints[optbucket.SideBid], tel.SidePrints[optbucket.SideZero]
	fmt.Printf("classified: prints=%d contracts=%d ask=%.1f%% bid=%.1f%% zero-sign=%.1f%% sweep=%.1f%% unclassifiable=%d\n",
		prints, contracts, pct(ask), pct(bid), pct(zero), pct(tel.SweepPrints), tel.Unclassifiable)

	minSec, maxSec, ok := store.Bounds()
	if !ok {
		fmt.Println("buckets: store empty — nothing to write")
		return
	}

	if *viewAt != "" {
		date := time.Unix(minSec, 0).In(session.ET()).Format("2006-01-02")
		at, err := time.ParseInLocation("2006-01-02 15:04:05", date+" "+*viewAt, session.ET())
		if err != nil {
			fmt.Fprintf(os.Stderr, "bad -view-at: %v\n", err)
			os.Exit(2)
		}
		bks, err := universe.LoadBaskets(*basketsCfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		var base *conviction.Baselines
		if *profilesDir != "" {
			if base, err = conviction.Load(*profilesDir, bks, stamp); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
		}
		if err := renderSnapshot(store, bks, base, date, at.Unix()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	if *bucketsPath == "" {
		return
	}
	// The 1.4/D3 naming rule: the filename states coverage, decided from
	// the data. A non-spanning store must carry the .partial name.
	writePath := *bucketsPath
	dataDate, spans, serr := optbucket.SpansRegularSession(minSec, maxSec)
	if serr != nil || !spans {
		if !strings.HasSuffix(writePath, ".partial.csv") {
			dir := filepath.Dir(writePath)
			writePath = optbucket.PartialPath(dir, dataDate)
			fmt.Printf("buckets: store does not span the regular session (spans=%v err=%v) — writing %s\n", spans, serr, writePath)
		}
	}
	rows, err := store.WriteCSV(writePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bucket write FAILED: %v\n", err)
		os.Exit(4)
	}
	fmt.Printf("buckets: %d (second,symbol) rows -> %s\n", rows, writePath)
}
