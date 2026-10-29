// Command seed creates the buckets of the integration stack: trove-blobs with
// versioning on and trove-backups. It waits for the S3 gateway, which accepts
// connections a few seconds before it can create buckets, and is idempotent.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}

func env(name string) (string, error) {
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("%s is not set; run test/integration/up.sh", name)
	}
	return v, nil
}

func run() error {
	var vals [6]string
	for i, n := range []string{"TROVE_TEST_S3_ENDPOINT", "TROVE_TEST_S3_REGION", "TROVE_TEST_S3_ACCESS_KEY", "TROVE_TEST_S3_SECRET_KEY", "TROVE_TEST_S3_BUCKET_BLOBS", "TROVE_TEST_S3_BUCKET_BACKUPS"} {
		v, err := env(n)
		if err != nil {
			return err
		}
		vals[i] = v
	}
	endpoint, region, access, secret, blobs, backups := vals[0], vals[1], vals[2], vals[3], vals[4], vals[5]

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := s3.New(s3.Options{
		Region:       region,
		BaseEndpoint: aws.String(endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(access, secret, ""),
		UsePathStyle: true,
	})

	for _, b := range []string{blobs, backups} {
		if err := ensureBucket(ctx, client, b); err != nil {
			return err
		}
	}
	if err := ensureVersioning(ctx, client, blobs); err != nil {
		return err
	}
	fmt.Printf("buckets ready: %s (versioning on), %s\n", blobs, backups)
	return nil
}

// retry calls fn until it succeeds or the context ends.
func retry(ctx context.Context, what string, fn func() error) error {
	var last error
	for {
		if last = fn(); last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w (last error: %w)", what, ctx.Err(), last)
		case <-time.After(time.Second):
		}
	}
}

func ensureBucket(ctx context.Context, c *s3.Client, bucket string) error {
	return retry(ctx, "create bucket "+bucket, func() error {
		_, err := c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
		var owned *s3types.BucketAlreadyOwnedByYou
		var exists *s3types.BucketAlreadyExists
		if err == nil || errors.As(err, &owned) || errors.As(err, &exists) {
			return nil
		}
		return err
	})
}

func ensureVersioning(ctx context.Context, c *s3.Client, bucket string) error {
	return retry(ctx, "enable versioning on "+bucket, func() error {
		_, err := c.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
			Bucket:                  aws.String(bucket),
			VersioningConfiguration: &s3types.VersioningConfiguration{Status: s3types.BucketVersioningStatusEnabled},
		})
		if err != nil {
			return err
		}
		out, err := c.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(bucket)})
		if err != nil {
			return err
		}
		if out.Status != s3types.BucketVersioningStatusEnabled {
			return fmt.Errorf("versioning of %s is %q, want Enabled", bucket, out.Status)
		}
		return nil
	})
}
