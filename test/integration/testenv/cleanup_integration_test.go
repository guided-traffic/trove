//go:build integration

package testenv

import (
	"bytes"
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/guided-traffic/trove/internal/blob"
)

// A stack leaves nothing in the shared blob bucket when its test ends (F-113): not its objects, not the noncurrent
// versions and delete markers that the bucket's versioning keeps after a deletion, not an unfinished multipart upload.
func TestStackRemovesItsObjectsAtCleanup(t *testing.T) {
	bucket := S3Config(t).BlobsBucket
	var prefix string
	t.Run("the stack writes", func(t *testing.T) {
		s := NewStack(t, StackOptions{Name: "cleanup"})
		prefix = s.ObjectPrefix()
		kept, gone := sha256.Sum256([]byte("kept")), sha256.Sum256([]byte("gone"))
		s.PutObject(t, kept[:], []byte("kept"))
		s.PutObject(t, gone[:], []byte("gone"))
		s.PutObject(t, gone[:], []byte("gone")) // a second version of the same key
		s.DeleteObject(t, gone[:])              // a delete marker; both versions stay below it

		c := objectStore(t)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		key := blob.Key(prefix, blob.Digest(sha256.Sum256([]byte("unfinished"))))
		up, err := c.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String(key)})
		if err != nil {
			t.Fatalf("start a multipart upload: %v", err)
		}
		part := bytes.Repeat([]byte{7}, 5<<20)
		if _, err := c.UploadPart(ctx, &s3.UploadPartInput{
			Bucket: aws.String(bucket), Key: aws.String(key), UploadId: up.UploadId, PartNumber: aws.Int32(1), Body: bytes.NewReader(part),
		}); err != nil {
			t.Fatalf("upload a part: %v", err)
		}

		v, m, u := Leftovers(t, bucket, prefix)
		if v < 3 || m < 1 || u < 1 {
			t.Fatalf("below %s before the cleanup: %d versions, %d delete markers, %d uploads; want at least 3, 1, 1", prefix, v, m, u)
		}
	})
	if t.Failed() {
		return
	}
	if v, m, u := Leftovers(t, bucket, prefix); v+m+u != 0 {
		t.Fatalf("below %s after the stack's cleanup: %d versions, %d delete markers, %d unfinished uploads; want none", prefix, v, m, u)
	}
}

// The removal takes only what is below its own prefix: a prefix that begins with the same characters keeps its objects.
func TestRemoveObjectsAtCleanupKeepsANeighbour(t *testing.T) {
	bucket := S3Config(t).BlobsBucket
	base := "it-cleanup/" + RandHex(5)
	c := objectStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	put := func(key string) {
		t.Helper()
		if _, err := c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader([]byte(key))}); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
	}
	RemoveObjectsAtCleanup(t, bucket, base+"x") // the neighbour's own removal, after the check below
	t.Run("one prefix is removed", func(t *testing.T) {
		RemoveObjectsAtCleanup(t, bucket, base)
		put(base + "/a")
		put(base + "x/b")
	})
	if v, m, u := Leftovers(t, bucket, base); v+m+u != 0 {
		t.Fatalf("below %s after its cleanup: %d versions, %d delete markers, %d uploads; want none", base, v, m, u)
	}
	if v, _, _ := Leftovers(t, bucket, base+"x"); v != 1 {
		t.Fatalf("below the neighbour %sx: %d versions, want 1", base, v)
	}
}
