package s3_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/guided-traffic/trove/internal/backup/target"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/outbound"
)

type targetEnv struct {
	fake *fakeS3
	t    *s3.Target
}

func newTargetEnv(t *testing.T, sse string, opts ...s3.TargetOption) *targetEnv {
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
	var b config.Backup
	b.Target = config.BackupTarget{
		Endpoint: srv.URL, Region: "us-east-1", Bucket: "b", Prefix: "trove", ForcePathStyle: true, AllowInsecureEndpoint: true,
		Credentials: config.StorageS3Credentials{Source: "static", AccessKeyIDFile: write("id", "AKIDBACKUP"), SecretAccessKeyFile: write("secret", "backup-secret")},
		SSE:         config.BackupSSE{Mode: sse},
	}
	b.Database.PartSize = 5 << 20
	b.Copy.PartSize = 5 << 20
	tg, err := s3.NewTarget(context.Background(), b, outbound.New(config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true}}), opts...)
	if err != nil {
		t.Fatal(err)
	}
	return &targetEnv{fake: fake, t: tg}
}

func TestR_BAK_015_TargetSignsWithItsOwnCredential(t *testing.T) {
	e := newTargetEnv(t, "none")
	if _, err := e.t.Put(context.Background(), "k", []byte("x"), target.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	e.fake.mu.Lock()
	defer e.fake.mu.Unlock()
	if len(e.fake.keys) == 0 {
		t.Fatal("no signed request")
	}
	for _, k := range e.fake.keys {
		if k != "AKIDBACKUP" {
			t.Fatalf("request signed with %q", k)
		}
	}
}

func TestR_BAK_002_ConditionalCreate(t *testing.T) {
	t.Run("R-BAK-002 a precondition failure is ErrExists", func(t *testing.T) {
		e := newTargetEnv(t, "none")
		e.fake.fail = func(w http.ResponseWriter, r *http.Request) bool {
			if r.Method == http.MethodPut && r.Header.Get("If-None-Match") == "*" {
				s3Error(w, r, http.StatusPreconditionFailed, "PreconditionFailed")
				return true
			}
			return false
		}
		if _, err := e.t.Put(context.Background(), "marker", []byte("x"), target.PutOptions{IfAbsent: true}); !errors.Is(err, target.ErrExists) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-BAK-002 a store without conditional writes gets a HEAD check and is remembered", func(t *testing.T) {
		e := newTargetEnv(t, "none")
		var conditional atomic.Int32
		e.fake.fail = func(w http.ResponseWriter, r *http.Request) bool {
			if r.Method == http.MethodPut && r.Header.Get("If-None-Match") != "" {
				conditional.Add(1)
				s3Error(w, r, http.StatusNotImplemented, "NotImplemented")
				return true
			}
			return false
		}
		ctx := context.Background()
		if _, err := e.t.Put(ctx, "marker", []byte("a"), target.PutOptions{IfAbsent: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.t.Put(ctx, "marker", []byte("b"), target.PutOptions{IfAbsent: true}); !errors.Is(err, target.ErrExists) {
			t.Fatalf("second create err = %v", err)
		}
		if got := string(e.fake.objects["marker"]); got != "a" {
			t.Fatalf("object = %q", got)
		}
		if n := conditional.Load(); n != 1 {
			t.Fatalf("%d conditional requests, want 1 (the answer is remembered)", n)
		}
	})
}

func TestR_BAK_026_CheckSSE(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		obj        target.Object
		wantErr    bool
	}{
		{"s3 reported", "s3", target.Object{Key: "k", SSE: "AES256"}, false},
		{"s3 not reported", "s3", target.Object{Key: "k"}, true},
		{"kms reported", "kms", target.Object{Key: "k", SSE: "aws:kms", KMSKeyID: "arn:aws:kms:eu:1:key/abc"}, false},
		{"kms other mode", "kms", target.Object{Key: "k", SSE: "AES256"}, true},
		{"kms other key", "kms", target.Object{Key: "k", SSE: "aws:kms", KMSKeyID: "arn:aws:kms:eu:1:key/zzz"}, true},
		{"none accepts anything", "none", target.Object{Key: "k"}, false},
	} {
		t.Run("R-BAK-026 "+tc.name, func(t *testing.T) {
			e := newTargetEnv(t, tc.mode)
			if tc.mode == "kms" {
				e = newTargetEnvKMS(t)
			}
			err := e.t.CheckSSE(tc.obj)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			if err != nil && !strings.Contains(err.Error(), "backup.target.sse") {
				t.Fatalf("the error does not name the setting: %v", err)
			}
		})
	}
}

func newTargetEnvKMS(t *testing.T) *targetEnv {
	t.Helper()
	e := newTargetEnv(t, "none")
	var b config.Backup
	b.Target = config.BackupTarget{
		Endpoint: "https://s3.example.test", Region: "eu-central-1", Bucket: "b",
		Credentials: config.StorageS3Credentials{Source: "static", AccessKeyIDFile: "/dev/null", SecretAccessKeyFile: "/dev/null"},
		SSE:         config.BackupSSE{Mode: "kms", KMSKeyID: "abc", BucketKey: true},
	}
	b.Database.PartSize, b.Copy.PartSize = 5<<20, 5<<20
	tg, err := s3.NewTarget(context.Background(), b, outbound.New(config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true}}))
	if err != nil {
		t.Fatal(err)
	}
	e.t = tg
	return e
}

func TestR_BAK_026_TargetRefusesPlainHTTP(t *testing.T) {
	var b config.Backup
	b.Target = config.BackupTarget{Endpoint: "http://127.0.0.1:9", Region: "us-east-1", Bucket: "b"}
	if _, err := s3.NewTarget(context.Background(), b, outbound.New(config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true}})); err == nil {
		t.Fatal("an http endpoint was accepted without allow_insecure_endpoint")
	}
}
