//go:build integration

// Package projects tests the projects of R-AUTHZ-026 against the integration
// stack: the management API over HTTP on two in-process web replicas that share one
// database, the project service behind it, the audit events and journal entries of
// R-AUTHZ-029 and R-AUTHZ-031 and the epochs of R-AUTHZ-032. Authentication is the
// stand-in of test/integration/stack (a request names its person); the rights a
// project administrator gets from the grant index are tested with the engine in
// test/integration/authz.
package projects

import (
	"os"
	"testing"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestMain fails the whole package, before any test runs, when the environment names a
// plaintext or unverified endpoint (INV-20): the integration stack is TLS-only.
func TestMain(m *testing.M) {
	// An assignment notifies the change area repo_config, which the repository area
	// registers; a build of this tree without that work registers it here.
	if _, ok := cluster.LookupArea("repo_config"); !ok {
		cluster.RegisterArea(cluster.Area{Name: "repo_config", Owner: "R-REPO-008", Class: cluster.ChangeArea, PayloadVersion: 1})
	}
	os.Exit(testenv.Main(m))
}
