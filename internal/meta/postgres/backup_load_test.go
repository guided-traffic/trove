package postgres

import (
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/backup/tables"
)

func fk(table, column, ref string) tables.ForeignKey {
	return tables.ForeignKey{Table: table, Column: column, RefTable: ref}
}

func TestR_BAK_004_LoadOrder(t *testing.T) {
	names := func(order []string) string { return strings.Join(order, ",") }
	ts := func(names ...string) []LoadTable {
		out := make([]LoadTable, len(names))
		for i, n := range names {
			out[i] = LoadTable{Name: n}
		}
		return out
	}

	t.Run("R-BAK-004 parents load before children, ties by name", func(t *testing.T) {
		order, err := loadOrder(ts("items", "repositories", "packages", "blobs"), []tables.ForeignKey{
			fk("items", "repo_id", "repositories"), fk("items", "blob_sha256", "blobs"), fk("packages", "repo_id", "repositories"),
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := names(order); got != "blobs,repositories,items,packages" {
			t.Fatalf("order = %s", got)
		}
	})

	t.Run("R-BAK-004 a cycle is cut at the CycleBreak column and the partner loads first", func(t *testing.T) {
		in := ts("items", "package_versions", "packages")
		in[1].CycleBreak = "lead_item_id"
		order, err := loadOrder(in, []tables.ForeignKey{
			fk("items", "version_id", "package_versions"), fk("package_versions", "lead_item_id", "items"),
			fk("package_versions", "package_id", "packages"),
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := names(order); got != "packages,package_versions,items" {
			t.Fatalf("order = %s", got)
		}
	})

	t.Run("R-BAK-004 a cycle without a break is refused, self references and unexported tables are ignored", func(t *testing.T) {
		_, err := loadOrder(ts("a", "b"), []tables.ForeignKey{fk("a", "b_id", "b"), fk("b", "a_id", "a")})
		if err == nil || !strings.Contains(err.Error(), "a, b") {
			t.Fatalf("err = %v", err)
		}
		order, err := loadOrder(ts("principal", "x"), []tables.ForeignKey{fk("principal", "parent", "principal"), fk("x", "p", "transient_table")})
		if err != nil || names(order) != "principal,x" {
			t.Fatalf("order = %v, %v", order, err)
		}
	})
}

func TestR_BAK_021_CheckLoad(t *testing.T) {
	reg := tables.New()
	reg.Register("items", tables.Data, tables.Options{})
	reg.Register("ui_session", tables.Transient, tables.Options{})
	reg.Register("schema_migration", tables.Schema, tables.Options{})
	cat := loadCatalog{
		tables:    []string{"items", "ui_session", "schema_migration", "extra"},
		columns:   map[string][][2]string{"items": {{"id", "bigint"}, {"path", "text"}}},
		sequences: map[string]bool{"items_id_seq": true},
	}
	plan := LoadPlan{
		Tables:    []LoadTable{{Name: "items", Columns: [][2]string{{"id", "bigint"}, {"path", "text"}}}},
		Sequences: []LoadSequence{{Name: "items_id_seq"}},
	}
	t.Run("R-BAK-021 an unregistered table the backup lacks is a mismatch, transient and schema tables are not", func(t *testing.T) {
		got := checkLoad(plan, reg, cat)
		if len(got) != 1 || !strings.Contains(got[0], "table extra of the schema is not in the backup") {
			t.Fatalf("problems = %v", got)
		}
	})
	t.Run("R-BAK-021 a column list that differs in name, type or order is a mismatch", func(t *testing.T) {
		cat.tables = []string{"items"}
		for _, cols := range [][][2]string{
			{{"id", "bigint"}, {"path", "varchar"}}, {{"path", "text"}, {"id", "bigint"}}, {{"id", "bigint"}},
		} {
			p := plan
			p.Tables = []LoadTable{{Name: "items", Columns: cols}}
			got := checkLoad(p, reg, cat)
			if len(got) != 1 || !strings.Contains(got[0], "table items has the columns (id bigint, path text)") {
				t.Errorf("columns %v: problems = %v", cols, got)
			}
		}
	})
	t.Run("R-BAK-021 a table or sequence of the backup that the schema lacks is a mismatch", func(t *testing.T) {
		p := plan
		p.Tables = append([]LoadTable{{Name: "gone", Columns: [][2]string{{"id", "bigint"}}}}, plan.Tables...)
		p.Sequences = []LoadSequence{{Name: "gone_seq"}}
		got := checkLoad(p, reg, cat)
		if len(got) != 2 {
			t.Fatalf("problems = %v", got)
		}
	})
}

func TestParseHexDigest(t *testing.T) {
	d, err := parseHexDigest(strings.Repeat("ab", 32))
	if err != nil || len(d) != 32 || d[0] != 0xab {
		t.Fatalf("digest = %x, %v", d, err)
	}
	for _, bad := range []string{"", strings.Repeat("zz", 32), strings.Repeat("a", 63)} {
		if _, err := parseHexDigest(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}
