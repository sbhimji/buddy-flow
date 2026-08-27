package archive

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestS3RoundTrip runs only with ARCHIVE_S3_BUCKET set (real bucket). It
// writes and reads under the "_test" pseudo-class so it never touches
// archived data.
func TestS3RoundTrip(t *testing.T) {
	cfg, err := ConfigFromEnv(filepath.Join("..", "..", ".env"))
	if err != nil {
		t.Skip("archive not configured:", err)
	}
	s, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dir := t.TempDir()
	src := filepath.Join(dir, "roundtrip.txt")
	body := "archive round-trip " + time.Now().UTC().Format(time.RFC3339Nano)
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	const class = Class("_test")
	name := "roundtrip.txt"
	if err := s.Put(ctx, class, name, src); err != nil {
		t.Fatal(err)
	}
	obj, ok, err := s.Stat(ctx, class, name)
	if err != nil || !ok {
		t.Fatalf("stat: ok=%v err=%v", ok, err)
	}
	sha, size, _ := FileSHA256(src)
	if obj.Size != size || obj.SHA256 != sha {
		t.Fatalf("stat mismatch: %+v vs size=%d sha=%s", obj, size, sha)
	}
	if err := s.Put(ctx, class, name, src); err != nil { // idempotent second put
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "back", name)
	if err := s.Get(ctx, class, name, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != body {
		t.Fatalf("round-trip mismatch: %q", b)
	}
	objs, err := s.List(ctx, class)
	if err != nil || len(objs) == 0 {
		t.Fatalf("list: %v %v", objs, err)
	}
}
