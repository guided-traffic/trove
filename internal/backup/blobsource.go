package backup

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/target"
	"github.com/guided-traffic/trove/internal/blob"
)

// StoreSource is the [BlobSource] over the blob store's object client (R-BAK-006). It only reads. The client is the one
// internal/blob/s3 builds for storage.s3; this adapter lives here because the infrastructure layer may not import this package.
type StoreSource struct {
	objects  blob.ObjectStore
	prefix   string
	endpoint string
	bucket   string
}

var _ BlobSource = (*StoreSource)(nil)

// NewStoreSource reads the blobs below prefix (storage.s3.prefix). endpoint and bucket name the store for a server-side copy.
func NewStoreSource(objects blob.ObjectStore, prefix, endpoint, bucket string) *StoreSource {
	return &StoreSource{objects: objects, prefix: prefix, endpoint: endpoint, bucket: bucket}
}

func (s *StoreSource) key(d format.Digest) string { return blob.Key(s.prefix, blob.Digest(d)) }

// Size implements BlobSource: HeadObject, or ErrBlobNotFound.
func (s *StoreSource) Size(ctx context.Context, d format.Digest) (int64, error) {
	info, err := s.objects.Head(ctx, s.key(d))
	if errors.Is(err, blob.ErrNotFound) {
		return 0, ErrBlobNotFound
	}
	if err != nil {
		return 0, err
	}
	if info.Size < 0 {
		return 0, fmt.Errorf("the blob store did not report the size of %s", d.Hex())
	}
	return info.Size, nil
}

// Open implements BlobSource: a ranged GetObject; a negative length reads to the end.
func (s *StoreSource) Open(ctx context.Context, d format.Digest, offset, length int64) (io.ReadCloser, error) {
	rc, _, err := s.objects.Get(ctx, s.key(d), &blob.ByteRange{Offset: offset, Length: length})
	if errors.Is(err, blob.ErrNotFound) {
		return nil, ErrBlobNotFound
	}
	return rc, err
}

// Ref implements BlobSource.
func (s *StoreSource) Ref(d format.Digest) target.ObjectRef {
	return target.ObjectRef{Endpoint: s.endpoint, Bucket: s.bucket, Key: s.key(d)}
}
