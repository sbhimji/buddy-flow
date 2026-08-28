// follow.go — the live dashboard as a separate read-only process (the
// backlog's "trader view as a separate process"): tail the capture file the
// feeder is writing, decode through the production path, and re-render the
// basket table on an event-time cadence. The follower itself is the shared
// internal/optfollow (MO-4); this file is the terminal renderer around it.
// Kill and restart freely; the capture process never knows.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"buddy-flow/internal/conviction"
	"buddy-flow/internal/optbucket"
	"buddy-flow/internal/optfollow"
	"buddy-flow/internal/optingest"
	"buddy-flow/internal/session"
	"buddy-flow/internal/universe"
	"buddy-flow/internal/uwfeed"
)

func runFollow(capturePath, weightsPath, basketsCfg, profilesDir string, refresh time.Duration) error {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	day := filepath.Base(filepath.Dir(capturePath))
	v := &viewer{refresh: refresh,
		header: fmt.Sprintf("FOLLOW %s", day) + "  (Ctrl-C to stop; exits when the session manifest appears)"}
	f, err := optfollow.Start(optfollow.Config{
		CapturePath: capturePath, WeightsPath: weightsPath, BasketsCfg: basketsCfg, ProfilesDir: profilesDir,
		// Renders happen from the Tick callback AFTER draining, so reads
		// never race writes: drain-then-render, as before.
		Tick:    func(int64) { v.render(false) },
		Waiting: func(path string) { fmt.Printf("waiting for %s to appear...\n", path) },
		// First frame immediately: the served page must not show yesterday's
		// last table marked stale while the market is still closed.
		Opened: func() {
			fmt.Print("\033[H\033[2J")
			fmt.Printf("FOLLOW %s — connected to the capture; the options tape prints from 09:30 ET. Table appears with the first print.\n", day)
		},
	})
	if err != nil {
		return err
	}
	v.store, v.p, v.stats, v.bks, v.base = f.Store, f.Pipeline, f.Stats, f.Baskets, f.Base
	go func() { <-stop; f.Signal() }()
	<-f.Done()
	if !f.Opened() {
		return nil // stopped while still waiting for the file
	}
	v.render(true)
	fmt.Printf("\nfollow ended: frames=%d prints=%d dupes=%d decode-errs=%d\n", f.Stats.Frames, f.Stats.Prints, f.Pipeline.Dupes.Load(), f.Stats.DecodeErrs)
	return f.Err()
}

// viewer renders the basket table on an event-time cadence. Drain-then-
// render is the whole synchronization story: the consumer is allowed to
// catch up with everything submitted, then the store is read on this
// goroutine while the producer is parked in this very call.
type viewer struct {
	store      *optbucket.Store
	p          *optingest.Pipeline
	stats      *uwfeed.DecodeStats
	bks        []universe.Basket
	base       *conviction.Baselines
	refresh    time.Duration
	header     string
	lastRender int64
	date       string
}

func (v *viewer) render(final bool) {
	maxSec := v.store.MaxSec.Load()
	if maxSec == 0 {
		return
	}
	if !final && maxSec-v.lastRender < int64(v.refresh/time.Second) {
		return
	}
	for v.p.Processed.Load()+v.p.Dupes.Load() < v.stats.Prints {
		time.Sleep(2 * time.Millisecond)
	}
	maxSec = v.store.MaxSec.Load() // may have advanced during the drain
	if v.date == "" {
		v.date = time.Unix(maxSec, 0).In(session.ET()).Format("2006-01-02")
	}
	v.lastRender = maxSec
	fmt.Print("\033[H\033[2J")
	fmt.Printf("%s  frames=%d prints=%d decode-errs=%d\n", v.header, v.stats.Frames, v.stats.Prints, v.stats.DecodeErrs)
	if err := renderSnapshot(v.store, v.bks, v.base, v.date, maxSec+1); err != nil {
		fmt.Println("render:", err)
	}
}
