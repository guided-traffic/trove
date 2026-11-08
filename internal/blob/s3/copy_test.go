package s3_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/internal/blob"
)

// copyRecorder answers the requests of a server-side copy the way S3 does and records what it was asked.
type copyRecorder struct {
	mu       sync.Mutex
	copies   []string // x-amz-copy-source of CopyObject
	ranges   []string // x-amz-copy-source-range of UploadPartCopy, in the order received
	parts    []string
	complete int
	missing  bool
}

func (c *copyRecorder) handle(w http.ResponseWriter, r *http.Request) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	q := r.URL.Query()
	xml := func(body string) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>` + body))
	}
	switch {
	case r.Method == http.MethodPut && r.Header.Get("x-amz-copy-source") != "" && !q.Has("partNumber"):
		if c.missing {
			s3Error(w, r, 404, "NoSuchKey")
			return true
		}
		c.copies = append(c.copies, r.Header.Get("x-amz-copy-source"))
		xml(`<CopyObjectResult><ETag>"e"</ETag></CopyObjectResult>`)
	case r.Method == http.MethodPost && q.Has("uploads"):
		xml(`<InitiateMultipartUploadResult><Bucket>b</Bucket><Key>k</Key><UploadId>u1</UploadId></InitiateMultipartUploadResult>`)
	case r.Method == http.MethodPut && q.Has("partNumber"):
		c.parts = append(c.parts, q.Get("partNumber"))
		c.ranges = append(c.ranges, r.Header.Get("x-amz-copy-source-range"))
		xml(`<CopyPartResult><ETag>"p` + q.Get("partNumber") + `"</ETag></CopyPartResult>`)
	case r.Method == http.MethodPost && q.Has("uploadId"):
		c.complete++
		xml(`<CompleteMultipartUploadResult><Bucket>b</Bucket><Key>k</Key><ETag>"c"</ETag></CompleteMultipartUploadResult>`)
	default:
		return false
	}
	return true
}

func TestR_STOR_011_ServerSideCopy(t *testing.T) {
	ctx := context.Background()
	t.Run("an object up to the limit is one CopyObject of the source in the same bucket", func(t *testing.T) {
		e := newEnv(t, nil)
		rec := &copyRecorder{}
		e.fake.fail = rec.handle
		if err := e.client.Copy(ctx, "p/_uploads/s/data", "p/sha256/ab/cd/x", 100); err != nil {
			t.Fatal(err)
		}
		if len(rec.copies) != 1 || rec.complete != 0 {
			t.Fatalf("copies %v, completions %d", rec.copies, rec.complete)
		}
		if !strings.HasPrefix(rec.copies[0], "b/p/_uploads/s/data") && !strings.HasPrefix(rec.copies[0], "/b/p/_uploads/s/data") {
			t.Fatalf("copy source = %q", rec.copies[0])
		}
	})
	t.Run("a larger object is copied in ranges and completed", func(t *testing.T) {
		e := newEnv(t, nil)
		rec := &copyRecorder{}
		e.fake.fail = rec.handle
		e.client.SetCopyLimits(5, 4)
		if err := e.client.Copy(ctx, "src", "dst", 10); err != nil {
			t.Fatal(err)
		}
		if strings.Join(rec.parts, ",") != "1,2,3" || strings.Join(rec.ranges, ",") != "bytes=0-3,bytes=4-7,bytes=8-9" || rec.complete != 1 {
			t.Fatalf("parts %v, ranges %v, completions %d", rec.parts, rec.ranges, rec.complete)
		}
	})
	t.Run("a missing source is not found", func(t *testing.T) {
		e := newEnv(t, nil)
		e.fake.fail = (&copyRecorder{missing: true}).handle
		if err := e.client.Copy(ctx, "gone", "dst", 100); !errors.Is(err, blob.ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
}
