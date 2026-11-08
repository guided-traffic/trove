//go:build integration

package vulnfindings

import (
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/evaluate"
	_ "github.com/guided-traffic/trove/internal/vuln/triage" // the replay handler of the download blocks
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The replay of a block entry against a database whose evaluation answers the gates (R-VULN-026, Q-VULN-114, Q-VULN-131): the
// writes of the handler run in a transaction of a pool of their own, as the restore's replay runs them, and the gates are the
// real EvaluateMiss and the download's join of files.sql.

const (
	restoredFinding = "018f4c1e-0000-7000-8000-0000000000a1"
	restoredEntry   = "018f4c1e-0000-7000-8000-0000000000e1"
)

// blockEntryJSON is the entry a producer writes for a finding of the repository.
func (e *env) blockEntryJSON(findingID, repo, name, version, advisoryID, kind string) []byte {
	e.t.Helper()
	repoID := e.st.Number(`SELECT id FROM repositories WHERE key = $1`, repo)
	raw, err := vuln.NewBlockEntry(findingID, repoID, "it/"+repo, "npm", name, version, advisoryID, kind, "high").JSON()
	if err != nil {
		e.t.Fatal(err)
	}
	return raw
}

// replayDB opens the pool the replay's transactions run on.
func (e *env) replayDB() *postgres.DB {
	e.t.Helper()
	db, err := postgres.Open(context.Background(), e.st.DatabaseConfig(), "vulnfind-replay", "web", testenv.Quiet())
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(db.Close)
	return db
}

// replay applies one entry as the restore's replay does and returns the result.
func (e *env) replay(db *postgres.DB, entryID string, entry []byte) journal.Result {
	e.t.Helper()
	h, ok := journal.HandlerFor(audit.EventVulnFindingStatusChanged)
	if !ok {
		e.t.Fatal("no replay handler for the block event")
	}
	var res journal.Result
	err := db.InTx(context.Background(), func(tx meta.Tx) error {
		var err error
		res, err = h.Apply(context.Background(), tx, journal.Env{}, journal.Object{V: journal.Version, ID: entryID, Event: audit.EventVulnFindingStatusChanged, Entry: entry})
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

// shape reads the members of a finding that the gates read.
func (e *env) shape(id string) string {
	e.t.Helper()
	return e.st.Text(`SELECT status || ' ' || COALESCE(resolved_reason, '-') || ' ' || COALESCE(status_before_resolution, '-') || ' ' ||
		(version_id IS NOT NULL)::text FROM vuln_finding WHERE id = $1::uuid`, id)
}

// R-VULN-013, R-VULN-015, Q-VULN-114, Q-VULN-131 (the correction): a restored finding that resolved with version_removed while
// blocked is the shape the remote miss counts while the data is incomplete. The replay of a block for it must leave that shape; it
// used to set the status blocked on the row and leave it unlinked, which neither the download's join (by version_id) nor the miss
// (resolved while blocked) read, so the restored installation fetched the version again.
func TestR_VULN_013_AReplayedBlockOfAnEvictedVersionKeepsTheShapeTheMissReads(t *testing.T) {
	e := newEnv(t)
	svc := e.service(evaluate.Options{}) // no data: the restored installation has none
	ctx := context.Background()
	db := e.replayDB()
	miss := func() vuln.Block {
		t.Helper()
		b, err := svc.EvaluateMiss(ctx, vuln.Miss{Repo: qr("npm-remote"), Format: "npm", Name: "trove-test-vuln", Version: "1.0.0"})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	e.st.Exec(`INSERT INTO vuln_finding (id, repo_id, ecosystem, package_name, version, advisory_id, kind, severity, severity_rank, fix_available,
			status, status_before_resolution, resolved_reason, resolved_at, advisory_modified)
		SELECT $1::uuid, r.id, 'npm', 'trove-test-vuln', '1.0.0', 'GHSA-aaaa-aaaa-0001', 'vulnerability', 'high', 4, false,
		       'resolved', 'blocked', 'version_removed', now(), now() FROM repositories r WHERE r.key = 'npm-remote'`, restoredFinding)
	if b := miss(); !b.Refused() || b.FindingID != restoredFinding {
		t.Fatalf("before the replay the miss is %+v, want refused by the evicted block", b)
	}

	res := e.replay(db, restoredEntry, e.blockEntryJSON(restoredFinding, "npm-remote", "trove-test-vuln", "1.0.0", "GHSA-aaaa-aaaa-0001", "vulnerability"))
	if res.Outcome != journal.AlreadyInEffect {
		t.Errorf("the replay of a block that is in its shape: %s", res.Line())
	}
	if got := e.shape(restoredFinding); got != "resolved version_removed blocked false" {
		t.Errorf("the finding reads %q after the replay, want it left resolved while blocked and unlinked", got)
	}
	if b := miss(); !b.Refused() || b.FindingID != restoredFinding {
		t.Errorf("after the replay the remote miss is %+v: the version would be fetched and served", b)
	}
}
