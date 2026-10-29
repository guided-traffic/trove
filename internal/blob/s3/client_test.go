package s3_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/blob/blobtest"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/observability"
	"github.com/guided-traffic/trove/internal/outbound"
)

// fakeS3 is a minimal path-style S3 endpoint: PUT, GET, HEAD and DELETE of
// objects, with hooks to answer errors.
type fakeS3 struct {
	t       *testing.T
	mu      sync.Mutex
	objects map[string][]byte
	// fail, when set, may answer a request itself; it returns true if it did.
	fail     func(w http.ResponseWriter, r *http.Request) bool
	requests []*http.Request
	keys     []string // access key of every request
	puts     atomic.Int32
}

var credential = regexp.MustCompile(`Credential=([^/]+)/`)

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r)
	if m := credential.FindStringSubmatch(r.Header.Get("Authorization")); m != nil {
		f.keys = append(f.keys, m[1])
	}
	fail := f.fail
	f.mu.Unlock()
	if r.Method == http.MethodPut {
		f.puts.Add(1)
	}
	if fail != nil && fail(w, r) {
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/b/")
	switch r.Method {
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.objects[key] = body
		f.mu.Unlock()
		w.Header().Set("ETag", `"x"`)
	case http.MethodGet, http.MethodHead:
		f.mu.Lock()
		b, ok := f.objects[key]
		f.mu.Unlock()
		if !ok {
			s3Error(w, r, 404, "NoSuchKey")
			return
		}
		w.Header().Set("Content-Length", itoa(len(b)))
		w.Header().Set("ETag", `"x"`)
		if r.Method == http.MethodGet {
			_, _ = w.Write(b)
		}
	case http.MethodDelete:
		f.mu.Lock()
		delete(f.objects, key)
		f.mu.Unlock()
		w.WriteHeader(204)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func s3Error(w http.ResponseWriter, r *http.Request, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>` + code + `</Code><Message>test</Message><RequestId>1</RequestId></Error>`))
	}
}

type env struct {
	srv    *httptest.Server
	fake   *fakeS3
	cfg    config.StorageS3
	client *s3.Client
}

func newEnv(t *testing.T, mutate func(*config.StorageS3)) *env {
	t.Helper()
	fake := &fakeS3{t: t, objects: map[string][]byte{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	write := func(name, v string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(v+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cfg := blobtest.Storage(t, dir).S3
	cfg.Endpoint, cfg.Region, cfg.Bucket = srv.URL, "us-east-1", "b"
	cfg.AllowInsecureEndpoint, cfg.ForcePathStyle = true, true
	cfg.Retry = config.StorageS3Retry{MaxAttempts: 5, MaxBackoff: 20 * time.Millisecond}
	cfg.Credentials = config.StorageS3Credentials{
		Source: "static", AccessKeyIDFile: write("id", "AKIDONE"), SecretAccessKeyFile: write("secret", "secret-one"),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	e := &env{srv: srv, fake: fake, cfg: cfg}
	c, err := s3.NewFromFactory(context.Background(), cfg, outbound.New(config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true}}))
	if err != nil {
		t.Fatal(err)
	}
	e.client = c
	return e
}

func put(c *s3.Client, key string, data []byte, sum blob.Checksum) error {
	return c.Put(context.Background(), key, bytes.NewReader(data), int64(len(data)), sum)
}

func slowDown(n int32) func(http.ResponseWriter, *http.Request) bool {
	var seen atomic.Int32
	return func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPut && seen.Add(1) <= n {
			s3Error(w, r, 503, "SlowDown")
			return true
		}
		return false
	}
}

func TestRetriesAndBreaker(t *testing.T) {
	t.Run("R-STOR-033 two 503 SlowDown answers before a PutObject succeeds make the upload succeed", func(t *testing.T) {
		e := newEnv(t, nil)
		e.fake.fail = slowDown(2)
		if err := put(e.client, "k", []byte("data"), blob.Checksum{}); err != nil {
			t.Fatal(err)
		}
		if e.fake.puts.Load() != 3 {
			t.Fatalf("%d PUT requests, want 3", e.fake.puts.Load())
		}
		if string(e.fake.objects["k"]) != "data" {
			t.Fatal("object not stored")
		}
	})
	t.Run("R-STOR-033 retries stop at max_attempts and the error is transient with the store's code", func(t *testing.T) {
		e := newEnv(t, func(c *config.StorageS3) { c.Retry.MaxAttempts = 3 })
		e.fake.fail = slowDown(100)
		err := put(e.client, "k", []byte("data"), blob.Checksum{})
		var se *blob.StoreError
		if !errors.As(err, &se) || se.Code != "SlowDown" || se.Status != 503 || !blob.IsTransient(err) {
			t.Fatalf("got %#v", err)
		}
		if e.fake.puts.Load() != 3 {
			t.Fatalf("%d attempts, want 3", e.fake.puts.Load())
		}
	})
	t.Run("R-STOR-033 consecutive failures open the circuit and the next download fails without a request", func(t *testing.T) {
		e := newEnv(t, func(c *config.StorageS3) { c.Retry.MaxAttempts = 1 })
		e.fake.fail = func(w http.ResponseWriter, r *http.Request) bool { s3Error(w, r, 500, "InternalError"); return true }
		g := blob.NewGuard(e.client, config.StorageS3Breaker{FailureThreshold: 5, OpenDuration: time.Minute})
		for i := 0; i < 5; i++ {
			if _, _, err := g.Get(context.Background(), "k", nil); !blob.IsTransient(err) {
				t.Fatalf("call %d: %v", i, err)
			}
		}
		e.fake.mu.Lock()
		n := len(e.fake.requests)
		e.fake.mu.Unlock()
		start := time.Now()
		if _, _, err := g.Get(context.Background(), "k", nil); !errors.Is(err, blob.ErrCircuitOpen) {
			t.Fatalf("got %v", err)
		}
		e.fake.mu.Lock()
		after := len(e.fake.requests)
		e.fake.mu.Unlock()
		if after != n || time.Since(start) > 50*time.Millisecond {
			t.Fatalf("requests %d -> %d in %v", n, after, time.Since(start))
		}
	})
}

func TestErrorMapping(t *testing.T) {
	t.Run("R-STOR-033 a missing key is ErrNotFound for HEAD and GET and a delete of it succeeds", func(t *testing.T) {
		e := newEnv(t, nil)
		if _, err := e.client.Head(context.Background(), "nope"); !errors.Is(err, blob.ErrNotFound) {
			t.Fatalf("head: %v", err)
		}
		if _, _, err := e.client.Get(context.Background(), "nope", nil); !errors.Is(err, blob.ErrNotFound) {
			t.Fatalf("get: %v", err)
		}
		if err := e.client.Delete(context.Background(), "nope"); err != nil {
			t.Fatalf("delete: %v", err)
		}
	})
	t.Run("R-STOR-013 a response without Content-Length has an unknown size, not size 0", func(t *testing.T) {
		e := newEnv(t, nil)
		e.fake.fail = func(w http.ResponseWriter, r *http.Request) bool {
			if r.Method != http.MethodGet {
				return false
			}
			_, _ = w.Write([]byte("hello"))
			w.(http.Flusher).Flush() // chunked: the response carries no Content-Length
			return true
		}
		body, info, err := e.client.Get(context.Background(), "k", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer body.Close()
		if info.Size != -1 {
			t.Fatalf("Size = %d, want -1 (unknown)", info.Size)
		}
		if got, _ := io.ReadAll(body); string(got) != "hello" {
			t.Fatalf("body %q", got)
		}
	})
	t.Run("R-STOR-035 AccessDenied and NoSuchBucket are configuration errors, not transient", func(t *testing.T) {
		for _, c := range []struct {
			status int
			code   string
		}{{403, "AccessDenied"}, {404, "NoSuchBucket"}, {403, "InvalidAccessKeyId"}} {
			e := newEnv(t, nil)
			e.fake.fail = func(w http.ResponseWriter, r *http.Request) bool { s3Error(w, r, c.status, c.code); return true }
			err := put(e.client, "k", []byte("x"), blob.Checksum{})
			if !blob.IsConfig(err) || blob.IsTransient(err) || !strings.Contains(err.Error(), c.code) {
				t.Errorf("%s: %v", c.code, err)
			}
		}
	})
	t.Run("R-STOR-033 an unreachable store is transient", func(t *testing.T) {
		e := newEnv(t, func(c *config.StorageS3) { c.Retry.MaxAttempts = 1 })
		e.srv.Close()
		if err := put(e.client, "k", []byte("x"), blob.Checksum{}); !blob.IsTransient(err) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("R-STOR-033 the caller's cancellation is not a store error", func(t *testing.T) {
		e := newEnv(t, nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := e.client.Put(ctx, "k", bytes.NewReader([]byte("x")), 1, blob.Checksum{})
		if !errors.Is(err, context.Canceled) || blob.IsTransient(err) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestStalledTransfer(t *testing.T) {
	t.Run("R-STOR-033 a download in which no byte moves fails after idle_transfer_timeout", func(t *testing.T) {
		e := newEnv(t, func(c *config.StorageS3) { c.HTTP.IdleTransferTimeout = 150 * time.Millisecond })
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		e.fake.objects["k"] = bytes.Repeat([]byte("a"), 100)
		e.fake.fail = func(w http.ResponseWriter, r *http.Request) bool {
			if r.Method != http.MethodGet {
				return false
			}
			w.Header().Set("Content-Length", "1000")
			w.WriteHeader(200)
			_, _ = w.Write(bytes.Repeat([]byte("a"), 100))
			w.(http.Flusher).Flush()
			<-release
			return true
		}
		body, _, err := e.client.Get(context.Background(), "k", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer body.Close()
		start := time.Now()
		_, err = io.ReadAll(body)
		var se *blob.StoreError
		if !errors.As(err, &se) || !strings.Contains(err.Error(), "no byte moved") || time.Since(start) > 3*time.Second {
			t.Fatalf("after %v: %v", time.Since(start), err)
		}
	})
}

func TestPayloadChecksum(t *testing.T) {
	data := []byte("content addressed")
	sha := sha256.Sum256(data)
	sum5 := md5.Sum(data)
	t.Run("R-STOR-006 a PutObject carries the SHA-256 or the Content-MD5 the store verifies", func(t *testing.T) {
		e := newEnv(t, nil)
		var gotSHA, gotMD5 string
		e.fake.fail = func(w http.ResponseWriter, r *http.Request) bool {
			if r.Method == http.MethodPut {
				gotSHA, gotMD5 = r.Header.Get("X-Amz-Checksum-Sha256"), r.Header.Get("Content-Md5")
			}
			return false
		}
		if err := put(e.client, "a", data, blob.Checksum{Algorithm: "sha256", Value: sha[:]}); err != nil {
			t.Fatal(err)
		}
		if gotSHA != base64.StdEncoding.EncodeToString(sha[:]) {
			t.Fatalf("x-amz-checksum-sha256 = %q", gotSHA)
		}
		if err := put(e.client, "b", data, blob.Checksum{Algorithm: "md5", Value: sum5[:]}); err != nil {
			t.Fatal(err)
		}
		if gotMD5 != base64.StdEncoding.EncodeToString(sum5[:]) {
			t.Fatalf("content-md5 = %q", gotMD5)
		}
	})
}

func TestOutboundClient(t *testing.T) {
	t.Run("R-ARCH-022 requests leave through the outbound factory and count as purpose objectstore", func(t *testing.T) {
		e := newEnv(t, nil)
		m := observability.NewMetrics("web")
		c, err := s3.NewFromFactory(context.Background(), e.cfg, outbound.New(config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true}}, outbound.WithMetrics(m)))
		if err != nil {
			t.Fatal(err)
		}
		if err := put(c, "k", []byte("x"), blob.Checksum{}); err != nil {
			t.Fatal(err)
		}
		fams, err := m.Registry().Gather()
		if err != nil {
			t.Fatal(err)
		}
		var n float64
		for _, f := range fams {
			if f.GetName() != "trove_outbound_requests_total" {
				continue
			}
			for _, mm := range f.GetMetric() {
				for _, l := range mm.GetLabel() {
					if l.GetName() == "kind" && l.GetValue() == outbound.PurposeObjectStore {
						n += mm.GetCounter().GetValue()
					}
				}
			}
		}
		if n < 1 {
			t.Fatalf("no outbound request counted for the object store")
		}
	})
}

func TestStaticCredentials(t *testing.T) {
	t.Run("R-STOR-031 replacing the credential files makes later requests use the new key without a restart", func(t *testing.T) {
		e := newEnv(t, nil)
		if err := put(e.client, "k1", []byte("x"), blob.Checksum{}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(e.cfg.Credentials.AccessKeyIDFile, []byte("AKIDTWO\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(e.cfg.Credentials.SecretAccessKeyFile, []byte("secret-two\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			if err := put(e.client, "k2", []byte("x"), blob.Checksum{}); err != nil {
				t.Fatal(err)
			}
			e.fake.mu.Lock()
			last := e.fake.keys[len(e.fake.keys)-1]
			e.fake.mu.Unlock()
			if last == "AKIDTWO" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("still signing with %s", last)
			}
			time.Sleep(200 * time.Millisecond)
		}
		e.fake.mu.Lock()
		defer e.fake.mu.Unlock()
		if e.fake.keys[0] != "AKIDONE" {
			t.Fatalf("first key %s", e.fake.keys[0])
		}
	})
	t.Run("R-STOR-031 a missing credential file is a transient failure of the request, not a panic", func(t *testing.T) {
		e := newEnv(t, nil)
		if err := os.Remove(e.cfg.Credentials.SecretAccessKeyFile); err != nil {
			t.Fatal(err)
		}
		if err := put(e.client, "k", []byte("x"), blob.Checksum{}); !blob.IsTransient(err) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestConfiguration(t *testing.T) {
	base := func(t *testing.T) config.StorageS3 { return newEnv(t, nil).cfg }
	hc := func() *outbound.Factory {
		return outbound.New(config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true}})
	}
	t.Run("R-STOR-030 an http endpoint needs allow_insecure_endpoint", func(t *testing.T) {
		cfg := base(t)
		cfg.AllowInsecureEndpoint = false
		if _, err := s3.NewFromFactory(context.Background(), cfg, hc()); err == nil || !strings.Contains(err.Error(), "allow_insecure_endpoint") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("R-STOR-030 the bucket is required", func(t *testing.T) {
		cfg := base(t)
		cfg.Bucket = ""
		if _, err := s3.NewFromFactory(context.Background(), cfg, hc()); err == nil || !strings.Contains(err.Error(), "storage.s3.bucket") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("R-STOR-030 a region is required", func(t *testing.T) {
		cfg := base(t)
		cfg.Region = ""
		if _, err := s3.NewFromFactory(context.Background(), cfg, hc()); err == nil || !strings.Contains(err.Error(), "storage.s3.region") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("R-STOR-030 a CA file that does not load is a configuration error naming storage.s3.ca_file", func(t *testing.T) {
		cfg := base(t)
		cfg.CAFile = filepath.Join(t.TempDir(), "missing-ca.pem")
		if _, err := s3.NewFromFactory(context.Background(), cfg, hc()); err == nil || !strings.Contains(err.Error(), "storage.s3.ca_file") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("R-STOR-033 a proxy credentials file is refused until the outbound layer can send it, not ignored", func(t *testing.T) {
		cfg := base(t)
		cfg.HTTP.ProxyCredentialsFile = "/etc/trove/proxy-credentials"
		if _, err := s3.NewFromFactory(context.Background(), cfg, hc()); err == nil || !strings.Contains(err.Error(), "proxy_credentials_file") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("R-STOR-014 a presigned URL names the bucket, the key and the validity, and honours presign.endpoint", func(t *testing.T) {
		e := newEnv(t, func(c *config.StorageS3) { c.Presign.Endpoint = "https://blobs.example.com" })
		u, err := e.client.Presign(context.Background(), "trove/sha256/aa/bb/x", 60*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"https://blobs.example.com/b/trove/sha256/aa/bb/x", "X-Amz-Expires=60", "X-Amz-Signature="} {
			if !strings.Contains(u, want) {
				t.Errorf("missing %q in %s", want, u)
			}
		}
	})
}

func TestWebIdentity(t *testing.T) {
	t.Run("R-STOR-031 the projected token is exchanged at sts_endpoint and the temporary key signs the requests", func(t *testing.T) {
		var gotToken, gotRole atomic.Value
		sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			gotToken.Store(r.Form.Get("WebIdentityToken"))
			gotRole.Store(r.Form.Get("RoleArn"))
			w.Header().Set("Content-Type", "text/xml")
			_, _ = w.Write([]byte(`<AssumeRoleWithWebIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleWithWebIdentityResult><Credentials>` +
				`<AccessKeyId>AKIDSTS</AccessKeyId><SecretAccessKey>sts-secret</SecretAccessKey><SessionToken>sts-token</SessionToken>` +
				`<Expiration>2099-01-01T00:00:00Z</Expiration></Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`))
		}))
		t.Cleanup(sts.Close)
		token := filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(token, []byte("projected-jwt"), 0o600); err != nil {
			t.Fatal(err)
		}
		e := newEnv(t, func(c *config.StorageS3) {
			c.Credentials = config.StorageS3Credentials{
				Source: "webIdentity", RoleARN: "arn:aws:iam::123456789012:role/trove",
				WebIdentityTokenFile: token, STSEndpoint: sts.URL,
			}
		})
		if err := put(e.client, "k", []byte("x"), blob.Checksum{}); err != nil {
			t.Fatal(err)
		}
		e.fake.mu.Lock()
		defer e.fake.mu.Unlock()
		if e.fake.keys[0] != "AKIDSTS" {
			t.Fatalf("signed with %v", e.fake.keys)
		}
		if gotToken.Load() != "projected-jwt" || gotRole.Load() != "arn:aws:iam::123456789012:role/trove" {
			t.Fatalf("token %v role %v", gotToken.Load(), gotRole.Load())
		}
	})
}
