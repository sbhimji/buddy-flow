package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"buddy-flow/internal/archive"
)

func touch(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newTestJob builds a data/ tree with 7 equity capture days (one raw), 4
// options capture days, 27 bucket days, 3 zips, a profile dir and a big log.
func newTestJob(t *testing.T) *job {
	t.Helper()
	data := t.TempDir()
	j := &job{
		data: data, date: "2026-08-25", today: "2026-08-25", scratch: t.TempDir(),
		keepCapture: 5, keepCaptureOptions: 3, keepBuckets: 25, logKeepBytes: 10,
		refDays: map[string]bool{"2026-08-14": true},
		store:   archive.NewMemStore(),
	}
	eq := []string{"2026-08-13", "2026-08-14", "2026-08-16", "2026-08-17", "2026-08-18", "2026-08-19", "2026-08-20"}
	for _, d := range eq {
		touch(t, filepath.Join(data, "capture", d, "manifest.json"), "{}")
		if d == "2026-08-16" {
			touch(t, filepath.Join(data, "capture", d, "stream.jsonl"), "raw")
		} else {
			touch(t, filepath.Join(data, "capture", d, "stream.jsonl.gz"), "gz"+d)
		}
	}
	for _, d := range []string{"2026-08-20", "2026-08-21", "2026-08-24", "2026-08-25"} {
		touch(t, filepath.Join(data, "capture-options", d, "manifest.json"), "{}")
		touch(t, filepath.Join(data, "capture-options", d, "stream.jsonl.gz"), "gz"+d)
	}
	for i := 1; i <= 27; i++ {
		d := "2026-07-" + pad(i)
		touch(t, filepath.Join(data, "buckets", d+".csv"), "b"+d)
		touch(t, filepath.Join(data, "buckets-options", d+".csv"), "o"+d)
	}
	touch(t, filepath.Join(data, "buckets", "2026-07-01.trades-only.csv"), "t")
	touch(t, filepath.Join(data, "buckets", "2026-07-01.partial.csv"), "p")
	for _, d := range []string{"2026-08-24", "2026-08-25", "2026-07-02"} {
		touch(t, filepath.Join(data, "full-tape", d+".zip"), "zip"+d)
	}
	touch(t, filepath.Join(data, "full-tape", "2026-07-03.zip.part"), "torn")
	touch(t, filepath.Join(data, "profiles", "NVDA.csv"), "nvda")
	touch(t, filepath.Join(data, "profiles", "baskets", "semis.csv"), "semis")
	touch(t, filepath.Join(data, "live.log"), strings.Repeat("x", 100))
	return j
}

func pad(i int) string {
	if i < 10 {
		return "0" + string(rune('0'+i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}

func TestUploadSetShapes(t *testing.T) {
	j := newTestJob(t)
	set, _ := j.uploadSet(archive.Capture)
	if _, ok := set["2026-08-16/stream.jsonl"]; ok {
		t.Fatal("raw stream must never be uploaded")
	}
	if _, ok := set["2026-08-13/stream.jsonl.gz"]; !ok {
		t.Fatal("gz missing from upload set")
	}
	set, _ = j.uploadSet(archive.FullTape)
	if _, ok := set["2026-07-03.zip.part"]; ok {
		t.Fatal(".part must be skipped")
	}
	set, _ = j.uploadSet(archive.Profiles)
	if _, ok := set["2026-08-25/baskets/semis.csv"]; !ok {
		t.Fatalf("profiles must upload under the date prefix: %v", set)
	}
	set, _ = j.uploadSet(archive.Buckets)
	if len(set) != 29 {
		t.Fatalf("buckets set: %d", len(set))
	}
}

func TestUploadThenRetention(t *testing.T) {
	j := newTestJob(t)
	ctx := context.Background()
	if err := j.stepUpload(ctx); err != nil {
		t.Fatal(err)
	}
	mem := j.store.(*archive.MemStore)
	n := len(mem.Puts)
	if err := j.stepUpload(ctx); err != nil { // idempotent
		t.Fatal(err)
	}
	if len(mem.Puts) != n {
		t.Fatalf("second upload wrote %d more objects", len(mem.Puts)-n)
	}
	if err := j.stepRetention(ctx); err != nil {
		t.Fatal(err)
	}
	d := func(parts ...string) string { return filepath.Join(append([]string{j.data}, parts...)...) }
	// captures: 7 days, keep 5 → 08-13 and 08-14 are old; 08-14 is a reference day.
	if exists(d("capture", "2026-08-13")) {
		t.Fatal("08-13 should be deleted")
	}
	if !exists(d("capture", "2026-08-14")) {
		t.Fatal("reference day must survive")
	}
	if !exists(d("capture", "2026-08-16", "stream.jsonl")) {
		t.Fatal("raw capture must never be deleted by retention")
	}
	// options captures: 4 days keep 3 → 08-20 gone; 08-25 (today) kept.
	if exists(d("capture-options", "2026-08-20")) || !exists(d("capture-options", "2026-08-25")) {
		t.Fatal("options capture retention wrong")
	}
	// buckets: dates 07-01..07-27 = 27 dates, keep 25 → 07-01, 07-02 gone (all suffixes).
	for _, f := range []string{"2026-07-01.csv", "2026-07-01.trades-only.csv", "2026-07-01.partial.csv", "2026-07-02.csv"} {
		if exists(d("buckets", f)) {
			t.Fatalf("%s should be deleted", f)
		}
	}
	if !exists(d("buckets", "2026-07-03.csv")) || !exists(d("buckets-options", "2026-07-27.csv")) {
		t.Fatal("kept bucket days deleted")
	}
	// full-tape: keep 0, today protected, .part untouched.
	if exists(d("full-tape", "2026-08-24.zip")) || exists(d("full-tape", "2026-07-02.zip")) {
		t.Fatal("uploaded zips should be deleted")
	}
	if !exists(d("full-tape", "2026-08-25.zip")) || !exists(d("full-tape", "2026-07-03.zip.part")) {
		t.Fatal("today's zip / .part must survive")
	}
	if sz, _ := fileSize(d("live.log")); sz != 0 {
		t.Fatal("oversized live.log must be truncated")
	}
}

func TestRetentionRefusesUnarchived(t *testing.T) {
	j := newTestJob(t)
	// Nothing uploaded: nothing may be deleted.
	if err := j.stepRetention(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(j.data, "capture", "2026-08-13")) || !exists(filepath.Join(j.data, "full-tape", "2026-07-02.zip")) {
		t.Fatal("unarchived files deleted")
	}
}

func TestDryRunTouchesNothing(t *testing.T) {
	j := newTestJob(t)
	j.dryRun = true
	ctx := context.Background()
	if err := j.stepUpload(ctx); err != nil {
		t.Fatal(err)
	}
	if len(j.store.(*archive.MemStore).Puts) != 0 {
		t.Fatal("dry-run uploaded")
	}
	before := dirSize(j.data)
	if err := j.stepRetention(ctx); err != nil {
		t.Fatal(err)
	}
	if err := j.stepCompress(ctx); err != nil {
		t.Fatal(err)
	}
	if dirSize(j.data) != before || exists(filepath.Join(j.data, "capture", "2026-08-16", "stream.jsonl.gz")) {
		t.Fatal("dry-run changed data/")
	}
}
