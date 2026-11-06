//go:build integration

package vulnsync

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/vuln/store"
)

// importState is the state of the tables in the first import of an installation: the autovacuum has not reached them, has analyzed
// them at the first few records of the ecosystem, or has analyzed them when only another ecosystem was stored, which is what the
// import of NuGet before npm leaves.
type importState struct {
	name       string
	analyzedAt int  // the number of npm records the table held when it was analyzed; 0 never
	nugetFirst bool // 7,403 NuGet records were stored and analyzed before the first npm record
}

var importStates = []importState{
	{"never analyzed", 0, false},
	{"analyzed at the first 2,000 records", 2000, false},
	{"analyzed when only NuGet was stored", 0, true},
}

// planModes are the plans a prepared statement gets: one made for the values of the call (the first five executions of a statement,
// and the only one of a statement that runs once) and the generic plan that follows. A value the statistics never saw is estimated
// from them in the first and not in the second, which is where the two differ.
var planModes = []string{"force_custom_plan", "force_generic_plan"}

// eachImport runs f in every state and plan mode, with a transaction on a table in that state.
func eachImport(t *testing.T, records int, f func(t *testing.T, tx pgx.Tx, state importState, mode string)) {
	t.Helper()
	for _, state := range importStates {
		for _, mode := range planModes {
			t.Run(state.name+", "+mode, func(t *testing.T) { f(t, importedAdvisories(t, records, state, mode), state, mode) })
		}
	}
}

// importedAdvisories stores npm records in the state, in the width of real advisories, and returns a transaction that gets the plans of
// the mode and the statement timeout that ends a plan which would not end.
func importedAdvisories(t *testing.T, records int, state importState, mode string) pgx.Tx {
	t.Helper()
	st, conn := groupRowsDB(t)
	st.Exec(`ALTER TABLE vuln_advisory SET (autovacuum_enabled = false)`)
	load := func(ecosystem, prefix string, from, to int) {
		st.Exec(`INSERT INTO vuln_advisory (ecosystem, id, canonical_id, modified, kind, aliases, summary, details, severity, severity_source, record_sha256)
			SELECT $1, $2 || lpad(g::text, 7, '0'), $2 || lpad(g::text, 7, '0'), now(), 'malicious', '{}', repeat('s', 80), repeat('d', 600),
			       'high', 'cvss', '\x00' FROM generate_series($3::int, $4::int - 1) g`, ecosystem, prefix, from, to)
	}
	if state.nugetFirst {
		load("NuGet", "GHSA-nu-", 0, 7403)
		st.Exec(`ANALYZE vuln_advisory`)
	}
	if state.analyzedAt > 0 {
		load("npm", "MAL-2026-", 0, state.analyzedAt)
		st.Exec(`ANALYZE vuln_advisory`)
		load("npm", "MAL-2026-", state.analyzedAt, records)
	} else {
		load("npm", "MAL-2026-", 0, records)
	}
	if !state.nugetFirst && state.analyzedAt == 0 {
		if n := st.Number(`SELECT count(*) FROM pg_stats WHERE tablename = 'vuln_advisory'`); n != 0 {
			t.Fatalf("the table was analyzed (%d columns with statistics): the test needs the state of a first import", n)
		}
	}
	for _, set := range []string{`SET plan_cache_mode = ` + mode, `SET statement_timeout = '20s'`} {
		if _, err := conn.Exec(t.Context(), set); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}

func advisoryID(n int) string { return fmt.Sprintf("MAL-2026-%07d", n) }

// advisoryReads returns the tuples the transaction fetched through indexes and read sequentially in vuln_advisory.
func advisoryReads(t *testing.T, tx pgx.Tx) (fetched, seq int64) {
	t.Helper()
	if err := tx.QueryRow(t.Context(), `SELECT COALESCE(sum(idx_tup_fetch), 0), COALESCE(sum(seq_tup_read), 0) FROM pg_stat_xact_user_tables WHERE relname = 'vuln_advisory'`).
		Scan(&fetched, &seq); err != nil {
		t.Fatal(err)
	}
	return fetched, seq
}

// TestR_VULN_028_TheOrphansRoundDoesNotDependOnTheStatistics is F-57 itself (Q-VULN-95, Q-VULN-97). The first statement after the
// last batch of the import looks for the records whose canonical record is gone, and with the statistics of a table that held only
// NuGet it was planned for the value 'npm' as a nested loop in which every one of the 231,012 records was tested against every one of
// the 231,012: a statement of an hour, ended by the statement timeout after 30 s, and the sync `database_timeout`. Every record is
// looked up by the primary key, and the statistics of an ecosystem column are not asked.
func TestR_VULN_028_TheOrphansRoundDoesNotDependOnTheStatistics(t *testing.T) {
	eachImport(t, 50000, func(t *testing.T, tx pgx.Tx, state importState, mode string) {
		ctx := t.Context()
		if _, err := tx.Exec(ctx, `UPDATE vuln_advisory SET canonical_id = 'GHSA-gone' WHERE ecosystem = 'npm' AND id = ANY($1)`, []string{advisoryID(10), advisoryID(49999)}); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		got, err := store.Sync{}.Orphans(ctx, tx, "npm")
		d := time.Since(start)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(got)
		if want := []string{advisoryID(10), advisoryID(49999)}; !slices.Equal(got, want) {
			t.Fatalf("orphans %v, want %v", got, want)
		}
		t.Logf("the orphans of 50,000 records, %s, %s: %v", state.name, mode, d.Round(time.Millisecond))
		if d > 5*time.Second {
			t.Errorf("the round took %v", d)
		}
	})
}

// TestR_VULN_028_TheGroupRowsRoundDoesNotDependOnWhichEcosystemWasAnalyzed is the closure's round (Q-VULN-94) in the states of
// importStates, the last of which made the planner scan the whole ecosystem for every name: the two indexes on `ecosystem` were
// estimated at one row each, and the one without the identifier won the tie.
func TestR_VULN_028_TheGroupRowsRoundDoesNotDependOnWhichEcosystemWasAnalyzed(t *testing.T) {
	eachImport(t, 50000, func(t *testing.T, tx pgx.Tx, state importState, mode string) {
		names := make([]string, 0, 2000)
		for i := range 2000 {
			names = append(names, advisoryID(40000+i))
		}
		start := time.Now()
		rows, err := store.Sync{}.GroupRows(t.Context(), tx, "npm", names)
		d := time.Since(start)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != len(names) {
			t.Fatalf("%d rows, want %d", len(rows), len(names))
		}
		fetched, seq := advisoryReads(t, tx)
		t.Logf("one round of %d names against 50,000 records, %s, %s: %v, %d tuples fetched through indexes, %d read sequentially", len(names), state.name, mode, d.Round(time.Millisecond), fetched, seq)
		if limit := int64(4 * len(names)); fetched > limit || seq != 0 {
			t.Errorf("the round fetched %d tuples through indexes (limit %d) and read %d sequentially: the table is not looked up name by name", fetched, limit, seq)
		}
	})
}

// TestR_VULN_028_TheStoredLookupDoesNotDependOnTheStatistics is the second statement of F-57 (Q-VULN-95). A batch of 1,000 records
// asks which of its identifiers are stored, and `id = ANY(ids)` was planned, for statistics that knew only another ecosystem, as a
// bitmap scan of the ecosystem's index, which returns every stored row of the ecosystem for every batch and filters them: 0.5 s a
// batch at 42,000 stored records, 1.2 s at 100,000, 86 of the import's 232 batches, 65 s of 107 s. The lookup reads the primary key
// once per identifier.
func TestR_VULN_028_TheStoredLookupDoesNotDependOnTheStatistics(t *testing.T) {
	eachImport(t, 100000, func(t *testing.T, tx pgx.Tx, state importState, mode string) {
		ids := make([]string, 0, 1000)
		for i := range 500 {
			ids = append(ids, advisoryID(90000+i), "MAL-2026-new-"+strconv.Itoa(i))
		}
		start := time.Now()
		got, err := store.Sync{}.Stored(t.Context(), tx, "npm", ids)
		d := time.Since(start)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 500 {
			t.Fatalf("%d stored identifiers, want 500", len(got))
		}
		fetched, seq := advisoryReads(t, tx)
		t.Logf("one batch of %d identifiers against 100,000 records, %s, %s: %v, %d tuples fetched through indexes, %d read sequentially", len(ids), state.name, mode, d.Round(time.Millisecond), fetched, seq)
		if limit := int64(len(ids)); fetched > limit || seq != 0 {
			t.Errorf("the lookup fetched %d tuples through indexes (limit %d) and read %d sequentially: the table is not looked up identifier by identifier", fetched, limit, seq)
		}
	})
}

// TestR_VULN_028_TheKnownExploitedRoundDoesNotDependOnTheStatistics is the third (Q-VULN-95). The end of every sync applies the
// catalog's flags to the canonical records in chunks of 2,000, and the statement read the whole ecosystem twice per chunk, as a bitmap
// scan for the records and as the build side of the update's hash join, on a table that has not been analyzed: 231,012 rows twice for
// each of 116 chunks, 43 s of an import whose statements take 7 s in total with the lookups of this test. A chunk reads the group of
// each of its records and nothing else, and updates only the records whose flag differs.
func TestR_VULN_028_TheKnownExploitedRoundDoesNotDependOnTheStatistics(t *testing.T) {
	eachImport(t, 50000, func(t *testing.T, tx pgx.Tx, state importState, mode string) {
		ctx := t.Context()
		if _, err := tx.Exec(ctx, `INSERT INTO vuln_kev (cve_id, date_added) VALUES ('CVE-2026-1', '2026-01-02'), ('CVE-2026-2', '2026-01-03')`); err != nil {
			t.Fatal(err)
		}
		// One record of the chunk aliases a listed identifier, another is the canonical record of a group whose member does.
		if _, err := tx.Exec(ctx, `UPDATE vuln_advisory SET aliases = '{CVE-2026-1}' WHERE ecosystem = 'npm' AND id = $1`, advisoryID(40010)); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE vuln_advisory SET canonical_id = $1, aliases = '{CVE-2026-2}' WHERE ecosystem = 'npm' AND id = $2`, advisoryID(40020), advisoryID(10)); err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, 2000)
		for i := range 2000 {
			ids = append(ids, advisoryID(40000+i))
		}
		start := time.Now()
		changed, err := store.Sync{}.ApplyKEV(ctx, tx, "npm", ids)
		d := time.Since(start)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(changed)
		if want := []string{advisoryID(40010), advisoryID(40020)}; !slices.Equal(changed, want) {
			t.Fatalf("changed %v, want %v", changed, want)
		}
		if again, err := (store.Sync{}).ApplyKEV(ctx, tx, "npm", ids); err != nil || len(again) != 0 {
			t.Errorf("the second round changed %v (%v)", again, err)
		}
		fetched, seq := advisoryReads(t, tx)
		var flagged int
		var added string
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE known_exploited), min(kev_date_added)::text FROM vuln_advisory WHERE ecosystem = 'npm'`).Scan(&flagged, &added); err != nil {
			t.Fatal(err)
		}
		if flagged != 2 || added != "2026-01-02" {
			t.Errorf("%d flagged, earliest date %s", flagged, added)
		}
		t.Logf("a chunk of %d records against 50,000 records, %s, %s, twice: %v the first time, %d tuples fetched through indexes, %d read sequentially", len(ids), state.name, mode, d.Round(time.Millisecond), fetched, seq)
		if limit := int64(4 * 2 * len(ids)); fetched > limit || seq != 0 {
			t.Errorf("the rounds fetched %d tuples through indexes (limit %d) and read %d sequentially: the table is not looked up record by record", fetched, limit, seq)
		}
	})
}

// TestR_VULN_028_TheSyncsAnalyzeFillsTheStatisticsOfTheImportedTables is Q-VULN-96 against the database: the statement the sync runs
// after a large import gives the tables it filled their statistics.
func TestR_VULN_028_TheSyncsAnalyzeFillsTheStatisticsOfTheImportedTables(t *testing.T) {
	tx := importedAdvisories(t, 1000, importStates[0], planModes[0])
	if err := (store.Sync{}).Analyze(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM pg_stats WHERE tablename = 'vuln_advisory'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("vuln_advisory has no statistics after the sync's ANALYZE")
	}
}

// TestR_VULN_028_TheEcosystemColumnsOfTheSyncsTablesHaveNoStatistics pins migration 00042 (Q-VULN-97): ANALYZE keeps no statistics
// of the column that names the ecosystem, so that no plan can be made of what a sample of another ecosystem said about it.
func TestR_VULN_028_TheEcosystemColumnsOfTheSyncsTablesHaveNoStatistics(t *testing.T) {
	st, _ := groupRowsDB(t)
	for _, tb := range []string{"vuln_advisory", "vuln_affected", "vuln_sync_touched", "vuln_pending_evaluation"} {
		if target := st.Number(`SELECT COALESCE(attstattarget::int, -1) FROM pg_attribute WHERE attrelid = $1::regclass AND attname = 'ecosystem'`, tb); target != 0 {
			t.Errorf("%s.ecosystem: statistics target %d, want 0", tb, target)
		}
	}
	st.Exec(`INSERT INTO vuln_advisory (ecosystem, id, canonical_id, modified, kind, aliases, summary, severity, severity_source, record_sha256)
		SELECT 'NuGet', 'GHSA-nu-' || g, 'GHSA-nu-' || g, now(), 'malicious', '{}', '', 'high', 'cvss', '\x00' FROM generate_series(0, 999) g`)
	st.Exec(`ANALYZE vuln_advisory`)
	if n := st.Number(`SELECT count(*) FROM pg_stats WHERE schemaname = 'trove' AND tablename = 'vuln_advisory' AND attname = 'ecosystem'`); n != 0 {
		t.Errorf("ANALYZE kept statistics of vuln_advisory.ecosystem")
	}
	if n := st.Number(`SELECT count(*) FROM pg_stats WHERE schemaname = 'trove' AND tablename = 'vuln_advisory' AND attname = 'id'`); n == 0 {
		t.Errorf("ANALYZE kept no statistics of vuln_advisory.id: the other columns keep theirs")
	}
}
