package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"buddy-flow/internal/archive"
)

// openStore connects once; a missing configuration is a step failure, not
// a crash, so the local rolls still complete without S3.
func (j *job) openStore() (archive.Store, error) {
	if j.store != nil || j.storeErr != nil {
		return j.store, j.storeErr
	}
	cfg, err := archive.ConfigFromEnv(j.envFile)
	if err != nil {
		j.storeErr = err
		return nil, err
	}
	s, err := archive.Open(cfg)
	if err != nil {
		j.storeErr = err
		return nil, err
	}
	j.store = s
	return s, nil
}

// uploadSet lists what each class contributes: relative name → local path.
// Raw stream.jsonl files are never uploaded (the .gz is the archived form);
// .part/.tmp files are skipped everywhere.
func (j *job) uploadSet(class archive.Class) (map[string]string, error) {
	dir := j.classDir(class)
	out := map[string]string{}
	add := func(rel, path string) { out[filepath.ToSlash(rel)] = path }
	switch class {
	case archive.Capture, archive.CaptureOptions:
		dates, err := captureDates(dir)
		if err != nil {
			return nil, err
		}
		for _, d := range dates {
			for _, f := range []string{"stream.jsonl.gz", "manifest.json"} {
				if p := filepath.Join(dir, d, f); exists(p) {
					add(filepath.Join(d, f), p)
				}
			}
		}
	case archive.Buckets, archive.BucketsOptions, archive.FullTape:
		entries, err := os.ReadDir(dir)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() || strings.HasSuffix(n, ".part") || strings.HasSuffix(n, ".tmp") || strings.HasPrefix(n, ".") {
				continue
			}
			if _, ok := dateOf(n); !ok {
				continue
			}
			add(n, filepath.Join(dir, n))
		}
	case archive.Profiles, archive.ProfilesOptions:
		// The whole directory under a date prefix: a rebuild never
		// overwrites an earlier night's artifact.
		if !exists(dir) {
			return out, nil
		}
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.HasPrefix(d.Name(), ".") {
				return err
			}
			rel, _ := filepath.Rel(dir, p)
			add(filepath.Join(j.date, rel), p)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// stepUpload syncs every class; Put is idempotent so this is a no-op for
// anything already archived with the same bytes.
func (j *job) stepUpload(ctx context.Context) error {
	s, err := j.openStore()
	if err != nil {
		return err
	}
	var firstErr error
	for _, class := range archive.Classes {
		if j.exclude[class] {
			j.logf("upload: %s: excluded (-exclude)", class)
			continue
		}
		set, err := j.uploadSet(class)
		if err != nil {
			return err
		}
		names := make([]string, 0, len(set))
		for n := range set {
			names = append(names, n)
		}
		sort.Strings(names)
		var uploaded, skipped int
		var bytes int64
		for _, n := range names {
			local := set[n]
			sz, _ := fileSize(local)
			if !j.dryRun {
				// Stat first so the log distinguishes "new" from "already there".
				remote, ok, err := s.Stat(ctx, class, n)
				if err != nil {
					return err
				}
				if ok && remote.Size == sz {
					if sha, _, err := archive.FileSHA256(local); err == nil && sha == remote.SHA256 {
						skipped++
						continue
					}
				}
			}
			if !j.act("put %s (%s)", archive.Key(class, n), humanBytes(sz)) {
				continue
			}
			start := time.Now()
			if err := s.Put(ctx, class, n, local); err != nil {
				j.logf("upload: %s FAILED: %v", archive.Key(class, n), err)
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			uploaded++
			bytes += sz
			j.logf("upload: %s (%s) in %s", archive.Key(class, n), humanBytes(sz), time.Since(start).Round(time.Second))
		}
		j.logf("upload: %s: %d files, uploaded %d (%s), already archived %d", class, len(names), uploaded, humanBytes(bytes), skipped)
	}
	return firstErr
}

// archived reports whether the local file is in S3 with the same size.
func (j *job) archived(ctx context.Context, s archive.Store, class archive.Class, name, local string) (bool, error) {
	sz, ok := fileSize(local)
	if !ok {
		return false, nil
	}
	remote, ok, err := s.Stat(ctx, class, name)
	if err != nil {
		return false, err
	}
	return ok && remote.Size == sz, nil
}

// stepRetention applies the local table (S3 keeps everything): captures
// keep N most recent days, bucket classes keep 25, full-tape keeps 0; a
// local copy is deleted only when it is archived with matching size, never
// for today's date, never for a reference day. Raw (uncompressed) captures
// are never deleted here — compress owns that. Also truncates oversized
// live logs.
func (j *job) stepRetention(ctx context.Context) error {
	s, err := j.openStore()
	if err != nil {
		return fmt.Errorf("retention needs the archive to confirm copies: %w", err)
	}
	protect := func(date string) (string, bool) {
		if date == j.today {
			return "today", true
		}
		if j.refDays[date] {
			return "reference day", true
		}
		return "", false
	}
	var freed int64
	var firstErr error
	fail := func(err error) {
		j.logf("retention: %v", err)
		if firstErr == nil {
			firstErr = err
		}
	}

	// Captures: whole date dirs beyond the keep window.
	for _, cl := range []struct {
		class archive.Class
		keep  int
	}{{archive.Capture, j.keepCapture}, {archive.CaptureOptions, j.keepCaptureOptions}} {
		dates, err := captureDates(j.classDir(cl.class))
		if err != nil {
			return err
		}
		if len(dates) <= cl.keep {
			continue
		}
		for _, d := range dates[:len(dates)-cl.keep] {
			if why, p := protect(d); p {
				j.logf("retention: %s/%s kept (%s)", cl.class, d, why)
				continue
			}
			dir := filepath.Join(j.classDir(cl.class), d)
			if exists(filepath.Join(dir, "stream.jsonl")) {
				j.logf("retention: %s/%s kept (raw not yet compressed)", cl.class, d)
				continue
			}
			ok := true
			for _, f := range []string{"stream.jsonl.gz", "manifest.json"} {
				p := filepath.Join(dir, f)
				if !exists(p) {
					continue
				}
				a, err := j.archived(ctx, s, cl.class, filepath.Join(d, f), p)
				if err != nil {
					fail(err)
					ok = false
					break
				}
				if !a {
					ok = false
				}
			}
			if !ok {
				j.logf("retention: %s/%s kept (not confirmed in archive)", cl.class, d)
				continue
			}
			sz := dirSize(dir)
			if j.act("delete %s (%s)", dir, humanBytes(sz)) {
				if err := os.RemoveAll(dir); err != nil {
					fail(err)
					continue
				}
			}
			freed += sz
		}
	}

	// Flat classes: files keyed by date beyond the keep window.
	for _, cl := range []struct {
		class archive.Class
		keep  int
	}{{archive.Buckets, j.keepBuckets}, {archive.BucketsOptions, j.keepBuckets}, {archive.FullTape, 0}} {
		set, err := j.uploadSet(cl.class)
		if err != nil {
			return err
		}
		dateSet := map[string]bool{}
		for n := range set {
			d, _ := dateOf(n)
			dateSet[d] = true
		}
		dates := make([]string, 0, len(dateSet))
		for d := range dateSet {
			dates = append(dates, d)
		}
		sort.Strings(dates)
		if len(dates) <= cl.keep {
			continue
		}
		old := map[string]bool{}
		for _, d := range dates[:len(dates)-cl.keep] {
			old[d] = true
		}
		names := make([]string, 0, len(set))
		for n := range set {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			d, _ := dateOf(n)
			if !old[d] {
				continue
			}
			if why, p := protect(d); p {
				j.logf("retention: %s/%s kept (%s)", cl.class, n, why)
				continue
			}
			local := set[n]
			a, err := j.archived(ctx, s, cl.class, n, local)
			if err != nil {
				fail(err)
				continue
			}
			if !a {
				j.logf("retention: %s/%s kept (not confirmed in archive)", cl.class, n)
				continue
			}
			sz, _ := fileSize(local)
			if j.act("delete %s (%s)", local, humanBytes(sz)) {
				if err := os.Remove(local); err != nil {
					fail(err)
					continue
				}
			}
			freed += sz
		}
	}

	// Live logs: the view server needs only the latest frame; frames are
	// reproducible from replay. Truncate (the server holds the file open).
	for _, name := range []string{"live.log", "live-options.log"} {
		p := filepath.Join(j.data, name)
		sz, ok := fileSize(p)
		if !ok || sz <= j.logKeepBytes {
			continue
		}
		if j.act("truncate %s (%s)", p, humanBytes(sz)) {
			if err := os.Truncate(p, 0); err != nil {
				fail(err)
				continue
			}
		}
		freed += sz
	}
	j.logf("retention: freed %s", humanBytes(freed))
	return firstErr
}

func dirSize(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// stepDisk logs one line of usage and fails when free space is below the
// floor, so launchd's last-exit-status shows the problem.
func (j *job) stepDisk(context.Context) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(j.data, &st); err != nil {
		return err
	}
	free := int64(st.Bavail) * int64(st.Bsize)
	total := int64(st.Blocks) * int64(st.Bsize)
	used := total - int64(st.Bfree)*int64(st.Bsize)
	var parts []string
	var dataTotal int64
	for _, c := range archive.Classes {
		sz := dirSize(j.classDir(c))
		dataTotal += sz
		parts = append(parts, fmt.Sprintf("%s=%s", c, humanBytes(sz)))
	}
	other := dirSize(j.data) - dataTotal
	j.logf("disk: used=%s free=%s data/=%s classes=[%s other=%s]", humanBytes(used), humanBytes(free), humanBytes(dataTotal+other), strings.Join(parts, " "), humanBytes(other))
	if free < j.minFree {
		return fmt.Errorf("free space %s below floor %s", humanBytes(free), humanBytes(j.minFree))
	}
	return nil
}
