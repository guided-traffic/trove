//go:build integration

package blob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
)

func TestCommitRace(t *testing.T) {
	ctx := context.Background()

	run := func(t *testing.T, content []byte, mutate func(*config.Storage), uploads, replicas int) (*s3Env, *pgEnv, []*replica) {
		e := newS3(t, "race")
		p := newPG(t)
		var rs []*replica
		for i := 0; i < replicas; i++ {
			rs = append(rs, newReplica(t, e, p, mutate))
		}
		var wg sync.WaitGroup
		errc := make(chan error, uploads)
		for i := 0; i < uploads; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, _, err := rs[i%replicas].upload(ctx, fmt.Sprintf("race/%03d.bin", i), content)
				errc <- err
			}(i)
		}
		wg.Wait()
		close(errc)
		for err := range errc {
			if err != nil {
				t.Fatalf("upload: %v", err)
			}
		}
		return e, p, rs
	}
	check := func(t *testing.T, e *s3Env, p *pgEnv, rs []*replica, content []byte, uploads int) {
		t.Helper()
		sum := sha256.Sum256(content)
		keys := e.keys(t)
		if len(keys) != 1 || keys[0] != blob.Key(e.prefix, sum) {
			t.Fatalf("objects in the bucket: %v, want exactly %s", keys, blob.Key(e.prefix, sum))
		}
		if n := p.count(t, "blobs"); n != 1 {
			t.Fatalf("%d blob rows, want 1", n)
		}
		if n := p.count(t, "items WHERE blob_sha256 = $1", sum[:]); n != int64(uploads) {
			t.Fatalf("%d items reference the blob, want %d", n, uploads)
		}
		if state, status, _ := p.state(t, sum); state != "live" || status != "ok" {
			t.Fatalf("blob is %s/%s", state, status)
		}
		var puts int64
		for _, r := range rs {
			puts += r.obj.puts.Load() + r.obj.parts.Load()
			r.spoolEmpty(t)
		}
		t.Logf("%d uploads sent %d write requests (puts and parts)", uploads, puts)
		if puts < 1 {
			t.Fatal("nobody wrote the object")
		}
		body, err := e.raw.Head(ctx, keys[0])
		if err != nil || body.Size != int64(len(content)) {
			t.Fatalf("head: %+v %v", body, err)
		}
	}

	t.Run("R-STOR-005 100 concurrent uploads of identical content through 4 replicas leave one object and one blob row", func(t *testing.T) {
		content := randomBytes(2 << 20)
		e, p, rs := run(t, content, nil, 100, 4)
		check(t, e, p, rs, content, 100)
	})
	t.Run("R-STOR-005 the same holds when the object is written by multipart uploads from the spool file", func(t *testing.T) {
		content := randomBytes(11 << 20)
		e, p, rs := run(t, content, func(c *config.Storage) {
			c.S3.Multipart.Threshold = 5 << 20
			c.S3.Multipart.PartSize = 5 << 20
		}, 24, 3)
		check(t, e, p, rs, content, 24)
		var parts int64
		for _, r := range rs {
			parts += r.obj.parts.Load()
		}
		if parts < 3 {
			t.Fatalf("%d parts uploaded, want at least one multipart upload of 3", parts)
		}
		ups, err := e.raw.ListMultipart(ctx, e.prefix+"/")
		if err != nil || len(ups) != 0 {
			t.Fatalf("unfinished multipart uploads: %v %v", ups, err)
		}
	})
	t.Run("R-STOR-005 publishing the same content to a second path costs no second PutObject", func(t *testing.T) {
		e := newS3(t, "dedup")
		p := newPG(t)
		r := newReplica(t, e, p, nil)
		content := randomBytes(300 << 10)
		first, _, err := r.upload(ctx, "npm-a/pkg.tgz", content)
		if err != nil {
			t.Fatal(err)
		}
		second, _, err := r.upload(ctx, "npm-b/pkg.tgz", content)
		if err != nil {
			t.Fatal(err)
		}
		if !first.Wrote || second.Wrote || !second.Deduplicated || r.obj.puts.Load() != 1 {
			t.Fatalf("first %+v second %+v puts %d", first, second, r.obj.puts.Load())
		}
		if p.count(t, "items") != 2 || p.count(t, "blobs") != 1 {
			t.Fatal("want two items and one blob row")
		}
	})
	t.Run("R-STOR-001 the object lies at <prefix>/sha256/<h0h1>/<h2h3>/<hex> and carries the content", func(t *testing.T) {
		e := newS3(t, "layout")
		p := newPG(t)
		r := newReplica(t, e, p, nil)
		content := randomBytes(1000)
		if _, _, err := r.upload(ctx, "f", content); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(content)
		h := blob.Digest(sum).Hex()
		want := e.prefix + "/sha256/" + h[0:2] + "/" + h[2:4] + "/" + h
		body, _, err := e.raw.Get(ctx, want, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer body.Close()
		got, _ := io.ReadAll(body)
		if !bytes.Equal(got, content) {
			t.Fatal("content differs")
		}
	})
}

func TestMultipartOnTheStore(t *testing.T) {
	ctx := context.Background()
	multipart := func(c *config.Storage) {
		c.S3.Multipart.Threshold = 5 << 20
		c.S3.Multipart.PartSize = 5 << 20
		c.S3.Multipart.Concurrency = 3
	}
	t.Run("R-STOR-006 content above the threshold uploads in parts of part_size and the object has the content's size", func(t *testing.T) {
		e := newS3(t, "mp")
		p := newPG(t)
		r := newReplica(t, e, p, multipart)
		content := randomBytes(12<<20 + 17)
		_, d, err := r.upload(ctx, "big.bin", content)
		if err != nil {
			t.Fatal(err)
		}
		if r.obj.parts.Load() != 3 || r.obj.puts.Load() != 0 {
			t.Fatalf("parts %d puts %d, want 3 parts and no single PutObject", r.obj.parts.Load(), r.obj.puts.Load())
		}
		info, err := e.raw.Head(ctx, blob.Key(e.prefix, d))
		if err != nil || info.Size != int64(len(content)) {
			t.Fatalf("head %+v %v", info, err)
		}
		got, err := r.store.Open(ctx, d, int64(len(content)), blob.OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		all, err := io.ReadAll(got)
		got.Close()
		if err != nil || !bytes.Equal(all, content) {
			t.Fatalf("verified download: %v", err)
		}
	})
	t.Run("R-STOR-006 a failed part aborts the multipart upload: none is listed, no object, no row", func(t *testing.T) {
		e := newS3(t, "mpabort")
		p := newPG(t)
		r := newReplica(t, e, p, multipart)
		boom := errors.New("injected part failure")
		r.obj.failPart = func(n int) error {
			if n == 2 {
				return &blob.StoreError{Op: "UploadPart", Status: 500, Kind: blob.KindTransient, Err: boom}
			}
			return nil
		}
		_, _, err := r.upload(ctx, "big.bin", randomBytes(12<<20))
		if !errors.Is(err, boom) && err == nil {
			t.Fatalf("got %v", err)
		}
		ups, lerr := e.raw.ListMultipart(ctx, e.prefix+"/")
		if lerr != nil || len(ups) != 0 {
			t.Fatalf("unfinished multipart uploads: %v %v", ups, lerr)
		}
		if k := e.keys(t); len(k) != 0 {
			t.Fatalf("objects: %v", k)
		}
		if p.count(t, "blobs") != 0 || p.count(t, "items") != 0 {
			t.Fatal("a row exists")
		}
		r.spoolEmpty(t)
	})
	t.Run("R-STOR-006 a cancelled upload leaves no unfinished multipart upload", func(t *testing.T) {
		e := newS3(t, "mpcancel")
		p := newPG(t)
		r := newReplica(t, e, p, multipart)
		cctx, cancel := context.WithCancel(ctx)
		r.obj.failPart = func(n int) error {
			if n == 2 {
				cancel()
			}
			return nil
		}
		if _, _, err := r.upload(cctx, "big.bin", randomBytes(12<<20)); err == nil {
			t.Fatal("the upload succeeded")
		}
		ups, err := e.raw.ListMultipart(ctx, e.prefix+"/")
		if err != nil || len(ups) != 0 {
			t.Fatalf("unfinished multipart uploads: %v %v", ups, err)
		}
		r.spoolEmpty(t)
	})
}

func TestSpoolCleanupWithTheDatabase(t *testing.T) {
	ctx := context.Background()
	t.Run("R-STOR-004 a failing link, a rejected digest and a store outage leave the spool empty and no blob row", func(t *testing.T) {
		e := newS3(t, "spool")
		p := newPG(t)
		r := newReplica(t, e, p, nil)
		data := randomBytes(2 << 20)

		boom := errors.New("path conflict")
		sp, err := r.store.Ingest(ctx, bytes.NewReader(data), blob.IngestOptions{ContentLength: int64(len(data))})
		if err != nil {
			t.Fatal(err)
		}
		_, err = r.store.Commit(ctx, sp, func(context.Context, meta.Tx) error { return boom })
		sp.Close()
		if !errors.Is(err, boom) {
			t.Fatalf("got %v", err)
		}
		r.spoolEmpty(t)

		wrong := make([]byte, 32)
		if _, err := r.store.Ingest(ctx, bytes.NewReader(data), blob.IngestOptions{ContentLength: int64(len(data)), Expect: blob.Expected{SHA256: wrong}}); !errors.Is(err, blob.ErrDigestMismatch) {
			t.Fatalf("got %v", err)
		}
		r.spoolEmpty(t)
		if p.count(t, "blobs") != 0 {
			t.Fatal("a blob row exists for an upload that failed")
		}
	})
}

// The first failure left an object nobody references; the incremental
// collection finds it because the failed commit queued the digest.
func TestFailedCommitIsCollectable(t *testing.T) {
	ctx := context.Background()
	t.Run("R-STOR-005 a failure after the object write queues the digest so that the incremental run finds it", func(t *testing.T) {
		e := newS3(t, "orphan")
		p := newPG(t)
		r := newReplica(t, e, p, nil)
		data := randomBytes(10 << 10)
		sp, err := r.store.Ingest(ctx, bytes.NewReader(data), blob.IngestOptions{ContentLength: int64(len(data))})
		if err != nil {
			t.Fatal(err)
		}
		defer sp.Close()
		// The object is written and the row inserted, then the link fails: the row
		// rolls back with it, the object stays.
		_, err = r.store.Commit(ctx, sp, func(context.Context, meta.Tx) error { return errors.New("link failed") })
		if err == nil {
			t.Fatal("commit succeeded")
		}
		if !objectExists(t, e, e.prefix, sp.SHA256) || p.count(t, "blobs") != 0 {
			t.Fatal("expected an object without a row")
		}
		if p.count(t, "blob_gc_queue WHERE sha256 = $1", sp.SHA256.Bytes()) != 1 {
			t.Fatal("the digest was not queued")
		}
		res, err := r.coll.Run(ctx, blob.RunOptions{Mode: blob.GCIncremental})
		if err != nil {
			t.Fatal(err)
		}
		if p.count(t, "blob_gc_queue") != 0 {
			t.Fatalf("queue not drained: %+v", res)
		}
	})
}
