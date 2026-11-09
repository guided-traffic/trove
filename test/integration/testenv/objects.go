package testenv

import (
	"bytes"
	"context"
	"errors"
	"io"
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
	cfg.Prefix = s.ObjectPrefix()
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

// TakeObject moves the object of the blob with the SHA-256 digest out of the installation's object store and returns its
// bytes, for PutObject to put back: an object store that answers 404 for an object that exists, as with a wrong prefix.
func (s *Stack) TakeObject(tb testing.TB, digest []byte) []byte {
	tb.Helper()
	c, prefix := s.objects(tb)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	body, _, err := c.Get(ctx, blob.Key(prefix, blob.Digest(digest)), nil)
	if err != nil {
		tb.Fatalf("read the object: %v", err)
	}
	data, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil {
		tb.Fatalf("read the object: %v", err)
	}
	s.DeleteObject(tb, digest)
	return data
}

// PutObject writes the object of the blob with the SHA-256 digest into the installation's object store, as an operator's
// restore does; the rows are not touched.
func (s *Stack) PutObject(tb testing.TB, digest, data []byte) {
	tb.Helper()
	c, prefix := s.objects(tb)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.Put(ctx, blob.Key(prefix, blob.Digest(digest)), bytes.NewReader(data), int64(len(data)), blob.Checksum{}); err != nil {
		tb.Fatalf("put the object: %v", err)
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

// ObjectClient is the client of the installation's object store below its prefix, with the prefix, for a test that asks the
// store as a command of the installation does.
func (s *Stack) ObjectClient(tb testing.TB) (blob.ObjectStore, string) {
	tb.Helper()
	return s.objects(tb)
}
