//go:build integration

package bastion

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/valkey-io/valkey-go"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// valkeyAdmin is a client of the primary as the administrative user, for what the Bastion's own user may not do:
// to list and rewrite the records the replicas share.
func valkeyAdmin(t *testing.T) valkey.Client {
	t.Helper()
	v := testenv.ValkeyConfig(t)
	pem, err := os.ReadFile(v.CAFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	tc := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	c, err := valkey.NewClient(valkey.ClientOption{
		InitAddress: v.Sentinels, Username: v.AdminUsername, Password: v.AdminPassword, TLSConfig: tc, DisableCache: true,
		Sentinel: valkey.SentinelOption{MasterSet: v.PrimaryName, Password: v.SentinelPassword, TLSConfig: tc},
	})
	if err != nil {
		t.Fatalf("admin client: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

// TestR_BAS_034_R_BAS_020_AForgedAnswerRecordIsNotTrusted: two replicas share their answers through Valkey. An
// answer one replica stored is used by the other without a question to the main installation; when somebody with
// access to Valkey rewrites the records, the signature fails, the other replica ignores them and asks the main
// installation, which still decides.
func TestR_BAS_034_R_BAS_020_AForgedAnswerRecordIsNotTrusted(t *testing.T) {
	w := newWorld(t, worldOptions{})
	w.content()
	b := w.newBastion(t, bastionOptions{Backend: "volume", Valkey: true, Replicas: 2}).wait()
	a, c := b.reps[0], b.reps[1]
	path := tarballPath(npmAll, "@acme/sdk", "1.0.0")
	reader := w.newReader(npmAll, npmLocal, npmRemote)

	// Replica A learns the answer for the reader; replica B has the bytes only if it fetched them, so let it
	// serve the artifact first (its own cache, no answer for this reader).
	if got := c.do(t, "GET", path, callOpts{token: w.full.token, addr: "198.51.100.181"}); got.Status != http.StatusOK {
		t.Fatalf("B warms its cache: %d", got.Status)
	}
	if got := a.do(t, "GET", path, callOpts{token: reader.token, addr: "198.51.100.182"}); got.Status != http.StatusOK {
		t.Fatalf("A serves the reader: %d", got.Status)
	}
	m := w.front.Mark()
	if got := c.do(t, "GET", path, callOpts{token: reader.token, addr: "198.51.100.182"}); got.Status != http.StatusOK {
		t.Fatalf("B serves the reader: %d", got.Status)
	}
	if n := w.front.Cost(m); n != 0 {
		t.Fatalf("B did not use the answer A stored in Valkey: %d requests", n)
	}

	// A third caller: A stores its answer, then every answer record of the Bastion is rewritten, then B is asked.
	other := w.newReader(npmAll, npmLocal, npmRemote)
	if got := a.do(t, "GET", path, callOpts{token: other.token, addr: "198.51.100.183"}); got.Status != http.StatusOK {
		t.Fatalf("A serves the other reader: %d", got.Status)
	}
	adm := valkeyAdmin(t)
	ctx := context.Background()
	pattern := "trove-bastion:" + b.sh.vprefix + ":*"
	var keys []string
	cursor := uint64(0)
	for {
		e, err := adm.Do(ctx, adm.B().Scan().Cursor(cursor).Match(pattern).Count(1000).Build()).AsScanEntry()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, e.Elements...)
		if cursor = e.Cursor; cursor == 0 {
			break
		}
	}
	forged := 0
	for _, k := range keys {
		if !strings.Contains(k, "ans") {
			continue
		}
		v, err := adm.Do(ctx, adm.B().Get().Key(k).Build()).ToString()
		if err != nil || v == "" {
			continue
		}
		flipped := []byte(v)
		flipped[len(flipped)/2] ^= 0x01
		if err := adm.Do(ctx, adm.B().Set().Key(k).Value(string(flipped)).Keepttl().Build()).Error(); err != nil {
			t.Fatal(err)
		}
		forged++
	}
	if forged == 0 {
		t.Fatalf("no answer record found among %v", keys)
	}
	m = w.front.Mark()
	if got := c.do(t, "GET", path, callOpts{token: other.token, addr: "198.51.100.183"}); got.Status != http.StatusOK {
		t.Fatalf("B with forged records: %d", got.Status)
	}
	if n := w.front.Cost(m); n == 0 {
		t.Fatal("B trusted a forged answer record: the main installation was not asked")
	}
}
