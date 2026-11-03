package s3

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
)

// The methods below give a caller that must not import internal/blob (the Bastion's cache, R-BAS-001, R-BAS-030)
// the five operations it uses in plain types: PutObject, GetObject, HeadObject, ListObjectsV2 and DeleteObject
// (Client.Delete). They add no request of their own; each is a thin form of the method of the same operation.

// Object is what a head or a listing says about an object.
type Object struct {
	Key      string
	Size     int64
	Modified time.Time
}

// IsNotFound reports whether err says the object does not exist.
func IsNotFound(err error) bool { return errors.Is(err, blob.ErrNotFound) }

// PutPlain stores size bytes of rs under key. A non-empty sha256 (the raw 32 bytes) is sent as the payload
// checksum, so the store rejects bytes that differ from it.
func (c *Client) PutPlain(ctx context.Context, key string, rs io.ReadSeeker, size int64, sha256 []byte) error {
	var sum blob.Checksum
	if len(sha256) > 0 {
		sum = blob.Checksum{Algorithm: "sha256", Value: sha256}
	}
	return c.Put(ctx, key, rs, size, sum)
}

// OpenPlain opens the object, or length bytes from offset (length -1 is to the end). It returns the size of the
// whole object, or -1 when the store did not say.
func (c *Client) OpenPlain(ctx context.Context, key string, offset, length int64) (io.ReadCloser, int64, error) {
	var rng *blob.ByteRange
	if offset > 0 || length >= 0 {
		rng = &blob.ByteRange{Offset: max(offset, 0), Length: length}
	}
	rc, info, err := c.Get(ctx, key, rng)
	return rc, info.Size, err
}

// StatPlain returns the size and the modification time of the object.
func (c *Client) StatPlain(ctx context.Context, key string) (Object, error) {
	info, err := c.Head(ctx, key)
	if err != nil {
		return Object{}, err
	}
	return Object{Key: info.Key, Size: info.Size, Modified: info.LastModified}, nil
}

// ListPlain returns one page of the objects below prefix and the token of the next page, empty after the last.
// limit bounds the page (0 is the store's default).
func (c *Client) ListPlain(ctx context.Context, prefix, token string, limit int) ([]Object, string, error) {
	page, err := c.List(ctx, prefix, blob.ListOptions{Token: token, Max: limit})
	if err != nil {
		return nil, "", err
	}
	out := make([]Object, len(page.Objects))
	for i, o := range page.Objects {
		out[i] = Object{Key: o.Key, Size: o.Size, Modified: o.LastModified}
	}
	return out, page.Next, nil
}
