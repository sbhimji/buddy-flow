package main

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"buddy-flow/internal/archive"
	"buddy-flow/internal/bucket"
	"buddy-flow/internal/capture"
	"buddy-flow/internal/optbucket"
	"buddy-flow/internal/optclassify"
	"buddy-flow/internal/optingest"
	"buddy-flow/internal/universe"
	"buddy-flow/internal/uwfeed"
)

const fullTapeURL = "https://api.unusualwhales.com/api/option-trades/full-tape/"

// stepOptions: full-tape zip → buckets-options/<date>.csv → profiles-options.
// The tape is the sole source of options bucket files (7.5: it scrubs
// cancels and covers reconnect gaps); an existing bucket file is left alone
// (F2), so a re-run is a no-op until the profile roll.
func (j *job) stepOptions(ctx context.Context) error {
	zipPath := filepath.Join(j.classDir(archive.FullTape), j.date+".zip")
	bucketPath := optbucket.Path(j.classDir(archive.BucketsOptions), j.date)
	haveBucket := exists(bucketPath)
	switch {
	case haveBucket:
		j.logf("options: %s exists — tape download/convert skipped (F2)", bucketPath)
	case zipComplete(zipPath):
		j.logf("options: %s present and complete — download skipped", zipPath)
	default:
		if j.act("download %s%s -> %s", fullTapeURL, j.date, zipPath) {
			n, err := j.downloadFullTape(ctx, zipPath)
			if err != nil {
				return err
			}
			j.logf("options: downloaded %s (%s)", zipPath, humanBytes(n))
		}
	}
	if !haveBucket {
		if j.act("convert %s -> %s (uw-backfill in-process)", zipPath, bucketPath) {
			if err := j.convertFullTape(zipPath, bucketPath); err != nil {
				return err
			}
		}
	}
	if j.act("run profiles-options -days 20 -through %s", j.date) {
		out, err := j.run(ctx, "profiles-options", "-days", "20", "-through", j.date,
			"-buckets-dir", j.classDir(archive.BucketsOptions), "-out", j.classDir(archive.ProfilesOptions),
			"-baskets", j.baskets, "-weights", j.weights)
		if err != nil {
			return err
		}
		j.logf("options: %s", lastLine(out, "wrote "))
	}
	return nil
}

// zipComplete: present, not a .part, and opens as a zip (a torn download
// fails the central-directory read).
func zipComplete(path string) bool {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return false
	}
	zr.Close()
	return true
}

// downloadFullTape fetches the day's zip with the vendor bearer token,
// following the endpoint's 302 (Go drops the Authorization header on the
// cross-host hop, which is what a presigned redirect wants), .part + rename.
func (j *job) downloadFullTape(ctx context.Context, dst string) (int64, error) {
	key := envValue(j.envFile, "UNUSUAL_WHALES_API_KEY")
	if key == "" {
		return 0, fmt.Errorf("UNUSUAL_WHALES_API_KEY not set (env or %s)", j.envFile)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullTapeURL+j.date, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/zip, */*")
	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("full-tape %s: %w", j.date, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, fmt.Errorf("full-tape %s: HTTP %d %s", j.date, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	part := dst + ".part"
	f, err := os.Create(part)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, resp.Body)
	if err != nil {
		f.Close()
		os.Remove(part)
		return 0, fmt.Errorf("full-tape %s: body: %w", j.date, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(part)
		return 0, err
	}
	if want := resp.Header.Get("Content-Length"); want != "" {
		if w, _ := strconv.ParseInt(want, 10, 64); w > 0 && w != n {
			os.Remove(part)
			return 0, fmt.Errorf("full-tape %s: short body %d of %d bytes", j.date, n, w)
		}
	}
	if !zipComplete(part) {
		os.Remove(part)
		return 0, fmt.Errorf("full-tape %s: downloaded file is not a readable zip", j.date)
	}
	return n, os.Rename(part, dst)
}

// convertFullTape is cmd/uw-backfill for one date, in-process: the same
// StreamFullTape → optbucket.Store path, same partial-name rule.
func (j *job) convertFullTape(zipPath, out string) error {
	syms, err := universe.Load(j.baskets)
	if err != nil {
		return err
	}
	uni := make(map[string]bool, len(syms))
	for _, s := range syms {
		uni[s] = true
	}
	w, hash, err := optclassify.LoadWeights(j.weights)
	if err != nil {
		return err
	}
	stamp := w.Version + "@" + hash
	start := time.Now()
	p := optingest.NewPipeline(0)
	store, err := optbucket.NewStore(w, stamp)
	if err != nil {
		return err
	}
	p.SetObserver(store)
	done := make(chan struct{})
	go func() { p.Run(); close(done) }()
	st, err := uwfeed.StreamFullTape(zipPath, uni, p)
	p.Close()
	<-done
	if err != nil {
		return fmt.Errorf("%s: %w", zipPath, err)
	}
	minSec, maxSec, ok := store.Bounds()
	if !ok {
		return fmt.Errorf("%s: no universe prints — nothing written", zipPath)
	}
	writePath := out
	if dataDate, spans, serr := optbucket.SpansRegularSession(minSec, maxSec); serr != nil || !spans || dataDate != j.date {
		writePath = optbucket.PartialPath(filepath.Dir(out), j.date)
		j.logf("options: tape does not span the regular session (spans=%v date=%s err=%v) — writing partial name", spans, dataDate, serr)
	}
	rows, err := store.WriteCSV(writePath)
	if err != nil {
		return fmt.Errorf("write %s: %w", writePath, err)
	}
	j.logf("options: %s rows=%d universe=%d canceled=%d decode-errs=%d dupes=%d unclassifiable=%d -> %d bucket rows (%s) in %s",
		j.date, st.Rows, st.Universe, st.Canceled, st.DecodeErrs, p.Dupes.Load(), store.Telemetry().Unclassifiable, rows,
		filepath.Base(writePath), time.Since(start).Round(time.Second))
	if writePath != out {
		return fmt.Errorf("tape for %s produced a partial bucket file; profiles will not include it", j.date)
	}
	return nil
}

// stepEquity: buckets/<date>.csv must exist (cmd/live writes it at session
// end); if missing or only .partial, regenerate through cmd/replay from the
// capture, then roll profiles.
func (j *job) stepEquity(ctx context.Context) error {
	dir := j.classDir(archive.Buckets)
	full := bucket.Path(dir, j.date)
	if exists(full) {
		sz, _ := fileSize(full)
		j.logf("equity: %s present (%s)", full, humanBytes(sz))
	} else {
		partial := bucket.PartialPath(dir, j.date)
		src := j.capturePath(archive.Capture, j.date)
		if src == "" {
			return fmt.Errorf("%s missing and no capture for %s to regenerate from", full, j.date)
		}
		if exists(partial) {
			j.logf("equity: only %s exists — regenerating from %s", partial, src)
		} else {
			j.logf("equity: %s missing — regenerating from %s", full, src)
		}
		if j.act("replay %s -> %s", src, full) {
			got, err := j.replayEquity(ctx, src, j.scratch)
			if err != nil {
				return err
			}
			if got == "" {
				return fmt.Errorf("replay of %s produced no bucket file (empty capture?)", src)
			}
			dst := filepath.Join(dir, filepath.Base(got))
			if err := os.Rename(got, dst); err != nil {
				return err
			}
			j.logf("equity: wrote %s", dst)
			if dst != full {
				return fmt.Errorf("capture for %s does not span the session; %s written, profiles will not include it", j.date, filepath.Base(dst))
			}
		}
	}
	if j.act("run profiles -days 20 -through %s", j.date) {
		out, err := j.run(ctx, "profiles", "-days", "20", "-through", j.date,
			"-buckets-dir", dir, "-out", j.classDir(archive.Profiles), "-baskets", j.baskets)
		if err != nil {
			return err
		}
		j.logf("equity: %s", lastLine(out, "wrote "))
	}
	return nil
}

// capturePath returns the stream file for a date — raw preferred, .gz
// otherwise — or "" when neither exists.
func (j *job) capturePath(class archive.Class, date string) string {
	raw := capture.StreamPath(j.classDir(class), date)
	if exists(raw) {
		return raw
	}
	if exists(raw + ".gz") {
		return raw + ".gz"
	}
	return ""
}

// replayEquity replays one capture through cmd/replay into outDir, letting
// the replayer decide the coverage name: it refuses a plain name for a
// non-spanning capture, so the .partial name is retried. Returns the file
// written, or "" when the store was empty.
func (j *job) replayEquity(ctx context.Context, src, outDir string) (string, error) {
	date, _ := dateOf(filepath.Base(filepath.Dir(src)))
	full := bucket.Path(outDir, date)
	os.Remove(full)
	out, err := j.run(ctx, "replay", "-capture", src, "-buckets", full, "-baskets", j.baskets)
	if err == nil {
		if exists(full) {
			return full, nil
		}
		return "", nil
	}
	if !strings.Contains(out, "does not span the regular session") {
		return "", err
	}
	partial := bucket.PartialPath(outDir, date)
	os.Remove(partial)
	if _, err := j.run(ctx, "replay", "-capture", src, "-buckets", partial, "-baskets", j.baskets); err != nil {
		return "", err
	}
	if exists(partial) {
		return partial, nil
	}
	return "", nil
}

// replayOptions replays one options capture through cmd/replay-options into
// outDir; the replayer picks the .partial name itself. Returns the file
// written, or "" when the store was empty.
func (j *job) replayOptions(ctx context.Context, src, outDir string) (string, error) {
	date, _ := dateOf(filepath.Base(filepath.Dir(src)))
	full := optbucket.Path(outDir, date)
	partial := optbucket.PartialPath(outDir, date)
	os.Remove(full)
	os.Remove(partial)
	if _, err := j.run(ctx, "replay-options", "-capture", src, "-buckets", full, "-weights", j.weights); err != nil {
		return "", err
	}
	for _, p := range []string{full, partial} {
		if exists(p) {
			return p, nil
		}
	}
	return "", nil
}

func lastLine(out, prefix string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], prefix) {
			return lines[i]
		}
	}
	if len(lines) > 0 {
		return lines[len(lines)-1]
	}
	return ""
}
