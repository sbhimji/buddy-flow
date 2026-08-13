// Command capture runs the live websocket feeder with unconditional capture
// (mini-spec 1.2). There is deliberately no flag to disable capture.
//
//	go run ./cmd/capture                      # capture until 20:00 ET today
//	go run ./cmd/capture -until 16:30:00      # shorter session (smoke tests)
//
// The API key comes from MASSIVE_API_KEY (environment, falling back to .env
// at the repo root). Ctrl-C closes cleanly and writes the manifest.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"buddy-flow/internal/capture"
	"buddy-flow/internal/feed"
	"buddy-flow/internal/ingest"
	"buddy-flow/internal/universe"
)

func main() {
	var (
		basketsPath = flag.String("baskets", "docs/foundations/morning-tape-baskets-v2.json", "trader-owned basket config")
		outDir      = flag.String("out", "data/capture", "capture base directory")
		untilS      = flag.String("until", "20:00:00", "stop time, ET HH:MM:SS today")
		url         = flag.String("url", "wss://socket.massive.com/stocks", "websocket endpoint")
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
	pipeDone := make(chan struct{})
	go func() { p.Run(); close(pipeDone) }()

	w, err := capture.NewWriter(*outDir, date)
	if err != nil {
		fatal(err)
	}
	w.Control("start", fmt.Sprintf("universe=%d until=%s", len(syms), until.Format("15:04:05")))
	fmt.Printf("capturing %d symbols (T+Q) to %s until %s ET\n", len(syms), capture.StreamPath(*outDir, date), until.Format("15:04:05"))

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
			fmt.Printf("[%s] "+f+"\n", append([]any{time.Now().In(loc).Format("15:04:05")}, a...)...)
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
				fmt.Printf("[%s] frames=%d bytes=%dMB processed=%d queue-max=%d\n",
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
		Note:          fmt.Sprintf("trades=%d quotes=%d status=%d reconnects=%d decode-errs=%d unknown=%d stop=%s", uT, uQ, stats.Status, stats.Reconnects, stats.DecodeErrs, stats.Unknown, reason),
	}); err != nil {
		fatal(err)
	}
	fmt.Printf("done: frames=%d trades=%d quotes=%d status=%d reconnects=%d decode-errs=%d unknown-sym=%d\n",
		stats.Frames, uT, uQ, stats.Status, stats.Reconnects, stats.DecodeErrs, stats.Unknown)
	if runErr != nil {
		os.Exit(1) // the manifest records the fatal stop; the exit code makes cron/scripts see it too
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
