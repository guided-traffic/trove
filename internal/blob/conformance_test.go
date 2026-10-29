package blob_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/blob/blobtest"
)

func conformanceOpts(m *blobtest.Store) blob.ConformanceOptions {
	return blob.ConformanceOptions{Prefix: "trove", Replica: "unit", HTTP: &http.Client{Transport: m}}
}

func TestConformance(t *testing.T) {
	t.Run("R-STOR-035 every check passes against a store that meets the assumptions", func(t *testing.T) {
		m := blobtest.New()
		res := blob.RunConformance(context.Background(), m, conformanceOpts(m))
		if len(res) != len(blob.ConformanceChecks()) {
			t.Fatalf("%d results", len(res))
		}
		for _, r := range res {
			if !r.OK {
				t.Errorf("%s: %s", r.Name, r.Error)
			}
		}
		if keys := m.Keys(""); len(keys) != 0 || m.OpenUploads() != 0 {
			t.Fatalf("the checks left %v and %d uploads", keys, m.OpenUploads())
		}
	})
	t.Run("R-STOR-035 the checks change nothing outside _probe/", func(t *testing.T) {
		m := blobtest.New()
		var outside []string
		m.SetFault(func(op, key string) error {
			if (op == "put" || op == "delete" || op == "create_multipart") && !strings.HasPrefix(key, "trove/_probe/") {
				outside = append(outside, op+" "+key)
			}
			return nil
		})
		blob.RunConformance(context.Background(), m, conformanceOpts(m))
		if len(outside) != 0 {
			t.Fatalf("writes outside the probe prefix: %v", outside)
		}
	})
	t.Run("R-STOR-035 a store without the permission to delete reports exactly that failure", func(t *testing.T) {
		m := blobtest.New()
		m.SetFault(func(op, key string) error {
			if op == "delete" || op == "delete_many" {
				return &blob.StoreError{Op: "DeleteObject", Key: key, Code: "AccessDenied", Status: 403, Kind: blob.KindConfig}
			}
			return nil
		})
		var failed []string
		for _, r := range blob.RunConformance(context.Background(), m, conformanceOpts(m)) {
			if !r.OK {
				failed = append(failed, r.Name)
				if !strings.Contains(r.Error, "AccessDenied") {
					t.Errorf("%s fails for another reason: %s", r.Name, r.Error)
				}
			}
		}
		if len(failed) == 0 {
			t.Fatal("no check reported the missing permission")
		}
		err := blob.StartupCheck(context.Background(), m, "trove", "unit")
		if !blob.IsConfig(err) || !strings.Contains(err.Error(), "delete") {
			t.Fatalf("startup check: %v", err)
		}
	})
	t.Run("R-STOR-035 a store that serves an old version after an overwrite fails A1", func(t *testing.T) {
		m := blobtest.New()
		stale := &staleStore{Store: m}
		for _, r := range blob.RunConformance(context.Background(), stale, conformanceOpts(m)) {
			if strings.Contains(r.Name, "overwrite") && r.OK {
				t.Fatal("a stale read passed the overwrite check")
			}
			if strings.Contains(r.Name, "overwrite") && !strings.Contains(r.Error, "earlier content") {
				t.Fatalf("unexpected error %q", r.Error)
			}
		}
	})
	t.Run("R-STOR-035 DeleteObjects is optional and its failure names the setting", func(t *testing.T) {
		m := blobtest.New()
		m.SetFault(func(op, _ string) error {
			if op == "delete_many" {
				return &blob.StoreError{Op: "DeleteObjects", Code: "NotImplemented", Status: 501, Kind: blob.KindConfig}
			}
			return nil
		})
		for _, r := range blob.RunConformance(context.Background(), m, conformanceOpts(m)) {
			if strings.Contains(r.Name, "DeleteObjects") {
				if r.OK || !r.Optional || !strings.Contains(r.Error, "batch_delete") {
					t.Fatalf("%+v", r)
				}
			} else if !r.OK {
				t.Errorf("%s: %s", r.Name, r.Error)
			}
		}
	})
}

// staleStore returns the first version of every key it ever stored.
type staleStore struct {
	*blobtest.Store
	first map[string][]byte
}

func (s *staleStore) Get(ctx context.Context, key string, r *blob.ByteRange) (io.ReadCloser, blob.ObjectInfo, error) {
	if s.first == nil {
		s.first = map[string][]byte{}
	}
	body, info, err := s.Store.Get(ctx, key, r)
	if err != nil {
		return nil, info, err
	}
	b, _ := io.ReadAll(body)
	if old, ok := s.first[key]; ok {
		b = old
	} else {
		s.first[key] = b
	}
	return io.NopCloser(strings.NewReader(string(b))), info, nil
}

func TestStartupCheck(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fast := blob.WithAwaitBackoff(time.Millisecond, 5*time.Millisecond)
	t.Run("R-STOR-035 the startup check writes, reads back, deletes and lists", func(t *testing.T) {
		m := blobtest.New()
		if err := blob.StartupCheck(context.Background(), m, "trove", "r1"); err != nil {
			t.Fatal(err)
		}
		if m.Count("put") != 1 || m.Count("get") != 1 || m.Count("delete") != 1 || m.Count("list") != 1 || len(m.Keys("")) != 0 {
			t.Fatalf("put=%d get=%d delete=%d list=%d keys=%v", m.Count("put"), m.Count("get"), m.Count("delete"), m.Count("list"), m.Keys(""))
		}
	})
	t.Run("R-STOR-035 a transient error is retried for as long as it lasts and the call does not return", func(t *testing.T) {
		m := blobtest.New()
		var n atomic.Int32
		m.SetFault(func(op, _ string) error {
			if op == "put" && n.Add(1) <= 6 {
				return &blob.StoreError{Op: "PutObject", Status: 503, Kind: blob.KindTransient}
			}
			return nil
		})
		if err := blob.AwaitStartupCheck(context.Background(), m, "trove", "r1", log, fast); err != nil {
			t.Fatal(err)
		}
		if n.Load() != 7 {
			t.Fatalf("%d attempts", n.Load())
		}
	})
	t.Run("R-STOR-035 an open circuit is retried like a transient error", func(t *testing.T) {
		m := blobtest.New()
		var n atomic.Int32
		m.SetFault(func(op, _ string) error {
			if n.Add(1) <= 2 {
				return blob.ErrCircuitOpen
			}
			return nil
		})
		if err := blob.AwaitStartupCheck(context.Background(), m, "trove", "r1", log, fast); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-STOR-035 a configuration error (no such bucket, access denied) ends the wait with that error", func(t *testing.T) {
		for _, code := range []string{"NoSuchBucket", "AccessDenied"} {
			m := blobtest.New()
			m.SetFault(func(op, _ string) error {
				return &blob.StoreError{Op: op, Code: code, Status: 404, Kind: blob.KindConfig}
			})
			err := blob.AwaitStartupCheck(context.Background(), m, "trove", "r1", log, fast)
			if !blob.IsConfig(err) || !strings.Contains(err.Error(), code) {
				t.Fatalf("%s: got %v", code, err)
			}
			if m.Count("put") != 1 {
				t.Fatalf("%s: retried %d times", code, m.Count("put"))
			}
		}
	})
	t.Run("R-STOR-035 a store that returns other bytes than were written stops the replica", func(t *testing.T) {
		m := blobtest.New()
		m.SetGetBody(func(rc io.ReadCloser) io.ReadCloser { rc.Close(); return io.NopCloser(strings.NewReader("other")) })
		err := blob.AwaitStartupCheck(context.Background(), m, "trove", "r1", log, fast)
		if err == nil || !strings.Contains(err.Error(), "differ") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("R-STOR-035 the wait ends when the context does", func(t *testing.T) {
		m := blobtest.New()
		m.SetFault(func(string, string) error { return &blob.StoreError{Kind: blob.KindTransient} })
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		if err := blob.AwaitStartupCheck(ctx, m, "trove", "r1", log, fast); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("got %v", err)
		}
	})
}
