// Package archive is the S3 archive behind data/ (mini-spec 8.1): S3 holds
// every recorded artifact, the Studio's data/ is a cache. Keys mirror the
// local layout, s3://<bucket>/<class>/<name>, where name is the path
// relative to the class directory (2026-08-25.csv, 2026-08-25/stream.jsonl.gz,
// baskets/semis_compute.csv). Only cmd/ packages import this; the metric and
// profile packages stay S3-free (the 8.3 repo-split seam).
//
// Credentials are the ARCHIVE_* variables only. The AWS_* pair in .env
// belongs to the vendor's flat-file endpoint and must never reach here.
package archive

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Class is one archived artifact family; the S3 key prefix and the data/
// subdirectory share the name.
type Class string

const (
	Capture         Class = "capture"
	CaptureOptions  Class = "capture-options"
	Buckets         Class = "buckets"
	BucketsOptions  Class = "buckets-options"
	FullTape        Class = "full-tape"
	Profiles        Class = "profiles"
	ProfilesOptions Class = "profiles-options"
)

// Classes lists every class in upload order.
var Classes = []Class{Capture, CaptureOptions, Buckets, BucketsOptions, FullTape, Profiles, ProfilesOptions}

// Cold reports whether a class is written once and rarely read (STANDARD_IA);
// buckets and profiles are read nightly and stay STANDARD.
func (c Class) Cold() bool {
	switch c {
	case Capture, CaptureOptions, FullTape:
		return true
	}
	return false
}

// Object is one archived file.
type Object struct {
	Name         string
	Size         int64
	LastModified time.Time
	SHA256       string // hex; empty when the object predates this package
}

// Store is the archive. Put is idempotent: an object whose size and
// sha256 metadata already match the local file is skipped.
type Store interface {
	Put(ctx context.Context, class Class, name, localPath string) error
	Get(ctx context.Context, class Class, name, localPath string) error
	List(ctx context.Context, class Class) ([]Object, error)
	Exists(ctx context.Context, class Class, name string) (bool, error)
	// Stat returns the remote object; ok=false when absent.
	Stat(ctx context.Context, class Class, name string) (obj Object, ok bool, err error)
}

// Config is the archive location and credentials.
type Config struct {
	Bucket, Region, AccessKeyID, SecretAccessKey string
}

// envKeys are the only variables this package reads.
var envKeys = []string{"ARCHIVE_S3_BUCKET", "ARCHIVE_S3_REGION", "ARCHIVE_AWS_ACCESS_KEY_ID", "ARCHIVE_AWS_SECRET_ACCESS_KEY"}

// ErrNotConfigured means no ARCHIVE_S3_BUCKET is set anywhere.
var ErrNotConfigured = errors.New("archive: ARCHIVE_S3_BUCKET not set (env or .env)")

// ConfigFromEnv reads the ARCHIVE_* variables from the environment, falling
// back to KEY=VALUE lines in envFile (the cmd/live .env contract). Values are
// never logged by this package; callers must not log them either.
func ConfigFromEnv(envFile string) (Config, error) {
	vals := map[string]string{}
	for _, k := range envKeys {
		vals[k] = os.Getenv(k)
	}
	if f, err := os.Open(envFile); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			for _, k := range envKeys {
				if v, ok := strings.CutPrefix(line, k+"="); ok && vals[k] == "" {
					vals[k] = strings.Trim(strings.TrimSpace(v), `"'`)
				}
			}
		}
	}
	cfg := Config{
		Bucket: vals["ARCHIVE_S3_BUCKET"], Region: vals["ARCHIVE_S3_REGION"],
		AccessKeyID: vals["ARCHIVE_AWS_ACCESS_KEY_ID"], SecretAccessKey: vals["ARCHIVE_AWS_SECRET_ACCESS_KEY"],
	}
	if cfg.Bucket == "" {
		return cfg, ErrNotConfigured
	}
	if cfg.Region == "" {
		return cfg, errors.New("archive: ARCHIVE_S3_REGION not set")
	}
	if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return cfg, errors.New("archive: ARCHIVE_AWS_ACCESS_KEY_ID / ARCHIVE_AWS_SECRET_ACCESS_KEY not set")
	}
	return cfg, nil
}

// Open connects to the S3 archive.
func Open(cfg Config) (Store, error) { return openS3(cfg) }

// EnsureLocal returns <localDir>/<name>, fetching it from the archive when
// absent. A local file is trusted as-is (the cache is authoritative for
// what it holds; profiles never hit the network for a present day).
func EnsureLocal(ctx context.Context, s Store, class Class, name, localDir string) (string, error) {
	path := filepath.Join(localDir, filepath.FromSlash(name))
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if err := s.Get(ctx, class, name, path); err != nil {
		return "", fmt.Errorf("archive: fetch %s/%s: %w", class, name, err)
	}
	return path, nil
}

// Key is the object key for a class and name.
func Key(class Class, name string) string { return string(class) + "/" + filepath.ToSlash(name) }

// FileSHA256 hashes a local file (hex).
func FileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// writePart streams r to localPath via <localPath>.part and an atomic rename,
// so a torn download never masquerades as a complete file.
func writePart(localPath string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return err
	}
	part := localPath + ".part"
	f, err := os.Create(part)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(part)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(part)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(part)
		return err
	}
	return os.Rename(part, localPath)
}

func dirOf(path string) string { return filepath.Dir(path) }
