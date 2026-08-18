// capture.go — replay a captured live session through the pipeline.
// Feeds each recorded frame through decodeFrame, the SAME function the live
// socket uses, so replay exercises the production decoder (mini-spec 1.2
// done-when #2: same capture in → identical counts and NBBO out).
package feed

import (
	"fmt"
	"io"
	"time"

	"buddy-flow/internal/capture"
	"buddy-flow/internal/ingest"
)

// maxReplaySleep caps any single pacing sleep (mini-spec 1.3): captures
// legitimately contain long dead gaps (a kill-drill hole, quiet after-hours)
// that would otherwise stall a paced replay for minutes with no sign of life.
const maxReplaySleep = 30 * time.Second

// ReplayOptions controls capture replay pacing.
type ReplayOptions struct {
	// Speed 0 = instant (as fast as disk+CPU allow). Speed 1 = real-time:
	// frames delivered at recorded arrival spacing (recv_ns deltas). Speed
	// N > 1 = accelerated, gaps divided by N. Pacing changes delivery timing
	// only — order and content are identical at every speed.
	Speed float64
	// PaceFrom, when non-empty (ET "15:04:05"), replays every frame before
	// that wall-clock time instantly and starts Speed pacing there — the
	// fast-forward-to-the-open control for full-day captures. Everything is
	// still INGESTED (premarket quotes build NBBO state, the auction lands);
	// only delivery timing changes, so metrics are identical with or
	// without it. Ignored when Speed == 0. The date comes from the first
	// record's arrival timestamp.
	PaceFrom string
	// Log, when set, receives pacing notices (capped long gaps).
	Log func(format string, args ...any)
}

// StreamCapture replays one capture file into the pipeline.
func StreamCapture(path string, p *ingest.Pipeline, opt ReplayOptions) (LiveStats, error) {
	var stats LiveStats
	r, err := capture.OpenReader(path)
	if err != nil {
		return stats, err
	}
	defer r.Close()

	table := p.Table()
	// Absolute-schedule pacing: each frame targets anchorWall + Δrecv/speed,
	// so per-sleep overshoot self-corrects instead of accumulating (a
	// per-delta sleep drifted ~19% on a dense slice — measured 2026-08-13).
	var anchorWall time.Time
	var anchorRecv int64
	paceFromNs := int64(-1) // resolved from the first record's date
	for {
		rec, err := r.Next()
		if err == io.EOF {
			return stats, nil
		}
		if err != nil {
			return stats, err
		}
		if opt.Speed > 0 && paceFromNs == -1 {
			paceFromNs = 0
			if opt.PaceFrom != "" {
				loc, lerr := time.LoadLocation("America/New_York")
				if lerr != nil {
					return stats, lerr
				}
				day := time.Unix(0, rec.RecvNs).In(loc).Format("2006-01-02")
				t, perr := time.ParseInLocation("2006-01-02 15:04:05", day+" "+opt.PaceFrom, loc)
				if perr != nil {
					return stats, fmt.Errorf("bad PaceFrom: %w", perr)
				}
				paceFromNs = t.UnixNano()
				if opt.Log != nil {
					opt.Log("replaying instantly until %s ET, then pacing at %gx", opt.PaceFrom, opt.Speed)
				}
			}
		}
		if opt.Speed > 0 && rec.RecvNs >= paceFromNs {
			if anchorRecv == 0 {
				anchorWall, anchorRecv = time.Now(), rec.RecvNs
			}
			target := anchorWall.Add(time.Duration(float64(rec.RecvNs-anchorRecv) / opt.Speed))
			sleep := time.Until(target)
			if sleep > maxReplaySleep {
				if opt.Log != nil {
					opt.Log("capping %s recorded gap at %s", sleep.Round(time.Second), maxReplaySleep)
				}
				// Pull the anchor back so the skipped part of the gap stays
				// skipped — future targets shift earlier by the same amount.
				anchorWall = anchorWall.Add(-(sleep - maxReplaySleep))
				sleep = maxReplaySleep
			}
			if sleep > 0 {
				time.Sleep(sleep)
			}
		}
		stats.Frames++
		decodeFrame(rec.Frame, table, p, &stats)
	}
}
