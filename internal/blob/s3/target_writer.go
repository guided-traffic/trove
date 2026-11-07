package s3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/guided-traffic/trove/internal/backup/target"
)

// maxParts is the number of parts S3 accepts in one upload.
const maxParts = 10000

// writer is the streaming upload of target.Store.Writer: it collects bytes into parts of the configured size and uploads
// each when full, one at a time, so memory is one part. An object below one part is written with a single PutObject at
// Close, and the multipart upload is created only when the first part is full (R-BAK-005 step 5).
type writer struct {
	t     *Target
	ctx   context.Context
	key   string
	opts  target.PutOptions
	buf   bytes.Buffer
	sum   hash.Hash
	size  int64
	id    string
	parts []types.CompletedPart
	err   error
	done  bool
}

// Writer implements target.Store.
func (t *Target) Writer(ctx context.Context, key string, opts target.PutOptions) (target.ObjectWriter, error) {
	return &writer{t: t, ctx: ctx, key: key, opts: opts, sum: sha256.New()}, nil
}

// Write implements io.Writer.
func (w *writer) Write(p []byte) (int, error) {
	if w.done {
		return 0, errors.New("s3: write after close")
	}
	if w.err != nil {
		return 0, w.err
	}
	n := 0
	for len(p) > 0 {
		room := int(w.t.partSize) - w.buf.Len()
		chunk := p
		if len(chunk) > room {
			chunk = chunk[:room]
		}
		w.buf.Write(chunk)
		_, _ = w.sum.Write(chunk)
		w.size += int64(len(chunk))
		n += len(chunk)
		p = p[len(chunk):]
		if int64(w.buf.Len()) >= w.t.partSize && len(p) > 0 {
			// The part is full and more follows: it is not the last one.
			if err := w.flush(); err != nil {
				w.err = err
				return n, err
			}
		}
	}
	return n, nil
}

// flush uploads the buffered bytes as the next part.
func (w *writer) flush() error {
	t := w.t
	if w.id == "" {
		s := t.c.sse()
		out, err := t.c.api.CreateMultipartUpload(w.ctx, &awss3.CreateMultipartUploadInput{
			Bucket: &t.c.bucket, Key: &w.key, ContentType: aws.String(contentType(w.opts)), StorageClass: types.StorageClass(t.c.cfg.StorageClass),
			ServerSideEncryption: s.mode, SSEKMSKeyId: s.key, BucketKeyEnabled: s.bucket,
		})
		if err != nil {
			return terr(mapError(w.ctx, "CreateMultipartUpload", w.key, err))
		}
		w.id = aws.ToString(out.UploadId)
	}
	if len(w.parts) >= maxParts {
		return fmt.Errorf("s3: %s needs more than %d parts: raise backup.database.part_size", w.key, maxParts)
	}
	n := int32(len(w.parts) + 1) //nolint:gosec // at most 10,000
	ctx, idle, done := w.t.c.watch(w.ctx)
	defer done()
	idle.touch()
	out, err := t.c.api.UploadPart(ctx, &awss3.UploadPartInput{
		Bucket: &t.c.bucket, Key: &w.key, UploadId: &w.id, PartNumber: aws.Int32(n),
		Body: &seeker{rs: bytes.NewReader(w.buf.Bytes()), w: idle}, ContentLength: aws.Int64(int64(w.buf.Len())),
	})
	if serr := idle.stalled("UploadPart", w.key); serr != nil {
		return serr
	}
	if err != nil {
		return terr(mapError(ctx, "UploadPart", w.key, err))
	}
	w.parts = append(w.parts, types.CompletedPart{
		ETag: out.ETag, PartNumber: aws.Int32(n), ChecksumCRC32: out.ChecksumCRC32,
		ChecksumCRC32C: out.ChecksumCRC32C, ChecksumCRC64NVME: out.ChecksumCRC64NVME, ChecksumSHA1: out.ChecksumSHA1, ChecksumSHA256: out.ChecksumSHA256,
	})
	w.buf.Reset()
	return nil
}

// Close implements target.ObjectWriter.
func (w *writer) Close(ctx context.Context) (target.Object, error) {
	if w.done {
		return target.Object{}, errors.New("s3: close after close")
	}
	w.done = true
	if w.err != nil {
		_ = w.abort(ctx)
		return target.Object{}, w.err
	}
	sum := hex.EncodeToString(w.sum.Sum(nil))
	if w.id == "" {
		o, err := w.t.Put(ctx, w.key, w.buf.Bytes(), w.opts)
		if err != nil {
			return target.Object{}, err
		}
		o.SHA256 = sum
		return o, nil
	}
	if err := w.complete(ctx); err != nil {
		_ = w.abort(ctx)
		return target.Object{}, err
	}
	o, err := w.t.Head(ctx, w.key)
	if err != nil {
		return target.Object{}, fmt.Errorf("s3: confirm %s: %w", w.key, err)
	}
	if o.Size != w.size {
		return target.Object{}, fmt.Errorf("s3: %s is %d bytes in the target, %d were written", w.key, o.Size, w.size)
	}
	o.SHA256 = sum
	return o, nil
}

// complete flushes the last part and completes the upload, conditionally where the store supports it.
func (w *writer) complete(ctx context.Context) error {
	t := w.t
	w.ctx = ctx
	if w.buf.Len() > 0 {
		if err := w.flush(); err != nil {
			return err
		}
	}
	in := &awss3.CompleteMultipartUploadInput{
		Bucket: &t.c.bucket, Key: &w.key, UploadId: &w.id, MultipartUpload: &types.CompletedMultipartUpload{Parts: w.parts},
	}
	if w.opts.IfAbsent {
		if t.noCond.Load() {
			if err := t.mustBeAbsent(ctx, w.key); err != nil {
				return err
			}
		} else {
			in.IfNoneMatch = aws.String("*")
		}
	}
	_, err := t.c.api.CompleteMultipartUpload(ctx, in)
	if err != nil && w.opts.IfAbsent && in.IfNoneMatch != nil {
		if cerr := t.conditionalError(ctx, w.key, err); cerr != nil {
			if !errors.Is(cerr, errRetryUnconditional) {
				return cerr
			}
			in.IfNoneMatch = nil
			_, err = t.c.api.CompleteMultipartUpload(ctx, in)
		}
	}
	return terr(mapError(ctx, "CompleteMultipartUpload", w.key, err))
}

// Abort implements target.ObjectWriter.
func (w *writer) Abort(ctx context.Context) error {
	w.done = true
	return w.abort(ctx)
}

func (w *writer) abort(ctx context.Context) error {
	if w.id == "" {
		return nil
	}
	id := w.id
	w.id = ""
	_, err := w.t.c.api.AbortMultipartUpload(ctx, &awss3.AbortMultipartUploadInput{Bucket: &w.t.c.bucket, Key: &w.key, UploadId: &id})
	return mapError(ctx, "AbortMultipartUpload", w.key, err)
}
