package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"buddy-flow/internal/archive"
	"buddy-flow/internal/bucket"
	"buddy-flow/internal/optbucket"
)

// stepCompress gzips every closed raw capture (equity and options) and
// unlinks the raw only after two proofs: the gunzipped stream hashes to the
// raw's sha256, and a replay of the .gz produces a bucket file byte-identical
// to a reference (equity: the file cmd/live wrote; otherwise a replay of the
// raw). A capture is "closed" when its manifest exists and nobody holds the
// writer's flock — the same lock capture.NewWriter takes — so a live
// process can never be compressed under.
func (j *job) stepCompress(ctx context.Context) error {
	var firstErr error
	for _, class := range []archive.Class{archive.Capture, archive.CaptureOptions} {
		dates, err := captureDates(j.classDir(class))
		if err != nil {
			return err
		}
		for _, date := range dates {
			if date > j.date {
				continue
			}
			if err := j.compressOne(ctx, class, date); err != nil {
				j.logf("compress: %s/%s FAILED: %v", class, date, err)
				if firstErr == nil {
					firstErr = fmt.Errorf("%s/%s: %w", class, date, err)
				}
			}
		}
	}
	return firstErr
}

func captureDates(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if d, ok := dateOf(e.Name()); ok && e.IsDir() && d == e.Name() {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (j *job) compressOne(ctx context.Context, class archive.Class, date string) error {
	dir := filepath.Join(j.classDir(class), date)
	raw := filepath.Join(dir, "stream.jsonl")
	gz := raw + ".gz"
	rawSize, haveRaw := fileSize(raw)
	if !haveRaw {
		return nil // already compressed (or never captured)
	}
	if !exists(filepath.Join(dir, "manifest.json")) {
		j.logf("compress: %s/%s has no manifest — not closed, skipped", class, date)
		return nil
	}
	if locked, err := writerLocked(raw); err != nil {
		return err
	} else if locked {
		j.logf("compress: %s/%s is held by a live writer — skipped", class, date)
		return nil
	}
	if !j.act("gzip %s (%s) -> %s, verify, unlink raw", raw, humanBytes(rawSize), gz) {
		return nil
	}

	start := time.Now()
	rawSHA, err := gzipFile(raw, gz)
	if err != nil {
		return err
	}
	gzSize, _ := fileSize(gz)
	j.logf("compress: %s/%s gzipped %s -> %s (%.1f%%) in %s", class, date, humanBytes(rawSize), humanBytes(gzSize),
		100*float64(gzSize)/float64(rawSize), time.Since(start).Round(time.Second))

	// Proof 1: the gz decompresses to exactly the raw bytes.
	gunzSHA, err := gunzipSHA(gz)
	if err != nil {
		return err
	}
	if gunzSHA != rawSHA {
		return fmt.Errorf("gunzip sha256 %s != raw %s — raw kept", gunzSHA[:12], rawSHA[:12])
	}
	// Proof 2: the replayed .gz reproduces the bucket file.
	how, err := j.verifyReplay(ctx, class, date, raw, gz)
	if err != nil {
		return fmt.Errorf("replay parity: %w — raw kept", err)
	}
	j.logf("compress: %s/%s verified: sha256 round-trip ok; replay parity %s", class, date, how)
	if err := os.Remove(raw); err != nil {
		return err
	}
	j.logf("compress: %s/%s unlinked raw (%s freed)", class, date, humanBytes(rawSize))
	return nil
}

// writerLocked probes the capture writer's advisory lock without holding it.
func writerLocked(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return true, nil
		}
		return false, err
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false, nil
}

// gzipFile writes src as BestCompression gzip to dst (via .tmp + rename) and
// returns the sha256 of src, hashed on the same pass.
func gzipFile(src, dst string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	zw, err := gzip.NewWriterLevel(out, gzip.BestCompression)
	if err != nil {
		out.Close()
		return "", err
	}
	h := sha256.New()
	buf := make([]byte, 4<<20)
	if _, err := io.CopyBuffer(zw, io.TeeReader(in, h), buf); err != nil {
		zw.Close()
		out.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := zw.Close(); err != nil {
		out.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func gunzipSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	h := sha256.New()
	if _, err := io.CopyBuffer(h, zr, make([]byte, 4<<20)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// verifyReplay replays the .gz to scratch and compares the bucket file
// against a reference. Equity: the file cmd/live wrote (<date>.csv or
// .partial.csv); options bucket files come from the tape, not the capture,
// so the reference there is a replay of the raw. A reference mismatch on
// equity falls back to the raw replay too (and says so): the invariant
// being proven is gz ≡ raw, not live ≡ replay.
func (j *job) verifyReplay(ctx context.Context, class archive.Class, date, raw, gz string) (string, error) {
	gzDir := filepath.Join(j.scratch, "verify-gz", string(class))
	rawDir := filepath.Join(j.scratch, "verify-raw", string(class))
	os.MkdirAll(gzDir, 0o755)
	os.MkdirAll(rawDir, 0o755)
	replay := j.replayOptions
	refDir := j.classDir(archive.BucketsOptions)
	refFull, refPartial := optbucket.Path(refDir, date), optbucket.PartialPath(refDir, date)
	if class == archive.Capture {
		replay = j.replayEquity
		refDir = j.classDir(archive.Buckets)
		refFull, refPartial = bucket.Path(refDir, date), bucket.PartialPath(refDir, date)
	}
	gzOut, err := replay(ctx, gz, gzDir)
	if err != nil {
		return "", fmt.Errorf("replay gz: %w", err)
	}
	if class == archive.Capture && gzOut != "" {
		ref := ""
		for _, p := range []string{refFull, refPartial} {
			if exists(p) && filepath.Base(p) == filepath.Base(gzOut) {
				ref = p
			}
		}
		if ref != "" {
			same, err := sameBytes(gzOut, ref)
			if err != nil {
				return "", err
			}
			if same {
				return "vs live-written " + filepath.Base(ref) + ": identical", nil
			}
			j.logf("compress: %s/%s gz replay differs from live-written %s — comparing against a raw replay instead", class, date, filepath.Base(ref))
		}
	}
	rawOut, err := replay(ctx, raw, rawDir)
	if err != nil {
		return "", fmt.Errorf("replay raw: %w", err)
	}
	if gzOut == "" && rawOut == "" {
		return "vs raw replay: both empty stores (no session data)", nil
	}
	if gzOut == "" || rawOut == "" || filepath.Base(gzOut) != filepath.Base(rawOut) {
		return "", fmt.Errorf("gz replay wrote %q, raw replay wrote %q", filepath.Base(gzOut), filepath.Base(rawOut))
	}
	same, err := sameBytes(gzOut, rawOut)
	if err != nil {
		return "", err
	}
	if !same {
		return "", fmt.Errorf("gz and raw replays differ (%s)", filepath.Base(gzOut))
	}
	return "vs raw replay " + filepath.Base(gzOut) + ": identical", nil
}

func sameBytes(a, b string) (bool, error) {
	sa, oka := fileSize(a)
	sb, okb := fileSize(b)
	if !oka || !okb {
		return false, fmt.Errorf("compare: missing %s or %s", a, b)
	}
	if sa != sb {
		return false, nil
	}
	fa, err := os.Open(a)
	if err != nil {
		return false, err
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false, err
	}
	defer fb.Close()
	ba, bb := make([]byte, 1<<20), make([]byte, 1<<20)
	for {
		na, ea := io.ReadFull(fa, ba)
		nb, eb := io.ReadFull(fb, bb)
		if na != nb || !bytes.Equal(ba[:na], bb[:nb]) {
			return false, nil
		}
		if ea == io.EOF || ea == io.ErrUnexpectedEOF {
			return eb == io.EOF || eb == io.ErrUnexpectedEOF, nil
		}
		if ea != nil {
			return false, ea
		}
		if eb != nil {
			return false, eb
		}
	}
}
