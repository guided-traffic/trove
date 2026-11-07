package s3

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/guided-traffic/trove/internal/backup/target"
	"github.com/guided-traffic/trove/internal/config"
)

// copySource is the x-amz-copy-source value of bucket/key, with the key percent-encoded.
func copySource(bucket, key string) string {
	segs := strings.Split(key, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return url.PathEscape(bucket) + "/" + strings.Join(segs, "/")
}

// unavailable reports the answers that make a server-side copy impossible for this pair of buckets and credentials: the
// target principal may not read the source, or the store has no cross-bucket copy.
func unavailable(code string, status int) bool {
	switch code {
	case "AccessDenied", "NotImplemented", "InvalidRequest":
		return true
	}
	return status == http.StatusForbidden || status == http.StatusNotImplemented
}

// bound is the time a request that moves n bytes inside the store may take: the base plus n at the assumed rate.
func (t *Target) bound(n int64) time.Duration {
	return t.copyBase + time.Duration(n)*time.Second/time.Duration(max(t.copyRate, 1))
}

// copyRun is one Copy: the bytes copied so far and the caller's progress callback.
type copyRun struct {
	done     atomic.Int64
	progress func(int64)
}

func (r *copyRun) tick() {
	if r.progress != nil {
		r.progress(r.done.Load())
	}
}

// keepalive calls the progress callback every interval until the returned function is called, so that a request that has
// no body to move, and so no progress of its own, still keeps the run's lease alive while it is bounded by its deadline.
func (t *Target) keepalive(r *copyRun) (stop func()) {
	if r.progress == nil || t.progressEvery <= 0 {
		return func() {}
	}
	quit, exited := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(exited)
		tk := time.NewTicker(t.progressEvery)
		defer tk.Stop()
		for {
			select {
			case <-tk.C:
				r.tick()
			case <-quit:
				return
			}
		}
	}()
	return func() { close(quit); <-exited }
}

// Copy implements target.Store. The source must be on the target's endpoint; the request is signed with the target's
// credentials, so they need s3:GetObject on the source (R-BAK-006). An object up to the single-copy limit moves with
// CopyObject, a larger one with UploadPartCopy parts of backup.copy.part_size. Both repeat the target's encryption
// settings (R-BAK-026).
//
// The requests have no body, so no byte-idle watchdog can judge them: each is bounded by a deadline derived from the size
// of what it copies (WithCopyBound). A copy that outlasts its bound is aborted and returns ErrServerCopyUnavailable, so that
// the caller streams. While a request is outstanding, src.Progress is called every progress interval.
func (t *Target) Copy(ctx context.Context, src target.ObjectRef, dstKey string) error {
	if src.Endpoint != "" && config.NormalizeEndpoint(src.Endpoint) != t.c.cfg.Endpoint {
		return target.ErrServerCopyUnavailable
	}
	run := &copyRun{progress: src.Progress}
	defer t.keepalive(run)()
	var err error
	if src.Size <= t.copyMax {
		err = t.copyOne(ctx, src, dstKey)
	} else {
		err = t.copyParts(ctx, run, src, dstKey)
	}
	if err == nil {
		return nil
	}
	if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: the copy of %s did not finish within %s: %w", target.ErrServerCopyUnavailable, src.Key, t.bound(src.Size), err)
	}
	if code, status := apiCode(err); unavailable(code, status) {
		return fmt.Errorf("%w: %w", target.ErrServerCopyUnavailable, err)
	}
	return terr(err)
}

func (t *Target) copyOne(ctx context.Context, src target.ObjectRef, dstKey string) error {
	s := t.c.sse()
	ctx, cancel := context.WithTimeout(ctx, t.bound(src.Size))
	defer cancel()
	_, err := t.c.long().CopyObject(ctx, &awss3.CopyObjectInput{
		Bucket: &t.c.bucket, Key: &dstKey, CopySource: aws.String(copySource(src.Bucket, src.Key)),
		MetadataDirective: types.MetadataDirectiveCopy, StorageClass: types.StorageClass(t.c.cfg.StorageClass),
		ServerSideEncryption: s.mode, SSEKMSKeyId: s.key, BucketKeyEnabled: s.bucket,
	})
	return mapError(ctx, "CopyObject", dstKey, err)
}

func (t *Target) copyParts(ctx context.Context, run *copyRun, src target.ObjectRef, dstKey string) (err error) {
	if t.copyPart < minPart {
		return fmt.Errorf("s3: copy part size %d is below the 5 MiB minimum", t.copyPart)
	}
	if (src.Size+t.copyPart-1)/t.copyPart > maxParts {
		return fmt.Errorf("s3: %s needs more than %d copy parts: raise backup.copy.part_size", src.Key, maxParts)
	}
	api := t.c.long()
	s := t.c.sse()
	cctx, cancel := context.WithTimeout(ctx, t.bound(0))
	out, err := api.CreateMultipartUpload(cctx, &awss3.CreateMultipartUploadInput{
		Bucket: &t.c.bucket, Key: &dstKey, ContentType: aws.String("application/octet-stream"), StorageClass: types.StorageClass(t.c.cfg.StorageClass),
		ServerSideEncryption: s.mode, SSEKMSKeyId: s.key, BucketKeyEnabled: s.bucket,
	})
	cancel()
	if err != nil {
		return mapError(ctx, "CreateMultipartUpload", dstKey, err)
	}
	id := out.UploadId
	defer func() {
		if err != nil {
			// The caller's context may be the reason (SIGTERM); the abort still goes out, but only within the grace period.
			actx, acancel := context.WithTimeout(context.WithoutCancel(ctx), t.abortGrace)
			defer acancel()
			_, _ = api.AbortMultipartUpload(actx, &awss3.AbortMultipartUploadInput{Bucket: &t.c.bucket, Key: &dstKey, UploadId: id})
		}
	}()
	var parts []types.CompletedPart
	for off := int64(0); off < src.Size; off += t.copyPart {
		last := min(off+t.copyPart, src.Size) - 1
		n := int32(len(parts) + 1) //nolint:gosec // at most 10,000
		pctx, pcancel := context.WithTimeout(ctx, t.bound(last-off+1))
		po, perr := api.UploadPartCopy(pctx, &awss3.UploadPartCopyInput{
			Bucket: &t.c.bucket, Key: &dstKey, UploadId: id, PartNumber: aws.Int32(n),
			CopySource: aws.String(copySource(src.Bucket, src.Key)), CopySourceRange: aws.String(fmt.Sprintf("bytes=%d-%d", off, last)),
		})
		pcancel()
		if perr != nil {
			return mapError(ctx, "UploadPartCopy", dstKey, perr)
		}
		parts = append(parts, types.CompletedPart{ETag: po.CopyPartResult.ETag, PartNumber: aws.Int32(n)})
		run.done.Store(last + 1)
		run.tick()
	}
	// The store assembles the parts when it completes the upload; it can take a while for a large object.
	cctx, cancel = context.WithTimeout(ctx, t.bound(src.Size/8))
	defer cancel()
	_, err = api.CompleteMultipartUpload(cctx, &awss3.CompleteMultipartUploadInput{
		Bucket: &t.c.bucket, Key: &dstKey, UploadId: id, MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
	})
	return mapError(ctx, "CompleteMultipartUpload", dstKey, err)
}
