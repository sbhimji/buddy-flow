package archive

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPutIdempotent(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	dir := t.TempDir()
	p := filepath.Join(dir, "2026-08-25.csv")
	write(t, p, "a,b\n1,2\n")
	for i := 0; i < 3; i++ {
		if err := m.Put(ctx, Buckets, "2026-08-25.csv", p); err != nil {
			t.Fatal(err)
		}
	}
	if len(m.Puts) != 1 {
		t.Fatalf("expected 1 write, got %d", len(m.Puts))
	}
	write(t, p, "a,b\n1,3\n") // same size, different bytes
	if err := m.Put(ctx, Buckets, "2026-08-25.csv", p); err != nil {
		t.Fatal(err)
	}
	if len(m.Puts) != 2 {
		t.Fatalf("changed content must re-upload; writes=%d", len(m.Puts))
	}
	obj, ok, err := m.Stat(ctx, Buckets, "2026-08-25.csv")
	if err != nil || !ok || obj.Size != 8 || obj.SHA256 == "" {
		t.Fatalf("stat: %+v ok=%v err=%v", obj, ok, err)
	}
}

func TestGetPartRename(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	dir := t.TempDir()
	src := filepath.Join(dir, "src", "stream.jsonl.gz")
	write(t, src, "gzbytes")
	if err := m.Put(ctx, Capture, "2026-08-25/stream.jsonl.gz", src); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst", "2026-08-25", "stream.jsonl.gz")
	if err := m.Get(ctx, Capture, "2026-08-25/stream.jsonl.gz", dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "gzbytes" {
		t.Fatalf("got %q", b)
	}
	if _, err := os.Stat(dst + ".part"); !os.IsNotExist(err) {
		t.Fatal(".part must be renamed away")
	}
	if err := m.Get(ctx, Capture, "missing", filepath.Join(dir, "x")); err == nil {
		t.Fatal("missing object must error")
	}
	if _, err := os.Stat(filepath.Join(dir, "x.part")); !os.IsNotExist(err) {
		t.Fatal("failed get must not leave a .part")
	}
}

func TestEnsureLocal(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	dir := t.TempDir()
	src := filepath.Join(dir, "src.csv")
	write(t, src, "remote")
	if err := m.Put(ctx, BucketsOptions, "2026-08-24.csv", src); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(dir, "cache")
	// hit: local file wins even when it differs from the archive
	write(t, filepath.Join(local, "2026-08-24.csv"), "local")
	p, err := EnsureLocal(ctx, m, BucketsOptions, "2026-08-24.csv", local)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "local" {
		t.Fatalf("cache hit must not fetch; got %q", b)
	}
	// miss: fetched
	os.Remove(p)
	p, err = EnsureLocal(ctx, m, BucketsOptions, "2026-08-24.csv", local)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "remote" {
		t.Fatalf("cache miss must fetch; got %q", b)
	}
	if _, err := EnsureLocal(ctx, m, BucketsOptions, "2026-01-01.csv", local); err == nil {
		t.Fatal("absent everywhere must error")
	}
}

func TestListAndKey(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	dir := t.TempDir()
	f := filepath.Join(dir, "f")
	write(t, f, "x")
	for _, n := range []string{"b.csv", "a.csv", "sub/c.csv"} {
		if err := m.Put(ctx, Profiles, n, f); err != nil {
			t.Fatal(err)
		}
	}
	m.Put(ctx, Buckets, "other.csv", f)
	objs, err := m.List(ctx, Profiles)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 3 || objs[0].Name != "a.csv" || objs[2].Name != "sub/c.csv" {
		t.Fatalf("list: %+v", objs)
	}
	if Key(Capture, filepath.Join("2026-08-25", "stream.jsonl.gz")) != "capture/2026-08-25/stream.jsonl.gz" {
		t.Fatal("key layout")
	}
}

func TestConfigFromEnvIgnoresVendorAWS(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "vendor")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "vendor")
	for _, k := range envKeys {
		t.Setenv(k, "")
	}
	envFile := filepath.Join(t.TempDir(), ".env")
	write(t, envFile, "AWS_ACCESS_KEY_ID=vendor\nARCHIVE_S3_BUCKET=\"bkt\"\nARCHIVE_S3_REGION=us-east-1\nARCHIVE_AWS_ACCESS_KEY_ID=id\nARCHIVE_AWS_SECRET_ACCESS_KEY='sec'\n")
	cfg, err := ConfigFromEnv(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Bucket != "bkt" || cfg.Region != "us-east-1" || cfg.AccessKeyID != "id" || cfg.SecretAccessKey != "sec" {
		t.Fatalf("cfg: %+v", cfg)
	}
	if _, err := ConfigFromEnv(filepath.Join(t.TempDir(), "none")); err != ErrNotConfigured {
		t.Fatalf("want ErrNotConfigured, got %v", err)
	}
}
