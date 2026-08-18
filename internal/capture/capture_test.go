package capture

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// jsonUnmarshal aliases encoding/json for test readability.
func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

func TestWriterResumeManifest(t *testing.T) {
	// After a kill+restart the stream file contains both runs but a naive
	// manifest would report last-process-only numbers next to it (2026-08-13
	// review #3). The manifest must carry resume provenance and an exact
	// whole-stream size so 1.5's integrity check compares against truth.
	dir := t.TempDir()

	w1, err := NewWriter(dir, "2026-01-02", false)
	if err != nil {
		t.Fatal(err)
	}
	if w1.resumed {
		t.Fatal("first open must not be resumed")
	}
	// While w1 is open, a second writer must be refused by the flock —
	// concurrent appenders corrupt the stream — regardless of resume flag.
	if _, err := NewWriter(dir, "2026-01-02", true); !errors.Is(err, ErrLocked) {
		t.Fatalf("concurrent open = %v, want ErrLocked", err)
	}
	if err := w1.Append(100, []byte(`[{"ev":"T"}]`)); err != nil {
		t.Fatal(err)
	}
	if err := w1.Close(Manifest{Date: "2026-01-02"}); err != nil {
		t.Fatal(err)
	}

	// A non-empty stream refuses a non-consenting open (the 2026-08-17
	// smoke-test pollution): resuming is a stated intent, never a default.
	if _, err := NewWriter(dir, "2026-01-02", false); !errors.Is(err, ErrExists) {
		t.Fatalf("unconsented reopen = %v, want ErrExists", err)
	}

	w2, err := NewWriter(dir, "2026-01-02", true)
	if err != nil {
		t.Fatal(err)
	}
	if !w2.resumed || w2.priorBytes == 0 {
		t.Fatalf("second open must detect resume: resumed=%v prior=%d", w2.resumed, w2.priorBytes)
	}
	if err := w2.Append(200, []byte(`[{"ev":"Q"}]`)); err != nil {
		t.Fatal(err)
	}
	if err := w2.Close(Manifest{Date: "2026-01-02"}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "2026-01-02", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := jsonUnmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if !m.Resumed || m.PriorStreamBytes == 0 {
		t.Fatalf("manifest must record resume provenance: %+v", m)
	}
	// Exact accounting: prior + this run == whole stream on disk.
	if m.PriorStreamBytes+m.BytesThisRun != m.StreamBytesTotal {
		t.Fatalf("prior(%d) + this-run(%d) != total(%d)", m.PriorStreamBytes, m.BytesThisRun, m.StreamBytesTotal)
	}
	fi, err := os.Stat(filepath.Join(dir, "2026-01-02", "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if m.StreamBytesTotal != fi.Size() {
		t.Fatalf("manifest total %d != file size %d", m.StreamBytesTotal, fi.Size())
	}
}

func TestReaderGzip(t *testing.T) {
	// The .gz branch becomes the DEFAULT read path once nightly compression
	// lands (mini-spec 1.2: compress after close) — it must roundtrip.
	dir := t.TempDir()
	plain := filepath.Join(dir, "stream.jsonl")
	content := "100 [{\"ev\":\"_capture\",\"event\":\"start\"}]\n200 [{\"ev\":\"T\",\"sym\":\"X\"}]\n"
	if err := os.WriteFile(plain, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gzPath := filepath.Join(dir, "stream.jsonl.gz")
	f, err := os.Create(gzPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	if _, err := zw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	for _, path := range []string{plain, gzPath} {
		r, err := OpenReader(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		var got []int64
		for {
			rec, err := r.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			got = append(got, rec.RecvNs)
		}
		r.Close()
		if len(got) != 2 || got[0] != 100 || got[1] != 200 {
			t.Fatalf("%s: records %v, want [100 200]", path, got)
		}
	}
}
