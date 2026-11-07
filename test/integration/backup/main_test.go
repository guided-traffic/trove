//go:build integration

// Package backup holds the integration tests of the backup lane (R-BAK-029) against the shared stack: PostgreSQL, Valkey (for
// the replicas of the scenarios that serve) and the S3 store with the buckets trove-blobs and trove-backups. The work packages of
// the lane add files of their own here, named by their prefix (create_, target_, restore_, journal_, formats_), and use
// testenv.NewStack for an installation with its own database. The scenarios that need a cluster belong to the end-to-end suite
// (test/e2e).
package backup

import (
	"os"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }
