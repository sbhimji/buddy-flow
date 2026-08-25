// capture.go — replay a captured options session through the pipeline.
// Feeds each recorded frame through DecodeFrame, the SAME function the live
// Frame hook uses, so replay exercises the production decoder (mini-spec
// 7.2, the 1.2/1.3 determinism contract). Pacing is the equity replayer's
// absolute-schedule algorithm, copied with its measured lessons.
package uwfeed

import (
	"fmt"
	"io"
	"time"

	"buddy-flow/internal/capture"
	"buddy-flow/internal/optingest"
)

// maxReplaySleep caps any single pacing sleep (the 1.3 rule): captures
// legitimately contain long dead gaps that would otherwise stall a paced
// replay for minutes with no sign of life.
const maxReplaySleep = 30 * time.Second

// ReplayOptions controls capture replay pacing — semantics identical to
// feed.ReplayOptions (Speed 0 instant; PaceFrom fast-forwards then paces;
// pacing changes delivery timing only, never order or content).
type ReplayOptions struct {
	Speed    float64
	PaceFrom string // ET "15:04:05"; ignored when Speed == 0
	Log      func(format string, args ...any)
	// Tick, when set, is called after every frame with its RecvNs — the
	// paced-view render seam (cmd/replay-options -view).
	Tick func(recvNs int64)
}

// StreamCapture replays one options capture file into the pipeline.
func StreamCapture(path string, p *optingest.Pipeline, stats *DecodeStats, opt ReplayOptions) error {
	r, err := capture.OpenReader(path)
	if err != nil {
		return err
	}
	defer r.Close()

	// Absolute-schedule pacing: each frame targets anchorWall + Δrecv/speed,
	// so per-sleep overshoot self-corrects instead of accumulating (the
	// 2026-08-13 drift measurement behind the equity implementation).
	var anchorWall time.Time
	var anchorRecv int64
	paceFromNs := int64(-1) // resolved from the first record's date
	for {
		rec, err := r.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if opt.Speed > 0 && paceFromNs == -1 {
			paceFromNs = 0
			if opt.PaceFrom != "" {
				loc, lerr := time.LoadLocation("America/New_York")
				if lerr != nil {
					return lerr
				}
				day := time.Unix(0, rec.RecvNs).In(loc).Format("2006-01-02")
				t, perr := time.ParseInLocation("2006-01-02 15:04:05", day+" "+opt.PaceFrom, loc)
				if perr != nil {
					return fmt.Errorf("bad PaceFrom: %w", perr)
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
		DecodeFrame(rec.Frame, p, stats)
		if opt.Tick != nil {
			opt.Tick(rec.RecvNs)
		}
	}
}
