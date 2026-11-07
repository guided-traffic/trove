package s3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/target"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/config"
)

// BlobSink is the new installation's blob store as `trove backup restore` writes it (R-BAK-021 step 3, backup.BlobSink): an
// object per digest at the key of R-STOR-001, written server-side from the backup target when the credentials of the blob
// store may read the target, otherwise streamed through the pod and hashed. It also lists and probes the prefix for the checks
// of R-BAK-020. It writes objects only; the blob rows come from the load.
type BlobSink struct {
	c        *Client
	copier   *Target
	prefix   string
	partSize int64
	maxSmall int64
}

// NewBlobSink wraps the blob-store client c of storage.s3 (cfg.Prefix is the key prefix). copyPart is backup.copy.part_size,
// the part size of a server-side copy above 5 GiB.
func NewBlobSink(c *Client, cfg config.StorageS3, copyPart int64) *BlobSink {
	if copyPart <= 0 {
		copyPart = 256 << 20
	}
	part := int64(cfg.Multipart.PartSize)
	if part < minPart {
		part = 16 << 20
	}
	small := int64(cfg.Multipart.Threshold)
	if small <= 0 || small < part {
		small = part
	}
	return &BlobSink{c: c, copier: newCopier(c, copyPart), prefix: cfg.Prefix, partSize: part, maxSmall: small}
}

func (s *BlobSink) key(d format.Digest) string { return blob.Key(s.prefix, blob.Digest(d)) }

// Has reports whether an object of the digest exists with the given size.
func (s *BlobSink) Has(ctx context.Context, d format.Digest, size int64) (bool, error) {
	info, err := s.c.Head(ctx, s.key(d))
	if errors.Is(err, blob.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Size == size, nil
}

// CopyFrom copies a pool object server-side. It returns target.ErrServerCopyUnavailable when the store or the credentials
// cannot, and target.ErrNotFound for a missing source.
func (s *BlobSink) CopyFrom(ctx context.Context, src target.ObjectRef, d format.Digest, size int64) error {
	if src.Size == 0 {
		src.Size = size
	}
	if err := s.copier.Copy(ctx, src, s.key(d)); err != nil {
		return err
	}
	return s.confirm(ctx, d, size)
}

// confirm checks that the object exists with the expected size.
func (s *BlobSink) confirm(ctx context.Context, d format.Digest, size int64) error {
	ok, err := s.Has(ctx, d, size)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("object %s is missing or has the wrong size after the write", d.Hex())
	}
	return nil
}

// Put streams the object and hashes it; a stream whose SHA-256 differs from d, or that is not size bytes long, is never made
// visible: a single request is not sent, a multipart upload is aborted before it is completed.
func (s *BlobSink) Put(ctx context.Context, d format.Digest, size int64, r io.Reader) error {
	key := s.key(d)
	h := sha256.New()
	verify := func(n int64) error {
		var got format.Digest
		copy(got[:], h.Sum(nil))
		if n != size {
			return fmt.Errorf("object %s: the stream has %d bytes, the blob list says %d", d.Hex(), n, size)
		}
		if got != d {
			return fmt.Errorf("object %s: the stream hashes to %s", d.Hex(), got.Hex())
		}
		return nil
	}
	if size <= s.maxSmall {
		buf, err := io.ReadAll(io.LimitReader(io.TeeReader(r, h), size+1))
		if err != nil {
			return err
		}
		if err := verify(int64(len(buf))); err != nil {
			return err
		}
		if err := s.c.Put(ctx, key, bytes.NewReader(buf), size, blob.Checksum{Algorithm: "sha256", Value: sha256Of(d)}); err != nil {
			return err
		}
		return s.confirm(ctx, d, size)
	}
	part := blob.PartSize(size, s.partSize)
	id, err := s.c.CreateMultipart(ctx, key, "")
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.copier.abortGrace)
			defer cancel()
			_ = s.c.AbortMultipart(actx, key, id)
		}
	}()
	var parts []blob.Part
	var total int64
	buf := make([]byte, part)
	for {
		n, rerr := io.ReadFull(io.TeeReader(r, h), buf)
		if n > 0 {
			total += int64(n)
			p, err := s.c.UploadPart(ctx, key, id, len(parts)+1, bytes.NewReader(buf[:n]), int64(n), blob.Checksum{})
			if err != nil {
				return err
			}
			parts = append(parts, p)
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF) {
				break
			}
			return rerr
		}
	}
	if err := verify(total); err != nil {
		return err
	}
	if err := s.c.CompleteMultipart(ctx, key, id, parts); err != nil {
		return err
	}
	done = true
	return s.confirm(ctx, d, size)
}

func sha256Of(d format.Digest) []byte { return d[:] }

// root is the key prefix of the installation's objects, with a trailing slash, empty for the bucket root.
func (s *BlobSink) root() string {
	if s.prefix == "" {
		return ""
	}
	return s.prefix + "/"
}

// FirstForeign returns the first thing below the blob prefix that is neither a probe object nor, with allowBlobs, a blob:
// the first object directly below the prefix or the first key prefix other than _probe/ (and sha256/). It lists with a
// delimiter, so a prefix full of blobs costs one request (R-BAK-020 check 5).
func (s *BlobSink) FirstForeign(ctx context.Context, allowBlobs bool) (string, error) {
	root := s.root()
	token := ""
	for {
		page, err := s.c.List(ctx, root, blob.ListOptions{Delimiter: "/", Token: token})
		if err != nil {
			return "", err
		}
		for _, o := range page.Objects {
			return o.Key, nil
		}
		for _, p := range page.Prefixes {
			name := strings.TrimPrefix(p, root)
			if name == "_probe/" || (allowBlobs && name == "sha256/") {
				continue
			}
			return p, nil
		}
		if page.Next == "" {
			return "", nil
		}
		token = page.Next
	}
}

// Probe writes and deletes the object <prefix>/_probe/<name> (R-BAK-020 check 7).
func (s *BlobSink) Probe(ctx context.Context, name string) error {
	key := s.root() + "_probe/" + name
	if err := s.c.Put(ctx, key, strings.NewReader("probe"), 5, blob.Checksum{}); err != nil {
		return err
	}
	return s.c.Delete(ctx, key)
}
