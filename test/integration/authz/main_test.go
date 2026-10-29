//go:build integration

package authz

import (
	"os"
	"testing"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestMain fails the whole package, before any test runs, when the environment names a
// plaintext or unverified endpoint (INV-20): the integration stack is TLS-only.
func TestMain(m *testing.M) {
	// The grant index follows the change area repo_config, which the repository
	// area registers in cluster/areas.go; a build of this tree without that work
	// registers it here so that the change log follows it.
	if _, ok := cluster.LookupArea("repo_config"); !ok {
		cluster.RegisterArea(cluster.Area{Name: "repo_config", Owner: "R-REPO-008", Class: cluster.ChangeArea, PayloadVersion: 1})
	}
	os.Exit(testenv.Main(m))
}
