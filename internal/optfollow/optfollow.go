// Package optfollow is the shared read-only options follower (mini-spec
// MO-4): the one piece of code that turns the options capture file the
// feeder is writing into a live optbucket.Store, for any process that
// wants to read it — cmd/replay-options -follow today, cmd/live's equity
// screen next (MO-5). A follower only ever opens the capture read-only;
// killing it never touches cmd/live-options (F5).
//
// Lifecycle: Start loads weights (+stamp), baskets and optional profiles,
// builds the store and pipeline, then on its own goroutine waits for the
// capture file to appear (15 s poll), tails it through the production
// decoder, and ends when the session manifest lands or Stop is called.
// Done closes after the pipeline has drained, so every print the follower
// read is in the store by then.
package optfollow

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"buddy-flow/internal/conviction"
	"buddy-flow/internal/optbucket"
	"buddy-flow/internal/optclassify"
	"buddy-flow/internal/optingest"
	"buddy-flow/internal/universe"
	"buddy-flow/internal/uwfeed"
)

// Defaults — the 7.7 follow mode's numbers, unchanged (F1).
const (
	DefaultPoll     = 250 * time.Millisecond // tail poll at EOF
	DefaultWaitPoll = 15 * time.Second       // wait-for-file poll
	ManifestName    = "manifest.json"        // in the capture's directory; its arrival ends the session
)

// Config is what Start needs.
type Config struct {
	CapturePath string // stream.jsonl being written (or finished)
	WeightsPath string // 7.3 conviction weights config
	BasketsCfg  string // trader-owned basket config
	ProfilesDir string // 7.6 options profiles; "" = no baselines

	Poll     time.Duration // 0 = DefaultPoll
	WaitPoll time.Duration // 0 = DefaultWaitPoll

	// Tick is the render seam: called on the follow goroutine after each
	// batch of records (at least every Poll) with the latest record's
	// RecvNs (0 if none yet). The pipeline may still be draining that
	// batch; a renderer that wants everything read so far waits on
	// Follower.Drained (or reads under the store lock and accepts the
	// in-flight tail).
	Tick func(latestRecvNs int64)
	// Waiting is called once per WaitPoll while the capture file does not
	// exist yet (a scheduled follower may start before the feeder).
	Waiting func(path string)
	// Opened is called once, when the capture file exists, before tailing
	// begins — the first-frame hook.
	Opened func()
}

// Follower is a running follow session. Store, Base and Stamp are fixed
// at Start; Progress and Clock are atomic snapshots safe from any
// goroutine.
type Follower struct {
	Store   *optbucket.Store
	Base    *conviction.Baselines // nil without ProfilesDir
	Baskets []universe.Basket
	Stamp   string // "<version>@<hash>", the weights stamp

	stats    uwfeed.DecodeStats
	pipeline *optingest.Pipeline
	clock    atomic.Int64 // latest RecvNs seen by Tick

	capturePath string
	manifest    string
	cfg         Config

	stopOnce sync.Once
	stop     chan struct{}
	stopped  atomic.Bool
	opened   atomic.Bool
	done     chan struct{}
	err      error
}

// Start loads the configuration, builds the store and pipeline, and
// begins following on its own goroutine. Configuration errors are
// returned here; follow errors surface through Err after Done.
func Start(cfg Config) (*Follower, error) {
	w, hash, err := optclassify.LoadWeights(cfg.WeightsPath)
	if err != nil {
		return nil, err
	}
	stamp := w.Version + "@" + hash
	bks, err := universe.LoadBaskets(cfg.BasketsCfg)
	if err != nil {
		return nil, err
	}
	var base *conviction.Baselines
	if cfg.ProfilesDir != "" {
		if base, err = conviction.Load(cfg.ProfilesDir, bks, stamp); err != nil {
			return nil, err
		}
	}
	store, err := optbucket.NewStore(w, stamp)
	if err != nil {
		return nil, err
	}
	if cfg.Poll <= 0 {
		cfg.Poll = DefaultPoll
	}
	if cfg.WaitPoll <= 0 {
		cfg.WaitPoll = DefaultWaitPoll
	}
	f := &Follower{
		Store: store, Base: base, Baskets: bks, Stamp: stamp,
		pipeline:    optingest.NewPipeline(0),
		capturePath: cfg.CapturePath,
		manifest:    filepath.Join(filepath.Dir(cfg.CapturePath), ManifestName),
		cfg:         cfg,
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}
	f.pipeline.SetObserver(store) // before Run (pipeline contract)
	go f.run()
	return f, nil
}

func (f *Follower) run() {
	defer close(f.done)
	pipeDone := make(chan struct{})
	go func() { f.pipeline.Run(); close(pipeDone) }()
	// Close the pipeline and drain it however we leave, so Done means
	// "every print read is in the store".
	defer func() {
		f.pipeline.Close()
		<-pipeDone
	}()

	// A scheduled follower may start before the feeder has created
	// today's file; wait for it rather than failing the day's dashboard.
	for {
		if _, err := os.Stat(f.capturePath); err == nil || f.stopped.Load() {
			break
		}
		if f.cfg.Waiting != nil {
			f.cfg.Waiting(f.capturePath)
		}
		select {
		case <-f.stop:
		case <-time.After(f.cfg.WaitPoll):
		}
	}
	if f.stopped.Load() {
		return
	}
	f.opened.Store(true)
	if f.cfg.Opened != nil {
		f.cfg.Opened()
	}
	f.err = uwfeed.FollowCapture(f.capturePath, f.pipeline, &f.stats, uwfeed.FollowOptions{
		Poll: f.cfg.Poll,
		Tick: func(latestRecvNs int64) {
			if latestRecvNs > f.clock.Load() {
				f.clock.Store(latestRecvNs)
			}
			if f.cfg.Tick != nil {
				f.cfg.Tick(latestRecvNs)
			}
		},
		Done: func() bool { return f.stopped.Load() || f.sessionOver() },
	})
}

func (f *Follower) sessionOver() bool {
	_, err := os.Stat(f.manifest)
	return err == nil
}

// Stop ends the follow (idempotent), closes the pipeline and waits for
// the follow goroutine to finish draining.
func (f *Follower) Stop() {
	f.Signal()
	<-f.done
}

// Signal requests a stop without waiting — for a signal handler that
// wants the caller's own Done wait to observe the end.
func (f *Follower) Signal() {
	f.stopOnce.Do(func() {
		f.stopped.Store(true)
		close(f.stop)
	})
}

// Done closes when the follow has ended — session manifest seen, Stop
// called, or a tail error — and the pipeline has drained.
func (f *Follower) Done() <-chan struct{} { return f.done }

// Err is the tail error (nil on a clean end). Blocks until Done.
func (f *Follower) Err() error {
	<-f.done
	return f.err
}

// Opened reports whether the capture file was ever opened (false when
// stopped while still waiting for it).
func (f *Follower) Opened() bool { return f.opened.Load() }

// Progress is the decode/pipeline telemetry (atomic snapshot): frames
// and prints seen by the decoder, dupes dropped, decode errors.
func (f *Follower) Progress() (frames, prints, dupes, decodeErrs int64) {
	return f.stats.Frames.Load(), f.stats.Prints.Load(), f.pipeline.Dupes.Load(), f.stats.DecodeErrs.Load()
}

// Clock is the latest capture RecvNs seen by Tick — the follower's
// notion of "how far the tape has been read". Heartbeat/ack records
// advance it through a print-silent minute; 0 before the first record.
func (f *Follower) Clock() int64 { return f.clock.Load() }

// Drained blocks until the pipeline has consumed every print the decoder
// has submitted so far — drain-then-render is the 7.7 synchronization
// story and it still holds for a Tick-driven renderer; the store lock
// covers readers who do not wait.
func (f *Follower) Drained() {
	for f.pipeline.Processed.Load()+f.pipeline.Dupes.Load() < f.stats.Prints.Load() {
		time.Sleep(2 * time.Millisecond)
	}
}

// Minute is the per-ticker minute reader tickerview.LoadOptions takes
// (F3): the store summed over [minuteSec, minuteSec+60) under the read
// lock, net_notional derived from the slices (7.4). ok=false — never a
// fabricated zero — when the minute is unaligned, the capture has not
// been opened, or the tape has not been read past the minute's end
// (Clock < minuteSec+60, RecvNs-based).
func (f *Follower) Minute(sym string, minuteSec int64) (netConv, netNotional float64, ok bool) {
	b, ok := f.MinuteBucket(sym, minuteSec)
	if !ok {
		return 0, 0, false
	}
	return b.NetConviction, b.SignedNotional(), true
}

// MinuteBucket is the same read as a bucket — the conviction.MinuteBuckets
// shape, for BasketMinute — with the same ok gate.
func (f *Follower) MinuteBucket(sym string, minuteSec int64) (optbucket.Bucket, bool) {
	if minuteSec%60 != 0 || !f.Opened() || f.Clock() < (minuteSec+60)*1_000_000_000 {
		return optbucket.Bucket{}, false
	}
	return f.Store.Window(sym, minuteSec, minuteSec+60), true
}
