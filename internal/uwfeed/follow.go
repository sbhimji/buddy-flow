// follow.go — follow-mode replay (the backlog's "trader view as a separate
// process"): tail a capture file while the feeder is still writing it. A
// read-only process; the capture never knows it exists. Unlike the
// finished-file reader, a torn final line is "not written yet" — retried,
// never skipped — so the view sees every frame exactly once, in order.
package uwfeed

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"time"

	"buddy-flow/internal/capture"
	"buddy-flow/internal/optingest"
)

// FollowReader reads complete lines from a growing file. Not gzip-aware:
// a file being written is never compressed.
type FollowReader struct {
	f      *os.File
	buf    []byte // unconsumed bytes (a partial line at the end)
	chunk  []byte
	offset int64
}

// OpenFollowReader opens the stream for tailing.
func OpenFollowReader(path string) (*FollowReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &FollowReader{f: f, chunk: make([]byte, 1<<20)}, nil
}

// Next returns the next COMPLETE record. io.EOF means "nothing complete yet"
// — the caller polls; a torn tail stays buffered until its newline arrives.
// Malformed complete lines (blank, bad prefix, invalid JSON) are skipped,
// matching capture.Reader's tolerance for lines that ARE terminated.
func (r *FollowReader) Next() (capture.Record, error) {
	for {
		if nl := bytes.IndexByte(r.buf, '\n'); nl >= 0 {
			line := r.buf[:nl]
			r.buf = r.buf[nl+1:]
			if rec, ok := parseLine(line); ok {
				return rec, nil
			}
			continue
		}
		n, err := r.f.Read(r.chunk)
		if n > 0 {
			r.buf = append(r.buf, r.chunk[:n]...)
			r.offset += int64(n)
			continue
		}
		if err == io.EOF || err == nil {
			return capture.Record{}, io.EOF
		}
		return capture.Record{}, err
	}
}

func parseLine(line []byte) (capture.Record, bool) {
	sp := bytes.IndexByte(line, ' ')
	if sp <= 0 {
		return capture.Record{}, false
	}
	ns, err := strconv.ParseInt(string(line[:sp]), 10, 64)
	if err != nil {
		return capture.Record{}, false
	}
	frame := line[sp+1:]
	if !json.Valid(frame) {
		return capture.Record{}, false
	}
	return capture.Record{RecvNs: ns, Frame: frame}, true
}

// Close closes the file.
func (r *FollowReader) Close() error { return r.f.Close() }

// FollowOptions controls a follow session.
type FollowOptions struct {
	Poll time.Duration // how long to wait at EOF before re-reading (default 250ms)
	// Tick is called after each batch of records (at least every Poll) with
	// the latest record's RecvNs (0 if none yet) — the render seam.
	Tick func(latestRecvNs int64)
	// Done, when it returns true, ends the follow (session over, Ctrl-C).
	Done func() bool
}

// FollowCapture tails path into the pipeline through DecodeFrame — the
// same decoder as live and finished-file replay — until opt.Done.
func FollowCapture(path string, p *optingest.Pipeline, stats *DecodeStats, opt FollowOptions) error {
	if opt.Poll <= 0 {
		opt.Poll = 250 * time.Millisecond
	}
	r, err := OpenFollowReader(path)
	if err != nil {
		return err
	}
	defer r.Close()
	var latest int64
	for {
		progressed := false
		for {
			rec, err := r.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			DecodeFrame(rec.Frame, p, stats)
			latest = rec.RecvNs
			progressed = true
		}
		if opt.Tick != nil {
			opt.Tick(latest)
		}
		if opt.Done != nil && opt.Done() {
			return nil
		}
		if !progressed {
			time.Sleep(opt.Poll)
		}
	}
}
