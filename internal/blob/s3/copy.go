package s3

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/guided-traffic/trove/internal/blob"
)

const (
	// singleCopyMax is the largest object CopyObject takes (5 GiB); a larger one moves with UploadPartCopy.
	singleCopyMax = 5 << 30
	// copyPartSize is the size of the parts of a copy above singleCopyMax: 1 GiB parts keep an object of the 5 TiB limit
	// within 10,000 parts.
	copyPartSize = 1 << 30
)

// copyBound is the time a server-side copy of n bytes may take: the store answers a copy when it has moved the bytes, so the
// request has no byte-idle watchdog and a deadline derived from its size bounds it instead.
func copyBound(n int64) time.Duration {
	return 2*time.Minute + time.Duration(n/(64<<20))*time.Second
}

// Copy implements blob.ObjectStore: CopyObject for an object up to 5 GiB, a multipart copy with UploadPartCopy above, both
// inside the bucket and with the encryption settings of every write (R-STOR-032).
func (c *Client) Copy(ctx context.Context, srcKey, dstKey string, size int64) error {
	if size <= c.copyLimit() {
		return c.copyOne(ctx, srcKey, dstKey, size)
	}
	return c.copyParts(ctx, srcKey, dstKey, size)
}

// copyLimit is the largest object a single CopyObject takes.
func (c *Client) copyLimit() int64 {
	if c.singleCopyMax > 0 {
		return c.singleCopyMax
	}
	return singleCopyMax
}

// copyPart is the size of the parts of a multipart copy.
func (c *Client) copyPart() int64 {
	if c.copyStep > 0 {
		return c.copyStep
	}
	return copyPartSize
}

func (c *Client) copyOne(ctx context.Context, srcKey, dstKey string, size int64) error {
	s := c.sse()
	ctx, cancel := context.WithTimeout(ctx, copyBound(size))
	defer cancel()
	_, err := c.long().CopyObject(ctx, &awss3.CopyObjectInput{
		Bucket: &c.bucket, Key: &dstKey, CopySource: aws.String(copySource(c.bucket, srcKey)),
		MetadataDirective: types.MetadataDirectiveCopy, StorageClass: types.StorageClass(c.cfg.StorageClass),
		ServerSideEncryption: s.mode, SSEKMSKeyId: s.key, BucketKeyEnabled: s.bucket,
	})
	return mapError(ctx, "CopyObject", srcKey, err)
}

func (c *Client) copyParts(ctx context.Context, srcKey, dstKey string, size int64) (err error) {
	api := c.long()
	s := c.sse()
	cctx, cancel := context.WithTimeout(ctx, copyBound(0))
	out, err := api.CreateMultipartUpload(cctx, &awss3.CreateMultipartUploadInput{
		Bucket: &c.bucket, Key: &dstKey, ContentType: aws.String("application/octet-stream"), StorageClass: types.StorageClass(c.cfg.StorageClass),
		ServerSideEncryption: s.mode, SSEKMSKeyId: s.key, BucketKeyEnabled: s.bucket,
	})
	cancel()
	if err != nil {
		return mapError(ctx, "CreateMultipartUpload", dstKey, err)
	}
	id := out.UploadId
	defer func() {
		if err != nil {
			actx, acancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer acancel()
			_, _ = api.AbortMultipartUpload(actx, &awss3.AbortMultipartUploadInput{Bucket: &c.bucket, Key: &dstKey, UploadId: id})
		}
	}()
	var parts []types.CompletedPart
	step := c.copyPart()
	for off := int64(0); off < size; off += step {
		last := min(off+step, size) - 1
		n := int32(len(parts) + 1) //nolint:gosec // at most 10,000 by the size limit of an object
		pctx, pcancel := context.WithTimeout(ctx, copyBound(last-off+1))
		po, perr := api.UploadPartCopy(pctx, &awss3.UploadPartCopyInput{
			Bucket: &c.bucket, Key: &dstKey, UploadId: id, PartNumber: aws.Int32(n),
			CopySource: aws.String(copySource(c.bucket, srcKey)), CopySourceRange: aws.String(fmt.Sprintf("bytes=%d-%d", off, last)),
		})
		pcancel()
		if perr != nil {
			return mapError(ctx, "UploadPartCopy", srcKey, perr)
		}
		parts = append(parts, types.CompletedPart{ETag: po.CopyPartResult.ETag, PartNumber: aws.Int32(n)})
	}
	cctx, cancel = context.WithTimeout(ctx, copyBound(size))
	defer cancel()
	_, err = api.CompleteMultipartUpload(cctx, &awss3.CompleteMultipartUploadInput{
		Bucket: &c.bucket, Key: &dstKey, UploadId: id, MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
	})
	return mapError(ctx, "CompleteMultipartUpload", dstKey, err)
}

// SetCopyLimits makes a copy of more than single bytes a multipart copy with parts of step bytes. The limits are 5 GiB and 1 GiB
// otherwise, which a test cannot reach with a real object; nothing else calls it.
func (c *Client) SetCopyLimits(single, step int64) { c.singleCopyMax, c.copyStep = single, step }

// ListMultipartPage implements [blob.MultipartPager].
func (c *Client) ListMultipartPage(ctx context.Context, prefix string, after blob.MultipartMarker, max int) ([]blob.MultipartUpload, blob.MultipartMarker, error) {
	in := &awss3.ListMultipartUploadsInput{Bucket: &c.bucket, Prefix: &prefix, MaxUploads: aws.Int32(int32(max))} //nolint:gosec // bounded by the caller
	if after.Key != "" {
		in.KeyMarker, in.UploadIdMarker = &after.Key, &after.UploadID
	}
	page, err := c.api.ListMultipartUploads(ctx, in)
	if err != nil {
		return nil, blob.MultipartMarker{}, mapError(ctx, "ListMultipartUploads", prefix, err)
	}
	out := make([]blob.MultipartUpload, 0, len(page.Uploads))
	for _, u := range page.Uploads {
		out = append(out, blob.MultipartUpload{Key: aws.ToString(u.Key), UploadID: aws.ToString(u.UploadId), Initiated: aws.ToTime(u.Initiated)})
	}
	if !aws.ToBool(page.IsTruncated) {
		return out, blob.MultipartMarker{}, nil
	}
	return out, blob.MultipartMarker{Key: aws.ToString(page.NextKeyMarker), UploadID: aws.ToString(page.NextUploadIdMarker)}, nil
}
