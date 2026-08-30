// Command live runs the live market session: unconditional capture of every
// raw frame (mini-spec 1.2 — deliberately no flag to disable it) plus the
// full ingest pipeline (NBBO state, counters, the 1.4 bucket store). One
// instance per session date — two appenders would corrupt the stream — and
// a capture that already exists for today refuses to start without -resume
// (deliberate mid-session restart) so accidental runs cannot pollute it.
//
//	go run ./cmd/live                      # session until 20:00 ET today
//	go run ./cmd/live -resume              # continue today's capture after a mid-session stop
//	go run ./cmd/live -until 16:30:00 -out /tmp/scratch  # smoke test (never the real -out)
//
// The API key comes from MASSIVE_API_KEY (environment, falling back to .env
// at the repo root). Ctrl-C closes cleanly and writes the manifest.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"buddy-flow/internal/breadth"
	"buddy-flow/internal/bucket"
	"buddy-flow/internal/capture"
	"buddy-flow/internal/delta"
	"buddy-flow/internal/devview"
	"buddy-flow/internal/feed"
	"buddy-flow/internal/flags"
	"buddy-flow/internal/flowshare"
	"buddy-flow/internal/ingest"
	"buddy-flow/internal/optequity"
	"buddy-flow/internal/optfollow"
	"buddy-flow/internal/premarket"
	"buddy-flow/internal/relperf"
	"buddy-flow/internal/tickerview"
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
		resume      = flag.Bool("resume", false, "append to an existing capture for today (deliberate mid-session restart ONLY — never run two instances at once)")
		drillPath   = flag.String("drill", "", "with -view: rewrite this file (atomically, every -drill-every of event time) with every basket's ticker drill-down (ticker-view-v0) — tools/live_view_server.py --drill serves it as ?basket=NAME; empty = off")
		drillEvery  = flag.Duration("drill-every", 5*time.Second, "event-time cadence of the -drill rewrite")
		optCapture  = flag.String("options-capture", "", "with -view: the day's options capture (7.1, data/capture-options/<date>/stream.jsonl) to follow read-only (MO-5) — adds basket conv_z/net_z to the trader table and per-ticker conv_z/net_z to the drill-down and strip; waits for the file if it does not exist yet; empty = no options columns")
		optProfiles = flag.String("options-profiles", "", "with -options-capture: options profile dir (7.6); a weights-stamp mismatch refuses the options columns loudly and the equity table starts without them")
		optWeights  = flag.String("options-weights", "docs/foundations/options-weights-v1.json", "conviction weights config (7.3) — the stamp the options profiles must carry")
		preLogPath  = flag.String("pre-log", "data/live-pre.log", "with -view: write the premarket frame (MO-7: pre_vol/pre_share/pre_conc ranked by pre_share, frozen at 09:30) here on every tick, a full-screen redraw per frame like live.log — tools/live_view_server.py --pre-log serves it as the PREMARKET tab; empty = off")
	)
	flag.Parse()
	if (*optCapture == "") != (*optProfiles == "") {
		fatal(fmt.Errorf("-options-capture and -options-profiles go together"))
	}
	if *optCapture != "" && !*view {
		fatal(fmt.Errorf("-options-capture requires -view"))
	}

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

	// The resume decision is the WRITER's (made under its flock, no
	// stat-then-open race); opening early — before the pipeline spins up —
	// keeps the refusal loud and instant. This command only translates the
	// refusal into flag advice.
	w, err := capture.NewWriter(*outDir, date, *resume)
	switch {
	case errors.Is(err, capture.ErrExists):
		fatal(fmt.Errorf("%w — pass -resume to deliberately continue today's capture, or -out <scratch dir> for a test run", err))
	case errors.Is(err, capture.ErrLocked):
		fatal(fmt.Errorf("%w — another live instance is running; one instance per session date", err))
	case err != nil:
		fatal(err)
	}

	syms, err := universe.Load(*basketsPath)
	if err != nil {
		fatal(err)
	}
	table := ingest.NewTable(syms)
	p := ingest.NewPipeline(table, 0)
	store := bucket.NewTimeOrderedStore() // the websocket is one time-ordered stream: signed volume is recorded

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
	var drill *tickerview.DrillWriter
	var pre *preLog
	var follower *optfollow.Follower // MO-5: read-only options follower, stopped with the view
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
		bc, err := breadth.New(store, table)
		if err != nil {
			fatal(err)
		}
		// 3.2b volume breadth: reuses the view's already-loaded
		// per-ticker profiles.
		vc := breadth.NewVol(bc, store, dv.Profiles())
		cols, rank, footer := flowshare.TraderColumns(store, unionStates, shares, floors, vc.BreadthColumn(true))
		// MO-6: the flags column reads each column's own colour predicate;
		// the cum_share_z key is the rank before premarket wraps it, and
		// every other slot is wired as its package is composed below (an
		// unwired slot — e.g. no options tape — stays unlit).
		run := flowshare.NewRun(store, unionStates, shares, floors)
		dc := delta.New(store)
		fl := flags.Set{Z: flowshare.CumZFlag(rank), R: run.RunFlag, B: bc.Flag, Dollar: vc.DollarFlag, Delta: dc.Flag}
		// Premarket columns + pre-open rank (premarket-view-v0): where
		// extended-hours dollars concentrate before the bell; frozen at
		// 09:30 as context for the day.
		// One Calc per process: the premarket frame (below) shares it,
		// so the union window pass runs once per second, not twice.
		pc := premarket.New(store, unionStates)
		cols, rank, footer = pc.ExtendTrader(cols, rank, footer)
		// MO-8 run metrics: since / 5m_z after cum_share_z (one Run per
		// process — the share series is rebuilt once per render second),
		// then vs_SPY after 5m_z over breadth's own anchors.
		cols, footer = run.ExtendTrader(cols, footer)
		cols, footer = relperf.New(bc).ExtendTrader(cols, footer)
		// MO-3: delta / class% after concentration_day (README order),
		// read from the signed columns the time-ordered store classifies.
		cols, footer = dc.ExtendTrader(cols, footer)
		// MO-5: the options tape on the equity screen, live. A separate
		// read-only follower over the day's options capture (MO-4; the
		// options capture process is never touched — L4); basket
		// conv_z/net_z after class%, per-ticker conv_z/net_z through the
		// same follower. Startup before the tape is the normal case (L3):
		// the follower waits for the file and the columns gap until the
		// tape has been read past a minute's end. A stamp mismatch — or
		// any load failure — refuses the options columns LOUDLY and the
		// equity table starts without them (L2): never a blended number.
		// Note: the follower's clock is RecvNs-based, so a basket cell here
		// may lag the :8788 table by one poll.
		// The clock line carries the tape's state (waiting for tape /
		// following / refused (<file>) / ended early) so a gap and a
		// fault read differently on screen.
		var opts *tickerview.OptionsSource
		var optState atomic.Value // string; read on the render goroutine
		optState.Store("")
		status := bc.Status
		if *optCapture != "" {
			optState.Store("waiting for tape")
			var waitOnce sync.Once
			f, err := optfollow.Start(optfollow.Config{
				CapturePath: *optCapture, WeightsPath: *optWeights, BasketsCfg: *basketsPath, ProfilesDir: *optProfiles,
				Waiting: func(path string) {
					waitOnce.Do(func() { fmt.Fprintf(logw, "options: waiting for %s to appear (conv_z/net_z gap until then)\n", path) })
				},
				Opened: func() {
					optState.Store("following")
					fmt.Fprintf(logw, "options: following %s\n", *optCapture)
				},
			})
			if err == nil {
				var src *optequity.Source
				if src, err = optequity.FromFollower(f, *optProfiles, syms); err != nil {
					f.Stop()
				} else {
					follower = f
					cols, footer = src.ExtendTrader(cols, footer)
					opts = src.Ticker
					fl.Conv = src.ConvFlag
					// A tail error ends the follow early: say so once, on
					// the log and on the clock line — the columns gap from
					// there, which must not read as a quiet tape.
					go func() {
						<-f.Done()
						if ferr := f.Err(); ferr != nil {
							optState.Store("ended early")
							fmt.Fprintf(logw, "options follower ENDED early (conv_z/net_z gap from here): %v\n", ferr)
						}
					}()
				}
			}
			if err != nil {
				optState.Store(optequity.RefusedState(err))
				fmt.Fprintf(os.Stderr, "options columns REFUSED (equity table starts without conv_z/net_z): %v\n", err)
			}
			status = optequity.Status(bc.Status, func() string { return optState.Load().(string) })
		}
		// MO-6: flags leftmost, after every slot's package has been
		// composed (basket-level only; the drill-down keeps `since`).
		cols, footer = fl.ExtendTrader(cols, footer)
		// Ticker view (ticker-view-v0): the crossings strip rides on every
		// frame; every basket's drill-down goes to the -drill file for the
		// frame server.
		tv, err := tickerview.New(store, table, bks, dv.Profiles(), floors, opts)
		if err != nil {
			fatal(err)
		}
		dv.SetTrailer(tv.Strip)
		footer += tickerview.Footer
		if *drillPath != "" {
			drill = &tickerview.DrillWriter{Calc: tv, Path: *drillPath, Every: int64(drillEvery.Seconds())}
		}
		dv.SetColumns(cols)
		dv.SetRank(rank)
		dv.SetFooter(footer)
		dv.SetStatus(status)
		// MO-7: the premarket frame — a second view over the SAME store
		// (T3: one renderer, two column sets), composed exactly as
		// cmd/replay -view-mode premarket. It never observes the pipeline
		// (dv holds the single observer slot and the clock); it only
		// renders on dv's clock, so the two logs' clock lines agree.
		if *preLogPath != "" {
			pv, err := devview.New(store, table, bks, *profilesDir)
			if err != nil {
				fatal(err)
			}
			pcols, prank, pfooter := pc.Tab()
			pv.SetColumns(pcols)
			pv.SetRank(prank)
			pv.SetFooter(pfooter)
			pv.SetStatus(pc.Status)
			f, err := os.Create(*preLogPath) // truncated at start, like live.log (8.1)
			if err != nil {
				fatal(fmt.Errorf("-pre-log: %w", err))
			}
			pre = &preLog{view: pv, w: f}
		}
		p.SetObserver(dv) // before Run starts (pipeline contract)
	} else {
		p.SetObserver(store) // before Run starts (pipeline contract)
	}
	pipeDone := make(chan struct{})
	go func() { p.Run(); close(pipeDone) }()
	stopRender := startRenderLoop(dv, drill, pre)

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
	if follower != nil {
		// L4: the follower stops with the view; the options capture
		// process is never touched. Stop waits for its pipeline to drain.
		follower.Stop()
		frames, prints, dupes, decodeErrs := follower.Progress()
		fmt.Fprintf(logw, "options follower: frames=%d prints=%d dupes=%d decode-errs=%d err=%v\n", frames, prints, dupes, decodeErrs, follower.Err())
	}

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
	// Store summary next to the done: stats — tripwire + MO-2 aggressor
	// honesty line, the same path cmd/replay uses.
	bucket.Report(os.Stdout, store, p.CondOverflow.Load())
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

// startRenderLoop drives the -view refresh: re-render whenever the LIVE
// clock (max SIP ts through the view) has crossed a second — the same
// contract as cmd/replay's loop; the renderer itself is pure. Stop prints
// the final table into scrollback (no clear) ahead of the session summary.
// No-op when dv is nil. drill and pre (both optional) are refreshed on the
// same clock, so every frame of every log carries the same second.
func startRenderLoop(dv *devview.View, drill *tickerview.DrillWriter, pre *preLog) (stop func()) {
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
					drill.MaybeWrite(sec)
					pre.write(sec)
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
			drill.MaybeWrite(sec)
			pre.write(sec)
		}
		pre.close()
	}
}

// preLog is the premarket frame's sink (MO-7): the second view rendered
// on the session view's clock, written as a full-screen redraw per frame —
// the same framing as live.log, so tools/live_view_server.py's frame split
// serves it unchanged. Nil-safe: a nil *preLog writes nothing.
type preLog struct {
	view *devview.View
	w    *os.File
	errs int
}

func (p *preLog) write(sec int64) {
	if p == nil {
		return
	}
	if _, err := fmt.Fprint(p.w, "\033[H\033[2J"+p.view.Render(sec)); err != nil {
		// The premarket log is a follower's mirror, never the record: a
		// write failure is reported once, not fatal to the capture.
		if p.errs++; p.errs == 1 {
			fmt.Fprintf(os.Stderr, "pre-log write failed (capture unaffected): %v\n", err)
		}
	}
}

func (p *preLog) close() {
	if p != nil {
		p.w.Close()
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
