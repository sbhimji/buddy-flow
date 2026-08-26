// Command live-options runs the live Unusual Whales options session:
// unconditional capture of every raw frame (mini-spec 7.1 — deliberately no
// flag to disable it), classification stats only; decode and buckets arrive
// in later stories. A separate binary from cmd/live on purpose: a fault here
// must never touch the equity session capture (plan §capture design).
//
//	go run ./cmd/live-options                    # session until 17:30 ET today
//	go run ./cmd/live-options -resume            # continue today's capture after a mid-session stop
//	go run ./cmd/live-options -until 10:30:00 -out /tmp/scratch  # smoke test (never the real -out)
//
// The API key comes from UNUSUAL_WHALES_API_KEY (environment, falling back
// to .env at the repo root). Ctrl-C closes cleanly and writes the manifest.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"buddy-flow/internal/capture"
	"buddy-flow/internal/universe"
	"buddy-flow/internal/uwfeed"
)

func main() {
	var (
		basketsPath = flag.String("baskets", "docs/foundations/morning-tape-baskets-v2.json", "trader-owned basket config")
		outDir      = flag.String("out", "data/capture-options", "capture base directory (separate from the equity capture)")
		untilS      = flag.String("until", "17:30:00", "stop time, ET HH:MM:SS today (options print to ~17:00 — 7.0 finding)")
		url         = flag.String("url", uwfeed.DefaultURL, "websocket endpoint (token appended at dial, never logged)")
		resume      = flag.Bool("resume", false, "append to an existing capture for today (deliberate mid-session restart ONLY — never run two instances at once)")
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
		fatal(fmt.Errorf("UNUSUAL_WHALES_API_KEY not set (env or .env)"))
	}

	// The resume decision is the WRITER's (made under its flock); opening
	// early keeps the refusal loud and instant — same wiring as cmd/live.
	w, err := capture.NewWriter(*outDir, date, *resume)
	switch {
	case errors.Is(err, capture.ErrExists):
		fatal(fmt.Errorf("%w — pass -resume to deliberately continue today's capture, or -out <scratch dir> for a test run", err))
	case errors.Is(err, capture.ErrLocked):
		fatal(fmt.Errorf("%w — another live-options instance is running; one instance per session date", err))
	case err != nil:
		fatal(err)
	}

	syms, err := universe.Load(*basketsPath)
	if err != nil {
		fatal(err)
	}

	w.Control("start", fmt.Sprintf("universe=%d until=%s", len(syms), until.Format("15:04:05")))
	fmt.Printf("capturing %d option_trades channels to %s until %s ET\n",
		len(syms), capture.StreamPath(*outDir, date), until.Format("15:04:05"))

	// Ctrl-C / SIGTERM → close stopCh, NOTHING else: the capture writer is
	// the feeder's; the stop record belongs to main, written once RunLive
	// has returned (the 1.2 review #6 lesson). After the first signal the
	// handler detaches, so a second Ctrl-C kills hard — the kill drill.
	stopCh := make(chan struct{})
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
				fmt.Printf("[%s] frames=%d bytes=%dMB\n",
					time.Now().In(loc).Format("15:04:05"), w.Frames.Load(), w.Bytes.Load()>>20)
			case <-stopped:
				return
			}
		}
	}()

	opt := uwfeed.LiveOptions{
		URL: *url, APIKey: key, Symbols: syms, Until: until, Stop: stopCh, Capture: w,
		Log: func(f string, a ...any) {
			fmt.Printf("[%s] "+f+"\n", append([]any{time.Now().In(loc).Format("15:04:05")}, a...)...)
		},
	}
	stats, runErr := uwfeed.RunLive(opt)
	close(stopped)

	reason := "session end"
	select {
	case <-stopCh:
		reason = "signal"
	default:
	}
	if runErr != nil {
		reason = "fatal: " + runErr.Error()
		fmt.Fprintf(os.Stderr, "live-options feeder FATAL: %v\n", runErr)
	}
	w.Control("stop", reason)

	if err := w.Close(capture.Manifest{
		Date: date, UniverseSize: len(syms),
		Subscriptions: []string{"option_trades:* for universe (see universe_size)"},
		Note: fmt.Sprintf("uw-options data=%d acks-ok=%d acks-non-ok=%d unknown-shape=%d unknown-channel=%d reconnects=%d stop=%s",
			stats.Data, stats.AcksOK, stats.AcksNonOK, stats.UnknownShape, stats.UnknownChannel, stats.Reconnects, reason),
	}); err != nil {
		fatal(err)
	}
	fmt.Printf("done: frames=%d data=%d acks-ok=%d acks-non-ok=%d unknown-shape=%d unknown-channel=%d reconnects=%d\n",
		stats.Frames, stats.Data, stats.AcksOK, stats.AcksNonOK, stats.UnknownShape, stats.UnknownChannel, stats.Reconnects)
	if stats.UnknownShape > 0 || stats.UnknownChannel > 0 {
		fmt.Printf("!! tripwire: %d unrecognized-shape frames, %d frames on unjoined channels — inspect the capture before 7.2 decode work\n",
			stats.UnknownShape, stats.UnknownChannel)
	}
	if runErr != nil {
		os.Exit(1)
	}
}

// apiKey reads UNUSUAL_WHALES_API_KEY from the environment, then .env at
// the repo root (KEY=VALUE lines; quotes stripped) — the cmd/live contract.
func apiKey() string {
	if k := os.Getenv("UNUSUAL_WHALES_API_KEY"); k != "" {
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
		if v, ok := strings.CutPrefix(line, "UNUSUAL_WHALES_API_KEY="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
