//go:build integration

package backup

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/journal"
	_ "github.com/guided-traffic/trove/internal/vuln/triage" // the replay handler of the download blocks
)

// R-VULN-013, R-HA-054 (H-489): the download blocks of the revocation journal replayed into a restored database. The entries
// have the producer's shape (internal/vuln/triage TestR_VULN_013_TheProducersEntryIsTheOneTheReplayReads); the database holds
// each finding in the status a restore to a point before the block brings back.

// seedFinding inserts a finding of npm-remote in a status and returns its ID.
func (s *twinSide) seedFinding(pkg, status, extraCols, extraVals string) string {
	s.t.Helper()
	return s.text(`INSERT INTO vuln_finding (id, repo_id, ecosystem, package_name, version, advisory_id, kind, severity, severity_rank,
			known_exploited, fix_available, fixed_versions, status, advisory_modified, detected_at`+extraCols+`)
		SELECT gen_random_uuid(), r.id, 'npm', $1, '1.0.0', 'GHSA-aaaa-aaaa-0001', 'vulnerability', 'high', 3, false, false, ARRAY[]::text[], $2,
		       now(), now()`+extraVals+`
		  FROM repositories r WHERE r.key = 'npm-remote' RETURNING id::text`, pkg, status)
}

func vulnBlockEntry(id, pkg string) []byte {
	b, _ := json.Marshal(map[string]string{
		"finding": id, "repository": "web/npm-remote", "ecosystem": "npm", "package": pkg, "version": "1.0.0",
		"advisory": "GHSA-aaaa-aaaa-0001", "block": "set",
	})
	return b
}

func TestR_VULN_013_ABlockSetAfterTheRestorePointIsReplayed(t *testing.T) {
	tw := newTwin(t)
	db := tw.open(tw.src)
	db.exec("INSERT INTO project (id, key, display_name) VALUES (gen_random_uuid(), 'web', 'web')")
	db.exec("INSERT INTO repositories (key, class, format, config, project_id) SELECT 'npm-remote', 'local', 'npm', '{}'::jsonb, id FROM project WHERE key = 'web'")
	findings := map[string]string{
		"confirmed":    db.seedFinding("p-confirmed", "confirmed", "", ""),
		"needs_triage": db.seedFinding("p-triage", "needs_triage", "", ""),
		"dismissed":    db.seedFinding("p-dismissed", "dismissed", ", dismissal_reason", ", 'false_positive'"),
		"resolved":     db.seedFinding("p-resolved", "resolved", ", status_before_resolution, resolved_reason, resolved_at", ", 'confirmed', 'no_longer_affected', now()"),
		"blocked":      db.seedFinding("p-blocked", "blocked", "", ""),
		"other":        db.seedFinding("p-other", "confirmed", "", ""),
	}
	store := newMemStore()
	ids := map[string]string{}
	for status, id := range findings {
		pkg := db.text("SELECT package_name FROM vuln_finding WHERE id = $1::uuid", id)
		if status == "other" {
			pkg = "p-forged" // the entry names this finding's ID with another package
		}
		ids[status] = handWritten(t, store, audit.EventVulnFindingStatusChanged, vulnBlockEntry(id, pkg))
	}
	missing := handWritten(t, store, audit.EventVulnFindingReopened, vulnBlockEntry("0192f0c4-7a1e-7c3b-9d2e-0000000000aa", "p-gone"))
	lifted, _ := json.Marshal(map[string]string{"finding": findings["confirmed"], "ecosystem": "npm", "package": "p-confirmed", "version": "1.0.0", "block": "lifted"})
	malformed := handWritten(t, store, audit.EventVulnFindingStatusChanged, lifted)
	eventsBefore := db.number("SELECT count(*) FROM audit_event")

	sum, printed := db.replay(store, time.Now().Add(-time.Minute), nil)
	outcome := map[string]journal.Outcome{}
	for _, r := range sum.Results {
		outcome[r.ID] = r.Outcome
	}
	t.Run("R-VULN-013 a finding without a block is blocked again, whatever its restored status", func(t *testing.T) {
		for _, status := range []string{"confirmed", "needs_triage", "dismissed", "resolved"} {
			if outcome[ids[status]] != journal.Applied {
				t.Errorf("%s: %s, want applied", status, outcome[ids[status]])
			}
			got := db.text(`SELECT status || ' ' || COALESCE(dismissal_reason, '-') || ' ' || COALESCE(resolved_reason, '-') || ' ' || revision
				FROM vuln_finding WHERE id = $1::uuid`, findings[status])
			if got != "blocked - - 2" {
				t.Errorf("%s: the finding reads %q, want blocked with no dismissal or resolution and a new revision", status, got)
			}
			hist := db.text(`SELECT COALESCE(string_agg(from_status || '>' || to_status || ' ' || COALESCE(actor::text, 'none') || ' ' || (details->>'journal'), ','), '')
				FROM vuln_finding_event WHERE finding_id = $1::uuid`, findings[status])
			if hist != status+">blocked none "+ids[status] {
				t.Errorf("%s: history %q", status, hist)
			}
		}
	})
	t.Run("R-VULN-013 a blocked finding is in effect, a missing one and another package version are unknown, a lift is no entry", func(t *testing.T) {
		if outcome[ids["blocked"]] != journal.AlreadyInEffect {
			t.Errorf("blocked: %s", outcome[ids["blocked"]])
		}
		if outcome[ids["other"]] != journal.SubjectUnknown || db.text("SELECT status FROM vuln_finding WHERE id = $1::uuid", findings["other"]) != "confirmed" {
			t.Errorf("a forged package name: %s, and the finding must stay confirmed", outcome[ids["other"]])
		}
		if outcome[missing] != journal.SubjectUnknown {
			t.Errorf("a missing finding: %s", outcome[missing])
		}
		if outcome[malformed] != journal.Skipped {
			t.Errorf("an entry of a lifted block: %s", outcome[malformed])
		}
	})
	t.Run("R-VULN-013 the replay writes no audit event, and a second replay changes nothing", func(t *testing.T) {
		if n := db.number("SELECT count(*) FROM audit_event") - eventsBefore; n != 0 {
			t.Errorf("%d audit events written (Q-AUTHZ-232)", n)
		}
		again, _ := db.replay(store, time.Now().Add(-time.Minute), nil)
		for _, r := range again.Results {
			if r.Outcome == journal.Applied {
				t.Errorf("second pass: %s", r.Line())
			}
		}
		if n := db.number("SELECT count(*) FROM vuln_finding_event"); n != 4 {
			t.Errorf("%d history rows, want the 4 of the first pass", n)
		}
	})
	if t.Failed() {
		t.Logf("replay:\n%s", printed)
	}
}
