//go:build integration

// Package search tests the suggestions of the search field (R-API-060 to R-API-064) against the integration stack: PostgreSQL with
// every migration, and in-process replicas started through the composition root, so the real chain, authentication, the grant
// index with the visibility levels of T-18 and the npm format run. Every answer of the management API is validated against the
// OpenAPI document by the harness.
package search

import (
	"os"
	"regexp"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

// statementText reads the text of a sqlc statement from the generated code, with its $n parameters, so that a test explains the
// statement the server runs and not a copy of it.
func statementText(t *testing.T, name string) string {
	t.Helper()
	src, err := os.ReadFile("../../../internal/meta/postgres/q/search.sql.go")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile("(?s)-- name: " + name + " :many\n(.*?)`").FindSubmatch(src)
	if m == nil {
		t.Fatalf("no statement %s in search.sql.go", name)
	}
	return string(m[1])
}
