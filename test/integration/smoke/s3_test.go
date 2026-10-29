//go:build integration

package smoke

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

func s3Client(t *testing.T) (*s3.Client, testenv.S3) {
	t.Helper()
	cfg := testenv.S3Config(t)
	return s3.New(s3.Options{
		Region:       cfg.Region,
		BaseEndpoint: aws.String(cfg.Endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		UsePathStyle: cfg.ForcePathStyle,
	}), cfg
}

func randomKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("smoke/%x", b)
}

func TestS3PutGet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client, cfg := s3Client(t)

	for _, bucket := range []string{cfg.BlobsBucket, cfg.BackupsBucket} {
		t.Run(bucket, func(t *testing.T) {
			key := randomKey(t)
			body := []byte("trove integration smoke " + key)
			if _, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(body)}); err != nil {
				t.Fatalf("PutObject: %v", err)
			}
			t.Cleanup(func() { deleteAllVersions(context.Background(), client, bucket, key) })

			out, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
			if err != nil {
				t.Fatalf("GetObject: %v", err)
			}
			defer out.Body.Close()
			got, err := io.ReadAll(out.Body)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if !bytes.Equal(got, body) {
				t.Fatalf("got %q, want %q", got, body)
			}
		})
	}
}

func TestS3BlobBucketIsVersioned(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client, cfg := s3Client(t)

	v, err := client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(cfg.BlobsBucket)})
	if err != nil {
		t.Fatalf("GetBucketVersioning: %v", err)
	}
	if v.Status != s3types.BucketVersioningStatusEnabled {
		t.Fatalf("versioning of %s = %q, want Enabled", cfg.BlobsBucket, v.Status)
	}

	key := randomKey(t)
	t.Cleanup(func() { deleteAllVersions(context.Background(), client, cfg.BlobsBucket, key) })
	for _, body := range []string{"first", "second"} {
		if _, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(cfg.BlobsBucket), Key: aws.String(key), Body: bytes.NewReader([]byte(body))}); err != nil {
			t.Fatalf("PutObject: %v", err)
		}
	}
	list, err := client.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{Bucket: aws.String(cfg.BlobsBucket), Prefix: aws.String(key)})
	if err != nil {
		t.Fatalf("ListObjectVersions: %v", err)
	}
	if len(list.Versions) != 2 {
		t.Fatalf("object has %d versions after two puts, want 2", len(list.Versions))
	}
}

func TestS3RejectsWrongCredentials(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg := testenv.S3Config(t)
	bad := s3.New(s3.Options{
		Region:       cfg.Region,
		BaseEndpoint: aws.String(cfg.Endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKey, "not-the-secret", ""),
		UsePathStyle: cfg.ForcePathStyle,
	})
	if _, err := bad.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(cfg.BlobsBucket), Key: aws.String("smoke/denied"), Body: bytes.NewReader([]byte("x"))}); err == nil {
		t.Fatal("a write with a wrong secret key succeeded: the gateway does not enforce credentials")
	}
}

// deleteAllVersions removes every version and delete marker of a key.
func deleteAllVersions(ctx context.Context, c *s3.Client, bucket, key string) {
	list, err := c.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{Bucket: aws.String(bucket), Prefix: aws.String(key)})
	if err != nil {
		return
	}
	for _, v := range list.Versions {
		_, _ = c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: v.Key, VersionId: v.VersionId})
	}
	for _, m := range list.DeleteMarkers {
		_, _ = c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: m.Key, VersionId: m.VersionId})
	}
	_, _ = c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
}
