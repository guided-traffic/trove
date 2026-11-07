//go:build integration

package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/backup/target"
	"github.com/guided-traffic/trove/internal/backup/target/targettest"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

const testPart = 5 << 20

func randomID(tb testing.TB) string {
	tb.Helper()
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		tb.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}

// targetConfig is backup configuration for bucket, with the stack's static credential in files of the test's own.
func targetConfig(tb testing.TB, bucket, sse string) config.Backup {
	tb.Helper()
	dir := tb.TempDir()
	write := func(name, v string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(v+"\n"), 0o600); err != nil {
			tb.Fatal(err)
		}
		return p
	}
	var b config.Backup
	b.Target = config.BackupTarget{
		Endpoint: testenv.Get(tb, "TROVE_TEST_S3_ENDPOINT"), Region: testenv.Get(tb, "TROVE_TEST_S3_REGION"), Bucket: bucket,
		Prefix: "it-backup/" + randomID(tb), ForcePathStyle: testenv.Get(tb, "TROVE_TEST_S3_FORCE_PATH_STYLE") == "true",
		CAFile: testenv.CAFile(tb),
		Credentials: config.StorageS3Credentials{
			Source:              "static",
			AccessKeyIDFile:     write("id", testenv.Get(tb, "TROVE_TEST_S3_ACCESS_KEY")),
			SecretAccessKeyFile: write("secret", testenv.Get(tb, "TROVE_TEST_S3_SECRET_KEY")),
		},
		SSE: config.BackupSSE{Mode: sse, BucketKey: true},
	}
	b.Database.PartSize = testPart
	b.Copy.PartSize = testPart
	return b
}

func newTarget(tb testing.TB, cfg config.Backup, opts ...s3.TargetOption) *s3.Target {
	tb.Helper()
	f := outbound.New(config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true}})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	t, err := s3.NewTarget(ctx, cfg, f, opts...)
	if err != nil {
		tb.Fatal(err)
	}
	return t
}

// cleanup removes everything below the prefix when the test ends.
func cleanup(tb testing.TB, s target.Store, prefix string) {
	tb.Helper()
	tb.Cleanup(func() {
		var keys []string
		_ = s.List(context.Background(), prefix, func(o target.Object) error { keys = append(keys, o.Key); return nil })
		if len(keys) > 0 {
			_ = s.Delete(context.Background(), keys)
		}
	})
}

// workingSSE is "s3" when the stack's store accepts SSE-S3 and reports it, otherwise "none". SeaweedFS without a KMS
// answers an SSE-S3 write with a 500; the local profile sets the mode to none for it (R-BAK-026), and a store that does
// encrypt (MinIO with a key, AWS) runs the same tests with "s3".
func workingSSE(tb testing.TB) string {
	tb.Helper()
	sseOnce.Do(func() { sseMode = probeSSE(tb) })
	return sseMode
}

var (
	sseOnce sync.Once
	sseMode string
)

func probeSSE(tb testing.TB) string {
	tb.Helper()
	cfg := targetConfig(tb, testenv.Get(tb, "TROVE_TEST_S3_BUCKET_BACKUPS"), "s3")
	cfg.Target.Prefix += "-probe"
	s := newTarget(tb, cfg)
	cleanup(tb, s, cfg.Target.Prefix+"/")
	o, err := s.Probe(context.Background(), cfg.Target.Prefix+"/probe")
	if err != nil || o.SSE != "AES256" {
		tb.Logf("the store does not do SSE-S3 (probe: %+v, %v): the tests use mode none", o, err)
		return "none"
	}
	return "s3"
}

func TestR_BAK_002_TargetAdapterConformance(t *testing.T) {
	mode := workingSSE(t)
	cfg := targetConfig(t, testenv.Get(t, "TROVE_TEST_S3_BUCKET_BACKUPS"), mode)
	s := newTarget(t, cfg)
	prefix := cfg.Target.Prefix + "/"
	cleanup(t, s, prefix)
	want := ""
	if mode == "s3" {
		want = "AES256"
	}
	targettest.Conformance(t, s, targettest.Options{Prefix: prefix, PartSize: testPart, SSE: want})
}

func TestR_BAK_026_TargetSSE(t *testing.T) {
	cfg := targetConfig(t, testenv.Get(t, "TROVE_TEST_S3_BUCKET_BACKUPS"), "s3")
	s := newTarget(t, cfg)
	prefix := cfg.Target.Prefix + "/"
	cleanup(t, s, prefix)
	ctx := context.Background()

	t.Run("R-BAK-026 the preflight either reports the encryption or fails naming backup.target.sse.mode", func(t *testing.T) {
		o, err := s.Probe(ctx, prefix+"probe")
		if err != nil {
			if !strings.Contains(err.Error(), "backup.target.sse.mode") {
				t.Fatalf("the probe error does not name the key: %v", err)
			}
			return
		}
		cerr := s.CheckSSE(o)
		switch {
		case o.SSE == "AES256" && cerr != nil:
			t.Fatalf("CheckSSE = %v", cerr)
		case o.SSE != "AES256" && (cerr == nil || !strings.Contains(cerr.Error(), "backup.target.sse.mode")):
			t.Fatalf("a store that reports SSE %q must fail the check naming the key, got %v", o.SSE, cerr)
		}
	})
	t.Run("R-BAK-026 mode none accepts a store without encryption", func(t *testing.T) {
		none := newTarget(t, targetConfig(t, testenv.Get(t, "TROVE_TEST_S3_BUCKET_BACKUPS"), "none"))
		o, err := none.Probe(ctx, prefix+"probe-none")
		if err != nil {
			t.Fatal(err)
		}
		if err := none.CheckSSE(o); err != nil {
			t.Fatal(err)
		}
		if o.SSE != "" {
			t.Fatalf("mode none sent encryption headers: %q", o.SSE)
		}
	})
	t.Run("R-BAK-026 a plain-http endpoint is refused without allow_insecure_endpoint", func(t *testing.T) {
		bad := targetConfig(t, "b", "s3")
		bad.Target.Endpoint = "http://127.0.0.1:1"
		f := outbound.New(config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true}})
		if _, err := s3.NewTarget(ctx, bad, f); err == nil {
			t.Fatal("http endpoint accepted")
		}
	})
}

func TestR_BAK_015_TargetOwnCredentials(t *testing.T) {
	cfg := targetConfig(t, testenv.Get(t, "TROVE_TEST_S3_BUCKET_BACKUPS"), "none")
	ctx := context.Background()
	// The credential comes from backup.target.credentials only: with another secret in the target's file, the target fails
	// while the process environment holds working keys.
	t.Setenv("AWS_ACCESS_KEY_ID", testenv.Get(t, "TROVE_TEST_S3_ACCESS_KEY"))
	t.Setenv("AWS_SECRET_ACCESS_KEY", testenv.Get(t, "TROVE_TEST_S3_SECRET_KEY"))
	if err := os.WriteFile(cfg.Target.Credentials.SecretAccessKeyFile, []byte("not-the-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newTarget(t, cfg)
	time.Sleep(1100 * time.Millisecond) // the credential files are re-read every second
	if _, err := s.Put(ctx, cfg.Target.Prefix+"/x", []byte("x"), target.PutOptions{}); err == nil {
		t.Fatal("the target used a credential other than backup.target.credentials")
	}
}

func TestR_BAK_002_TargetConditionalCreateConcurrent(t *testing.T) {
	cfg := targetConfig(t, testenv.Get(t, "TROVE_TEST_S3_BUCKET_BACKUPS"), "none")
	s := newTarget(t, cfg)
	prefix := cfg.Target.Prefix + "/"
	cleanup(t, s, prefix)
	ctx := context.Background()
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Put(ctx, prefix+"trove-backup-target.json", []byte(fmt.Sprintf(`{"writer":%d}`, i)), target.PutOptions{IfAbsent: true})
			if err == nil {
				mu.Lock()
				won++
				mu.Unlock()
			} else if !errors.Is(err, target.ErrExists) {
				t.Errorf("err = %v", err)
			}
		}()
	}
	wg.Wait()
	// Where the store supports conditional writes exactly one writer wins; without them the HEAD fallback is not atomic, and
	// the lane does not depend on it (the one-run rule is held in PostgreSQL).
	if won < 1 {
		t.Fatal("nobody created the object")
	}
	t.Logf("%d of 4 concurrent conditional creates succeeded", won)
}

func TestR_BAK_002_TargetBatchedDeleteAndPaging(t *testing.T) {
	cfg := targetConfig(t, testenv.Get(t, "TROVE_TEST_S3_BUCKET_BACKUPS"), "none")
	s := newTarget(t, cfg)
	prefix := cfg.Target.Prefix + "/many/"
	cleanup(t, s, prefix)
	ctx := context.Background()
	const n = 1100
	keys := make([]string, n)
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for i := range n {
		keys[i] = fmt.Sprintf("%s%05d", prefix, i)
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if _, err := s.Put(ctx, keys[i], []byte("x"), target.PutOptions{}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	var got []string
	if err := s.List(ctx, prefix, func(o target.Object) error { got = append(got, o.Key); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("listed %d keys across pages, want %d", len(got), n)
	}
	for i := range got {
		if got[i] != keys[i] {
			t.Fatalf("key %d is %s, want %s", i, got[i], keys[i])
		}
	}
	if err := s.Delete(ctx, append(keys, prefix+"never-existed")); err != nil {
		t.Fatalf("a delete of %d keys, one of them missing: %v", n+1, err)
	}
	left := 0
	_ = s.List(ctx, prefix, func(target.Object) error { left++; return nil })
	if left != 0 {
		t.Fatalf("%d objects left", left)
	}
}

func TestR_BAK_006_TargetServerSideCopy(t *testing.T) {
	cfg := targetConfig(t, testenv.Get(t, "TROVE_TEST_S3_BUCKET_BACKUPS"), workingSSE(t))
	// A single-copy limit of 6 MiB with 5 MiB parts makes a 13 MiB object take UploadPartCopy with three parts.
	s := newTarget(t, cfg, s3.WithSingleCopyLimit(6<<20), s3.WithCopyPartSize(testPart))
	prefix := cfg.Target.Prefix + "/"
	cleanup(t, s, prefix)
	ctx := context.Background()

	srcCfg := targetConfig(t, testenv.Get(t, "TROVE_TEST_S3_BUCKET_BLOBS"), "none")
	src := newTarget(t, srcCfg)
	cleanup(t, src, srcCfg.Target.Prefix+"/")
	ref := func(key string, size int) target.ObjectRef {
		return target.ObjectRef{Endpoint: testenv.Get(t, "TROVE_TEST_S3_ENDPOINT"), Bucket: src.Bucket(), Key: key, Size: int64(size)}
	}

	for _, size := range []int{1000, 13<<20 + 123} {
		t.Run(fmt.Sprintf("R-BAK-006 %d bytes", size), func(t *testing.T) {
			data := make([]byte, size)
			_, _ = rand.Read(data)
			skey := fmt.Sprintf("%s/src-%d", srcCfg.Target.Prefix, size)
			if _, err := src.Put(ctx, skey, data, target.PutOptions{}); err != nil {
				t.Fatal(err)
			}
			dkey := fmt.Sprintf("%spool/copy-%d", prefix, size)
			if err := s.Copy(ctx, ref(skey, size), dkey); err != nil {
				t.Fatal(err)
			}
			rc, o, err := s.Get(ctx, dkey)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rc.Close() }()
			got, err := io.ReadAll(rc)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, data) || o.Size != int64(size) {
				t.Fatalf("the copy differs: %d bytes, want %d", len(got), size)
			}
			h, err := s.Head(ctx, dkey)
			if err != nil {
				t.Fatal(err)
			}
			// R-BAK-026: the copy request repeats the encryption settings, so it reports what a plain write reports.
			plain, err := s.Probe(ctx, prefix+"probe")
			if err != nil {
				t.Fatal(err)
			}
			if h.SSE != plain.SSE {
				t.Fatalf("a copy reports SSE %q, a write %q", h.SSE, plain.SSE)
			}
		})
	}
	t.Run("R-BAK-006 a missing source is ErrNotFound", func(t *testing.T) {
		if err := s.Copy(ctx, ref(srcCfg.Target.Prefix+"/nope", 10), prefix+"pool/nope"); !errors.Is(err, target.ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-BAK-006 a source on another endpoint falls back to streaming", func(t *testing.T) {
		r := ref("k", 10)
		r.Endpoint = "https://other.invalid"
		if err := s.Copy(ctx, r, prefix+"pool/x"); !errors.Is(err, target.ErrServerCopyUnavailable) {
			t.Fatalf("err = %v", err)
		}
	})
}
