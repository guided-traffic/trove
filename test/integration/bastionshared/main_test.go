//go:build integration

package bastionshared

import (
	"os"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestMain fails the package before any test runs when the environment names a plaintext or unverified
// endpoint (INV-20): the integration stack is TLS-only.
func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }
