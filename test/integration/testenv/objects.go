package testenv

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/blob/blobtest"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/outbound"
)

// objects is a client of the installation's object store below its prefix, as an operator holds one: what it does
// happens behind trove's back (an out-of-band deletion, ha-and-helm.md R-HA-024).
func (s *Stack) objects(tb testing.TB) (blob.ObjectStore, string) {
	tb.Helper()
	cfg := blobtest.Storage(tb, tb.TempDir()).S3
	o := S3Config(tb)
	cfg.Endpoint, cfg.Region, cfg.Bucket, cfg.ForcePathStyle = o.Endpoint, o.Region, o.BlobsBucket, true
	cfg.CAFile = CAFile(tb)
	cfg.Prefix = "it/" + s.name
	dir := tb.TempDir()
	cfg.Credentials = config.StorageS3Credentials{
		Source: "static", AccessKeyIDFile: WriteFile(tb, dir, "s3-id", o.AccessKey), SecretAccessKeyFile: WriteFile(tb, dir, "s3-secret", o.SecretKey),
	}
	c, err := s3.NewFromFactory(context.Background(), cfg, outbound.New(config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true}}))
	if err != nil {
		tb.Fatalf("object store client: %v", err)
	}
	return c, cfg.Prefix
}

// DeleteObject removes the object of the blob with the SHA-256 digest from the installation's object store; the blob row
// and every reference to it stay.
func (s *Stack) DeleteObject(tb testing.TB, digest []byte) {
	tb.Helper()
	c, prefix := s.objects(tb)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.Delete(ctx, blob.Key(prefix, blob.Digest(digest))); err != nil {
		tb.Fatalf("delete the object: %v", err)
	}
	if s.ObjectExists(tb, digest) {
		tb.Fatal("the object is still there after its deletion")
	}
}

// ObjectExists reports whether the object of the blob with the SHA-256 digest is in the installation's object store.
func (s *Stack) ObjectExists(tb testing.TB, digest []byte) bool {
	tb.Helper()
	c, prefix := s.objects(tb)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := c.Head(ctx, blob.Key(prefix, blob.Digest(digest)))
	switch {
	case err == nil:
		return true
	case errors.Is(err, blob.ErrNotFound):
		return false
	}
	tb.Fatalf("head the object: %v", err)
	return false
}
