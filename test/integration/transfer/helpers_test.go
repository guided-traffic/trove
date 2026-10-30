//go:build integration

// Package transfer tests the HTTP surface of the files transfer and of the
// repository operations against the integration stack, on the in-process harness
// (test/integration/testenv): every replica is the production composition root,
// a caller is a real user with a real token, and the decision point, the chain
// and the wiring are the ones a deployment runs. The suites that need a seam
// inside the transfer (the storage hooks, the counting object store, a recorder
// that fails, the spool) are in test/integration/files, whose process also
// registers a format of its own that the composition root refuses to start with:
// a format with a data port needs the binding that only the root makes.
package transfer

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// person is a principal row of the test database and the credential that names it.
type person struct {
	ID, Name string
	Kind     principal.Kind
}

// A response is a request's answer, read.
type response struct {
	status int
	header http.Header
	body   []byte
}

func (r response) text() string { return string(r.body) }

func (r response) json(t testing.TB) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("answer %d is not a JSON object: %v\n%s", r.status, err, r.body)
	}
	return m
}

var client = &http.Client{
	Timeout:       10 * time.Minute,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Transport:     &http.Transport{DisableCompression: true, MaxIdleConnsPerHost: 8},
}

// waitFor polls cond until it holds or the time is up.
func waitFor(t testing.TB, what string, within time.Duration, cond func() bool) {
	t.Helper()
	testenv.WaitFor(t, what, within, cond)
}
