// follow.go — the live dashboard as a separate read-only process (the
// backlog's "trader view as a separate process"): tail the capture file the
// feeder is writing, decode through the production path, and re-render the
// basket table on an event-time cadence. Single goroutine end to end — the
// follow loop renders inline, so no store locking is needed. Kill and
// restart freely; the capture process never knows.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"buddy-flow/internal/conviction"
	"buddy-flow/internal/optbucket"
	"buddy-flow/internal/optclassify"
	"buddy-flow/internal/optingest"
	"buddy-flow/internal/session"
	"buddy-flow/internal/universe"
	"buddy-flow/internal/uwfeed"
)

func runFollow(capturePath, weightsPath, basketsCfg, profilesDir string, refresh time.Duration) error {
	w, hash, err := optclassify.LoadWeights(weightsPath)
	if err != nil {
		return err
	}
	stamp := w.Version + "@" + hash
	bks, err := universe.LoadBaskets(basketsCfg)
	if err != nil {
		return err
	}
	var base *conviction.Baselines
	if profilesDir != "" {
		if base, err = conviction.Load(profilesDir, bks, stamp); err != nil {
			return err
		}
	}
	store, err := optbucket.NewStore(w, stamp)
	if err != nil {
		return err
	}
	// The pipeline's consumer is the follow goroutine's only peer; renders
	// happen from the Tick callback AFTER draining, so reads never race
	// writes: drain-then-render is the whole synchronization story.
	p := optingest.NewPipeline(0)
	p.SetObserver(store)
	pipeDone := make(chan struct{})
	go func() { p.Run(); close(pipeDone) }()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	manifest := filepath.Join(filepath.Dir(capturePath), "manifest.json")
	sessionOver := func() bool {
		_, err := os.Stat(manifest)
		return err == nil
	}
	var stopped atomic.Bool
	go func() { <-stop; stopped.Store(true) }()

	var stats uwfeed.DecodeStats
	v := &viewer{store: store, p: p, stats: &stats, bks: bks, base: base, refresh: refresh,
		header: fmt.Sprintf("FOLLOW %s", filepath.Base(filepath.Dir(capturePath))) + "  (Ctrl-C to stop; exits when the session manifest appears)"}
	// A scheduled follower may start before the feeder has created today's
	// file; wait for it rather than failing the day's dashboard.
	for {
		if _, err := os.Stat(capturePath); err == nil || stopped.Load() {
			break
		}
		fmt.Printf("waiting for %s to appear...\n", capturePath)
		time.Sleep(15 * time.Second)
	}
	if stopped.Load() {
		return nil
	}
	// First frame immediately: the served page must not show yesterday's
	// last table marked stale while the market is still closed.
	fmt.Print("\033[H\033[2J")
	fmt.Printf("FOLLOW %s — connected to the capture; the options tape prints from 09:30 ET. Table appears with the first print.\n",
		filepath.Base(filepath.Dir(capturePath)))
	err = uwfeed.FollowCapture(capturePath, p, &stats, uwfeed.FollowOptions{
		Poll: 250 * time.Millisecond,
		Tick: func(int64) { v.render(false) },
		Done: func() bool { return stopped.Load() || sessionOver() },
	})
	p.Close()
	<-pipeDone
	v.render(true)
	fmt.Printf("\nfollow ended: frames=%d prints=%d dupes=%d decode-errs=%d\n", stats.Frames, stats.Prints, p.Dupes.Load(), stats.DecodeErrs)
	return err
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
