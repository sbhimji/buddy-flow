// Command nightly is the one job that owns every end-of-day roll (mini-spec
// 8.1): options full-tape → buckets → profiles, equity buckets → profiles,
// capture compression (verified before the raw is unlinked), S3 upload of
// everything new, local retention, and a disk report. Steps are idempotent
// and isolated: a failed step is logged and the later steps that do not
// depend on it still run. Exit 0 all ok; 1 a step failed (which one is in
// the log).
//
//	bin/nightly -dry-run -date 2026-08-25     # log the plan, do nothing
//	bin/nightly -date 2026-08-25              # re-run a night by hand
//	bin/nightly -skip upload,retention        # local rolls only
//
// The replay, profile and options-profile binaries are exec'd from -bin
// (default: next to this executable, then ./bin) so nightly shares their
// exact code paths; bucket regeneration goes through cmd/replay, never a
// second implementation.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"buddy-flow/internal/archive"
	"buddy-flow/internal/session"
)

// stepNames in execution order; -skip takes any of these.
var stepNames = []string{"options", "equity", "compress", "upload", "retention", "disk"}

type job struct {
	date     string // session date, YYYY-MM-DD (ET)
	today    string // wall-clock ET date: files stamped today are never touched by retention
	data     string // data/ root
	bin      string // sibling binaries
	scratch  string // throwaway working dir
	dryRun   bool
	baskets  string
	weights  string
	envFile  string
	refDays  map[string]bool
	minFree  int64         // bytes
	store    archive.Store // nil until upload opens it
	storeErr error

	// retention defaults (owner S3): captures 5/3 days, buckets 25, zips 0.
	keepCapture, keepCaptureOptions, keepBuckets int
	logKeepBytes                                 int64
	exclude                                      map[archive.Class]bool // classes left out of upload
}

func main() {
	var (
		date     = flag.String("date", "", "session date YYYY-MM-DD (default: today in ET)")
		dryRun   = flag.Bool("dry-run", false, "log every action, perform none")
		skip     = flag.String("skip", "", "comma-separated steps to skip: "+strings.Join(stepNames, ","))
		exclude  = flag.String("exclude", "", "comma-separated archive classes to leave out of upload (e.g. full-tape); retention never deletes what is not archived")
		data     = flag.String("data", "data", "data root")
		bin      = flag.String("bin", "", "directory of replay/replay-options/profiles/profiles-options binaries (default: next to this executable, then ./bin)")
		scratch  = flag.String("scratch", "", "scratch directory for verification replays (default: a temp dir, removed on exit)")
		baskets  = flag.String("baskets", "docs/foundations/morning-tape-baskets-v2.json", "trader-owned basket config")
		weights  = flag.String("weights", "docs/foundations/options-weights-v1.json", "conviction weights config")
		refPath  = flag.String("reference-days", "docs/foundations/reference-days.json", "owner-maintained reference days, never deleted locally")
		envFile  = flag.String("env", ".env", "KEY=VALUE file for ARCHIVE_* and UNUSUAL_WHALES_API_KEY fallbacks")
		minFree  = flag.Int("min-free-gb", 40, "exit non-zero when the data volume has less free space than this")
		keepCap  = flag.Int("keep-capture-days", 5, "local retention: equity capture days")
		keepCapO = flag.Int("keep-capture-options-days", 3, "local retention: options capture days")
		keepBk   = flag.Int("keep-bucket-days", 25, "local retention: bucket days (both classes)")
		logKeep  = flag.Int64("log-max-mb", 200, "truncate data/live*.log beyond this size")
	)
	flag.Parse()

	j := &job{
		data: *data, dryRun: *dryRun, baskets: *baskets, weights: *weights, envFile: *envFile,
		minFree: int64(*minFree) << 30, keepCapture: *keepCap, keepCaptureOptions: *keepCapO, keepBuckets: *keepBk,
		logKeepBytes: *logKeep << 20,
	}
	j.today = time.Now().In(session.ET()).Format("2006-01-02")
	j.date = *date
	if j.date == "" {
		j.date = j.today
	}
	if _, err := time.Parse("2006-01-02", j.date); err != nil {
		fatal(2, fmt.Errorf("-date must be YYYY-MM-DD: %w", err))
	}
	j.bin = *bin
	if j.bin == "" {
		if exe, err := os.Executable(); err == nil {
			if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "replay")); err == nil {
				j.bin = filepath.Dir(exe)
			}
		}
		if j.bin == "" {
			j.bin = "bin"
		}
	}
	for _, b := range []string{"replay", "replay-options", "profiles", "profiles-options"} {
		if _, err := os.Stat(filepath.Join(j.bin, b)); err != nil {
			fatal(2, fmt.Errorf("missing binary %s in %s (go build -o %s ./cmd/%s)", b, j.bin, j.bin, b))
		}
	}
	refs, err := loadReferenceDays(*refPath)
	if err != nil {
		fatal(2, err)
	}
	j.refDays = refs
	j.scratch = *scratch
	if j.scratch == "" {
		d, err := os.MkdirTemp("", "buddyflow-nightly-")
		if err != nil {
			fatal(2, err)
		}
		j.scratch = d
		defer os.RemoveAll(d)
	}
	j.exclude = map[archive.Class]bool{}
	for _, c := range strings.Split(*exclude, ",") {
		if c = strings.TrimSpace(c); c != "" {
			known := false
			for _, k := range archive.Classes {
				known = known || string(k) == c
			}
			if !known {
				fatal(2, fmt.Errorf("-exclude: unknown class %q", c))
			}
			j.exclude[archive.Class(c)] = true
		}
	}
	skipped := map[string]bool{}
	for _, s := range strings.Split(*skip, ",") {
		if s = strings.TrimSpace(s); s != "" {
			skipped[s] = true
		}
	}
	for s := range skipped {
		if !contains(stepNames, s) {
			fatal(2, fmt.Errorf("-skip: unknown step %q (want one of %s)", s, strings.Join(stepNames, ",")))
		}
	}

	mode := "live"
	if j.dryRun {
		mode = "DRY-RUN"
	}
	j.logf("nightly %s date=%s today=%s data=%s bin=%s scratch=%s reference-days=%d", mode, j.date, j.today, j.data, j.bin, j.scratch, len(j.refDays))

	steps := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"options", j.stepOptions},
		{"equity", j.stepEquity},
		{"compress", j.stepCompress},
		{"upload", j.stepUpload},
		{"retention", j.stepRetention},
		{"disk", j.stepDisk},
	}
	ctx := context.Background()
	failed := []string{}
	for _, s := range steps {
		if skipped[s.name] {
			j.logf("%s: skipped (-skip)", s.name)
			continue
		}
		start := time.Now()
		if err := s.fn(ctx); err != nil {
			j.logf("%s: FAILED in %s: %v", s.name, time.Since(start).Round(time.Second), err)
			failed = append(failed, s.name)
			continue
		}
		j.logf("%s: ok in %s", s.name, time.Since(start).Round(time.Second))
	}
	if len(failed) > 0 {
		j.logf("nightly done: FAILED steps: %s", strings.Join(failed, ","))
		os.Exit(1)
	}
	j.logf("nightly done: all steps ok")
}

func (j *job) logf(format string, args ...any) {
	fmt.Printf("%s %s\n", time.Now().In(session.ET()).Format("15:04:05"), fmt.Sprintf(format, args...))
}

// act logs an action and reports whether it should be performed (false in
// -dry-run, where the log line is the whole output).
func (j *job) act(format string, args ...any) bool {
	if j.dryRun {
		j.logf("would "+format, args...)
		return false
	}
	j.logf(format, args...)
	return true
}

// run execs a sibling binary with its output captured; on failure the tail
// of the output goes into the error so the log shows why.
func (j *job) run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, filepath.Join(j.bin, name), args...)
	out, err := cmd.CombinedOutput()
	s := string(out)
	if err != nil {
		tail := s
		if len(tail) > 2000 {
			tail = "…" + tail[len(tail)-2000:]
		}
		return s, fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(tail))
	}
	return s, nil
}

// classDir is the local directory of an archive class.
func (j *job) classDir(c archive.Class) string { return filepath.Join(j.data, string(c)) }

func loadReferenceDays(path string) (map[string]bool, error) {
	out := map[string]bool{}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	var doc struct {
		Dates []string `json:"dates"`
	}
	if err := jsonUnmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, d := range doc.Dates {
		if _, err := time.Parse("2006-01-02", d); err != nil {
			return nil, fmt.Errorf("%s: bad date %q", path, d)
		}
		out[d] = true
	}
	return out, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func fatal(code int, err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(code)
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
