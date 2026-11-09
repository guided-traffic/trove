package testenv

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// The stack's blob bucket keeps every version (the seed turns versioning on), so a deletion through trove's object store
// only writes a delete marker and the bytes stay as a noncurrent version. A suite that removed its objects that way still
// filled the store: F-113 measured 9.1 GiB in 239,000 versions after one day of runs, and SeaweedFS refused every upload
// once its 100 volumes were full. RemoveObjectsAtCleanup removes the versions themselves.

// objectStore is a client of the stack's S3 gateway with the operator's credentials, outside trove's own client.
func objectStore(tb testing.TB) *s3.Client {
	tb.Helper()
	o := S3Config(tb)
	return s3.New(s3.Options{
		Region:       o.Region,
		BaseEndpoint: aws.String(o.Endpoint),
		HTTPClient:   HTTPClient(tb),
		Credentials:  credentials.NewStaticCredentialsProvider(o.AccessKey, o.SecretKey, ""),
		UsePathStyle: o.ForcePathStyle,
	})
}

// below is the listing prefix of everything below prefix: prefix with one trailing slash, so that the prefix it/a does
// not also take it/ab.
func below(prefix string) string { return strings.TrimSuffix(prefix, "/") + "/" }

// RemoveObjectsAtCleanup removes, when the test ends, every object version, delete marker and unfinished multipart upload
// below prefix in bucket. A suite calls it once for the prefix it gives its object-store client, before it starts what
// writes there: cleanups run in reverse order, so the removal runs after the replicas and clients have stopped. A removal
// that fails fails the test, since what it leaves behind fills the shared stack.
func RemoveObjectsAtCleanup(tb testing.TB, bucket, prefix string) {
	tb.Helper()
	if strings.Trim(prefix, "/") == "" {
		tb.Fatal("RemoveObjectsAtCleanup needs a prefix of the test's own; an empty one would empty the bucket")
	}
	c := objectStore(tb)
	tb.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := removeObjects(ctx, c, bucket, below(prefix)); err != nil {
			tb.Errorf("remove the objects below %s/%s: %v", bucket, prefix, err)
		}
	})
}

// removeObjects aborts the unfinished multipart uploads below prefix and deletes every version and delete marker there,
// a thousand per request.
func removeObjects(ctx context.Context, c *s3.Client, bucket, prefix string) error {
	if err := abortUploads(ctx, c, bucket, prefix); err != nil {
		return err
	}
	in := &s3.ListObjectVersionsInput{Bucket: aws.String(bucket), Prefix: aws.String(prefix)}
	for {
		page, err := c.ListObjectVersions(ctx, in)
		if err != nil {
			return fmt.Errorf("list the versions: %w", err)
		}
		ids := make([]s3types.ObjectIdentifier, 0, len(page.Versions)+len(page.DeleteMarkers))
		for _, v := range page.Versions {
			ids = append(ids, s3types.ObjectIdentifier{Key: v.Key, VersionId: v.VersionId})
		}
		for _, m := range page.DeleteMarkers {
			ids = append(ids, s3types.ObjectIdentifier{Key: m.Key, VersionId: m.VersionId})
		}
		for len(ids) > 0 {
			n := min(len(ids), 1000)
			out, err := c.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: aws.String(bucket), Delete: &s3types.Delete{Objects: ids[:n], Quiet: aws.Bool(true)}})
			if err != nil {
				return fmt.Errorf("delete the versions: %w", err)
			}
			if len(out.Errors) > 0 {
				e := out.Errors[0]
				return fmt.Errorf("delete the version %s of %s: %s %s (%d of %d failed)",
					aws.ToString(e.VersionId), aws.ToString(e.Key), aws.ToString(e.Code), aws.ToString(e.Message), len(out.Errors), n)
			}
			ids = ids[n:]
		}
		if !aws.ToBool(page.IsTruncated) {
			return nil
		}
		in.KeyMarker, in.VersionIdMarker = page.NextKeyMarker, page.NextVersionIdMarker
	}
}

// abortUploads aborts every unfinished multipart upload below prefix.
func abortUploads(ctx context.Context, c *s3.Client, bucket, prefix string) error {
	in := &s3.ListMultipartUploadsInput{Bucket: aws.String(bucket), Prefix: aws.String(prefix)}
	for {
		page, err := c.ListMultipartUploads(ctx, in)
		if err != nil {
			return fmt.Errorf("list the multipart uploads: %w", err)
		}
		for _, u := range page.Uploads {
			if _, err := c.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String(bucket), Key: u.Key, UploadId: u.UploadId}); err != nil {
				return fmt.Errorf("abort the upload %s of %s: %w", aws.ToString(u.UploadId), aws.ToString(u.Key), err)
			}
		}
		if !aws.ToBool(page.IsTruncated) {
			return nil
		}
		in.KeyMarker, in.UploadIdMarker = page.NextKeyMarker, page.NextUploadIdMarker
	}
}

// Leftovers counts what is below prefix in bucket: object versions, delete markers and unfinished multipart uploads. A
// test of a cleanup expects all three to be zero.
func Leftovers(tb testing.TB, bucket, prefix string) (versions, markers, uploads int) {
	tb.Helper()
	c := objectStore(tb)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	in := &s3.ListObjectVersionsInput{Bucket: aws.String(bucket), Prefix: aws.String(below(prefix))}
	for {
		page, err := c.ListObjectVersions(ctx, in)
		if err != nil {
			tb.Fatalf("list the versions below %s/%s: %v", bucket, prefix, err)
		}
		versions += len(page.Versions)
		markers += len(page.DeleteMarkers)
		if !aws.ToBool(page.IsTruncated) {
			break
		}
		in.KeyMarker, in.VersionIdMarker = page.NextKeyMarker, page.NextVersionIdMarker
	}
	ups, err := c.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{Bucket: aws.String(bucket), Prefix: aws.String(below(prefix))})
	if err != nil {
		tb.Fatalf("list the multipart uploads below %s/%s: %v", bucket, prefix, err)
	}
	return versions, markers, len(ups.Uploads)
}
