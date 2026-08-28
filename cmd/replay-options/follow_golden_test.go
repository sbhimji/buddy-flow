package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// MO-4 F1, asserted: runFollow on a finished capture must produce the
// bytes the pre-extraction binary produced. The golden was generated from
// the BASE binary (0885426) in follow mode WITHOUT profiles (sums only —
// stable across nightly profile rolls) on the 08-24 capture cut at 10:36
// ET RecvNs, manifest alongside. Gated:
//
//	MO4_CAPTURE=<dir named 2026-08-24 with stream.jsonl + manifest.json> \
//	  go test ./cmd/replay-options -run Golden -v
//
// The golden file is testdata/follow-<capture dir basename>.golden.
func TestFollowGolden(t *testing.T) {
	capDir := os.Getenv("MO4_CAPTURE")
	if capDir == "" {
		t.Skip("set MO4_CAPTURE to a finished capture directory")
	}
	golden := filepath.Join("testdata", "follow-"+filepath.Base(capDir)+".golden")
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}

	// Capture stdout: runFollow renders with fmt.Print*.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	var got bytes.Buffer
	copied := make(chan struct{})
	go func() { io.Copy(&got, r); close(copied) }()

	runErr := runFollow(filepath.Join(capDir, "stream.jsonl"),
		"../../docs/foundations/options-weights-v1.json",
		"../../docs/foundations/morning-tape-baskets-v2.json",
		"", 5*time.Second)
	w.Close()
	os.Stdout = saved
	<-copied
	if runErr != nil {
		t.Fatalf("runFollow: %v", runErr)
	}
	if !bytes.Equal(got.Bytes(), want) {
		out := filepath.Join(t.TempDir(), "follow.actual")
		os.WriteFile(out, got.Bytes(), 0o644)
		t.Fatalf("follow output differs from %s (%d vs %d bytes); actual written to %s", golden, got.Len(), len(want), out)
	}
}
