//go:build integration

package vulnrestore

import (
	"context"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/vuln"
	_ "github.com/guided-traffic/trove/internal/vuln/triage" // the replay handler of the download blocks
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/osvstub"
)

// replayBlock applies one block entry as the restore's replay does.
func (e *env) replayBlock(db *postgres.DB, id string, entry []byte) journal.Result {
	e.t.Helper()
	h, ok := journal.HandlerFor(audit.EventVulnFindingStatusChanged)
	if !ok {
		e.t.Fatal("no replay handler for the block event")
	}
	var res journal.Result
	err := db.InTx(context.Background(), func(tx meta.Tx) error {
		var err error
		res, err = h.Apply(context.Background(), tx, journal.Env{}, journal.Object{V: journal.Version, ID: id, Event: audit.EventVulnFindingStatusChanged, Entry: entry})
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func (e *env) blockEntry(repo, name, advisoryID, finding string) []byte {
	e.t.Helper()
	repoID := e.st.Number(`SELECT id FROM repositories WHERE key = $1`, repo)
	raw, err := vuln.NewBlockEntry(finding, repoID, "it/"+repo, "npm", name, "1.0.0", advisoryID, "vulnerability", "high").JSON()
	if err != nil {
		e.t.Fatal(err)
	}
	return raw
}

// R-VULN-026, R-VULN-015 (Q-VULN-131, Q-VULN-132): the restore replays two blocks of the evaluation into an installation that has no
// advisory data. Before the first sync both versions are refused. The first sync and the full evaluation after it reconcile them
// like any finding: the block whose advisory still affects the version stays, the one of an advisory no source knows resolves and
// its version downloads again.
func TestR_VULN_026_TheFirstSyncAfterTheRestoreReconcilesReplayedBlocks(t *testing.T) {
	e := newEnv(t)
	e.seedInstallation()
	db, err := postgres.Open(context.Background(), e.st.DatabaseConfig(), "vulnrestore-replay", "web", testenv.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const (
		genuine = "018f4c1e-0000-7000-8000-0000000000a1"
		forged  = "018f4c1e-0000-7000-8000-0000000000a2"
	)
	// The genuine block names the version that the advisory affects and that the restored finding holds dismissed (a finding of
	// another ID, found by its identity); the forged one names a clean version and an advisory no source has.
	if r := e.replayBlock(db, "018f4c1e-0000-7000-8000-0000000000e1", e.blockEntry(repoLocal, osvstub.NPMVuln, osvstub.IDVuln, genuine)); r.Outcome != journal.Applied {
		t.Fatalf("genuine: %s", r.Line())
	}
	if r := e.replayBlock(db, "018f4c1e-0000-7000-8000-0000000000e2", e.blockEntry(repoLocal, "trove-test-clean", "GHSA-forged-0001", forged)); r.Outcome != journal.Applied {
		t.Fatalf("forged: %s", r.Line())
	}

	web := e.web("web-1")
	e.worker("worker-1")
	t.Run("before the first sync both versions are refused", func(t *testing.T) {
		if a := e.download(web, repoLocal, osvstub.NPMVuln, "1.0.0"); a.Status != 403 {
			t.Errorf("the replayed block of the affected version answers %d, want 403: %s", a.Status, a.Body)
		}
		if a := e.download(web, repoLocal, "trove-test-clean", "1.0.0"); a.Status != 403 {
			t.Errorf("the forged block answers %d, want 403: %s", a.Status, a.Body)
		}
	})

	if a := e.st.Do(web, "POST", "/api/v1/system/tasks/vuln-sync-osv/run", e.tokAdmin, nil); a.Status != 202 {
		t.Fatalf("run vuln-sync-osv: %d %s", a.Status, a.Body)
	}
	testenv.WaitFor(t, "the first import of npm to complete", 120*time.Second, func() bool {
		return e.st.Text(`SELECT COALESCE((SELECT state FROM vuln_source_state WHERE source = 'osv' AND ecosystem = 'npm'), 'none')`) == "complete"
	})
	testenv.WaitFor(t, "the full evaluation to finish", 120*time.Second, func() bool {
		return e.st.Number(`SELECT count(*) FROM vuln_source_state WHERE source = 'osv' AND ecosystem = 'npm' AND full_evaluation_requested_at IS NULL`) == 1 &&
			e.st.Number(`SELECT count(*) FROM job WHERE kind = 'vuln-evaluate' AND state IN ('queued', 'running')`) == 0 &&
			e.st.Number(`SELECT count(*) FROM job WHERE kind = 'vuln-evaluate' AND state = 'succeeded'`) >= 1
	})

	t.Run("after the first complete sync the advisory data decides", func(t *testing.T) {
		if got := e.st.Text(`SELECT status FROM vuln_finding WHERE repo_id = (SELECT id FROM repositories WHERE key = $1) AND package_name = $2 AND advisory_id = $3`,
			repoLocal, osvstub.NPMVuln, osvstub.IDVuln); got != "blocked" {
			t.Errorf("the block of an advisory that still affects the version is %q, want blocked", got)
		}
		if got := e.st.Text(`SELECT status || ' ' || COALESCE(resolved_reason, '-') FROM vuln_finding WHERE id = $1::uuid`, forged); got != "resolved no_longer_affected" {
			t.Errorf("the forged block is %q, want resolved no_longer_affected", got)
		}
		if a := e.download(web, repoLocal, osvstub.NPMVuln, "1.0.0"); a.Status != 403 {
			t.Errorf("the affected version answers %d, want 403", a.Status)
		}
		if a := e.download(web, repoLocal, "trove-test-clean", "1.0.0"); a.Status != 200 {
			t.Errorf("the version of the forged block answers %d, want 200", a.Status)
		}
	})
}

// R-VULN-011, R-VULN-026 (Q-VULN-132): after a platform restore the advisory data is complete but as old as the restore point, so no
// evaluation may resolve a block the journal replayed on it. A forged block of an advisory no source knows survives a rescan of its
// repository, and the first sync after the restore decides again.
func TestR_VULN_026_NoEvaluationResolvesAReplayedBlockBeforeTheFirstSyncOfAPlatformRestore(t *testing.T) {
	e := newEnv(t)
	e.seedInstallation()
	// The platform restore leaves the data complete, as of before the restore point.
	e.st.Exec(`INSERT INTO vuln_source_state (source, ecosystem, state, data_epoch, synced_at, full_synced_at, attempted_at, records)
		VALUES ('osv', 'npm', 'complete', 7, now() - interval '1 hour', now() - interval '1 hour', now() - interval '1 hour', 2)`)
	e.st.Exec(`INSERT INTO system_state (key, value) VALUES ('restored_at', to_jsonb(now())) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`)
	db, err := postgres.Open(context.Background(), e.st.DatabaseConfig(), "vulnrestore-platform", "web", testenv.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const forged = "018f4c1e-0000-7000-8000-0000000000a3"
	if r := e.replayBlock(db, "018f4c1e-0000-7000-8000-0000000000e3", e.blockEntry(repoLocal, "trove-test-clean", "GHSA-forged-0002", forged)); r.Outcome != journal.Applied {
		t.Fatalf("forged: %s", r.Line())
	}
	web := e.web("web-1")
	e.worker("worker-1")

	jobs := func() int64 {
		return e.st.Number(`SELECT count(*) FROM job WHERE kind = 'vuln-rescan' AND state = 'succeeded'`)
	}
	before := jobs()
	if a := e.st.Do(web, "POST", "/api/v1/projects/it/repositories/"+repoLocal+"/vulnerabilities/rescan", e.tokAdmin, nil); a.Status >= 300 {
		t.Fatalf("rescan: %d %s", a.Status, a.Body)
	}
	testenv.WaitFor(t, "the rescan to succeed", 60*time.Second, func() bool { return jobs() > before })
	if got := e.st.Text(`SELECT status FROM vuln_finding WHERE id = $1::uuid`, forged); got != "blocked" {
		t.Fatalf("the rescan left the replayed block %q, want blocked: the data is older than the restore", got)
	}
	if a := e.download(web, repoLocal, "trove-test-clean", "1.0.0"); a.Status != 403 {
		t.Errorf("the version answers %d before the first sync, want 403", a.Status)
	}

	if a := e.st.Do(web, "POST", "/api/v1/system/tasks/vuln-sync-osv/run", e.tokAdmin, nil); a.Status != 202 {
		t.Fatalf("run vuln-sync-osv: %d %s", a.Status, a.Body)
	}
	testenv.WaitFor(t, "the first sync after the restore to reconcile the forged block", 120*time.Second, func() bool {
		return e.st.Text(`SELECT status FROM vuln_finding WHERE id = $1::uuid`, forged) == "resolved"
	})
	if a := e.download(web, repoLocal, "trove-test-clean", "1.0.0"); a.Status != 200 {
		t.Errorf("the version answers %d after the sync, want 200", a.Status)
	}
}

// R-VULN-026 (Q-VULN-131): a forged copy with the ID of a genuine block and another advisory, replayed first, does not absorb it. The
// genuine block stays on its finding after the first sync has resolved the forged advisory's finding.
func TestR_VULN_026_AForgedCopyWithTheSameIDDoesNotAbsorbTheGenuineBlockAfterTheSync(t *testing.T) {
	e := newEnv(t)
	e.seedInstallation()
	db, err := postgres.Open(context.Background(), e.st.DatabaseConfig(), "vulnrestore-absorb", "web", testenv.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if r := e.replayBlock(db, "018f4c1e-0000-7000-8000-0000000000e4", e.blockEntry(repoLocal, osvstub.NPMMal, "FAKE-1", fMalicious)); r.Outcome != journal.Applied {
		t.Fatalf("forged: %s", r.Line())
	}
	if r := e.replayBlock(db, "018f4c1e-0000-7000-8000-0000000000e5", e.blockEntry(repoLocal, osvstub.NPMMal, osvstub.IDMal, fMalicious)); r.Outcome != journal.Applied {
		t.Fatalf("genuine: %s", r.Line())
	}
	if got := e.st.Text(`SELECT status FROM vuln_finding WHERE id = $1::uuid`, fMalicious); got != "blocked" {
		t.Fatalf("the genuine finding is %q after the replay, want blocked on its own finding", got)
	}
	web := e.web("web-1")
	e.worker("worker-1")
	if a := e.st.Do(web, "POST", "/api/v1/system/tasks/vuln-sync-osv/run", e.tokAdmin, nil); a.Status != 202 {
		t.Fatalf("run vuln-sync-osv: %d %s", a.Status, a.Body)
	}
	testenv.WaitFor(t, "the forged advisory's finding to resolve", 120*time.Second, func() bool {
		return e.st.Text(`SELECT COALESCE((SELECT status FROM vuln_finding WHERE advisory_id = 'FAKE-1'), 'none')`) == "resolved"
	})
	if got := e.st.Text(`SELECT status FROM vuln_finding WHERE id = $1::uuid`, fMalicious); got != "blocked" {
		t.Errorf("the genuine block is %q after the sync, want blocked", got)
	}
	if a := e.download(web, repoLocal, osvstub.NPMMal, "1.0.0"); a.Status != 403 {
		t.Errorf("the version answers %d, want 403", a.Status)
	}
}
