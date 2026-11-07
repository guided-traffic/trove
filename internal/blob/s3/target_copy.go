package s3

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

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

// Copy implements target.Store. The source must be on the target's endpoint; the request is signed with the target's
// credentials, so they need s3:GetObject on the source (R-BAK-006). An object up to the single-copy limit moves with
// CopyObject, a larger one with UploadPartCopy parts of backup.copy.part_size. Both repeat the target's encryption
// settings (R-BAK-026).
func (t *Target) Copy(ctx context.Context, src target.ObjectRef, dstKey string) error {
	if src.Endpoint != "" && config.NormalizeEndpoint(src.Endpoint) != t.c.cfg.Endpoint {
		return target.ErrServerCopyUnavailable
	}
	var err error
	if src.Size <= t.copyMax {
		err = t.copyOne(ctx, src, dstKey)
	} else {
		err = t.copyParts(ctx, src, dstKey)
	}
	if err != nil {
		if code, status := apiCode(err); unavailable(code, status) {
			return fmt.Errorf("%w: %w", target.ErrServerCopyUnavailable, err)
		}
		return terr(err)
	}
	return nil
}

func (t *Target) copyOne(ctx context.Context, src target.ObjectRef, dstKey string) error {
	s := t.c.sse()
	ctx, w, done := t.c.watch(ctx)
	defer done()
	w.touch()
	_, err := t.c.api.CopyObject(ctx, &awss3.CopyObjectInput{
		Bucket: &t.c.bucket, Key: &dstKey, CopySource: aws.String(copySource(src.Bucket, src.Key)),
		MetadataDirective: types.MetadataDirectiveCopy, StorageClass: types.StorageClass(t.c.cfg.StorageClass),
		ServerSideEncryption: s.mode, SSEKMSKeyId: s.key, BucketKeyEnabled: s.bucket,
	})
	return mapError(ctx, "CopyObject", dstKey, err)
}

func (t *Target) copyParts(ctx context.Context, src target.ObjectRef, dstKey string) (err error) {
	if t.copyPart < minPart {
		return fmt.Errorf("s3: copy part size %d is below the 5 MiB minimum", t.copyPart)
	}
	if (src.Size+t.copyPart-1)/t.copyPart > maxParts {
		return fmt.Errorf("s3: %s needs more than %d copy parts: raise backup.copy.part_size", src.Key, maxParts)
	}
	s := t.c.sse()
	out, err := t.c.api.CreateMultipartUpload(ctx, &awss3.CreateMultipartUploadInput{
		Bucket: &t.c.bucket, Key: &dstKey, ContentType: aws.String("application/octet-stream"), StorageClass: types.StorageClass(t.c.cfg.StorageClass),
		ServerSideEncryption: s.mode, SSEKMSKeyId: s.key, BucketKeyEnabled: s.bucket,
	})
	if err != nil {
		return mapError(ctx, "CreateMultipartUpload", dstKey, err)
	}
	id := out.UploadId
	defer func() {
		if err != nil {
			// The caller's context may be the reason; the abort must still go out.
			_, _ = t.c.api.AbortMultipartUpload(context.WithoutCancel(ctx), &awss3.AbortMultipartUploadInput{Bucket: &t.c.bucket, Key: &dstKey, UploadId: id})
		}
	}()
	var parts []types.CompletedPart
	for off := int64(0); off < src.Size; off += t.copyPart {
		last := min(off+t.copyPart, src.Size) - 1
		n := int32(len(parts) + 1) //nolint:gosec // at most 10,000
		po, perr := t.c.api.UploadPartCopy(ctx, &awss3.UploadPartCopyInput{
			Bucket: &t.c.bucket, Key: &dstKey, UploadId: id, PartNumber: aws.Int32(n),
			CopySource: aws.String(copySource(src.Bucket, src.Key)), CopySourceRange: aws.String(fmt.Sprintf("bytes=%d-%d", off, last)),
		})
		if perr != nil {
			return mapError(ctx, "UploadPartCopy", dstKey, perr)
		}
		parts = append(parts, types.CompletedPart{ETag: po.CopyPartResult.ETag, PartNumber: aws.Int32(n)})
	}
	_, err = t.c.api.CompleteMultipartUpload(ctx, &awss3.CompleteMultipartUploadInput{
		Bucket: &t.c.bucket, Key: &dstKey, UploadId: id, MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
	})
	return mapError(ctx, "CompleteMultipartUpload", dstKey, err)
}
