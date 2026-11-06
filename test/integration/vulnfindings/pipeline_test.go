//go:build integration

package vulnfindings

import (
	"context"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_VULN_011_PipelineAnswersLikeTheStatementsOneByOne: the reads of the commit hook's evaluation run as one batch on the
// transaction (Q-VULN-82). Every answer is the one the same generated method gives when it runs alone: rows, an empty answer and
// a statement that expects one row; no step is no round trip, and a step that does not repeat itself is refused.
func TestR_VULN_011_PipelineAnswersLikeTheStatementsOneByOne(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	e.addAdvisory("npm", "GHSA-pipe-0001", "", "vulnerability", "high")
	e.addAffected("npm", "GHSA-pipe-0001", 0, "pipe-a", fixedRange("SEMVER", "1.0.1"), nil, false, []string{"1.0.1"})
	e.addAffected("npm", "GHSA-pipe-0001", 1, "pipe-b", fixedRange("SEMVER", "2.0.0"), nil, false, []string{"2.0.0"})
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, testenv.PgDSN(t, e.st.Database()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	alone := q.New(tx)

	t.Run("R-VULN-011 Q-VULN-82 several statements, rows and none, in the order of the steps", func(t *testing.T) {
		var (
			a, b, none []q.VulnAffectedForNamesRow
			complete   bool
			missing    bool
		)
		err := q.Pipeline(ctx, tx,
			func(p *q.Queries) (err error) {
				a, err = p.VulnAffectedForNames(ctx, q.VulnAffectedForNamesParams{Ecosystem: "npm", Names: []string{"pipe-a"}})
				return err
			},
			func(p *q.Queries) (err error) { complete, err = p.VulnDataComplete(ctx, "npm"); return err },
			func(p *q.Queries) (err error) {
				b, err = p.VulnAffectedForNames(ctx, q.VulnAffectedForNamesParams{Ecosystem: "npm", Names: []string{"pipe-b"}})
				return err
			},
			func(p *q.Queries) (err error) {
				none, err = p.VulnAffectedForNames(ctx, q.VulnAffectedForNamesParams{Ecosystem: "npm", Names: []string{"pipe-none"}})
				return err
			},
			func(p *q.Queries) (err error) {
				rows, err := p.VulnItemVersion(ctx, 0)
				missing = len(rows) == 0
				return err
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		wantA, _ := alone.VulnAffectedForNames(ctx, q.VulnAffectedForNamesParams{Ecosystem: "npm", Names: []string{"pipe-a"}})
		wantB, _ := alone.VulnAffectedForNames(ctx, q.VulnAffectedForNamesParams{Ecosystem: "npm", Names: []string{"pipe-b"}})
		if len(a) != 1 || len(b) != 1 || !reflect.DeepEqual(a, wantA) || !reflect.DeepEqual(b, wantB) || a[0].Name != "pipe-a" || b[0].Name != "pipe-b" {
			t.Errorf("rows: %+v / %+v, one by one %+v / %+v", a, b, wantA, wantB)
		}
		if len(none) != 0 || !complete || !missing {
			t.Errorf("none %+v, complete %v, missing %v", none, complete, missing)
		}
	})

	t.Run("R-VULN-011 Q-VULN-82 no step is no round trip", func(t *testing.T) {
		if err := q.Pipeline(ctx, tx); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("R-VULN-011 Q-VULN-82 a step that issues another statement the second time is refused", func(t *testing.T) {
		calls := 0
		err := q.Pipeline(ctx, tx, func(p *q.Queries) error {
			calls++
			if calls == 1 {
				_, err := p.VulnDataComplete(ctx, "npm")
				return err
			}
			_, err := p.VulnAffectedForNames(ctx, q.VulnAffectedForNamesParams{Ecosystem: "npm", Names: []string{"pipe-a"}})
			return err
		})
		if err == nil {
			t.Fatal("a step that changed its statement was accepted")
		}
	})

	t.Run("R-VULN-011 Q-VULN-82 the statements that take the item answer like the ones that take the name", func(t *testing.T) {
		e.publish("npm-vendor", "pipe-a", "1.0.0") // the version has a finding, and the first publish claims the name
		item := e.st.Number(`SELECT pv.lead_item_id FROM package_versions pv JOIN packages p ON p.id = pv.package_id JOIN repositories r ON r.id = p.repo_id
			WHERE r.key = 'npm-vendor' AND p.name = 'pipe-a'`)
		repoID := e.st.Number(`SELECT id FROM repositories WHERE key = 'npm-vendor'`)
		byName, byItem := alone, alone
		wantAff, err := byName.VulnAffectedForNames(ctx, q.VulnAffectedForNamesParams{Ecosystem: "npm", Names: []string{"pipe-a"}})
		if err != nil || len(wantAff) != 1 {
			t.Fatalf("affected by name: %v %+v", err, wantAff)
		}
		gotAff, err := byItem.VulnAffectedForItem(ctx, q.VulnAffectedForItemParams{Ecosystem: "npm", ItemID: item})
		if err != nil || len(gotAff) != 1 || q.VulnAffectedForNamesRow(gotAff[0]).RecordID != wantAff[0].RecordID || gotAff[0].Name != "pipe-a" {
			t.Errorf("affected by item: %v %+v, by name %+v", err, gotAff, wantAff)
		}
		wantF, err := byName.VulnFindingsOfRepoNames(ctx, q.VulnFindingsOfRepoNamesParams{RepoID: repoID, Ecosystem: "npm", Names: []string{"pipe-a"}})
		if err != nil || len(wantF) != 1 {
			t.Fatalf("findings by name: %v %+v", err, wantF)
		}
		gotF, err := byItem.VulnFindingsOfRepoItem(ctx, q.VulnFindingsOfRepoItemParams{RepoID: repoID, Ecosystem: "npm", ItemID: item})
		if err != nil || len(gotF) != 1 || gotF[0].ID != wantF[0].ID {
			t.Errorf("findings by item: %v %+v, by name %+v", err, gotF, wantF)
		}
		wantC, err := byName.VulnClaimHolders(ctx, q.VulnClaimHoldersParams{Format: "npm", Name: "pipe-a"})
		if err != nil || len(wantC) == 0 {
			t.Fatalf("claims by name: %v %+v", err, wantC)
		}
		gotC, err := byItem.VulnClaimHoldersForItem(ctx, q.VulnClaimHoldersForItemParams{Format: "npm", ItemID: item})
		if err != nil || len(gotC) != len(wantC) || gotC[0].Key != wantC[0].Key || gotC[0].Pattern != wantC[0].Pattern {
			t.Errorf("claims by item: %v %+v, by name %+v", err, gotC, wantC)
		}
		none, err := byItem.VulnAffectedForItem(ctx, q.VulnAffectedForItemParams{Ecosystem: "npm", ItemID: 0})
		if err != nil || len(none) != 0 {
			t.Errorf("an item that belongs to no version names no package: %v %+v", err, none)
		}
	})
}
