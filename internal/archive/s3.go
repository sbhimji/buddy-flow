package archive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// shaMeta is the user-metadata key carrying the local file's sha256. S3's
// ETag is not an MD5 for multipart uploads, so idempotence is decided on
// size + this stamp instead.
const shaMeta = "sha256"

type s3Store struct {
	bucket string
	client *s3.Client
	up     *manager.Uploader
	down   *manager.Downloader
}

func openS3(cfg Config) (Store, error) {
	if cfg.Bucket == "" || cfg.Region == "" || cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, errors.New("archive: incomplete config (bucket, region, key id, secret all required)")
	}
	// Static ARCHIVE_* credentials only: the default chain would happily pick
	// up the vendor's AWS_* pair from the environment.
	ac, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("archive: aws config: %w", err)
	}
	client := s3.NewFromConfig(ac)
	return &s3Store{
		bucket: cfg.Bucket,
		client: client,
		up:     manager.NewUploader(client, func(u *manager.Uploader) { u.PartSize = 64 << 20; u.Concurrency = 4 }),
		down:   manager.NewDownloader(client, func(d *manager.Downloader) { d.PartSize = 64 << 20; d.Concurrency = 4 }),
	}, nil
}

func (s *s3Store) Stat(ctx context.Context, class Class, name string) (Object, bool, error) {
	key := Key(class, name)
	h, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		var nf *types.NotFound
		if errors.As(err, &nf) || strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "404") {
			return Object{}, false, nil
		}
		return Object{}, false, fmt.Errorf("head %s: %w", key, err)
	}
	obj := Object{Name: name, Size: aws.ToInt64(h.ContentLength), SHA256: h.Metadata[shaMeta]}
	if h.LastModified != nil {
		obj.LastModified = *h.LastModified
	}
	return obj, true, nil
}

func (s *s3Store) Exists(ctx context.Context, class Class, name string) (bool, error) {
	_, ok, err := s.Stat(ctx, class, name)
	return ok, err
}

func (s *s3Store) Put(ctx context.Context, class Class, name, localPath string) error {
	sha, size, err := FileSHA256(localPath)
	if err != nil {
		return err
	}
	if remote, ok, err := s.Stat(ctx, class, name); err != nil {
		return err
	} else if ok && remote.Size == size && remote.SHA256 == sha {
		return nil // idempotent: already archived
	}
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	key := Key(class, name)
	in := &s3.PutObjectInput{
		Bucket:   &s.bucket,
		Key:      &key,
		Body:     f,
		Metadata: map[string]string{shaMeta: sha},
	}
	if class.Cold() {
		in.StorageClass = types.StorageClassStandardIa
	} else {
		in.StorageClass = types.StorageClassStandard
	}
	if _, err := s.up.Upload(ctx, in); err != nil {
		return fmt.Errorf("put %s: %w", key, err)
	}
	return nil
}

func (s *s3Store) Get(ctx context.Context, class Class, name, localPath string) error {
	key := Key(class, name)
	// The download manager writes ranges concurrently, so it needs a
	// WriterAt: a temp .part file, renamed only after a complete fetch.
	if err := os.MkdirAll(dirOf(localPath), 0o755); err != nil {
		return err
	}
	part := localPath + ".part"
	f, err := os.Create(part)
	if err != nil {
		return err
	}
	if _, err := s.down.Download(ctx, f, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key}); err != nil {
		f.Close()
		os.Remove(part)
		return fmt.Errorf("get %s: %w", key, err)
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

func (s *s3Store) List(ctx context.Context, class Class) ([]Object, error) {
	prefix := string(class) + "/"
	var out []Object
	p := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: &s.bucket, Prefix: &prefix})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", prefix, err)
		}
		for _, o := range page.Contents {
			k := aws.ToString(o.Key)
			if len(k) <= len(prefix) {
				continue
			}
			obj := Object{Name: k[len(prefix):], Size: aws.ToInt64(o.Size)}
			if o.LastModified != nil {
				obj.LastModified = *o.LastModified
			}
			out = append(out, obj)
		}
	}
	return out, nil
}
