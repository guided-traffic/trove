//go:build integration

package tls

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/blob/blobtest"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/tlsfiles"
	"github.com/guided-traffic/trove/internal/tlsfiles/tlstest"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestObjectStoreTLS(t *testing.T) {
	dir := t.TempDir()
	caFile := copyCA(t, dir, "s3-ca.crt")
	env := testenv.S3Config(t)
	cfg := testenv.SecureS3(t, blobtest.Storage(t, dir).S3)
	cfg.CAFile = caFile // a copy this test may rewrite
	cfg.Region, cfg.Bucket, cfg.ForcePathStyle = env.Region, env.BlobsBucket, env.ForcePathStyle
	cfg.Prefix = "it-tls/" + randHex(5)
	cfg.Credentials = config.StorageS3Credentials{
		Source:              "static",
		AccessKeyIDFile:     filepath.Join(dir, "id"),
		SecretAccessKeyFile: filepath.Join(dir, "secret"),
	}
	write(t, dir, "id", env.AccessKey)
	write(t, dir, "secret", env.SecretKey)
	cfg.Retry = config.StorageS3Retry{MaxAttempts: 1, MaxBackoff: 10 * time.Millisecond}

	reg := prometheus.NewRegistry()
	m, err := tlsfiles.NewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	const every = 200 * time.Millisecond
	// The factory trusts the system roots only: the gateway verifies through
	// storage.s3.ca_file or not at all.
	f := outbound.New(config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true, MinVersion: "1.2"}},
		outbound.WithTrustCheckEvery(every), outbound.WithTLSMetrics(m))
	c, err := s3.NewFromFactory(context.Background(), cfg, f)
	if err != nil {
		t.Fatalf("object store client with storage.s3.ca_file: %v", err)
	}
	key := cfg.Prefix + "/probe"
	put := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return c.Put(ctx, key, bytes.NewReader([]byte("tls")), 3, blob.Checksum{})
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), key) })
	stack, err := os.ReadFile(testenv.CAFile(t))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("R-STOR-030 R-ARCH-035 the https endpoint verifies against storage.s3.ca_file", func(t *testing.T) {
		if !strings.HasPrefix(cfg.Endpoint, "https://") || cfg.AllowInsecureEndpoint {
			t.Fatalf("endpoint %q, allow_insecure_endpoint %v", cfg.Endpoint, cfg.AllowInsecureEndpoint)
		}
		if err := put(); err != nil {
			t.Fatal(err)
		}
		// The presigned URL verifies the same way through the client's own HTTP client.
		u, err := c.Presign(context.Background(), key, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.HTTPClient().Get(u)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || string(b) != "tls" {
			t.Fatalf("presigned GET %d %q", resp.StatusCode, b)
		}
	})

	t.Run("R-ARCH-022 R-ARCH-035 a ca_file rewritten in place applies to the next connection without a restart", func(t *testing.T) {
		tlstest.WriteFile(t, caFile, tlstest.NewCA(t, "unrelated ca").PEM)
		time.Sleep(2 * every)
		if err := put(); err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Fatalf("an unrelated CA still verified the gateway: %v", err)
		}
		tlstest.WriteFile(t, caFile, stack)
		time.Sleep(2 * every)
		if err := put(); err != nil {
			t.Fatalf("the restored CA was not used: %v", err)
		}
		if n := seriesValue(t, reg, tlsfiles.FamilyReloads, caFile); n < 2 {
			t.Errorf("trove_tls_reloads_total{kind=objectstore} = %v, want at least 2", n)
		}
	})

	t.Run("R-ARCH-022 R-ARCH-035 a ca_file that does not load keeps the last good bundle", func(t *testing.T) {
		tlstest.WriteFile(t, caFile, []byte("garbage"))
		time.Sleep(2 * every)
		if err := put(); err != nil {
			t.Fatalf("a broken ca_file failed the request: %v", err)
		}
		tlstest.WriteFile(t, caFile, stack)
	})
}
