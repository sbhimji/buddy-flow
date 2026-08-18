// Package capture owns the on-disk session record (mini-spec 1.2).
//
// Format: one line per websocket frame, "<receive_ns> <raw frame>\n", plain
// text during the session (live compression would lose its buffer on a
// crash; compress after close). Everything on the wire is recorded — data,
// status, auth frames — plus synthetic control records marking connects,
// disconnects and shutdowns, so a replay sees gaps explicitly.
package capture

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// flushEvery bounds how much capture can sit in the write buffer: at most
// this much wall time between flushes to the OS (crash of the process loses
// nothing flushed; only power loss can cost the tail).
const flushEvery = 500 * time.Millisecond

// Writer appends one session's frames. The feeder's read loop is the only
// intended writer, but every method is mutex-guarded anyway — an interleaved
// write from a second goroutine would corrupt a line in place, and the
// reader's torn-line tolerance would then silently skip real data (2026-08-13
// review #6). Frames/Bytes are atomics so the status ticker can read them
// without racing the writer (#9).
type Writer struct {
	dir       string
	f         *os.File
	bw        *bufio.Writer
	mu        sync.Mutex // guards f/bw/lastFlush; counters are atomic
	lastFlush time.Time
	started   time.Time

	priorBytes int64 // stream.jsonl size found at open (resumed session)
	resumed    bool

	Frames atomic.Int64 // this process's appended frames
	Bytes  atomic.Int64 // this process's appended bytes (exact, incl. framing)
}

// ErrExists means a non-empty capture stream already exists for the date
// and the caller did not consent to resuming it. The capture is the
// canonical, irreplaceable record: appending must be a stated intent
// (an accidental smoke-test resume polluted a real session on 2026-08-17),
// so the refusal lives here — decided under the writer's own lock, not by
// a caller's separate racy stat.
var ErrExists = errors.New("capture stream already exists (resume not requested)")

// ErrLocked means another live process holds this date's stream right now.
// Two concurrent appenders would interleave mid-line and corrupt the
// record; the advisory flock held for the writer's lifetime makes the
// one-instance-per-session-date rule mechanical instead of procedural.
var ErrLocked = errors.New("capture stream is locked by another process")

// NewWriter opens (creating or appending) data/capture/<date>/stream.jsonl.
// Append mode is what makes a restart after a kill continue the same session
// file rather than truncating it (done-when #3) — but resuming requires
// resume=true; a pre-existing non-empty stream otherwise returns ErrExists.
// A resumed stream marks the session resumed; the manifest carries that
// provenance so this-run counts are never mistaken for whole-file counts
// (review #3). The prior-size decision is made AFTER taking an exclusive
// flock on the opened file, so there is no stat-then-open window for a
// second instance to slip through; the lock is held until Close, so a
// concurrent second instance fails with ErrLocked no matter its flags.
// (A refused open may leave behind an empty stream.jsonl from O_CREATE —
// harmless: empty means not resumed.)
func NewWriter(baseDir, date string, resume bool) (*Writer, error) {
	dir := filepath.Join(baseDir, date)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "stream.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("%w: %s", ErrLocked, path)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close() // never proceed on an unverifiable prior size
		return nil, err
	}
	prior := fi.Size()
	if prior > 0 && !resume {
		f.Close()
		return nil, fmt.Errorf("%w: %s (%d bytes)", ErrExists, path, prior)
	}
	return &Writer{
		dir: dir, f: f, bw: bufio.NewWriterSize(f, 1<<20),
		started: time.Now(), priorBytes: prior, resumed: prior > 0,
	}, nil
}

// appendLocked writes one record; caller holds w.mu. Returns bytes written.
func (w *Writer) appendLocked(recvNs int64, frame []byte) (int, error) {
	var buf [20]byte
	prefix := strconv.AppendInt(buf[:0], recvNs, 10)
	if _, err := w.bw.Write(prefix); err != nil {
		return 0, err
	}
	if err := w.bw.WriteByte(' '); err != nil {
		return 0, err
	}
	if _, err := w.bw.Write(frame); err != nil {
		return 0, err
	}
	if err := w.bw.WriteByte('\n'); err != nil {
		return 0, err
	}
	return len(prefix) + 1 + len(frame) + 1, nil
}

// Append records one raw frame with its receive timestamp.
func (w *Writer) Append(recvNs int64, frame []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.appendLocked(recvNs, frame)
	if err != nil {
		return err
	}
	w.Frames.Add(1)
	w.Bytes.Add(int64(n))
	if now := time.Now(); now.Sub(w.lastFlush) > flushEvery {
		w.lastFlush = now
		return w.bw.Flush()
	}
	return nil
}

// Control writes a synthetic capture-lifecycle record in the same line
// format, so gaps and restarts are explicit in-stream events.
func (w *Writer) Control(event, detail string) error {
	frame, _ := json.Marshal([]map[string]string{{
		"ev":     "_capture",
		"event":  event,
		"detail": detail,
	}})
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.appendLocked(time.Now().UnixNano(), frame)
	if err != nil {
		return err
	}
	w.Frames.Add(1)
	w.Bytes.Add(int64(n))
	return w.bw.Flush() // control records are rare and load-bearing: flush now
}

// Manifest is written on clean close; its absence after a session is the
// integrity signal that the capture ended ungracefully (1.5). This-run
// counts and whole-stream size are reported separately: after a kill+restart
// they legitimately differ, and conflating them either flags a healthy
// restart as corrupt or masks real truncation (review #3). The 1.5 integrity
// check should compare stream_bytes_total against the file on disk.
type Manifest struct {
	Date             string   `json:"date"`
	StartedNs        int64    `json:"started_ns"` // this process
	EndedNs          int64    `json:"ended_ns"`
	FramesThisRun    int64    `json:"frames_this_run"`
	BytesThisRun     int64    `json:"bytes_this_run"`
	PriorStreamBytes int64    `json:"prior_stream_bytes"`
	StreamBytesTotal int64    `json:"stream_bytes_total"` // stat at close, exact
	Resumed          bool     `json:"resumed"`
	UniverseSize     int      `json:"universe_size"`
	Subscriptions    []string `json:"subscriptions"`
	Note             string   `json:"note,omitempty"`
}

// Close flushes, fsyncs, and writes the manifest.
func (w *Writer) Close(m Manifest) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	m.StartedNs = w.started.UnixNano()
	m.EndedNs = time.Now().UnixNano()
	m.FramesThisRun = w.Frames.Load()
	m.BytesThisRun = w.Bytes.Load()
	m.PriorStreamBytes = w.priorBytes
	m.Resumed = w.resumed
	if err := w.bw.Flush(); err != nil {
		return err
	}
	if err := w.f.Sync(); err != nil {
		return err
	}
	if fi, err := w.f.Stat(); err == nil {
		m.StreamBytesTotal = fi.Size()
	}
	if err := w.f.Close(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(w.dir, "manifest.json"), raw, 0o644)
}

// Record is one line of a capture file.
type Record struct {
	RecvNs int64
	Frame  []byte // valid until the next Next() call
}

// Reader streams a capture file (.jsonl or .jsonl.gz). A torn final line
// (power loss mid-append) yields io.EOF, not an error — one frame lost,
// never the session.
type Reader struct {
	f   *os.File
	sc  *bufio.Scanner
	rec Record
}

// OpenReader opens a capture stream file.
func OpenReader(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	var r io.Reader = f
	if filepath.Ext(path) == ".gz" {
		gz, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		r = gz
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<24) // frames can be large event arrays
	return &Reader{f: f, sc: sc}, nil
}

// Next returns the next record, or io.EOF. The returned frame is only valid
// until the following Next call (scanner buffer reuse — same contract as
// csv.Reader.ReuseRecord).
func (r *Reader) Next() (Record, error) {
	for r.sc.Scan() {
		line := r.sc.Bytes()
		sp := bytes.IndexByte(line, ' ')
		if sp <= 0 {
			continue // torn or blank line: tolerate, skip
		}
		ns, err := strconv.ParseInt(string(line[:sp]), 10, 64)
		if err != nil {
			continue // torn prefix: tolerate
		}
		frame := line[sp+1:]
		if !json.Valid(frame) {
			continue // torn tail: a truncated frame is not an error
		}
		r.rec = Record{RecvNs: ns, Frame: frame}
		return r.rec, nil
	}
	if err := r.sc.Err(); err != nil {
		return Record{}, err
	}
	return Record{}, io.EOF
}

// Close closes the underlying file.
func (r *Reader) Close() error { return r.f.Close() }

// StreamPath returns the conventional stream file path for a session dir.
func StreamPath(baseDir, date string) string {
	return filepath.Join(baseDir, date, "stream.jsonl")
}
