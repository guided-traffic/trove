//go:build integration

package blob

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
)

// TestConformance is the object-store conformance check of R-STOR-035 against the
// store of the stack. It needs only the TROVE_TEST_S3_* variables, so the
// S3 conformance leg of Test and Release (the MinIO job) runs it alone:
//
//	go test -tags integration -run Conformance ./test/integration/blob/...
func TestConformance(t *testing.T) {
	e := newS3(t, "conformance")
	opts := blob.ConformanceOptions{Prefix: e.prefix, Replica: "it-" + runID(t), HTTP: e.http}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	t.Run("R-STOR-035 the startup check passes", func(t *testing.T) {
		if err := blob.StartupCheck(ctx, e.raw, e.prefix, opts.Replica); err != nil {
			t.Fatal(err)
		}
	})
	for _, c := range blob.ConformanceChecks() {
		name := "R-STOR-035 "
		if c.Assumption != "" {
			name += c.Assumption + " "
		}
		t.Run(name+c.Name, func(t *testing.T) {
			r := blob.RunCheck(ctx, e.raw, opts, c)
			if !r.OK {
				t.Fatal(r.Error)
			}
		})
	}
	t.Run("R-STOR-035 the checks changed nothing outside _probe/", func(t *testing.T) {
		for _, k := range e.keys(t) {
			t.Errorf("object left behind: %s", k)
		}
		ups, err := e.raw.ListMultipart(ctx, e.prefix+"/")
		if err != nil {
			t.Fatal(err)
		}
		if len(ups) != 0 {
			t.Errorf("unfinished multipart uploads left behind: %v", ups)
		}
	})
	t.Run("R-STOR-035 a wrong secret key is a configuration error, not a transient one", func(t *testing.T) {
		cfg := e.cfg
		wrong := filepath.Join(t.TempDir(), "secret")
		if err := os.WriteFile(wrong, []byte("not-the-secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg.Credentials.SecretAccessKeyFile = wrong
		c, err := newClient(t, cfg)
		if err != nil {
			t.Fatal(err)
		}
		serr := blob.StartupCheck(ctx, c, e.prefix, opts.Replica)
		if !blob.IsConfig(serr) {
			t.Fatalf("got %v, want a configuration error", serr)
		}
	})
}
