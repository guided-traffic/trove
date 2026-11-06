//go:build integration

package vulnsync

import (
	"context"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/vuln/store"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// groupRowsDB is a migrated database without replicas and a connection to it that reads the tables of the schema.
func groupRowsDB(t *testing.T) (*testenv.Stack, *pgx.Conn) {
	t.Helper()
	st := testenv.NewStack(t, testenv.StackOptions{Name: "grouprows"})
	conn, err := pgx.Connect(t.Context(), testenv.PgDSN(t, st.Database()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	if _, err := conn.Exec(t.Context(), `SET search_path = trove`); err != nil {
		t.Fatal(err)
	}
	return st, conn
}

// TestR_VULN_006_TheGroupRowsAreTheStoredRecordsThatAreOrAliasTheNames pins the result of the statement of one closure round
// (Q-VULN-90, Q-VULN-91): a record that is a name, a record that lists a name as an alias, and nothing of another ecosystem or
// of a record that merely shares an alias with a name.
func TestR_VULN_006_TheGroupRowsAreTheStoredRecordsThatAreOrAliasTheNames(t *testing.T) {
	st, conn := groupRowsDB(t)
	st.Exec(`INSERT INTO vuln_advisory (ecosystem, id, canonical_id, modified, kind, aliases, summary, severity, severity_source, record_sha256)
		SELECT e, i, i, now(), 'vulnerability', a::text[], '', 'high', 'cvss', '\x00' FROM (VALUES
		  ('npm',   'GHSA-a', '{CVE-1}'),
		  ('npm',   'CVE-1',  '{GHSA-a}'),
		  ('npm',   'GHSA-b', '{CVE-2,CVE-3}'),
		  ('npm',   'GHSA-c', '{CVE-3}'),
		  ('npm',   'GHSA-d', '{}'),
		  ('npm',   'CVE-9',  '{}'),
		  ('nuget', 'GHSA-a', '{CVE-1}'),
		  ('nuget', 'CVE-2',  '{}')) AS v(e, i, a)`)
	for _, c := range []struct {
		names []string
		want  []string
	}{
		{[]string{"GHSA-a"}, []string{"CVE-1", "GHSA-a"}},
		{[]string{"CVE-1"}, []string{"CVE-1", "GHSA-a"}},
		{[]string{"CVE-2"}, []string{"GHSA-b"}},
		{[]string{"CVE-3"}, []string{"GHSA-b", "GHSA-c"}},
		{[]string{"GHSA-d", "CVE-9", "GHSA-none"}, []string{"CVE-9", "GHSA-d"}},
		{[]string{"GHSA-b", "CVE-2", "CVE-3"}, []string{"GHSA-b", "GHSA-c"}},
		{[]string{"GHSA-none"}, nil},
		{nil, nil},
	} {
		rows, err := store.Sync{}.GroupRows(t.Context(), conn, "npm", c.names)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range rows {
			got = append(got, r.ID)
		}
		slices.Sort(got)
		if !slices.Equal(got, c.want) {
			t.Errorf("names %v: got %v, want %v", c.names, got, c.want)
		}
	}
}

// TestR_VULN_028_TheGroupRowsRoundReadsTheIndexesOnly is F-57, which had two causes (Q-VULN-90, Q-VULN-91). The index of the
// aliases was a GIN index with fastupdate, which keeps what an insert adds in a pending list that a lookup reads in full for
// every key, so a round of 2,000 names took seconds while the import was inserting; the index is built without the list
// (migration 00041) and the list is empty after the inserts of an import's batches. And with aliases that are mostly empty the
// planner takes `aliases && names` for a match of a share of the table and scanned the table sequentially in every round; the
// statement probes the two indexes once per name, and the round reads no table sequentially. The two assertions are
// deterministic; the round is logged and bounded loosely (the shared stack's load is not the reference environment's).
func TestR_VULN_028_TheGroupRowsRoundReadsTheIndexesOnly(t *testing.T) {
	st, conn := groupRowsDB(t)
	st.Exec(`CREATE EXTENSION IF NOT EXISTS pgstattuple`)
	st.Exec(`INSERT INTO vuln_advisory (ecosystem, id, canonical_id, modified, kind, aliases, summary, severity, severity_source, record_sha256)
		SELECT 'npm', 'GHSA-fill-' || g, 'GHSA-fill-' || g, now(), 'vulnerability', '{}', 'a test advisory', 'high', 'cvss', '\x00'
		  FROM generate_series(0, 230999) g`)
	st.Exec(`ANALYZE vuln_advisory`)
	// The records of an import arrive in batches of 500 in one transaction each, with aliases.
	for b := range 24 {
		st.Exec(`INSERT INTO vuln_advisory (ecosystem, id, canonical_id, modified, kind, aliases, summary, severity, severity_source, record_sha256)
			SELECT 'npm', 'GHSA-imp-' || g, 'GHSA-imp-' || g, now(), 'vulnerability', ARRAY['CVE-imp-' || g, 'GHSA-imp-alias-' || g], '',
			       'high', 'cvss', '\x00'
			  FROM generate_series($1::int, $2::int) g`, b*500, b*500+499)
	}
	if n := st.Number(`SELECT pending_tuples FROM pgstatginindex('trove.vuln_advisory_aliases')`); n != 0 {
		t.Fatalf("the index of the aliases holds %d tuples in its pending list; a lookup reads all of them for every name", n)
	}

	names := make([]string, 0, 2000)
	for i := range 1000 {
		names = append(names, "GHSA-imp-"+strconv.Itoa(i), "CVE-imp-"+strconv.Itoa(i+1000))
	}
	// The statistics are those of a table whose aliases are mostly empty, as the import's first batches leave them.
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	start := time.Now()
	rows, err := store.Sync{}.GroupRows(t.Context(), tx, "npm", names)
	d := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2000 {
		t.Fatalf("%d rows, want 2000 (1000 by id, 1000 by alias)", len(rows))
	}
	t.Logf("one round of %d names against 231,000 + 12,000 records: %v", len(names), d.Round(time.Millisecond))
	if d > 5*time.Second {
		t.Errorf("the round took %v", d)
	}
	var seq, idx int64
	if err := tx.QueryRow(t.Context(), `SELECT COALESCE(sum(seq_scan), 0), COALESCE(sum(idx_scan), 0) FROM pg_stat_xact_user_tables WHERE relname = 'vuln_advisory'`).
		Scan(&seq, &idx); err != nil {
		t.Fatal(err)
	}
	if seq != 0 || idx == 0 {
		t.Errorf("the round scanned the table sequentially %d times and used its indexes %d times; every name must be looked up in the indexes", seq, idx)
	}
}
