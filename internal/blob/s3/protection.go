package s3

import (
	"context"
	"errors"
	"fmt"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// ErrProtectionUnknown is returned by the bucket-protection reads when the credential may not read the setting (R-HA-023:
// "otherwise unknown"). The runtime credential deliberately holds no bucket-level rights, so this is the common answer.
var ErrProtectionUnknown = errors.New("s3: the credential may not read this bucket setting")

// VersioningStatus returns the versioning state of the blob bucket (GetBucketVersioning): "Enabled", "Suspended", or "" for a
// bucket that never had versioning.
func (c *Client) VersioningStatus(ctx context.Context) (string, error) {
	out, err := c.api.GetBucketVersioning(ctx, &awss3.GetBucketVersioningInput{Bucket: &c.bucket})
	if err != nil {
		return "", protectionErr("GetBucketVersioning", err)
	}
	return string(out.Status), nil
}

// NoncurrentExpiryDays returns the shortest period after which an enabled lifecycle rule expires noncurrent versions
// (GetBucketLifecycleConfiguration). limited is false when no enabled rule expires them, so deleted objects are kept.
func (c *Client) NoncurrentExpiryDays(ctx context.Context) (days int, limited bool, err error) {
	out, err := c.api.GetBucketLifecycleConfiguration(ctx, &awss3.GetBucketLifecycleConfigurationInput{Bucket: &c.bucket})
	if err != nil {
		var ae smithy.APIError
		if errors.As(err, &ae) && ae.ErrorCode() == "NoSuchLifecycleConfiguration" {
			return 0, false, nil
		}
		return 0, false, protectionErr("GetBucketLifecycleConfiguration", err)
	}
	for _, r := range out.Rules {
		if r.Status != types.ExpirationStatusEnabled || r.NoncurrentVersionExpiration == nil || r.NoncurrentVersionExpiration.NoncurrentDays == nil {
			continue
		}
		d := int(*r.NoncurrentVersionExpiration.NoncurrentDays)
		if !limited || d < days {
			days, limited = d, true
		}
	}
	return days, limited, nil
}

func protectionErr(op string, err error) error {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "AccessDenied", "AllAccessDisabled", "Forbidden", "NotImplemented", "MethodNotAllowed", "XNotImplemented":
			return fmt.Errorf("%s: %w", op, ErrProtectionUnknown)
		}
	}
	return fmt.Errorf("%s: %w", op, err)
}
