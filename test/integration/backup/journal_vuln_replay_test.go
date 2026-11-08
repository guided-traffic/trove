//go:build integration

package backup

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/vuln"
	_ "github.com/guided-traffic/trove/internal/vuln/triage" // the replay handler of the download blocks
)

// R-VULN-013, R-VULN-026, R-HA-054: the download blocks of the revocation journal replayed into a restored database. The entries
// have the producers' shape (vuln.NewBlockEntry); the database holds each finding in the status a restore to a point before the
// block brings back, and the packages the repository stores.

// seedFinding inserts a finding of npm-remote in a status and returns its ID. The version is stored, as the repository of a
// restored installation stores it, and the finding is linked to it.
func (s *twinSide) seedFinding(pkg, status, extraCols, extraVals string) string {
	s.t.Helper()
	s.storeVersion(pkg)
	return s.text(`INSERT INTO vuln_finding (id, repo_id, ecosystem, package_name, version, advisory_id, package_id, version_id, kind, severity, severity_rank,
			known_exploited, fix_available, fixed_versions, status, advisory_modified, detected_at`+extraCols+`)
		SELECT gen_random_uuid(), r.id, 'npm', $1, '1.0.0', 'GHSA-aaaa-aaaa-0001', p.id, pv.id, 'vulnerability', 'high', 4, false, false, ARRAY[]::text[], $2,
		       now(), now()`+extraVals+`
		  FROM repositories r JOIN packages p ON p.repo_id = r.id AND p.name = $1 JOIN package_versions pv ON pv.package_id = p.id
		 WHERE r.key = 'npm-remote' RETURNING id::text`, pkg, status)
}

// storeVersion makes npm-remote store pkg@1.0.0.
func (s *twinSide) storeVersion(pkg string) {
	s.t.Helper()
	s.exec(`INSERT INTO packages (repo_id, name, display_name) SELECT id, $1, $1 FROM repositories WHERE key = 'npm-remote'`, pkg)
	s.exec(`INSERT INTO package_versions (package_id, version, display_version)
		SELECT p.id, '1.0.0', '1.0.0' FROM packages p JOIN repositories r ON r.id = p.repo_id WHERE r.key = 'npm-remote' AND p.name = $1`, pkg)
}

// vulnBlockEntry is the entry of a block of the finding pkg@1.0.0 in the repository with the ID, in the producers' shape.
func vulnBlockEntry(repoID int64, id, pkg string) []byte {
	b, err := vuln.NewBlockEntry(id, repoID, "web/npm-remote", "npm", pkg, "1.0.0", "GHSA-aaaa-aaaa-0001", "vulnerability", "high").JSON()
	if err != nil {
		panic(err)
	}
	return b
}

func setupVulnRepositories(db *twinSide) {
	db.exec("INSERT INTO project (id, key, display_name) VALUES (gen_random_uuid(), 'web', 'web')")
	db.exec("INSERT INTO repositories (key, class, format, config, project_id) SELECT 'npm-remote', 'local', 'npm', '{}'::jsonb, id FROM project WHERE key = 'web'")
}

func TestR_VULN_013_ABlockSetAfterTheRestorePointIsReplayed(t *testing.T) {
	tw := newTwin(t)
	db := tw.open(tw.src)
	setupVulnRepositories(db)
	repoID := db.number("SELECT id FROM repositories WHERE key = 'npm-remote'")
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
		ids[status] = handWritten(t, store, audit.EventVulnFindingStatusChanged, vulnBlockEntry(repoID, id, pkg))
	}
	missing := handWritten(t, store, audit.EventVulnFindingReopened, vulnBlockEntry(repoID, "0192f0c4-7a1e-7c3b-9d2e-0000000000aa", "p-gone"))
	var liftedEntry map[string]string
	_ = json.Unmarshal(vulnBlockEntry(repoID, findings["confirmed"], "p-confirmed"), &liftedEntry)
	liftedEntry["block"] = "lifted"
	lifted, _ := json.Marshal(liftedEntry)
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
	t.Run("R-VULN-013 a blocked finding is in effect, a lift is no entry", func(t *testing.T) {
		if outcome[ids["blocked"]] != journal.AlreadyInEffect {
			t.Errorf("blocked: %s", outcome[ids["blocked"]])
		}
		if outcome[malformed] != journal.Skipped {
			t.Errorf("an entry of a lifted block: %s", outcome[malformed])
		}
	})
	t.Run("R-VULN-026 an ID of another package version leaves that finding alone, and a finding the database lacks is created", func(t *testing.T) {
		if db.text("SELECT status FROM vuln_finding WHERE id = $1::uuid", findings["other"]) != "confirmed" {
			t.Error("the finding of the forged entry's ID must stay confirmed")
		}
		// The forged package and the missing finding are packages the repository does not store: the created findings are in the
		// shape both gates read for a version that is gone.
		for _, pkg := range []string{"p-forged", "p-gone"} {
			if got := db.text(`SELECT status || ' ' || resolved_reason || ' ' || status_before_resolution || ' ' || (version_id IS NULL)::text
				FROM vuln_finding WHERE package_name = $1`, pkg); got != "resolved version_removed blocked true" {
				t.Errorf("%s: the created finding reads %q", pkg, got)
			}
		}
		if outcome[ids["other"]] != journal.Applied || outcome[missing] != journal.Applied {
			t.Errorf("forged %s, missing %s: want both applied (created)", outcome[ids["other"]], outcome[missing])
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
		if n := db.number("SELECT count(*) FROM vuln_finding_event"); n != 6 {
			t.Errorf("%d history rows, want the 4 blocks and the 2 created findings of the first pass", n)
		}
	})
	if t.Failed() {
		t.Logf("replay:\n%s", printed)
	}
}

// R-VULN-026, R-VULN-013, R-BAK-024 (Q-VULN-131): the replay of an evaluation's block on a database that lacks the finding creates it
// in the shape both download gates read, by the repository's package versions; an entry of a repository that cannot hold the
// finding is unknown and an entry without a kind is skipped; a second replay applies nothing.
func TestR_VULN_026_AnEvaluationBlockIsReplayedBeforeTheFirstSync(t *testing.T) {
	tw := newTwin(t)
	db := tw.open(tw.src)
	setupVulnRepositories(db)
	db.exec("INSERT INTO repositories (key, class, format, config, project_id) SELECT 'npm-virtual', 'virtual', 'npm', '{}'::jsonb, id FROM project WHERE key = 'web'")
	db.exec("INSERT INTO repositories (key, class, format, config, project_id) SELECT 'nuget-local', 'local', 'nuget', '{}'::jsonb, id FROM project WHERE key = 'web'")
	db.exec("INSERT INTO repositories (key, class, format, config, project_id, lifecycle) SELECT 'npm-deleting', 'local', 'npm', '{}'::jsonb, id, 'deleting' FROM project WHERE key = 'web'")
	repoID := db.number("SELECT id FROM repositories WHERE key = 'npm-remote'")
	db.storeVersion("p-stored")
	existing := db.seedFinding("p-existing-kind", "needs_triage", "", "") // a finding of the ID, as a kind change finds it
	store := newMemStore()
	const stored, gone = "0192f0c4-0000-7000-8000-0000000000a1", "0192f0c4-0000-7000-8000-0000000000a2"
	entry := func(mutate func(m map[string]string), id, pkg string) []byte {
		var m map[string]string
		_ = json.Unmarshal(vulnBlockEntry(repoID, id, pkg), &m)
		if mutate != nil {
			mutate(m)
		}
		raw, _ := json.Marshal(m)
		return raw
	}
	ids := map[string]string{
		"existing": handWritten(t, store, audit.EventVulnFindingStatusChanged, entry(nil, existing, "p-existing-kind")),
		"stored":   handWritten(t, store, audit.EventVulnFindingStatusChanged, entry(nil, stored, "p-stored")),
		"gone":     handWritten(t, store, audit.EventVulnFindingStatusChanged, entry(nil, gone, "p-gone")),
		"virtual": handWritten(t, store, audit.EventVulnFindingStatusChanged, entry(func(m map[string]string) {
			m["repositoryId"] = fmt.Sprint(db.number("SELECT id FROM repositories WHERE key = 'npm-virtual'"))
		}, "0192f0c4-0000-7000-8000-0000000000a3", "p-virtual")),
		"another ecosystem": handWritten(t, store, audit.EventVulnFindingStatusChanged, entry(func(m map[string]string) {
			m["repositoryId"] = fmt.Sprint(db.number("SELECT id FROM repositories WHERE key = 'nuget-local'"))
		}, "0192f0c4-0000-7000-8000-0000000000a4", "p-nuget")),
		"deleting": handWritten(t, store, audit.EventVulnFindingStatusChanged, entry(func(m map[string]string) {
			m["repositoryId"] = fmt.Sprint(db.number("SELECT id FROM repositories WHERE key = 'npm-deleting'"))
		}, "0192f0c4-0000-7000-8000-0000000000a5", "p-deleting")),
		"no repository": handWritten(t, store, audit.EventVulnFindingStatusChanged, entry(func(m map[string]string) {
			m["repositoryId"] = "99999999"
		}, "0192f0c4-0000-7000-8000-0000000000a6", "p-nowhere")),
		"no kind": handWritten(t, store, audit.EventVulnFindingStatusChanged, entry(func(m map[string]string) { delete(m, "kind") },
			"0192f0c4-0000-7000-8000-0000000000a7", "p-nokind")),
	}
	eventsBefore := db.number("SELECT count(*) FROM audit_event")

	sum, printed := db.replay(store, time.Now().Add(-time.Minute), nil)
	outcome := map[string]journal.Outcome{}
	for _, r := range sum.Results {
		outcome[r.ID] = r.Outcome
	}
	want := map[string]journal.Outcome{
		"existing": journal.Applied, "stored": journal.Applied, "gone": journal.Applied, "virtual": journal.SubjectUnknown,
		"another ecosystem": journal.SubjectUnknown, "deleting": journal.SubjectUnknown, "no repository": journal.SubjectUnknown, "no kind": journal.Skipped,
	}
	for name, w := range want {
		if outcome[ids[name]] != w {
			t.Errorf("%s: %s, want %s", name, outcome[ids[name]], w)
		}
	}
	shape := func(where string, args ...any) string {
		return db.text(`SELECT status || ' ' || COALESCE(resolved_reason, '-') || ' ' || COALESCE(status_before_resolution, '-') || ' ' ||
			(version_id IS NOT NULL)::text || ' ' || (package_id IS NOT NULL)::text FROM vuln_finding WHERE `+where, args...)
	}
	t.Run("R-VULN-013 the finding of the ID becomes blocked and stays linked", func(t *testing.T) {
		if got := shape("id = $1::uuid", existing); got != "blocked - - true true" {
			t.Errorf("the finding reads %q", got)
		}
	})
	t.Run("R-VULN-026 a finding the database lacks is created blocked and linked when the repository stores the version", func(t *testing.T) {
		if got := shape("id = $1::uuid", stored); got != "blocked - - true true" {
			t.Errorf("the finding reads %q", got)
		}
		if got := db.text(`SELECT change || ' ' || COALESCE(actor::text, 'none') || ' ' || (details->>'journal') FROM vuln_finding_event WHERE finding_id = $1::uuid`, stored); got != "detected none "+ids["stored"] {
			t.Errorf("the history reads %q", got)
		}
	})
	t.Run("R-VULN-026 a finding of a version the repository does not store is created resolved while blocked", func(t *testing.T) {
		if got := shape("id = $1::uuid", gone); got != "resolved version_removed blocked false false" {
			t.Errorf("the finding reads %q", got)
		}
	})
	t.Run("R-VULN-026 a repository that cannot hold the finding creates nothing", func(t *testing.T) {
		if n := db.number(`SELECT count(*) FROM vuln_finding WHERE package_name IN ('p-virtual', 'p-nuget', 'p-deleting', 'p-nowhere', 'p-nokind')`); n != 0 {
			t.Errorf("%d findings created", n)
		}
	})
	t.Run("R-BAK-024 a second replay applies nothing again, and the replay writes no audit event", func(t *testing.T) {
		if n := db.number("SELECT count(*) FROM audit_event") - eventsBefore; n != 0 {
			t.Errorf("%d audit events written (Q-AUTHZ-232)", n)
		}
		revisions := db.text("SELECT string_agg(revision::text, ',' ORDER BY id) FROM vuln_finding")
		events := db.number("SELECT count(*) FROM vuln_finding_event")
		again, _ := db.replay(store, time.Now().Add(-time.Minute), nil)
		for _, r := range again.Results {
			if r.Outcome == journal.Applied {
				t.Errorf("second pass: %s", r.Line())
			}
		}
		if got := db.text("SELECT string_agg(revision::text, ',' ORDER BY id) FROM vuln_finding"); got != revisions || db.number("SELECT count(*) FROM vuln_finding_event") != events {
			t.Errorf("a second replay wrote: revisions %q, was %q", got, revisions)
		}
	})
	if t.Failed() {
		t.Logf("replay:\n%s", printed)
	}
}

// R-VULN-026, R-HA-054 (Q-VULN-131): a forged copy of an entry cannot stop the replay or weaken a block. An entry whose strings cannot
// be stored, or whose package name the format does not admit, is skipped; each replayed copy raises the kind and the severity of the
// finding to the stronger value, in whichever order the copies come.
func TestR_VULN_026_AForgedCopyOfABlockCannotStopTheReplayOrWeakenIt(t *testing.T) {
	entry := func(repoID int64, id, pkg, kind, severity string) []byte {
		b, err := vuln.NewBlockEntry(id, repoID, "web/npm-remote", "npm", pkg, "1.0.0", "GHSA-aaaa-aaaa-0001", kind, severity).JSON()
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, order := range []string{"forged first", "genuine first"} {
		t.Run("R-VULN-026 "+order, func(t *testing.T) {
			tw := newTwin(t)
			db := tw.open(tw.src)
			setupVulnRepositories(db)
			repoID := db.number("SELECT id FROM repositories WHERE key = 'npm-remote'")
			db.storeVersion("p-copy")
			store := newMemStore()
			const id = "0192f0c4-0000-7000-8000-0000000000b1"
			forged, genuine := entry(repoID, id, "p-copy", "vulnerability", "low"), entry(repoID, id, "p-copy", "malicious", "critical")
			if order == "genuine first" {
				forged, genuine = genuine, forged
			}
			handWritten(t, store, audit.EventVulnFindingStatusChanged, forged)
			time.Sleep(5 * time.Millisecond)
			handWritten(t, store, audit.EventVulnFindingStatusChanged, genuine)
			bad := map[string]string{
				"a NUL in the package":         string(entry(repoID, "0192f0c4-0000-7000-8000-0000000000b2", "p\\u0000x", "vulnerability", "high")),
				"a package the format refuses": string(entry(repoID, "0192f0c4-0000-7000-8000-0000000000b3", "@only-a-scope", "vulnerability", "high")),
			}
			badIDs := map[string]string{}
			for name, raw := range bad {
				badIDs[name] = handWritten(t, store, audit.EventVulnFindingStatusChanged, []byte(strings.ReplaceAll(raw, `\\u0000`, `\u0000`)))
			}
			sum, printed := db.replay(store, time.Now().Add(-time.Minute), nil)
			for name, bid := range badIDs {
				for _, r := range sum.Results {
					if r.ID == bid && r.Outcome != journal.Skipped {
						t.Errorf("%s: %s, want skipped", name, r.Line())
					}
				}
			}
			if got := db.text(`SELECT kind || ' ' || severity || ' ' || status FROM vuln_finding WHERE package_name = 'p-copy'`); got != "malicious critical blocked" {
				t.Errorf("the finding reads %q, want the stronger facts whichever copy came first", got)
			}
			if n := db.number(`SELECT count(*) FROM vuln_finding`); n != 1 {
				t.Errorf("%d findings, want the one of p-copy", n)
			}
			if n := db.number(`SELECT count(*) FROM vuln_finding_event WHERE change = 'advisory_changed' AND details ? 'raised'`); n != map[bool]int64{true: 1, false: 0}[order == "forged first"] {
				t.Errorf("%d raise rows in the order %q", n, order)
			}
			if t.Failed() {
				t.Logf("replay:\n%s", printed)
			}
		})
	}
}

// R-VULN-026, R-HA-054 (Q-VULN-131): a forged copy with the ID of a genuine block and another advisory cannot absorb it: the genuine
// entry gets a finding of its own identity. A finding restored from the backup is not raised by an entry, a finding a replay created
// is, with a history row for the raise.
func TestR_VULN_026_AForgedCopyWithTheSameIDDoesNotAbsorbTheGenuineBlock(t *testing.T) {
	tw := newTwin(t)
	db := tw.open(tw.src)
	setupVulnRepositories(db)
	repoID := db.number("SELECT id FROM repositories WHERE key = 'npm-remote'")
	db.storeVersion("p-abs")
	restored := db.seedFinding("p-restored", "confirmed", "", "") // high, vulnerability
	store := newMemStore()
	const id = "0192f0c4-0000-7000-8000-0000000000c1"
	entry := func(finding, pkg, advisoryID, kind, severity string) []byte {
		b, err := vuln.NewBlockEntry(finding, repoID, "web/npm-remote", "npm", pkg, "1.0.0", advisoryID, kind, severity).JSON()
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	handWritten(t, store, audit.EventVulnFindingStatusChanged, entry(id, "p-abs", "FAKE-1", "vulnerability", "low"))
	time.Sleep(5 * time.Millisecond)
	handWritten(t, store, audit.EventVulnFindingStatusChanged, entry(id, "p-abs", "GHSA-genuine-0001", "malicious", "critical"))
	time.Sleep(5 * time.Millisecond)
	// A forged entry for a finding the backup holds names stronger facts.
	handWritten(t, store, audit.EventVulnFindingStatusChanged, entry(restored, "p-restored", "GHSA-aaaa-aaaa-0001", "malicious", "critical"))
	db.replay(store, time.Now().Add(-time.Minute), nil)

	if got := db.text(`SELECT string_agg(advisory_id || ':' || kind || ':' || severity || ':' || status, ' ' ORDER BY advisory_id) FROM vuln_finding WHERE package_name = 'p-abs'`); got !=
		"FAKE-1:vulnerability:low:blocked GHSA-genuine-0001:malicious:critical:blocked" {
		t.Errorf("the findings of p-abs: %q, want one each, the genuine one with its own facts", got)
	}
	if got := db.text(`SELECT kind || ' ' || severity FROM vuln_finding WHERE id = $1::uuid`, restored); got != "vulnerability high" {
		t.Errorf("the restored finding reads %q, an entry must not raise it", got)
	}
	if n := db.number(`SELECT count(*) FROM vuln_finding_event WHERE finding_id = $1::uuid AND change = 'advisory_changed'`, restored); n != 0 {
		t.Errorf("%d raise rows on the restored finding", n)
	}
}

// R-VULN-026 (Q-VULN-131): the raise marker expires with the restore. A finding that an earlier restore's replay created, older than
// the last restored_at, is not raised by a later entry.
func TestR_VULN_026_AReplayCreatedFindingOfAnEarlierRestoreIsNotRaised(t *testing.T) {
	tw := newTwin(t)
	db := tw.open(tw.src)
	setupVulnRepositories(db)
	repoID := db.number("SELECT id FROM repositories WHERE key = 'npm-remote'")
	db.storeVersion("p-old")
	entry := func(kind, severity string) []byte {
		b, err := vuln.NewBlockEntry("0192f0c4-0000-7000-8000-0000000000d1", repoID, "web/npm-remote", "npm", "p-old", "1.0.0", "GHSA-aaaa-aaaa-0001", kind, severity).JSON()
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	first := newMemStore()
	handWritten(t, first, audit.EventVulnFindingStatusChanged, entry("vulnerability", "low"))
	db.replay(first, time.Now().Add(-time.Minute), nil)
	db.exec(`INSERT INTO system_state (key, value) VALUES ('restored_at', to_jsonb(now() + interval '1 second')) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`)
	time.Sleep(1100 * time.Millisecond)
	second := newMemStore()
	handWritten(t, second, audit.EventVulnFindingStatusChanged, entry("malicious", "critical"))
	db.replay(second, time.Now().Add(-time.Minute), nil)
	if got := db.text(`SELECT kind || ' ' || severity FROM vuln_finding WHERE package_name = 'p-old'`); got != "vulnerability low" {
		t.Errorf("the finding reads %q, an entry of a later restore must not raise it", got)
	}
}
