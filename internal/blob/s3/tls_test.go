package s3_test

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/blob/blobtest"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/tlsfiles/tlstest"
)

func TestCAFile(t *testing.T) {
	storeCA := tlstest.NewCA(t, "object store ca")
	otherCA := tlstest.NewCA(t, "unrelated ca")
	fake := &fakeS3{t: t, objects: map[string][]byte{}}
	srv := httptest.NewUnstartedServer(fake)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{storeCA.Issue(t, tlstest.Options{}).TLS}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	caFile := storeCA.WriteCA(t, dir, "s3-ca.crt")
	cfg := blobtest.Storage(t, dir).S3
	cfg.Endpoint, cfg.Region, cfg.Bucket, cfg.ForcePathStyle = srv.URL, "us-east-1", "b", true
	cfg.AllowInsecureEndpoint, cfg.CAFile = false, caFile
	cfg.Retry = config.StorageS3Retry{MaxAttempts: 1, MaxBackoff: time.Millisecond}
	idFile, secretFile := filepath.Join(dir, "id"), filepath.Join(dir, "secret")
	tlstest.WriteFile(t, idFile, []byte("AKIDONE\n"))
	tlstest.WriteFile(t, secretFile, []byte("secret-one\n"))
	cfg.Credentials = config.StorageS3Credentials{Source: "static", AccessKeyIDFile: idFile, SecretAccessKeyFile: secretFile}

	const every = 50 * time.Millisecond
	// The factory trusts only the system roots: the endpoint verifies through
	// storage.s3.ca_file alone.
	f := outbound.New(config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true, MinVersion: "1.2"}}, outbound.WithTrustCheckEvery(every))
	c, err := s3.NewFromFactory(context.Background(), cfg, f)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("R-STOR-030 R-ARCH-035 an https endpoint verifies against storage.s3.ca_file", func(t *testing.T) {
		if err := put(c, "k", []byte("x"), blob.Checksum{}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("R-STOR-030 an endpoint without a scheme is reached over https", func(t *testing.T) {
		bare := cfg
		bare.Endpoint = strings.TrimPrefix(srv.URL, "https://")
		bc, err := s3.NewFromFactory(context.Background(), bare, f)
		if err != nil {
			t.Fatal(err)
		}
		if err := put(bc, "bare", []byte("x"), blob.Checksum{}); err != nil {
			t.Fatalf("a host without a scheme must mean https://: %v", err)
		}
	})

	t.Run("R-STOR-030 a presigned URL fetched with the client's HTTP client verifies against the same ca_file", func(t *testing.T) {
		u, err := c.Presign(context.Background(), "k", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, u, nil)
		resp, err := c.HTTPClient().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		}
	})

	t.Run("R-ARCH-035 a ca_file rewritten in place applies to the next connection without a restart", func(t *testing.T) {
		tlstest.WriteFile(t, caFile, otherCA.PEM)
		time.Sleep(2 * every)
		if err := put(c, "k", []byte("x"), blob.Checksum{}); err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Fatalf("an unrelated CA still verified the store: %v", err)
		}
		tlstest.WriteFile(t, caFile, storeCA.PEM)
		time.Sleep(2 * every)
		if err := put(c, "k", []byte("x"), blob.Checksum{}); err != nil {
			t.Fatalf("restored CA not used: %v", err)
		}
	})

	t.Run("R-ARCH-035 a broken ca_file keeps the last good bundle", func(t *testing.T) {
		tlstest.WriteFile(t, caFile, []byte("garbage"))
		time.Sleep(2 * every)
		if err := put(c, "k", []byte("x"), blob.Checksum{}); err != nil {
			t.Fatalf("the broken file replaced the good bundle: %v", err)
		}
	})
}
