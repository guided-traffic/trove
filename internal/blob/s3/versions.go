package s3

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
)

// ObjectVersion is one version of a key in a versioned bucket, or a delete marker.
type ObjectVersion struct {
	ID string
	// Marker is true for a delete marker.
	Marker bool
	// Latest is true for the version that a plain read of the key sees.
	Latest   bool
	Modified time.Time
}

// ListVersions returns every version and delete marker of exactly this key, in no defined order: versions written within one
// second carry the same time. It needs a credential that may list object versions (s3:ListBucketVersions), which the runtime credential
// deliberately lacks (R-HA-023); a bucket without versioning answers with the one current object.
func (c *Client) ListVersions(ctx context.Context, key string) ([]ObjectVersion, error) {
	var out []ObjectVersion
	var keyMarker, versionMarker *string
	for {
		page, err := c.api.ListObjectVersions(ctx, &awss3.ListObjectVersionsInput{
			Bucket: &c.bucket, Prefix: &key, KeyMarker: keyMarker, VersionIdMarker: versionMarker,
		})
		if err != nil {
			return nil, mapError(ctx, "ListObjectVersions", key, err)
		}
		for _, v := range page.Versions {
			if aws.ToString(v.Key) == key {
				out = append(out, ObjectVersion{ID: aws.ToString(v.VersionId), Latest: aws.ToBool(v.IsLatest), Modified: aws.ToTime(v.LastModified)})
			}
		}
		for _, m := range page.DeleteMarkers {
			if aws.ToString(m.Key) == key {
				out = append(out, ObjectVersion{ID: aws.ToString(m.VersionId), Marker: true, Latest: aws.ToBool(m.IsLatest), Modified: aws.ToTime(m.LastModified)})
			}
		}
		if !aws.ToBool(page.IsTruncated) {
			return out, nil
		}
		keyMarker, versionMarker = page.NextKeyMarker, page.NextVersionIdMarker
	}
}

// DeleteVersion removes one version of a key for good; for a delete marker that makes the object below it visible again. It
// needs s3:DeleteObjectVersion, which the runtime credential deliberately lacks (R-HA-023).
func (c *Client) DeleteVersion(ctx context.Context, key, versionID string) error {
	_, err := c.api.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: &c.bucket, Key: &key, VersionId: &versionID})
	if err != nil {
		return mapError(ctx, "DeleteObject", key, err)
	}
	return nil
}
