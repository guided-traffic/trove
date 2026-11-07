package tables_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/backup/tables"
	_ "github.com/guided-traffic/trove/internal/blob"
	_ "github.com/guided-traffic/trove/internal/cluster"
	_ "github.com/guided-traffic/trove/internal/formatdata/nuget"
	_ "github.com/guided-traffic/trove/internal/jobs"
	_ "github.com/guided-traffic/trove/internal/meta/postgres"
)

// The classes the pages state for their tables are compared with the registrations of the packages that own them
// (R-STOR-047 acceptance criterion, R-NPM-047, R-NUGET-033). The page is read, not copied: a change of the page's table
// or of a registration fails here.

func repoFile(t *testing.T, rel ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{"..", "..", ".."}, rel...)...))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var (
	backtick    = regexp.MustCompile("`([a-z0-9_]+)`")
	createTable = regexp.MustCompile(`(?m)^\s*CREATE TABLE ([a-z0-9_]+)`)
)

// classTable reads the rows of R-STOR-047's export-class table: the tables of the first cell with the class of the second,
// and the class of "every other table of this page".
func classTable(t *testing.T, page string) (named map[string]string, other string) {
	t.Helper()
	start := strings.Index(page, "- **Export classes** of this page's tables")
	if start < 0 {
		t.Fatal("storage.md: the export class list of R-STOR-047 is gone")
	}
	end := strings.Index(page[start:], "- **Restore fix-ups.**")
	if end < 0 {
		t.Fatal("storage.md: R-STOR-047 has no restore fix-ups bullet after the table")
	}
	named = map[string]string{}
	for _, line := range strings.Split(page[start:start+end], "\n") {
		cells := strings.Split(strings.TrimSpace(line), "|")
		if len(cells) < 4 || !strings.HasPrefix(strings.TrimSpace(cells[1]), "`") && !strings.HasPrefix(strings.TrimSpace(cells[1]), "every other") {
			continue
		}
		class := strings.Trim(strings.TrimSpace(cells[2]), "`")
		if strings.HasPrefix(strings.TrimSpace(cells[1]), "every other") {
			other = class
			continue
		}
		for _, m := range backtick.FindAllStringSubmatch(cells[1], -1) {
			named[m[1]] = class
		}
	}
	if len(named) == 0 || other == "" {
		t.Fatalf("storage.md: read %d named tables and class %q for the others", len(named), other)
	}
	return named, other
}

// migrated reports whether a migration creates the table.
func migrated(t *testing.T, name string) bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "meta", "postgres", "migrations", "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("migrations: %v (%d files)", err, len(files))
	}
	re := regexp.MustCompile(`(?m)^CREATE TABLE ` + regexp.QuoteMeta(name) + ` `)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if re.Match(b) {
			return true
		}
	}
	return false
}

func TestR_STOR_047_ClassesMatchTheRegistrations(t *testing.T) {
	page := repoFile(t, "docs", "spec", "storage.md")
	named, other := classTable(t, page)

	expected := map[string]string{}
	for name, class := range named {
		expected[name] = class
	}
	for _, m := range createTable.FindAllStringSubmatch(page, -1) {
		if _, ok := expected[m[1]]; !ok {
			expected[m[1]] = other
		}
	}
	names := make([]string, 0, len(expected))
	for n := range expected {
		names = append(names, n)
	}
	sort.Strings(names)

	var registered, pending []string
	for _, name := range names {
		got, ok := tables.Lookup(name)
		if !ok {
			if migrated(t, name) {
				t.Errorf("%s: the page gives it class %q and a migration creates it, but nothing registers it", name, expected[name])
			} else {
				pending = append(pending, name)
			}
			continue
		}
		registered = append(registered, name)
		if got.Class.String() != expected[name] {
			t.Errorf("%s: registered as %s, the page says %s", name, got.Class, expected[name])
		}
	}
	// The table of the page names the classes of the storage tables that exist today; the tables of later pages are
	// compared when they are registered.
	for _, must := range []string{"blobs", "blob_gc_queue", "items", "packages", "package_versions", "item_properties", "item_download_stats", "repository_usage", "storage_usage_totals", "storage_usage_deltas"} {
		if !contains(registered, must) {
			t.Errorf("%s is a storage table that exists and was not compared (compared: %v)", must, registered)
		}
	}
	t.Logf("compared %d registrations; the page's tables not built yet: %v", len(registered), pending)
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// The npm and NuGet tables are all data and none is transient (R-NPM-047, R-NUGET-033).
func TestR_NPM_047_TheFormatTablesAreAllData(t *testing.T) {
	for _, name := range []string{
		"npm_packages", "npm_versions", "npm_tombstones", "npm_remote_packages", "npm_remote_versions",
		"nuget_version", "nuget_tombstone", "nuget_symbol_package", "nuget_symbol_file",
	} {
		got, ok := tables.Lookup(name)
		if !ok || got.Class != tables.Data {
			t.Errorf("%s: registered %v (found %v), want data", name, got.Class, ok)
		}
	}
}
