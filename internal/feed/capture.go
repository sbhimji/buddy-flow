// capture.go — replay a captured live session through the pipeline.
// Feeds each recorded frame through decodeFrame, the SAME function the live
// socket uses, so replay exercises the production decoder (mini-spec 1.2
// done-when #2: same capture in → identical counts and NBBO out).
package feed

import (
	"io"

	"buddy-flow/internal/capture"
	"buddy-flow/internal/ingest"
)

// StreamCapture replays one capture file into the pipeline at full speed.
// (Speed control — real-time and scaled replay — is story 1.3.)
func StreamCapture(path string, p *ingest.Pipeline) (LiveStats, error) {
	var stats LiveStats
	r, err := capture.OpenReader(path)
	if err != nil {
		return stats, err
	}
	defer r.Close()

	table := p.Table()
	for {
		rec, err := r.Next()
		if err == io.EOF {
			return stats, nil
		}
		if err != nil {
			return stats, err
		}
		stats.Frames++
		decodeFrame(rec.Frame, table, p, &stats)
	}
}
