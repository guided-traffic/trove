//go:build integration

package postgres

import (
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

const (
	vulnFinding1 = "dd000000-0000-0000-0000-0000000000f1"
	vulnFinding2 = "dd000000-0000-0000-0000-0000000000f2"
)

// TestVulnMigration checks 00035_vulnerabilities and 00036_vulnerabilities_packages_name against the migrated schema
// (R-VULN-004, R-VULN-006, R-VULN-012, R-VULN-026, R-HA-018, R-BAK-004). Contract test: the requirements' evidence comes from the
// tests of the work packages.
func TestVulnMigration(t *testing.T) {
	st := getStack(t)
	ensureRuntimeRole(t, st)
	_, _, admin := migrated(t, st)

	t.Run("R-VULN-026 the export classes: findings and their events are data, the public data is transient", func(t *testing.T) {
		want := map[string]tables.Class{
			"vuln_finding": tables.Data, "vuln_finding_event": tables.Data,
			"vuln_source_state": tables.Transient, "vuln_advisory": tables.Transient, "vuln_affected": tables.Transient,
			"vuln_kev": tables.Transient, "vuln_pending_evaluation": tables.Transient, "vuln_claimed_match": tables.Transient,
		}
		for name, class := range want {
			if got, ok := tables.Lookup(name); !ok || got.Class != class {
				t.Errorf("%s registered as %+v (%v), want %s", name, got, ok, class)
			}
			if !scalar[bool](t, admin, "SELECT to_regclass($1) IS NOT NULL", schemaName+"."+name) {
				t.Errorf("table %s is not created by the migrations", name)
			}
		}
		if !meta.IsRevisionColumn("vuln_finding", "revision") {
			t.Error("vuln_finding.revision is not a revision column")
		}
	})

	t.Run("R-HA-018 both steps are expand steps, the index of packages is concurrent and in a step of its own", func(t *testing.T) {
		steps, err := postgres.EmbeddedSteps()
		if err != nil {
			t.Fatal(err)
		}
		seen := 0
		for _, s := range steps {
			if s.Version != 35 && s.Version != 36 {
				continue
			}
			seen++
			if s.Phase != "expand" || s.RequiresApp != "" {
				t.Errorf("%s: phase %q, requires-app %q", s.Name, s.Phase, s.RequiresApp)
			}
			if s.NoTx != (s.Version == 36) || (s.Version == 36 && !strings.Contains(strings.ToUpper(s.SQL), "CONCURRENTLY")) {
				t.Errorf("%s: no-transaction %t", s.Name, s.NoTx)
			}
		}
		if seen != 2 {
			t.Errorf("vulnerability steps = %d, want 2 (00035, 00036)", seen)
		}
		if !scalar[bool](t, admin, "SELECT to_regclass($1) IS NOT NULL", schemaName+".packages_name") {
			t.Error("the index packages_name is missing")
		}
	})

	t.Run("R-ARCH-018 the change area vuln_data is seeded", func(t *testing.T) {
		if scalar[int](t, admin, "SELECT count(*) FROM trove.cluster_epoch WHERE area = 'vuln_data'") != 1 {
			t.Error("no cluster_epoch row for vuln_data")
		}
	})

	t.Run("R-VULN-006 the advisory tables take the record, its cwe ids and its affected entries, and refuse what the model refuses", func(t *testing.T) {
		const adv = `INSERT INTO trove.vuln_advisory (ecosystem, id, canonical_id, modified, kind, severity, severity_source, record_sha256, cwe_ids, "references")
			VALUES ('npm', $1, $1, now(), $2, $3, $4, '\x00', ARRAY['CWE-506'], '[{"url":"https://x.example"}]')`
		mustExec(t, admin, adv, "MAL-2026-1", "malicious", "critical", "malicious")
		wantState(t, admin, "23505", adv, "MAL-2026-1", "malicious", "critical", "malicious")
		wantState(t, admin, "23514", adv, "X-1", "weird", "critical", "cvss")
		wantState(t, admin, "23514", adv, "X-2", "vulnerability", "severe", "cvss")
		wantState(t, admin, "23514", adv, "X-3", "vulnerability", "high", "guess")
		if got := scalar[string](t, admin, "SELECT cwe_ids[1] FROM trove.vuln_advisory WHERE id = 'MAL-2026-1'"); got != "CWE-506" {
			t.Errorf("cwe_ids = %q", got)
		}
		mustExec(t, admin, `INSERT INTO trove.vuln_affected (ecosystem, advisory_id, ord, name, ranges, every_version) VALUES ('npm', 'MAL-2026-1', 0, 'evil', '[{"type":"SEMVER","events":[{"introduced":"0"}]}]', true)`)
		wantState(t, admin, "23503", `INSERT INTO trove.vuln_affected (ecosystem, advisory_id, ord, name, every_version) VALUES ('npm', 'MAL-NONE', 0, 'evil', true)`)
		mustExec(t, admin, "DELETE FROM trove.vuln_advisory WHERE id = 'MAL-2026-1'")
		if n := scalar[int](t, admin, "SELECT count(*) FROM trove.vuln_affected WHERE advisory_id = 'MAL-2026-1'"); n != 0 {
			t.Errorf("the affected entries survived their advisory: %d", n)
		}
	})

	t.Run("R-VULN-004 the source state starts empty", func(t *testing.T) {
		mustExec(t, admin, "INSERT INTO trove.vuln_source_state (source, ecosystem) VALUES ('osv', 'npm')")
		if got := scalar[string](t, admin, "SELECT state FROM trove.vuln_source_state WHERE ecosystem = 'npm'"); got != "empty" {
			t.Errorf("state = %q, want empty", got)
		}
		wantState(t, admin, "23514", "INSERT INTO trove.vuln_source_state (source, ecosystem, state) VALUES ('kev', '', 'done')")
	})

	t.Run("R-VULN-012 a finding is unique by its identity, takes the statuses of the model and keeps its history", func(t *testing.T) {
		mustExec(t, admin, "INSERT INTO trove.repositories (key, format, class) VALUES ('npm-vuln', 'npm', 'remote')")
		repo := scalar[int64](t, admin, "SELECT id FROM trove.repositories WHERE key = 'npm-vuln'")
		const ins = `INSERT INTO trove.vuln_finding (id, repo_id, ecosystem, package_name, version, advisory_id, kind, severity, severity_rank, fix_available, advisory_modified%s)
			VALUES ($1, $2, 'npm', 'lodash', $3, 'GHSA-35jh-r3h4-6jhm', 'vulnerability', 'high', 4, true, now()%s)`
		plain := strings.NewReplacer("%s", "")
		mustExec(t, admin, plain.Replace(ins), vulnFinding1, repo, "4.17.20")
		wantState(t, admin, "23505", plain.Replace(ins), vulnFinding2, repo, "4.17.20")
		mustExec(t, admin, plain.Replace(ins), vulnFinding2, repo, "4.17.19")
		wantState(t, admin, "23514", strings.Replace(strings.Replace(ins, "%s", ", status", 1), "%s", ", 'maybe'", 1), "dd000000-0000-0000-0000-0000000000f3", repo, "1.0.0")
		wantState(t, admin, "23514", strings.Replace(strings.Replace(ins, "%s", ", dismissal_reason", 1), "%s", ", 'boring'", 1), "dd000000-0000-0000-0000-0000000000f3", repo, "1.0.1")
		wantState(t, admin, "23514", strings.Replace(strings.Replace(ins, "%s", ", comment", 1), "%s", ", repeat('x', 4001)", 1), "dd000000-0000-0000-0000-0000000000f3", repo, "1.0.2")
		for _, c := range []struct {
			col  string
			want any
		}{{"status", "needs_triage"}, {"revision", int64(1)}} {
			switch w := c.want.(type) {
			case string:
				if got := scalar[string](t, admin, "SELECT "+c.col+" FROM trove.vuln_finding WHERE id = $1", vulnFinding1); got != w {
					t.Errorf("%s = %q, want %q", c.col, got, w)
				}
			case int64:
				if got := scalar[int64](t, admin, "SELECT "+c.col+" FROM trove.vuln_finding WHERE id = $1", vulnFinding1); got != w {
					t.Errorf("%s = %d, want %d", c.col, got, w)
				}
			}
		}
		mustExec(t, admin, "INSERT INTO trove.vuln_finding_event (finding_id, change, to_status) VALUES ($1, 'detected', 'needs_triage'), ($1, 'status_changed', 'confirmed')", vulnFinding1)
		if seqs := scalar[int](t, admin, "SELECT count(DISTINCT seq) FROM trove.vuln_finding_event WHERE finding_id = $1", vulnFinding1); seqs != 2 {
			t.Errorf("events with distinct seq = %d, want 2", seqs)
		}
		wantState(t, admin, "23514", "INSERT INTO trove.vuln_finding_event (finding_id, change) VALUES ($1, 'invented')", vulnFinding1)
		// Deleting the repository deletes its findings and their events (R-VULN-014).
		mustExec(t, admin, "DELETE FROM trove.repositories WHERE id = $1", repo)
		if n := scalar[int](t, admin, "SELECT count(*) FROM trove.vuln_finding WHERE repo_id = $1", repo) + scalar[int](t, admin, "SELECT count(*) FROM trove.vuln_finding_event WHERE finding_id = $1", vulnFinding1); n != 0 {
			t.Errorf("findings or events survived their repository: %d", n)
		}
	})

	t.Run("R-VULN-012 the partial index of open findings serves the block check", func(t *testing.T) {
		def := scalar[string](t, admin, "SELECT indexdef FROM pg_indexes WHERE schemaname = $1 AND indexname = 'vuln_finding_open_version'", schemaName)
		if !strings.Contains(def, "needs_triage") || !strings.Contains(def, "blocked") || !strings.Contains(def, "version_id") {
			t.Errorf("vuln_finding_open_version = %s", def)
		}
	})
}
