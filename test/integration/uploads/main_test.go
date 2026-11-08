//go:build integration

package uploads

import (
	"os"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestMain fails the whole package, before any test runs, when the environment names a
// plaintext or unverified endpoint (INV-20): the integration stack is TLS-only. It also drops the template database
// that the tests clone.
func TestMain(m *testing.M) {
	code := testenv.Main(m)
	dropTemplate()
	os.Exit(code)
}
