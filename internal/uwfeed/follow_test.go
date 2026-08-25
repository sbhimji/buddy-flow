package uwfeed

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"buddy-flow/internal/optingest"
)

// A torn tail must be retried, not skipped: write half a line, expect EOF
// with nothing delivered; complete it, expect exactly that record.
func TestFollowReaderTornTailRetried(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stream.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, err := OpenFollowReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	full := "1 " + realAck + "\n"
	half := full[:len(full)/2]
	f.WriteString(half)
	if _, err := r.Next(); err != io.EOF {
		t.Fatalf("torn tail: err = %v, want EOF (wait)", err)
	}
	f.WriteString(full[len(half):])
	rec, err := r.Next()
	if err != nil || rec.RecvNs != 1 || string(rec.Frame) != realAck {
		t.Fatalf("completed line: rec=%+v err=%v", rec, err)
	}
	if _, err := r.Next(); err != io.EOF {
		t.Fatal("expected EOF after the only record")
	}
	// A second, complete record plus a fresh torn tail: exactly one delivered.
	f.WriteString("2 " + realDataFrame + "\n3 [\"opt")
	rec, err = r.Next()
	if err != nil || rec.RecvNs != 2 {
		t.Fatalf("second record: rec=%+v err=%v", rec, err)
	}
	if _, err := r.Next(); err != io.EOF {
		t.Fatal("torn third line must wait")
	}
}

func TestFollowReaderSkipsMalformedCompleteLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stream.jsonl")
	os.WriteFile(path, []byte("\nnotanumber "+realAck+"\n4 {broken\n5 "+realAck+"\n"), 0o644)
	r, _ := OpenFollowReader(path)
	defer r.Close()
	rec, err := r.Next()
	if err != nil || rec.RecvNs != 5 {
		t.Fatalf("rec=%+v err=%v, want the one well-formed record (5)", rec, err)
	}
}

// FollowCapture over a file that grows between polls delivers every frame
// exactly once through DecodeFrame and stops on Done.
func TestFollowCaptureGrowsAndStops(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stream.jsonl")
	f, _ := os.Create(path)
	defer f.Close()
	f.WriteString("1 " + realDataFrame + "\n")

	p := runPipelineAsync(t)
	var stats DecodeStats
	ticks := 0
	err := FollowCapture(path, p.p, &stats, FollowOptions{
		Poll: 5_000_000, // 5ms
		Tick: func(latest int64) {
			ticks++
			if ticks == 2 { // after the first EOF, grow the file
				f.WriteString("2 " + realAck + "\n3 " + realDataFrame + "\n")
			}
		},
		Done: func() bool { return stats.Frames >= 3 },
	})
	if err != nil {
		t.Fatal(err)
	}
	p.close()
	if stats.Frames != 3 || stats.Prints != 2 || stats.Acks != 1 {
		t.Errorf("stats = %+v", stats)
	}
	if len(p.obs.got) != 1 || p.p.Dupes.Load() != 1 { // same print id twice → dedupe
		t.Errorf("observed %d, dupes %d", len(p.obs.got), p.p.Dupes.Load())
	}
}

type asyncPipeline struct {
	p    *optingest.Pipeline
	obs  *capturingObserver
	done chan struct{}
}

func runPipelineAsync(t *testing.T) *asyncPipeline {
	t.Helper()
	p := optingest.NewPipeline(64)
	obs := &capturingObserver{}
	p.SetObserver(obs)
	done := make(chan struct{})
	go func() { p.Run(); close(done) }()
	return &asyncPipeline{p: p, obs: obs, done: done}
}

func (a *asyncPipeline) close() { a.p.Close(); <-a.done }
