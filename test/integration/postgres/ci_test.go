//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

const (
	ciIssuerA = "cc000000-0000-0000-0000-00000000000a"
	ciIssuerB = "cc000000-0000-0000-0000-00000000000b"
	ciMapping = "cc000000-0000-0000-0000-0000000000c1"
	ciPrinc   = "cc000000-0000-0000-0000-0000000000d1"
	ciToken   = "cc000000-0000-0000-0000-0000000000e1"
)

// TestCIMigration checks 00026_ci_oidc to 00028_ci_oidc_validate and the statements of the CI query
// files against the migrated schema (R-CI-001, R-CI-003, R-CI-009, R-CI-018, R-CI-020, R-HA-018,
// R-BAK-004).
func TestCIMigration(t *testing.T) {
	st := getStack(t)
	ensureRuntimeRole(t, st)
	_, _, admin := migrated(t, st)
	ctx := context.Background()

	t.Run("R-BAK-004 the CI tables carry their export classes and the revision columns are registered", func(t *testing.T) {
		want := map[string]tables.Class{
			"ci_issuers": tables.Data, "ci_issuer_keys": tables.Transient, "ci_mappings": tables.Data,
			"ci_subject_token_uses": tables.Data, "ci_token_issuances": tables.Data,
		}
		for name, class := range want {
			if got, ok := tables.Lookup(name); !ok || got.Class != class {
				t.Errorf("%s registered as %+v (%v), want %s", name, got, ok, class)
			}
			if !scalar[bool](t, admin, "SELECT to_regclass($1) IS NOT NULL", schemaName+"."+name) {
				t.Errorf("table %s is not created by the migrations", name)
			}
		}
		for _, c := range [][2]string{{"ci_issuers", "version"}, {"ci_issuers", "mappings_version"}, {"ci_mappings", "version"}} {
			if !meta.IsRevisionColumn(c[0], c[1]) {
				t.Errorf("%s.%s is not a revision column", c[0], c[1])
			}
		}
	})

	t.Run("R-HA-018 the CI steps are expand steps, the index is concurrent and the key is validated", func(t *testing.T) {
		steps, err := postgres.EmbeddedSteps()
		if err != nil {
			t.Fatal(err)
		}
		seen := 0
		for _, s := range steps {
			if s.Version < 26 || s.Version > 28 {
				continue
			}
			seen++
			if s.Phase != "expand" || s.RequiresApp != "" {
				t.Errorf("%s: phase %q, requires-app %q", s.Name, s.Phase, s.RequiresApp)
			}
			if s.NoTx && !strings.Contains(strings.ToUpper(s.SQL), "CONCURRENTLY") {
				t.Errorf("%s is a no-transaction step without CONCURRENTLY", s.Name)
			}
		}
		if seen != 3 {
			t.Errorf("CI steps = %d, want 3 (00026 to 00028)", seen)
		}
		if !scalar[bool](t, admin, "SELECT convalidated FROM pg_constraint WHERE conname = 'package_owner_ci_issuer_id_fkey' AND connamespace = $1::regnamespace", schemaName) {
			t.Error("package_owner_ci_issuer_id_fkey is still NOT VALID")
		}
		if !scalar[bool](t, admin, "SELECT to_regclass($1) IS NOT NULL", schemaName+".package_owner_ci_issuer") {
			t.Error("the index package_owner_ci_issuer is missing")
		}
	})

	t.Run("R-ARCH-018 the change areas of the CI federation are seeded", func(t *testing.T) {
		for _, a := range []string{"ci_mappings", "ci_issuer_keys"} {
			if scalar[int](t, admin, "SELECT count(*) FROM trove.cluster_epoch WHERE area = $1", a) != 1 {
				t.Errorf("no cluster_epoch row for %s", a)
			}
		}
	})

	t.Run("the issuer rules are enforced by the table", func(t *testing.T) { // R-CI-001
		insert := `INSERT INTO trove.ci_issuers (id, name, kind, issuer, allowed_audiences, required_claims, pin_claims, single_use_subject_tokens%s)
			VALUES ($1, $2, $3, $4, $5, ARRAY['jti'], ARRAY['project_id'], false%s)`
		plain := func(extraCol, extraVal string) string {
			return strings.Replace(strings.Replace(insert, "%s", extraCol, 1), "%s", extraVal, 1)
		}
		mustExec(t, admin, plain("", ""), ciIssuerA, "gitlab-a", "gitlab", "https://gitlab.a.example", []string{"https://t.example"})
		wantState(t, admin, "23505", plain("", ""), ciIssuerB, "gitlab-a", "gitlab", "https://gitlab.b.example", []string{"x"})
		wantState(t, admin, "23505", plain("", ""), ciIssuerB, "gitlab-b", "gitlab", "https://gitlab.a.example", []string{"x"})
		wantState(t, admin, "23514", plain("", ""), ciIssuerB, "gitlab-b", "gitlab", "http://gitlab.b.example", []string{"x"})
		wantState(t, admin, "23514", plain("", ""), ciIssuerB, "GitLab", "gitlab", "https://gitlab.b.example", []string{"x"})
		wantState(t, admin, "23514", plain("", ""), ciIssuerB, "gitlab-b", "bitbucket", "https://gitlab.b.example", []string{"x"})
		wantState(t, admin, "23514", plain("", ""), ciIssuerB, "gitlab-b", "gitlab", "https://gitlab.b.example", []string{})
		wantState(t, admin, "23514", plain(", clock_skew", ", interval '301 seconds'"), ciIssuerB, "gitlab-b", "gitlab", "https://gitlab.b.example", []string{"x"})
		wantState(t, admin, "23514", plain(", source", ", 'wizard'"), ciIssuerB, "gitlab-b", "gitlab", "https://gitlab.b.example", []string{"x"})
		if v := scalar[int64](t, admin, "SELECT version FROM trove.ci_issuers WHERE id = $1", ciIssuerA); v != 1 {
			t.Errorf("version = %d, want 1", v)
		}
		if v := scalar[int64](t, admin, "SELECT mappings_version FROM trove.ci_issuers WHERE id = $1", ciIssuerA); v != 0 {
			t.Errorf("mappings_version = %d, want 0", v)
		}
	})

	t.Run("a mapping is a principal of kind ci_mapping, unique by name and by priority in its tier", func(t *testing.T) { // R-CI-009
		mustExec(t, admin, "INSERT INTO trove.principal (id, kind, name) VALUES ($1, 'ci_mapping', 'gitlab-a/release')", ciPrinc)
		mustExec(t, admin, "INSERT INTO trove.principal (id, kind, name) VALUES ('cc000000-0000-0000-0000-0000000000d2', 'user', 'ci-test-user')")
		insert := `INSERT INTO trove.ci_mappings (id, issuer_id, principal_id, name, priority, match, grants, created_by)
			VALUES ($1, $2, $3, $4, $5, '{"all":[{"claim":"project_id","equals":"1"}]}', '{"policies":["p"]}', 'test')`
		mustExec(t, admin, insert, ciMapping, ciIssuerA, ciPrinc, "release", 100)
		// The principal must be of kind ci_mapping (AI001).
		wantState(t, admin, "AI001", insert, "cc000000-0000-0000-0000-0000000000c2", ciIssuerA, "cc000000-0000-0000-0000-0000000000d2", "other", 200)
		mustExec(t, admin, "INSERT INTO trove.principal (id, kind, name) VALUES ('cc000000-0000-0000-0000-0000000000d3', 'ci_mapping', 'gitlab-a/other')")
		wantState(t, admin, "23505", insert, "cc000000-0000-0000-0000-0000000000c2", ciIssuerA, "cc000000-0000-0000-0000-0000000000d3", "release", 200)
		wantState(t, admin, "23505", insert, "cc000000-0000-0000-0000-0000000000c2", ciIssuerA, "cc000000-0000-0000-0000-0000000000d3", "other", 100)
		wantState(t, admin, "23514", insert, "cc000000-0000-0000-0000-0000000000c2", ciIssuerA, "cc000000-0000-0000-0000-0000000000d3", "other", 0)
		wantState(t, admin, "23514", `INSERT INTO trove.principal (id, kind, name) VALUES (gen_random_uuid(), 'ci_mapping', 'no-slash')`)
		// An issuer with mappings cannot be deleted (ON DELETE RESTRICT).
		wantRestrict(t, admin, "DELETE FROM trove.ci_issuers WHERE id = $1", ciIssuerA)
	})

	t.Run("the statements of the CI query files run against the schema", func(t *testing.T) { // R-CI-003 R-CI-018 R-CI-020
		mustExec(t, admin, "SET search_path = trove, public")
		qs := q.New(admin)

		list, err := qs.CiIssuerList(ctx)
		if err != nil || len(list) != 1 || list[0].Name != "gitlab-a" || list[0].MappingCount != 1 || list[0].ClockSkewSecs != 60 {
			t.Fatalf("CiIssuerList: %+v %v", list, err)
		}
		if rows, err := qs.CiIssuerByIssuer(ctx, "https://gitlab.a.example"); err != nil || len(rows) != 1 || rows[0].ID != ciIssuerA {
			t.Fatalf("CiIssuerByIssuer: %v %v", rows, err)
		}
		if rows, err := qs.CiIssuerLock(ctx, "gitlab-a"); err != nil || len(rows) != 1 || rows[0].Source != "api" {
			t.Fatalf("CiIssuerLock: %v %v", rows, err)
		}
		if ms, err := qs.CiMappingList(ctx, "gitlab-a"); err != nil || len(ms) != 1 || ms[0].Name != "release" || ms[0].Priority != 100 {
			t.Fatalf("CiMappingList: %v %v", ms, err)
		}
		if v, err := qs.CiIssuerBumpMappings(ctx, ciIssuerA); err != nil || v != 1 {
			t.Fatalf("CiIssuerBumpMappings: %d %v", v, err)
		}

		// The key row: store, lock, store again with a later time, record an error, find due issuers.
		if err := qs.CiKeysStore(ctx, q.CiKeysStoreParams{IssuerID: ciIssuerA, Jwks: `{"keys":[{"kid":"k1","kty":"RSA"}]}`, JwksUri: "https://gitlab.a.example/oauth/discovery/keys", FetchedUs: time.Now().UnixMicro(), NextRefreshSecs: 3600}); err != nil {
			t.Fatalf("CiKeysStore: %v", err)
		}
		if rows, err := qs.CiKeysLock(ctx, q.CiKeysLockParams{IssuerID: ciIssuerA, Jwks: `{"keys":[{"kid":"k1","kty":"RSA"}]}`}); err != nil || len(rows) != 1 || !rows[0].Same {
			t.Fatalf("CiKeysLock: %v %v", rows, err)
		}
		if err := qs.CiKeysRecordError(ctx, q.CiKeysRecordErrorParams{IssuerID: ciIssuerA, LastError: "egress_denied host=169.254.169.254", RetrySecs: 300}); err != nil {
			t.Fatalf("CiKeysRecordError: %v", err)
		}
		if due, err := qs.CiKeysDue(ctx); err != nil || len(due) != 0 {
			t.Fatalf("CiKeysDue: %v %v", due, err)
		}
		if l, err := qs.CiIssuerList(ctx); err != nil || !strings.Contains(l[0].Jwks, "k1") || l[0].LastError == "" {
			t.Fatalf("CiIssuerList after a key store: %+v %v", l, err)
		}

		// The replay set: the atomic upsert counts.
		for want := int32(1); want <= 2; want++ {
			n, err := qs.CiExchangeUseJTI(ctx, q.CiExchangeUseJTIParams{IssuerID: ciIssuerA, Jti: "job-1", ExpiresUnix: 4102444800})
			if err != nil || n != want {
				t.Fatalf("CiExchangeUseJTI #%d = %d, %v", want, n, err)
			}
		}
		if n, err := qs.CiReplayDeleteExpired(ctx, q.CiReplayDeleteExpiredParams{RetentionSecs: 900, MaxRows: 1000}); err != nil || n != 0 {
			t.Fatalf("CiReplayDeleteExpired: %d %v", n, err)
		}

		// The issuance record and the revocation of the token it names.
		mustExec(t, admin, "INSERT INTO trove.api_token (id, type, secret_sha256, hint, principal_id, description, scope, created_via, expires_at) "+
			"VALUES ($1, 'cit', decode(repeat('ab', 32), 'hex'), 'trv_cit_…abcd', $2, 'CI exchange', '{\"inherit\":true}', 'ci-exchange', now() + interval '1 hour')", ciToken, ciPrinc)
		err = qs.CiExchangeInsertIssuance(ctx, q.CiExchangeInsertIssuanceParams{
			TokenID: ciToken, IssuerID: ciIssuerA, MappingIds: []string{ciMapping}, MappingNames: []string{"release"}, SubjectKind: "id_token",
			ProjectID: "4242", JobID: "9001", Claims: `{"project_id":"4242"}`, Grants: `{"issuer":"gitlab-a"}`, GrantedScope: "read",
			ExpiresUs: scalar[int64](t, admin, "SELECT (extract(epoch FROM now() + interval '1 hour') * 1000000)::bigint"),
		})
		if err != nil {
			t.Fatalf("CiExchangeInsertIssuance: %v", err)
		}
		page, err := qs.CiIssuanceList(ctx, q.CiIssuanceListParams{Issuer: "gitlab-a", JobID: "9001", MaxRows: 50})
		if err != nil || len(page) != 1 || page[0].TokenID != ciToken || page[0].RevokedUs != 0 || page[0].RefProtected != "" {
			t.Fatalf("CiIssuanceList: %+v %v", page, err)
		}
		if rows, err := qs.CiIssuanceList(ctx, q.CiIssuanceListParams{Active: "true", MaxRows: 50}); err != nil || len(rows) != 1 {
			t.Fatalf("CiIssuanceList active: %v %v", rows, err)
		}
		victims, err := qs.CiIssuanceTokensToRevoke(ctx, q.CiIssuanceTokensToRevokeParams{Issuer: "gitlab-a", JobID: "9001"})
		if err != nil || len(victims) != 1 {
			t.Fatalf("CiIssuanceTokensToRevoke: %v %v", victims, err)
		}
		if of, err := qs.CiIssuanceTokensOfMapping(ctx, ciPrinc); err != nil || len(of) != 1 {
			t.Fatalf("CiIssuanceTokensOfMapping: %v %v", of, err)
		}
		if of, err := qs.CiIssuanceTokensOfIssuer(ctx, ciIssuerA); err != nil || len(of) != 1 {
			t.Fatalf("CiIssuanceTokensOfIssuer: %v %v", of, err)
		}
		done, err := qs.CiRevokeTokens(ctx, q.CiRevokeTokensParams{Ids: []string{ciToken}, Reason: "admin"})
		if err != nil || len(done) != 1 {
			t.Fatalf("CiRevokeTokens: %v %v", done, err)
		}
		if rows, err := qs.CiIssuanceList(ctx, q.CiIssuanceListParams{Active: "false", MaxRows: 50}); err != nil || len(rows) != 1 {
			t.Fatalf("CiIssuanceList inactive: %v %v", rows, err)
		}
		if n, err := qs.CiIssuanceDeleteExpired(ctx, q.CiIssuanceDeleteExpiredParams{RetentionSecs: 3600, MaxRows: 1000}); err != nil || n != 0 {
			t.Fatalf("CiIssuanceDeleteExpired: %d %v", n, err)
		}

		// The write rule: deleting the issuer's mapping and principal, then the issuer, removes the rest.
		if err := qs.CiMappingDelete(ctx, ciMapping); err != nil {
			t.Fatalf("CiMappingDelete: %v", err)
		}
		if err := qs.CiMappingPrincipalDelete(ctx, ciPrinc); err == nil {
			// api_token references the principal with ON DELETE CASCADE: the token row is gone too.
			if scalar[int](t, admin, "SELECT count(*) FROM trove.api_token WHERE id = $1", ciToken) != 0 {
				t.Error("the token of a deleted mapping principal remained")
			}
		} else {
			t.Fatalf("CiMappingPrincipalDelete: %v", err)
		}
		if err := qs.CiIssuerDelete(ctx, ciIssuerA); err != nil {
			t.Fatalf("CiIssuerDelete: %v", err)
		}
		for _, tbl := range []string{"ci_issuer_keys", "ci_subject_token_uses", "ci_token_issuances"} {
			if scalar[int](t, admin, "SELECT count(*) FROM trove."+tbl+" WHERE issuer_id = $1", ciIssuerA) != 0 {
				t.Errorf("%s rows of a deleted issuer remained", tbl)
			}
		}
	})
}
