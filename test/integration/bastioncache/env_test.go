//go:build integration

// Package bastioncache tests the Bastion's cache backends against the integration stack's S3 (SeaweedFS, over
// TLS): the bucket backend of R-BAS-030 shared by two replicas, the eviction sweep of R-BAS-031 and the integrity
// of R-BAS-032, and the five S3 operations the backend may use.
//
// Variables read (testenv): TROVE_TEST_S3_ENDPOINT, _REGION, _ACCESS_KEY, _SECRET_KEY, _BUCKET_BACKUPS,
// _FORCE_PATH_STYLE and TROVE_TEST_CA_FILE. Every test works below a prefix of its own in the unversioned backups
// bucket and removes it when it ends; the blobs bucket and the other developers' prefixes are not touched.
package bastioncache

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/guided-traffic/trove/internal/bastion/cache"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

const mib = 1 << 20

// counting is the transport under the bucket's client. It counts what the client sends by S3 operation and
// records every request that is not one of the five operations R-BAS-030 allows, or that uses versioning or a
// conditional write.
type counting struct {
	bucket string

	mu  sync.Mutex
	ops map[string]int
	bad []string

	down atomic.Bool
}

// queryAllowed are the query parameters of the five operations; the SDK adds x-id.
var queryAllowed = map[string]bool{
	"list-type": true, "prefix": true, "continuation-token": true, "max-keys": true, "delimiter": true,
	"start-after": true, "encoding-type": true, "fetch-owner": true, "x-id": true,
}

func (c *counting) classify(r *http.Request) (op string, problem string) {
	q := r.URL.Query()
	for k := range q {
		if !queryAllowed[k] {
			return "", fmt.Sprintf("query parameter %q", k)
		}
	}
	atBucket := strings.Trim(r.URL.Path, "/") == c.bucket
	switch r.Method {
	case http.MethodPut:
		switch {
		case r.Header.Get("X-Amz-Copy-Source") != "":
			return "", "a server-side copy"
		case r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Match") != "":
			return "", "a conditional write"
		case atBucket:
			return "", "a bucket operation"
		}
		return "PutObject", ""
	case http.MethodGet:
		if q.Get("list-type") == "2" {
			return "ListObjectsV2", ""
		}
		if atBucket {
			return "", "a bucket operation"
		}
		return "GetObject", ""
	case http.MethodHead:
		if atBucket {
			return "", "HeadBucket"
		}
		return "HeadObject", ""
	case http.MethodDelete:
		if atBucket {
			return "", "a bucket operation"
		}
		return "DeleteObject", ""
	}
	return "", r.Method + " " + r.URL.Path
}

// tap is the transport of one replica's client; the counts are shared by the replicas of a test.
type tap struct {
	c    *counting
	next http.RoundTripper
}

func (t tap) RoundTrip(r *http.Request) (*http.Response, error) { return t.c.observe(r, t.next) }

func (c *counting) observe(r *http.Request, next http.RoundTripper) (*http.Response, error) {
	op, problem := c.classify(r)
	c.mu.Lock()
	if problem != "" {
		c.bad = append(c.bad, problem+": "+r.Method+" "+r.URL.RequestURI())
	} else {
		c.ops[op]++
	}
	c.mu.Unlock()
	if c.down.Load() {
		return nil, errors.New("the bucket is unreachable (test)")
	}
	return next.RoundTrip(r)
}

func (c *counting) counts() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]int{}
	for k, v := range c.ops {
		out[k] = v
	}
	return out
}

// env is one test's bucket prefix, with a raw client that is neither counted nor part of a replica (to forge,
// replace and clean up).
type env struct {
	t       *testing.T
	cfg     config.BastionCacheBucket
	prefix  string
	factory *outbound.Factory
	raw     *s3.Client
	count   *counting
	signer  *cache.Signer
	creds   config.BastionBucketCredentials
}

func runID(tb testing.TB) string {
	tb.Helper()
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		tb.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}

func newEnv(t *testing.T, name string) *env {
	t.Helper()
	s := testenv.S3Config(t)
	dir := t.TempDir()
	write := func(file, v string) string {
		p := filepath.Join(dir, file)
		if err := os.WriteFile(p, []byte(v+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	e := &env{t: t, prefix: "it-bastioncache/" + runID(t) + "/" + name}
	e.creds = config.BastionBucketCredentials{
		Source: "static", AccessKeyIDFile: write("access-key", s.AccessKey), SecretAccessKeyFile: write("secret-key", s.SecretKey),
	}
	e.cfg = config.BastionCacheBucket{
		Endpoint: s.Endpoint, Region: s.Region, Bucket: s.BackupsBucket, Prefix: e.prefix, ForcePathStyle: s.ForcePathStyle,
		CAFile: testenv.CAFile(t), Credentials: e.creds,
	}
	e.factory = outbound.New(config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true}})
	raw, err := s3.NewFromFactory(context.Background(), e.cfg.StorageS3(), e.factory)
	if err != nil {
		t.Fatalf("raw client: %v", err)
	}
	e.raw = raw
	e.count = &counting{bucket: s.BackupsBucket, ops: map[string]int{}}
	key := make([]byte, cache.MinKeyBytes)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	if e.signer, err = cache.NewSigner(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.removeAll)
	return e
}

// removeAll deletes everything below the test's prefix.
func (e *env) removeAll() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, o := range e.listAll() {
		if err := e.raw.Delete(ctx, e.prefix+"/"+o.Key); err != nil {
			e.t.Logf("clean up %s: %v", o.Key, err)
		}
	}
}

// listAll lists every object below the prefix, with keys relative to it.
func (e *env) listAll() []s3.Object {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var out []s3.Object
	token := ""
	for {
		objs, next, err := e.raw.ListPlain(ctx, e.prefix+"/", token, 1000)
		if err != nil {
			e.t.Logf("list %s: %v", e.prefix, err)
			return out
		}
		for _, o := range objs {
			o.Key = strings.TrimPrefix(o.Key, e.prefix+"/")
			out = append(out, o)
		}
		if next == "" {
			return out
		}
		token = next
	}
}

// replica is one Bastion replica's cache on the shared bucket, with the pod's own temporary directory.
type replica struct {
	*cache.Cache
	m   *metrics.Metrics
	tmp string
}

func (e *env) replica(mod ...func(*config.BastionCache)) *replica {
	e.t.Helper()
	tmp := e.t.TempDir()
	cfg := config.BastionCache{
		Backend: "bucket", MaxBytes: 100 * mib, MaxObjectBytes: 4 * mib, DocumentsMaxBytes: 10 * mib,
		SweepInterval: time.Minute, AccessResolution: time.Hour, Bucket: e.cfg,
	}
	for _, m := range mod {
		m(&cfg)
	}
	met, err := metrics.New(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	c, err := cache.New(context.Background(), cfg, e.signer, cache.Options{
		Metrics: met, Factory: e.factory, TempDir: tmp, WrapTransport: func(rt http.RoundTripper) http.RoundTripper {
			return tap{c: e.count, next: rt}
		},
	})
	if err != nil {
		e.t.Fatalf("replica: %v", err)
	}
	return &replica{Cache: c, m: met, tmp: tmp}
}

func payload(seed uint64, n int) []byte {
	b := make([]byte, n)
	r := mrand.New(mrand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	for i := 0; i < n; i += 8 {
		v := r.Uint64()
		for j := 0; j < 8 && i+j < n; j++ {
			b[i+j] = byte(v >> (8 * j))
		}
	}
	return b
}

func digest(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// put fills, commits and stores the entry of data under cacheKey, the way the engine does on a miss.
func (r *replica) put(t *testing.T, kind cache.EntryKind, cacheKey string, data []byte) cache.Entry {
	t.Helper()
	ctx := context.Background()
	f, err := r.NewFill(kind)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := f.Commit(ctx, digest(data)); err != nil {
		t.Fatal(err)
	}
	e := cache.Entry{
		Kind: kind, SHA256: digest(data), Size: int64(len(data)), Repository: "npm-all", Status: 200,
		Header: http.Header{"Content-Type": {"application/octet-stream"}}, ValidatedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := r.StoreEntry(ctx, cacheKey, e); err != nil {
		t.Fatal(err)
	}
	return e
}

// read does what a hit does: look up, open verified, read to the end.
func (r *replica) read(cacheKey string) ([]byte, error) {
	ctx := context.Background()
	e, _, err := r.LookupEntry(ctx, cacheKey)
	if err != nil {
		return nil, err
	}
	rc, err := r.Open(ctx, cacheKey, e)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	return io.ReadAll(rc)
}

func value(t testing.TB, c interface{ Write(*dto.Metric) error }) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatal(err)
	}
	if m.Counter != nil {
		return m.Counter.GetValue()
	}
	return m.Gauge.GetValue()
}

var layout = regexp.MustCompile(`^(?:(?:sha256|documents)/[0-9a-f]{2}/[0-9a-f]{2}/[0-9a-f]{64}|entries/[0-9a-f]{2}/[0-9a-f]{64}\.json|access/[0-9a-f]{64}|audit-spool/[A-Za-z0-9._-]+/[0-9]+\.jsonl)$`)

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
