//go:build integration

// Package bastion holds the integration tests of the whole Bastion role (R-BAS-048): the role in one process,
// started from a configuration file, against the in-process main installation of internal/bastion/maintest, with
// the bucket backend on the integration stack's S3 (SeaweedFS over TLS).
//
// Variables read (testenv): TROVE_TEST_S3_ENDPOINT, _REGION, _ACCESS_KEY, _SECRET_KEY, _BUCKET_BACKUPS,
// _FORCE_PATH_STYLE and TROVE_TEST_CA_FILE. The run works below a prefix of its own in the unversioned backups
// bucket and removes it when it ends.
package bastion

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/roletest"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_BAS_048_TheWholeRoleServesAColdMissAWarmHitARevocationAndTheOutage_Bucket: the same scenario as the
// volume run, with the bucket backend (and so with the signing key the bucket requires, R-BAS-032).
func TestR_BAS_048_TheWholeRoleServesAColdMissAWarmHitARevocationAndTheOutage_Bucket(t *testing.T) {
	s := testenv.S3Config(t)
	ca := testenv.CAFile(t)
	var id [5]byte
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatal(err)
	}
	prefix := "it-bastionrole/" + hex.EncodeToString(id[:])

	secrets := t.TempDir()
	write := func(dir, name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	accessFile, secretFile := write(secrets, "access-key", s.AccessKey+"\n"), write(secrets, "secret-key", s.SecretKey+"\n")
	cfg := config.BastionCacheBucket{
		Endpoint: s.Endpoint, Region: s.Region, Bucket: s.BackupsBucket, Prefix: prefix, ForcePathStyle: s.ForcePathStyle, CAFile: ca,
		Credentials: config.BastionBucketCredentials{Source: "static", AccessKeyIDFile: accessFile, SecretAccessKeyFile: secretFile},
	}
	raw, err := s3.NewFromFactory(context.Background(), cfg.StorageS3(), outbound.New(config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true}}))
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		token := ""
		for {
			objs, next, err := raw.ListPlain(ctx, prefix+"/", token, 1000)
			if err != nil {
				t.Logf("list %s: %v", prefix, err)
				return
			}
			for _, o := range objs {
				if err := raw.Delete(ctx, o.Key); err != nil {
					t.Logf("clean up %s: %v", o.Key, err)
				}
			}
			if next == "" {
				return
			}
			token = next
		}
	})

	roletest.Run(t, roletest.Backend{Name: "bucket", YAML: func(dir string) string {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		b.WriteString("  signing_key_file: " + write(dir, "signing-key", string(key)) + "\n")
		b.WriteString("  cache:\n    backend: bucket\n    bucket:\n")
		b.WriteString("      endpoint: " + s.Endpoint + "\n      region: " + s.Region + "\n      bucket: " + s.BackupsBucket + "\n")
		b.WriteString("      prefix: " + prefix + "\n      ca_file: " + ca + "\n")
		if s.ForcePathStyle {
			b.WriteString("      force_path_style: true\n")
		}
		b.WriteString("      credentials:\n        source: static\n")
		b.WriteString("        access_key_id_file: " + accessFile + "\n")
		b.WriteString("        secret_access_key_file: " + secretFile + "\n")
		return b.String()
	}})
}
