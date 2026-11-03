//go:build integration

package postgres

import (
	"context"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// The issuance statements on a seeded table (R-CI-020, R-HA-018, Q-CI-53): migrations 00029 to 00031 are expand
// steps, each a single CREATE INDEX CONCURRENTLY, and the plans of the list statements use them instead of sorting
// the whole table. The statements are read from the query file and run through EXPLAIN with the arguments a request
// gives, so the test follows the statement and not a copy of it.

const (
	planIssuerA  = "cd000000-0000-0000-0000-00000000000a"
	planIssuerB  = "cd000000-0000-0000-0000-00000000000b"
	planProject  = "cd000000-0000-0000-0000-0000000000f1"
	planMapping  = "cd000000-0000-0000-0000-0000000000c1"
	planToken    = "cd000000-0000-0000-0000-0000000000e1"
	planRowCount = 200000
)

var sqlcArg = regexp.MustCompile(`sqlc\.arg\((\w+)\)`)

// statement reads the named statement of a query file and numbers its arguments in order of first use.
func statement(t *testing.T, file, name string) (string, []string) {
	t.Helper()
	raw, err := os.ReadFile("../../../internal/meta/postgres/queries/" + file)
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(raw), "-- name: "+name+" ")
	if !ok {
		t.Fatalf("%s has no statement %s", file, name)
	}
	body, _, _ := strings.Cut(rest, "\n-- name: ")
	var lines []string
	for _, l := range strings.Split(body, "\n")[1:] {
		if !strings.HasPrefix(strings.TrimSpace(l), "--") {
			lines = append(lines, l)
		}
	}
	var args []string
	sql := sqlcArg.ReplaceAllStringFunc(strings.Join(lines, "\n"), func(m string) string {
		arg := sqlcArg.FindStringSubmatch(m)[1]
		for i, a := range args {
			if a == arg {
				return "$" + strconv.Itoa(i+1)
			}
		}
		args = append(args, arg)
		return "$" + strconv.Itoa(len(args))
	})
	return strings.TrimSuffix(strings.TrimSpace(sql), ";"), args
}

func TestCIIssuanceIndexes(t *testing.T) {
	st := getStack(t)
	ensureRuntimeRole(t, st)
	_, _, admin := migrated(t, st)
	ctx := context.Background()
	mustExec(t, admin, "SET search_path = trove, public")

	t.Run("R-HA-018 the index steps are expand steps of one concurrent statement and the indexes exist", func(t *testing.T) {
		steps, err := postgres.EmbeddedSteps()
		if err != nil {
			t.Fatal(err)
		}
		want := map[int]string{29: "ci_issuances_issued", 30: "ci_issuances_issuer_issued", 31: "ci_issuances_contributions"}
		for _, s := range steps {
			index, ok := want[s.Version]
			if !ok {
				continue
			}
			delete(want, s.Version)
			if s.Phase != "expand" || !s.NoTx || !strings.Contains(s.SQL, "CREATE INDEX CONCURRENTLY IF NOT EXISTS "+index+" ") {
				t.Errorf("%s: phase %q, no-transaction %v", s.Name, s.Phase, s.NoTx)
			}
			if !scalar[bool](t, admin, "SELECT to_regclass($1) IS NOT NULL", schemaName+"."+index) {
				t.Errorf("the index %s is missing", index)
			}
		}
		if len(want) != 0 {
			t.Errorf("steps missing: %v", want)
		}
	})

	mustExec(t, admin, `INSERT INTO ci_issuers (id, name, kind, issuer, allowed_audiences, required_claims, pin_claims, single_use_subject_tokens)
		VALUES ($1, 'gitlab-a', 'gitlab', 'https://gitlab.a.example', ARRAY['x'], ARRAY['jti'], ARRAY['project_id'], false),
		       ($2, 'gitlab-b', 'gitlab', 'https://gitlab.b.example', ARRAY['x'], ARRAY['jti'], ARRAY['project_id'], false)`, planIssuerA, planIssuerB)
	// Two issuers (B holds one record in 100), one record every 10 seconds backwards from now, each living 10 minutes:
	// the newest 60 are unexpired, the rest are within the retention; one record in 1000 has a contribution of planProject.
	mustExec(t, admin, `INSERT INTO ci_token_issuances (token_id, issuer_id, mapping_ids, mapping_names, subject_kind, project_id, job_id,
		claims, grants, granted_scope, issued_at, expires_at)
		SELECT gen_random_uuid(), CASE WHEN g % 100 = 0 THEN $2::uuid ELSE $1::uuid END, ARRAY[CASE WHEN g = 700 THEN $4::uuid ELSE gen_random_uuid() END],
		       ARRAY['m'], 'id_token', (g % 500)::text, g::text, '{}'::jsonb,
		       jsonb_build_object('contributions', jsonb_build_array(jsonb_build_object('project', jsonb_build_object('id',
		           CASE WHEN g % 1000 = 0 THEN $3 ELSE gen_random_uuid()::text END)))),
		       'read', now() - g * interval '10 seconds', now() - g * interval '10 seconds' + interval '10 minutes'
		  FROM generate_series(1, $5::int) g`, planIssuerA, planIssuerB, planProject, planMapping, planRowCount)
	mustExec(t, admin, "ANALYZE ci_token_issuances")

	plan := func(t *testing.T, file, name string, args map[string]any) string {
		t.Helper()
		sql, order := statement(t, file, name)
		values := make([]any, len(order))
		for i, a := range order {
			v, ok := args[a]
			if !ok {
				t.Fatalf("%s: no value for %s", name, a)
			}
			values[i] = v
		}
		rows, err := admin.Query(ctx, "EXPLAIN (FORMAT TEXT) "+sql, values...)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		defer rows.Close()
		var lines []string
		for rows.Next() {
			var l string
			if err := rows.Scan(&l); err != nil {
				t.Fatal(err)
			}
			lines = append(lines, l)
		}
		out := strings.Join(lines, "\n")
		t.Logf("%s:\n%s", name, out)
		return out
	}
	none := map[string]any{
		"issuer": "", "project_id": "", "project_path": "", "pipeline_id": "", "job_id": "", "mapping": "", "active": "",
		"from_us": int64(0), "to_us": int64(0), "after_us": int64(0), "after_id": planToken, "max_rows": int32(101),
	}
	with := func(kv ...any) map[string]any {
		out := map[string]any{}
		for k, v := range none {
			out[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			out[kv[i].(string)] = kv[i+1]
		}
		return out
	}

	t.Run("Q-CI-53 the list without a filter walks ci_issuances_issued and does not sort", func(t *testing.T) {
		p := plan(t, "ci_issuances.sql", "CiIssuanceList", none)
		if !strings.Contains(p, "ci_issuances_issued") || strings.Contains(p, "Sort") {
			t.Errorf("plan:\n%s", p)
		}
	})
	t.Run("Q-CI-53 the list of a small issuer walks ci_issuances_issuer_issued and does not sort", func(t *testing.T) {
		p := plan(t, "ci_issuances.sql", "CiIssuanceList", with("issuer", "gitlab-b"))
		if !strings.Contains(p, "ci_issuances_issuer_issued") || strings.Contains(p, "Sort") {
			t.Errorf("plan:\n%s", p)
		}
	})
	t.Run("Q-CI-53 the project's list finds its records through the GIN index", func(t *testing.T) {
		p := plan(t, "ci_issuances.sql", "CiIssuanceListForProject", map[string]any{
			"project_id": planProject, "issuer": "", "job_id": "", "active": "", "after_us": int64(0), "after_id": planToken, "max_rows": int32(101),
		})
		if !strings.Contains(p, "ci_issuances_contributions") {
			t.Errorf("plan:\n%s", p)
		}
	})
	t.Run("Q-CI-53 the question of a mapping's tokens reads unexpired records only", func(t *testing.T) {
		p := plan(t, "ci_mappings.sql", "CiMappingHasIssuances", map[string]any{"issuer_id": planIssuerA, "id": planMapping})
		if !strings.Contains(p, "ci_issuances_expiry") && !strings.Contains(p, "ci_issuances_issued") {
			t.Errorf("plan reads neither expiry nor issue index:\n%s", p)
		}
		if got := scalar[int](t, admin, "SELECT count(*) FROM ci_token_issuances WHERE issuer_id = $1 AND expires_at > now() AND $2::uuid = ANY(mapping_ids)", planIssuerA, planMapping); got != 0 {
			t.Errorf("the mapping of an expired record counts: %d", got)
		}
	})
	t.Run("Q-CI-53 the usage of a token reads the window of its life by the primary key of the partition", func(t *testing.T) {
		mustExec(t, admin, `INSERT INTO audit_event (id, occurred_at, action, category, outcome, actor_type, token_id)
			SELECT gen_random_uuid(), now() + g * interval '10 seconds', 'npm.install', 'package', 'success', 'ci',
			       CASE WHEN g % 100 = 0 THEN $1::uuid ELSE gen_random_uuid() END
			  FROM generate_series(1, 200000) g`, planToken)
		mustExec(t, admin, "ANALYZE audit_event")
		// The events lie from now to 23 days ahead (the partitions of the next months exist); the token lived 10 minutes
		// in the middle of them.
		window := time.Now().Add(12 * 24 * time.Hour)
		p := plan(t, "ci_issuances.sql", "CiIssuanceUsage", map[string]any{
			"token_id": planToken, "from_us": window.UnixMicro(), "to_us": window.Add(11 * time.Minute).UnixMicro(),
		})
		// No index on audit_event.token_id (Q-CI-53): the window is a range of the partition's primary key, so the
		// statement reads the events of the token's lifetime and filters them, and never scans a partition.
		if !strings.Contains(p, "_pkey") || strings.Contains(p, "Seq Scan") {
			t.Errorf("plan:\n%s", p)
		}
	})
}
