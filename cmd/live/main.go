// Command live runs the live market session: unconditional capture of every
// raw frame (mini-spec 1.2 — deliberately no flag to disable it) plus the
// full ingest pipeline (NBBO state, counters, the 1.4 bucket store). One
// instance per session date — two appenders would corrupt the stream.
//
//	go run ./cmd/live                      # session until 20:00 ET today
//	go run ./cmd/live -until 16:30:00      # shorter session (smoke tests)
//
// The API key comes from MASSIVE_API_KEY (environment, falling back to .env
// at the repo root). Ctrl-C closes cleanly and writes the manifest.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"buddy-flow/internal/bucket"
	"buddy-flow/internal/capture"
	"buddy-flow/internal/devview"
	"buddy-flow/internal/feed"
	"buddy-flow/internal/flowshare"
	"buddy-flow/internal/ingest"
	"buddy-flow/internal/universe"
)

func main() {
	var (
		basketsPath = flag.String("baskets", "docs/foundations/morning-tape-baskets-v2.json", "trader-owned basket config")
		outDir      = flag.String("out", "data/capture", "capture base directory")
		bucketsDir  = flag.String("buckets-dir", "data/buckets", "bucket store directory (1.4); session file is <dir>/<date>.csv (NB: cmd/replay's -buckets takes a file path)")
		untilS      = flag.String("until", "20:00:00", "stop time, ET HH:MM:SS today")
		url         = flag.String("url", "wss://socket.massive.com/stocks", "websocket endpoint")
		view        = flag.Bool("view", false, "trader view (trader-view-v0): live basket table on stdout; operational logs divert to stderr (2>live.log)")
		profilesDir = flag.String("profiles", "data/profiles", "profile directory for -view baselines")
	)
	flag.Parse()

	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		fatal(err)
	}
	now := time.Now().In(loc)
	date := now.Format("2006-01-02")
	until, err := time.ParseInLocation("2006-01-02 15:04:05", date+" "+*untilS, loc)
	if err != nil {
		fatal(fmt.Errorf("bad -until: %w", err))
	}
	if until.Before(now) {
		fatal(fmt.Errorf("-until %s is in the past (now %s ET)", *untilS, now.Format("15:04:05")))
	}

	key := apiKey()
	if key == "" {
		fatal(fmt.Errorf("MASSIVE_API_KEY not set (env or .env)"))
	}

	syms, err := universe.Load(*basketsPath)
	if err != nil {
		fatal(err)
	}
	table := ingest.NewTable(syms)
	p := ingest.NewPipeline(table, 0)
	store := bucket.NewStore()

	// Operational logs go to stderr when the view owns stdout — `2>live.log`
	// gives a clean table plus a complete log (trader-view-v0 T3).
	logw := io.Writer(os.Stdout)
	if *view {
		logw = os.Stderr
	}

	// The view wraps the store as the single observer — same wiring as
	// replay; the live SIP clock drives refresh exactly like the replayed
	// one (event-time buckets are the point).
	var dv *devview.View
	if *view {
		bks, err := universe.LoadBaskets(*basketsPath)
		if err != nil {
			fatal(err)
		}
		if dv, err = devview.New(store, table, bks, *profilesDir); err != nil {
			fatal(err)
		}
		unionStates, err := flowshare.Union(bks, table)
		if err != nil {
			fatal(err)
		}
		shares, floors, err := flowshare.LoadBaselines(*profilesDir, bks)
		if err != nil {
			fatal(fmt.Errorf("%w (roll profiles first: go run ./cmd/profiles -days 20)", err))
		}
		cols, rank := flowshare.TraderColumns(store, unionStates, shares, floors)
		dv.SetColumns(cols)
		dv.SetRank(rank)
		p.SetObserver(dv) // before Run starts (pipeline contract)
	} else {
		p.SetObserver(store) // before Run starts (pipeline contract)
	}
	pipeDone := make(chan struct{})
	go func() { p.Run(); close(pipeDone) }()
	stopRender := startRenderLoop(dv)

	w, err := capture.NewWriter(*outDir, date)
	if err != nil {
		fatal(err)
	}
	w.Control("start", fmt.Sprintf("universe=%d until=%s", len(syms), until.Format("15:04:05")))
	fmt.Fprintf(logw, "capturing %d symbols (T+Q) to %s until %s ET\n", len(syms), capture.StreamPath(*outDir, date), until.Format("15:04:05"))

	// Ctrl-C / SIGTERM → close stopCh, NOTHING else: the capture writer is
	// the feeder's; a control record written from this goroutine could
	// interleave inside a data frame mid-Append (review #6 — the Writer is
	// now also mutex-guarded as defense in depth, but the stop record still
	// belongs to main, written once RunLive has returned). After the first
	// signal the handler detaches (signal.Stop), so a second Ctrl-C kills
	// hard — that is the done-when #3 drill.
	stopCh := make(chan struct{})
	opt := feed.LiveOptions{
		URL: *url, APIKey: key, Symbols: syms, Until: until, Stop: stopCh, Capture: w,
		Log: func(f string, a ...any) {
			fmt.Fprintf(logw, "[%s] "+f+"\n", append([]any{time.Now().In(loc).Format("15:04:05")}, a...)...)
		},
	}
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	stopped := make(chan struct{})
	go func() {
		select {
		case <-sig:
			fmt.Println("signal: closing cleanly (Ctrl-C again to kill hard)")
			close(stopCh)
			signal.Stop(sig)
		case <-stopped:
		}
	}()

	// Periodic status line so a long capture is visibly alive.
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				fmt.Fprintf(logw, "[%s] frames=%d bytes=%dMB processed=%d queue-max=%d\n",
					time.Now().In(loc).Format("15:04:05"), w.Frames.Load(), w.Bytes.Load()>>20,
					p.Processed.Load(), p.MaxQueueDepth.Load())
			case <-stopped:
				return
			}
		}
	}()

	stats, runErr := feed.RunLive(p, opt)
	close(stopped)

	// Drain the pipeline BEFORE counting: queued-but-unapplied messages
	// would otherwise be missing from the manifest's numbers — the numbers
	// 1.5 reconciles against (review #4).
	p.Close()
	<-pipeDone
	stopRender() // final table into scrollback; summary lines follow on stdout

	reason := "session end"
	select {
	case <-stopCh:
		reason = "signal"
	default:
	}
	if runErr != nil {
		reason = "fatal: " + runErr.Error()
		fmt.Fprintf(os.Stderr, "live feeder FATAL: %v\n", runErr)
	}
	w.Control("stop", reason)

	var uT, uQ int64
	for _, s := range table.All() {
		uT += s.Trades.Load()
		uQ += s.Quotes.Load()
	}
	if err := w.Close(capture.Manifest{
		Date: date, UniverseSize: len(syms),
		Subscriptions: []string{"T.*+Q.* for universe (see universe_size)"},
		Note:          fmt.Sprintf("trades=%d quotes=%d status=%d reconnects=%d decode-errs=%d unknown=%d cond-overflow=%d stop=%s", uT, uQ, stats.Status, stats.Reconnects, stats.DecodeErrs, stats.Unknown, p.CondOverflow.Load(), reason),
	}); err != nil {
		fatal(err)
	}
	fmt.Printf("done: frames=%d trades=%d quotes=%d status=%d reconnects=%d decode-errs=%d unknown-sym=%d cond-overflow=%d\n",
		stats.Frames, uT, uQ, stats.Status, stats.Reconnects, stats.DecodeErrs, stats.Unknown, p.CondOverflow.Load())

	// Final bucket write, post-drain so every queued message is in the store,
	// and after the manifest: a bucket failure must never cost the capture
	// record. On a fatal feeder stop the write is skipped — a partial-day file
	// at the canonical path would masquerade as a full session (same refusal
	// as cmd/replay); the capture is the record, so regenerate from replay.
	// Nothing below exits early: every report line prints, then one exit code
	// — 1 fatal feeder stop, 4 bucket write failed (derived data only).
	exitCode := 0
	if runErr != nil {
		exitCode = 1 // the manifest records the fatal stop; the exit code makes cron/scripts see it too
		fmt.Fprintf(os.Stderr, "buckets not written (fatal stop = partial session): regenerate with cmd/replay -capture %s -buckets %s\n",
			capture.StreamPath(*outDir, date), bucket.PartialPath(*bucketsDir, date))
	} else if minSec, maxSec, ok := store.Bounds(); !ok {
		fmt.Printf("buckets: store empty — nothing to write\n")
	} else {
		// D3: the filename states coverage, decided from the data. A late
		// start, an early stop, or a silently dead feed gets .partial.csv —
		// truthful and invisible to baseline discovery — never an error:
		// the live process must not fail its final write over naming.
		dataDate, spans, serr := bucket.SpansRegularSession(minSec, maxSec)
		trades, quotes := store.Totals()
		writePath := bucket.Path(*bucketsDir, dataDate)
		if serr != nil || !spans || trades == 0 || quotes == 0 {
			writePath = bucket.PartialPath(*bucketsDir, dataDate)
			fmt.Printf("buckets: store does not span the regular session with both feeds (spans=%v trades=%d quotes=%d err=%v) — writing partial name\n",
				spans, trades, quotes, serr)
		}
		if rows, err := store.WriteCSV(writePath); err != nil {
			exitCode = 4
			fmt.Fprintf(os.Stderr, "final bucket write FAILED (capture intact; regenerate by replaying it): %v\n", err)
		} else {
			fmt.Printf("buckets: %d (second,symbol) rows -> %s\n", rows, writePath)
		}
	}
	if n, ids := store.Unknown(); n > 0 {
		fmt.Printf("!! tripwire: %d prints carried condition IDs missing from the 0.3 table: %v\n", n, ids)
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

// startRenderLoop drives the -view refresh: re-render whenever the LIVE
// clock (max SIP ts through the view) has crossed a second — the same
// contract as cmd/replay's loop; the renderer itself is pure. Stop prints
// the final table into scrollback (no clear) ahead of the session summary.
// No-op when dv is nil.
func startRenderLoop(dv *devview.View) (stop func()) {
	if dv == nil {
		return func() {}
	}
	quit := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		var last int64
		for {
			select {
			case <-quit:
				return
			case <-tick.C:
				if sec := dv.ClockSec(); sec > last {
					last = sec
					fmt.Print("\033[H\033[2J" + dv.Render(sec))
				}
			}
		}
	}()
	return func() {
		close(quit)
		wg.Wait()
		if sec := dv.ClockSec(); sec > 0 {
			fmt.Println()
			fmt.Print(dv.Render(sec))
		}
	}
}

// apiKey reads MASSIVE_API_KEY from the environment, then .env at the repo
// root (KEY=VALUE lines; quotes stripped).
func apiKey() string {
	if k := os.Getenv("MASSIVE_API_KEY"); k != "" {
		return k
	}
	f, err := os.Open(".env")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if v, ok := strings.CutPrefix(line, "MASSIVE_API_KEY="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
