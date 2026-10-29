//go:build integration

// Package blob tests the blob store against the integration stack: the
// object-store conformance check (it needs only the TROVE_TEST_S3_* variables, so
// the nightly MinIO and AWS legs run it with `-run Conformance` and no
// PostgreSQL or Valkey), and the commit protocol and garbage collection against
// PostgreSQL and the S3 store.
//
// Variables read (testenv):
//
//	TROVE_TEST_S3_ENDPOINT, _REGION, _ACCESS_KEY, _SECRET_KEY, _BUCKET_BLOBS, _FORCE_PATH_STYLE
//
// and optionally, for stores with other settings (R-STOR-030):
//
//	TROVE_TEST_S3_PAYLOAD_CHECKSUM   sha256 | md5 | none      (default sha256)
//	TROVE_TEST_S3_REQUEST_CHECKSUM   when_supported | when_required
//	TROVE_TEST_S3_RESPONSE_CHECKSUM  when_supported | when_required
//	TROVE_TEST_S3_BATCH_DELETE       true | false             (default true)
//
// The database tests also read TROVE_TEST_PG_*; nothing here reads
// TROVE_TEST_VALKEY_*: the blob store needs no Valkey (R-STOR-046).
package blob

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/blob/blobtest"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// runID separates the keys of this test run from every other run in the shared
// bucket.
func runID(tb testing.TB) string {
	tb.Helper()
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		tb.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}

func optional(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// s3Env is the object store under test.
type s3Env struct {
	cfg     config.StorageS3
	factory *outbound.Factory
	http    *http.Client
	raw     blob.ObjectStore // the client, without a breaker
	prefix  string
}

// newS3 builds the client for the shared bucket under a prefix of its own and
// removes everything below that prefix when the test ends.
func newS3(tb testing.TB, name string) *s3Env {
	tb.Helper()
	dir := tb.TempDir()
	writeSecret := func(file, v string) string {
		p := filepath.Join(dir, file)
		if err := os.WriteFile(p, []byte(v+"\n"), 0o600); err != nil {
			tb.Fatal(err)
		}
		return p
	}
	cfg := blobtest.Storage(tb, dir).S3
	cfg.Endpoint = testenv.Get(tb, "TROVE_TEST_S3_ENDPOINT")
	cfg.Region = testenv.Get(tb, "TROVE_TEST_S3_REGION")
	cfg.Bucket = testenv.Get(tb, "TROVE_TEST_S3_BUCKET_BLOBS")
	cfg.ForcePathStyle = testenv.Get(tb, "TROVE_TEST_S3_FORCE_PATH_STYLE") == "true"
	cfg.AllowInsecureEndpoint = false
	cfg.CAFile = testenv.CAFile(tb) // the gateway is HTTPS-only, signed by the stack's CA
	cfg.Prefix = "it-blob/" + runID(tb) + "/" + name
	cfg.PayloadChecksum = optional("TROVE_TEST_S3_PAYLOAD_CHECKSUM", cfg.PayloadChecksum)
	cfg.Checksums.RequestCalculation = optional("TROVE_TEST_S3_REQUEST_CHECKSUM", cfg.Checksums.RequestCalculation)
	cfg.Checksums.ResponseValidation = optional("TROVE_TEST_S3_RESPONSE_CHECKSUM", cfg.Checksums.ResponseValidation)
	cfg.Credentials = config.StorageS3Credentials{
		Source:              "static",
		AccessKeyIDFile:     writeSecret("access-key", testenv.Get(tb, "TROVE_TEST_S3_ACCESS_KEY")),
		SecretAccessKeyFile: writeSecret("secret-key", testenv.Get(tb, "TROVE_TEST_S3_SECRET_KEY")),
	}
	f := outbound.New(config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true}})
	c, err := s3.NewFromFactory(context.Background(), cfg, f)
	if err != nil {
		tb.Fatalf("object store client: %v", err)
	}
	e := &s3Env{cfg: cfg, factory: f, http: c.HTTPClient(), raw: c, prefix: cfg.Prefix}
	tb.Cleanup(func() { e.removeAll(tb) })
	return e
}

// removeAll deletes every object and unfinished upload below the test's prefix.
func (e *s3Env) removeAll(tb testing.TB) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ups, _ := e.raw.ListMultipart(ctx, e.prefix+"/")
	for _, u := range ups {
		_ = e.raw.AbortMultipart(ctx, u.Key, u.UploadID)
	}
	token := ""
	for {
		page, err := e.raw.List(ctx, e.prefix+"/", blob.ListOptions{Token: token})
		if err != nil {
			tb.Logf("clean up %s: %v", e.prefix, err)
			return
		}
		for _, o := range page.Objects {
			_ = e.raw.Delete(ctx, o.Key)
		}
		if page.Next == "" {
			return
		}
		token = page.Next
	}
}

// keys lists every object below the test's prefix.
func (e *s3Env) keys(tb testing.TB) []string {
	tb.Helper()
	var out []string
	token := ""
	for {
		page, err := e.raw.List(context.Background(), e.prefix+"/", blob.ListOptions{Token: token})
		if err != nil {
			tb.Fatal(err)
		}
		for _, o := range page.Objects {
			out = append(out, o.Key)
		}
		if page.Next == "" {
			return out
		}
		token = page.Next
	}
}

// storage returns the storage configuration for a Store on this S3 env.
func (e *s3Env) storage(tb testing.TB) config.Storage {
	tb.Helper()
	cfg := blobtest.Storage(tb, tb.TempDir())
	cfg.S3 = e.cfg
	cfg.Upload.BlobWaitTimeout = 60 * time.Second
	cfg.GC.BatchDelete = optional("TROVE_TEST_S3_BATCH_DELETE", "true") == "true"
	return cfg
}

// newClient builds a client for another configuration of the same store.
func newClient(tb testing.TB, cfg config.StorageS3) (blob.ObjectStore, error) {
	tb.Helper()
	f := outbound.New(config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true}})
	return s3.NewFromFactory(context.Background(), cfg, f)
}
