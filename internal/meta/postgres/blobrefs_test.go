package postgres

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestBlobRefsStatements(t *testing.T) {
	cols := []pgx.Identifier{
		{"trove", "items", "blob_sha256"},
		{"trove", "retention_run_items", "blob_sha256"},
		{`od"d`, "table", `Col`},
	}
	r := newBlobRefs(cols)

	t.Run("R-STOR-024 the full sweep anti-joins every table with a foreign key to blobs", func(t *testing.T) {
		for _, want := range []string{
			`NOT EXISTS (SELECT 1 FROM "trove"."items" r WHERE r."blob_sha256" = b.sha256)`,
			`NOT EXISTS (SELECT 1 FROM "trove"."retention_run_items" r WHERE r."blob_sha256" = b.sha256)`,
			"b.state = 'live'", "ORDER BY b.sha256 LIMIT $3",
		} {
			if !strings.Contains(r.sweep, want) {
				t.Errorf("missing %q in\n%s", want, r.sweep)
			}
		}
		// A table that carries the blob_sha256 column but no foreign key is not
		// listed; the catalog read is what makes a newer release's table count.
		if strings.Contains(newBlobRefs(nil).sweep, "NOT EXISTS") {
			t.Error("an anti-join without tables")
		}
	})
	t.Run("R-STOR-024 the reference check asks every table in one statement", func(t *testing.T) {
		want := `SELECT EXISTS (SELECT 1 FROM "trove"."items" WHERE "blob_sha256" = $1) OR ` +
			`EXISTS (SELECT 1 FROM "trove"."retention_run_items" WHERE "blob_sha256" = $1) OR ` +
			`EXISTS (SELECT 1 FROM "od""d"."table" WHERE "Col" = $1)`
		if r.referenced != want {
			t.Errorf("referenced =\n%s\nwant\n%s", r.referenced, want)
		}
		if newBlobRefs(nil).referenced != "" || newBlobRefs(nil).Len() != 0 {
			t.Error("a statement without tables")
		}
	})
	t.Run("R-ARCH-019 catalog names are quoted as identifiers, whatever they contain", func(t *testing.T) {
		evil := newBlobRefs([]pgx.Identifier{{"s", `t"; DROP TABLE blobs; --`, "c"}})
		if strings.Contains(evil.referenced, `t"; DROP`) || !strings.Contains(evil.referenced, `"t""; DROP TABLE blobs; --"`) {
			t.Errorf("unquoted name in %s", evil.referenced)
		}
	})
}
