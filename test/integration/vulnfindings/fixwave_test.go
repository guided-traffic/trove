//go:build integration

package vulnfindings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/evaluate"
	"github.com/guided-traffic/trove/internal/vuln/store"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The fix wave of the evaluation and the findings (Q-VULN-64 to Q-VULN-67): the kind of a finding, the merge of two findings of
// one alias group, the re-key of resolved findings, the races of the writes, the history's growth, the queue of an ecosystem the
// build cannot evaluate, the resume of a rescan, the cost of a fill and the backfill of cached tarballs.

// stmtCounter counts the statements of the pool it traces, apart from the transaction's own.
type stmtCounter struct {
	mu  sync.Mutex
	sql []string
}

func (c *stmtCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sql = append(c.sql, d.SQL)
	return ctx
}

func (*stmtCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (c *stmtCounter) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sql = nil
}

// statements are the traced statements, without BEGIN, COMMIT and ROLLBACK.
func (c *stmtCounter) statements() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, s := range c.sql {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "begin", "commit", "rollback", "begin read write", "begin isolation level read committed":
			continue
		}
		out = append(out, s)
	}
	return out
}

// fixedClaims is a claim index of fixed claims: the package name and the key of the repository that claims it.
type fixedClaims map[string]string

func (f fixedClaims) ClaimsFor(_ context.Context, _, name string) ([]repository.ClaimMatch, error) {
	if repo, ok := f[name]; ok {
		return []repository.ClaimMatch{{Repository: repo, Pattern: name, Scope: "members", Source: "auto"}}, nil
	}
	return nil, nil
}

// serviceDB is e.service with the database it runs on, traced when a tracer is given.
func (e *env) serviceDB(o evaluate.Options, tr pgx.QueryTracer) (*evaluate.Service, *postgres.DB) {
	e.t.Helper()
	var opts []postgres.Option
	if tr != nil {
		opts = append(opts, postgres.WithQueryTracer(tr))
	}
	db, err := postgres.Open(context.Background(), e.st.DatabaseConfig(), "vulnfind-svc", "web", testenv.Quiet(), opts...)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(db.Close)
	o.Runner = db
	if o.Repos == nil {
		o.Repos = e.rep.App.Repositories()
	}
	if o.Ecosystems == nil {
		o.Ecosystems = ecosystems()
	}
	o.Enabled = true
	if o.PublicUpstreams == nil {
		o.PublicUpstreams = map[string][]string{"npm": {"registry.npmjs.org"}, "nuget": {"api.nuget.org", "www.nuget.org"}}
	}
	svc, err := evaluate.New(o)
	if err != nil {
		e.t.Fatal(err)
	}
	return svc, db
}

// R-VULN-012, Q-VULN-65 (S1): a finding's kind is that of the record whose entry affects the version. A MAL- record that is the
// canonical record of a group with an ordinary advisory makes the versions its own entry names malicious, and no other version
// of the package.
func TestR_VULN_012_TheKindOfAFindingIsThatOfTheRecordThatAffectsTheVersion(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	e.addAdvisory("npm", "GHSA-aaaa-aaaa-1001", "MAL-2026-5001", "vulnerability", "high")
	e.addAffected("npm", "GHSA-aaaa-aaaa-1001", 0, "trove-test-kind", fixedRange("SEMVER", "1.0.1"), nil, false, nil)
	e.addAdvisory("npm", "MAL-2026-5001", "", "malicious", "critical")
	e.addAffected("npm", "MAL-2026-5001", 0, "trove-test-kind", "[]", []string{"9.9.9"}, false, nil)
	e.addVersion("npm-vendor", "trove-test-kind", "1.0.0")
	e.addVersion("npm-vendor", "trove-test-kind", "9.9.9")
	e.rescan("npm-vendor")

	kind := func(version string) string {
		return e.st.Text(`SELECT COALESCE((SELECT kind || ' ' || advisory_id FROM vuln_finding WHERE package_name = 'trove-test-kind' AND version = $1), '')`, version)
	}
	if got := kind("1.0.0"); got != "vulnerability MAL-2026-5001" {
		t.Errorf("1.0.0 (affected by the ordinary record only): %q", got)
	}
	if got := kind("9.9.9"); got != "malicious MAL-2026-5001" {
		t.Errorf("9.9.9 (named by the malicious record): %q", got)
	}
	// The malicious record is withdrawn: it contributes nothing, the ordinary one still affects 1.0.0.
	e.st.Exec(`UPDATE vuln_advisory SET withdrawn = now(), modified = now() + interval '1 hour' WHERE id = 'MAL-2026-5001'`)
	e.queue("npm", "MAL-2026-5001")
	e.evaluate()
	if got := kind("1.0.0"); got != "vulnerability MAL-2026-5001" {
		t.Errorf("1.0.0 after the withdrawal: %q", got)
	}
	if got := e.st.Text(`SELECT status FROM vuln_finding WHERE package_name = 'trove-test-kind' AND version = '9.9.9'`); got != "resolved" {
		t.Errorf("9.9.9 after the withdrawal: %q, want resolved", got)
	}
}

// R-VULN-006, R-VULN-012, Q-VULN-65 (S2): two findings of one version whose advisories joined one alias group merge into one, the
// stronger status first, whichever of them held the block; the version is never resolved while it is affected, the history of
// both is kept and nothing is lifted.
func TestR_VULN_012_TwoFindingsOfOneAliasGroupMergeBlockFirst(t *testing.T) {
	for _, tc := range []struct {
		name, blocked string
	}{
		{"the canonical record's finding is blocked", "GHSA-aaaa-aaaa-2001"},
		{"the other finding is blocked", "GHSA-aaaa-aaaa-2002"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.setComplete("npm", true)
			for _, id := range []string{"GHSA-aaaa-aaaa-2001", "GHSA-aaaa-aaaa-2002"} {
				e.addAdvisory("npm", id, "", "vulnerability", "high")
				e.addAffected("npm", id, 0, "trove-test-merge", fixedRange("SEMVER", "1.0.1"), nil, false, nil)
			}
			e.addVersion("npm-vendor", "trove-test-merge", "1.0.0")
			e.rescan("npm-vendor")
			if got := e.findings("npm-vendor", "trove-test-merge", "1.0.0", ""); got != 2 {
				t.Fatalf("%d findings before the merge, want 2", got)
			}
			e.st.Exec(`UPDATE vuln_finding SET status = 'blocked' WHERE advisory_id = $1`, tc.blocked)
			e.st.Exec(`UPDATE vuln_finding SET status = 'dismissed', dismissal_reason = 'risk_accepted' WHERE advisory_id <> $1`, tc.blocked)

			// The second record joins the group of the first.
			e.st.Exec(`UPDATE vuln_advisory SET canonical_id = 'GHSA-aaaa-aaaa-2001', aliases = ARRAY['GHSA-aaaa-aaaa-2001'], modified = now() + interval '1 hour'
				WHERE id = 'GHSA-aaaa-aaaa-2002'`)
			e.queue("npm", "GHSA-aaaa-aaaa-2001", "GHSA-aaaa-aaaa-2002")
			e.evaluate()

			if got := e.findings("npm-vendor", "trove-test-merge", "1.0.0", ""); got != 1 {
				t.Fatalf("%d findings after the merge, want 1", got)
			}
			if got := e.st.Text(`SELECT advisory_id || ' ' || status FROM vuln_finding WHERE package_name = 'trove-test-merge'`); got != "GHSA-aaaa-aaaa-2001 blocked" {
				t.Errorf("the merged finding: %q", got)
			}
			h := e.history("npm-vendor", "trove-test-merge", "1.0.0")
			if strings.Contains(h, "resolved") || !strings.Contains(h, "advisory_changed") {
				t.Errorf("history %q: want the history of both findings and no resolution", h)
			}
			// Evaluating again changes nothing.
			e.queue("npm", "GHSA-aaaa-aaaa-2001")
			e.evaluate()
			if got := e.history("npm-vendor", "trove-test-merge", "1.0.0"); got != h {
				t.Errorf("history after a second evaluation %q, was %q", got, h)
			}
		})
	}
}

// R-VULN-006, R-VULN-015, Q-VULN-65 (C5): a blocked finding that resolved with version_removed follows its advisory into another
// group: the miss of the version finds it by the new identifier and refuses, and the fill's re-detection keeps it blocked.
func TestR_VULN_015_AResolvedBlockedFindingFollowsItsAdvisoryIntoANewGroup(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	e.addAdvisory("npm", "GHSA-aaaa-aaaa-3002", "", "vulnerability", "high")
	e.addAffected("npm", "GHSA-aaaa-aaaa-3002", 0, "trove-test-rekey", fixedRange("SEMVER", "1.0.1"), nil, false, nil)
	e.addVersion("npm-remote", "trove-test-rekey", "1.0.0")
	e.rescan("npm-remote")
	id := e.st.Text(`SELECT id::text FROM vuln_finding`)
	e.st.Exec(`UPDATE vuln_finding SET status = 'resolved', status_before_resolution = 'blocked', resolved_reason = 'version_removed', resolved_at = now() WHERE id = $1::uuid`, id)
	e.st.Exec(`DELETE FROM package_versions WHERE version = '1.0.0'`)

	// The advisory joins the group of another record: the group's identifier is GHSA-aaaa-aaaa-3001 from now on.
	e.st.Exec(`UPDATE vuln_advisory SET canonical_id = 'GHSA-aaaa-aaaa-3001', aliases = ARRAY['GHSA-aaaa-aaaa-3001'], modified = now() + interval '1 hour' WHERE id = 'GHSA-aaaa-aaaa-3002'`)
	svc := e.service(evaluate.Options{States: fixedStates{"npm": {Complete: true}}})
	b, err := svc.EvaluateMiss(context.Background(), vuln.Miss{RepoKey: "npm-remote", Format: "npm", Name: "trove-test-rekey", Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if b.Reason != vuln.BlockTriage || b.FindingID != id || b.AdvisoryID != "GHSA-aaaa-aaaa-3001" || !b.Refused() {
		t.Errorf("the miss of a version whose blocked finding follows its advisory: %+v", b)
	}
	// The fetch happens (the block is not enforced by this service here): the version is back and the evaluation of its name
	// re-keys the resolved finding and re-detects it blocked.
	e.addVersion("npm-remote", "trove-test-rekey", "1.0.0")
	e.queue("npm", "GHSA-aaaa-aaaa-3002")
	e.evaluate()
	if got := e.st.Text(`SELECT id::text || ' ' || advisory_id || ' ' || status FROM vuln_finding`); got != id+" GHSA-aaaa-aaaa-3001 blocked" {
		t.Errorf("after the re-detection: %q", got)
	}
}

// R-VULN-012, Q-VULN-65 (C9, C10): an insert that finds the finding written returns false and the finding is read; a status change
// holds only while the finding has the status and the revision that were read, so a triage that came between is not overwritten.
func TestR_VULN_012_TheWritesOfTheEvaluationAreConditional(t *testing.T) {
	e := newEnv(t)
	_, db := e.serviceDB(evaluate.Options{}, nil)
	ctx := context.Background()
	e.addVersion("npm-vendor", "trove-test-race", "1.0.0")
	repoID := e.st.Number(`SELECT id FROM repositories WHERE key = 'npm-vendor'`)
	nf := store.NewFinding{
		ID: store.NewFindingID(), RepoID: repoID, Ecosystem: "npm", Name: "trove-test-race", Version: "1.0.0", AdvisoryID: "GHSA-aaaa-aaaa-4001",
		Kind: "vulnerability", Severity: "high", CVSS: store.NoScore, AdvisoryModifiedU: 1,
	}
	var first, second bool
	var read store.Finding
	if err := db.InTx(ctx, func(tx meta.Tx) error {
		ev, err := store.EvalOf(tx)
		if err != nil {
			return err
		}
		if first, err = ev.InsertFinding(ctx, nf); err != nil {
			return err
		}
		nf.ID = store.NewFindingID()
		if second, err = ev.InsertFinding(ctx, nf); err != nil {
			return err
		}
		var ok bool
		read, ok, err = ev.FindingByIdentity(ctx, repoID, "npm", "trove-test-race", "1.0.0", "GHSA-aaaa-aaaa-4001")
		if !ok {
			return errors.New("the finding is not read")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !first || second {
		t.Fatalf("insert results %v then %v, want true then false", first, second)
	}
	if got := e.findings("npm-vendor", "trove-test-race", "1.0.0", ""); got != 1 {
		t.Fatalf("%d findings", got)
	}
	// A triage sets the block after the evaluation read the finding.
	e.st.Exec(`UPDATE vuln_finding SET status = 'blocked', revision = revision + 1 WHERE id = $1::uuid`, read.ID)
	var done bool
	if err := db.InTx(ctx, func(tx meta.Tx) error {
		ev, err := store.EvalOf(tx)
		if err != nil {
			return err
		}
		done, err = ev.SetStatus(ctx, read.ID, read.Status, read.Revision, "dismissed")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if done {
		t.Error("the status change of a stale read was applied")
	}
	if got := e.status("npm-vendor", "trove-test-race", "1.0.0"); got != "blocked" {
		t.Errorf("the block was lifted: %q", got)
	}
}

// R-VULN-012, Q-VULN-65 (C11): a re-detection keeps the comment of a finding that returns to a status other than dismissed, and
// drops the dismissal's reason and comment when a finding moves away from dismissed.
func TestR_VULN_012_ARedetectionKeepsTheTriageCommentUnlessTheFindingLeavesDismissed(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	e.addAdvisory("npm", "GHSA-aaaa-aaaa-5001", "", "vulnerability", "medium")
	e.addAffected("npm", "GHSA-aaaa-aaaa-5001", 0, "trove-test-note", everyVersionRange, nil, true, nil)
	e.addVersion("npm-vendor", "trove-test-note", "1.0.0")
	e.addVersion("npm-vendor", "trove-test-note", "2.0.0")
	e.rescan("npm-vendor")
	e.st.Exec(`UPDATE vuln_finding SET status = 'resolved', status_before_resolution = 'confirmed', resolved_reason = 'version_removed', resolved_at = now(),
		comment = 'checked by the platform team' WHERE version = '1.0.0'`)
	e.st.Exec(`UPDATE vuln_finding SET status = 'dismissed', dismissal_reason = 'false_positive', comment = 'not reachable' WHERE version = '2.0.0'`)
	e.st.Exec(`UPDATE vuln_advisory SET severity = 'critical', cvss_score = 9.8, modified = now() + interval '1 hour'`)
	e.queue("npm", "GHSA-aaaa-aaaa-5001")
	e.evaluate()

	if got := e.st.Text(`SELECT status || ' ' || COALESCE(comment, '-') FROM vuln_finding WHERE version = '1.0.0'`); got != "confirmed checked by the platform team" {
		t.Errorf("the re-detected finding: %q", got)
	}
	if got := e.st.Text(`SELECT status || ' ' || COALESCE(comment, '-') || ' ' || COALESCE(dismissal_reason, '-') FROM vuln_finding WHERE version = '2.0.0'`); got != "needs_triage - -" {
		t.Errorf("the reopened finding: %q", got)
	}
}

// R-VULN-011, Q-VULN-65 (C13): queued advisories of an ecosystem this build has no adapter for stay queued; the others are
// worked off.
func TestR_VULN_011_QueuedAdvisoriesOfAnUnknownEcosystemStayQueued(t *testing.T) {
	e := newEnv(t)
	e.queue("PyPI", "PYSEC-2026-1")
	e.queue("npm", "GHSA-aaaa-aaaa-6001")
	e.evaluate()
	if got := e.st.Number(`SELECT count(*) FROM vuln_pending_evaluation WHERE ecosystem = 'PyPI'`); got != 1 {
		t.Errorf("%d queued rows of PyPI, want 1", got)
	}
	if got := e.st.Number(`SELECT count(*) FROM vuln_pending_evaluation WHERE ecosystem = 'npm'`); got != 0 {
		t.Errorf("%d queued rows of npm, want 0", got)
	}
}

// savingRun is a job handle that keeps the checkpoint it saved and can fail after the first one, to cut a job off.
type savingRun struct {
	fakeRun
	saved  []byte
	cutAt  int
	loaded []byte
}

func (s *savingRun) LoadCheckpoint(v any) (bool, error) {
	if s.loaded == nil {
		return false, nil
	}
	return true, json.Unmarshal(s.loaded, v)
}

func (s *savingRun) SaveCheckpoint(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.saved = b
	if err := s.fakeRun.SaveCheckpoint(ctx, v); err != nil {
		return err
	}
	if s.cutAt > 0 && s.checkpoints >= s.cutAt {
		return errors.New("the job is cut off")
	}
	return nil
}

// R-VULN-027, Q-VULN-65 (A11): a rescan that was cut off resumes at its checkpoint while the repository's scanning inputs are the
// same; a changed input starts it over.
func TestR_VULN_027_ARescanResumesFromItsCheckpointWhileItsInputsAreUnchanged(t *testing.T) {
	e := newEnvRole(t, "web")
	e.setComplete("npm", true)
	e.st.Exec(`UPDATE repositories SET config = jsonb_set(config, '{vulnerabilities}', '{"matchPublicAdvisories":"always"}') WHERE key = 'npm-mirror'`)
	for i := range 5 {
		name := fmt.Sprintf("trove-test-n%d", i)
		id := fmt.Sprintf("MAL-2026-91%02d", i)
		e.addAdvisory("npm", id, "", "malicious", "critical")
		e.addAffected("npm", id, 0, name, everyVersionRange, nil, true, nil)
		e.addVersion("npm-mirror", name, "1.0.0")
	}
	svc := e.service(evaluate.Options{BatchNames: 2})
	repoID := e.st.Number(`SELECT id FROM repositories WHERE key = 'npm-mirror'`)
	args, _ := json.Marshal(vuln.RescanArgs{Repository: "npm-mirror", RepositoryID: repoID})

	cut := &savingRun{cutAt: 1}
	if err := svc.RunRescan(context.Background(), cut, args); err == nil {
		t.Fatal("the cut-off job ended without an error")
	}
	if got := e.findings("npm-mirror", "", "", ""); got != 2 {
		t.Fatalf("%d findings after the first batch, want 2", got)
	}
	// The first batch's findings go; a run that resumes does not write them again.
	e.st.Exec(`DELETE FROM vuln_finding`)
	resumed := &savingRun{loaded: cut.saved}
	if err := svc.RunRescan(context.Background(), resumed, args); err != nil {
		t.Fatal(err)
	}
	if got := e.findings("npm-mirror", "", "", ""); got != 3 {
		t.Errorf("%d findings after the resumed run, want 3 (the names after the cursor)", got)
	}
	// A changed input: the same checkpoint does not apply, the job starts over.
	e.st.Exec(`DELETE FROM vuln_finding`)
	e.st.Exec(`UPDATE repositories SET config = jsonb_set(config, '{vulnerabilities}', '{"matchPublicAdvisories":"always","blockMalicious":false}') WHERE key = 'npm-mirror'`)
	again := &savingRun{loaded: cut.saved}
	if err := svc.RunRescan(context.Background(), again, args); err != nil {
		t.Fatal(err)
	}
	if got := e.findings("npm-mirror", "", "", ""); got != 5 {
		t.Errorf("%d findings after a rescan whose inputs changed, want 5", got)
	}
}

// R-VULN-012, Q-VULN-66 (A8): a modification of the record alone is stored without a revision and without an event; a version
// that is evicted and fetched again within the hour leaves one `redetected` event and no pair per cycle, and a resolution that
// lasted keeps its event.
func TestR_VULN_012_TheHistoryDoesNotGrowByModificationsOrByAFlappingCache(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	svc, db := e.serviceDB(evaluate.Options{}, nil)
	e.addAdvisory("npm", "GHSA-aaaa-aaaa-7001", "", "vulnerability", "medium")
	e.addAffected("npm", "GHSA-aaaa-aaaa-7001", 0, "trove-test-flap", everyVersionRange, nil, true, nil)
	e.addVersion("npm-vendor", "trove-test-flap", "1.0.0")
	e.rescan("npm-vendor")

	rev := func() int64 {
		return e.st.Number(`SELECT revision FROM vuln_finding WHERE package_name = 'trove-test-flap'`)
	}
	before := rev()
	e.st.Exec(`UPDATE vuln_advisory SET modified = modified + interval '1 hour'`)
	e.queue("npm", "GHSA-aaaa-aaaa-7001")
	e.evaluate()
	if got := rev(); got != before {
		t.Errorf("the revision moved from %d to %d by a bare modification", before, got)
	}
	if got := e.history("npm-vendor", "trove-test-flap", "1.0.0"); got != "detected" {
		t.Errorf("history %q after a bare modification", got)
	}
	if got := e.st.Number(`SELECT (advisory_modified > now() + interval '30 minutes')::int FROM vuln_finding WHERE package_name = 'trove-test-flap'`); got != 1 {
		t.Error("the modification time was not stored")
	}

	evict := func() {
		t.Helper()
		id := e.st.Number(`SELECT pv.id FROM package_versions pv JOIN packages p ON p.id = pv.package_id WHERE p.name = 'trove-test-flap'`)
		if err := db.InTx(context.Background(), func(tx meta.Tx) error { return svc.OnVersionsRemoved(context.Background(), tx, []int64{id}) }); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		evict()
		e.rescan("npm-vendor")
	}
	if got := e.history("npm-vendor", "trove-test-flap", "1.0.0"); got != "detected,redetected" {
		t.Errorf("history %q after three cycles of eviction and refill", got)
	}
	// A resolution that lasted stays in the history.
	evict()
	e.st.Exec(`UPDATE vuln_finding_event SET at = now() - interval '2 hours' WHERE change = 'resolved'`)
	e.rescan("npm-vendor")
	if got := e.history("npm-vendor", "trove-test-flap", "1.0.0"); got != "detected,redetected,resolved,redetected" {
		t.Errorf("history %q after a resolution that lasted", got)
	}
}

// R-VULN-011, Q-VULN-66 (A4): the statements of the commit hook's evaluation of one version. A version of a name that is no
// internal name costs six: the repository row, the version of the item, the findings of the name in the repository, the data state,
// the affected entries and the claims that can cover the name (the repository row and the claims are read in the transaction, so
// that the commit takes no second pool connection); a claimed name adds the prune of its matches. The claim of npm-local is the
// auto claim of the first publish.
func TestR_VULN_011_TheCommitEvaluationCostsAFixedNumberOfStatements(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	tr := &stmtCounter{}
	svc, db := e.serviceDB(evaluate.Options{}, tr)
	ctx := context.Background()
	count := func(repo, name string) []string {
		t.Helper()
		e.publish(repo, name, "1.0.0")
		item := e.st.Number(`SELECT pv.lead_item_id FROM package_versions pv JOIN packages p ON p.id = pv.package_id JOIN repositories r ON r.id = p.repo_id
			WHERE r.key = $1 AND p.name = $2`, repo, name)
		repoID := e.st.Number(`SELECT id FROM repositories WHERE key = $1`, repo)
		tr.reset()
		if err := db.InTx(ctx, func(tx meta.Tx) error {
			return svc.OnVersionCommitted(ctx, tx, vuln.CommittedVersion{RepoID: repoID, RepoKey: repo, Format: "npm", ItemID: item})
		}); err != nil {
			t.Fatal(err)
		}
		return tr.statements()
	}
	if got := count("npm-vendor", "trove-test-count-a"); len(got) != 6 {
		t.Errorf("%d statements for a public name, want 6:\n%s", len(got), strings.Join(got, "\n---\n"))
	}
	if got := count("npm-local", "trove-test-count-b"); len(got) != 7 {
		t.Errorf("%d statements for a claimed name, want 7:\n%s", len(got), strings.Join(got, "\n---\n"))
	}
}

// forbiddenClaims and forbiddenRepos fail the test when the commit hook reads a replica cache: the claim index and the repository
// snapshot load through the pool on a miss, and a commit that waits for a second connection starves behind the waiters of its row
// lock (R-VULN-011, R-NUGET-006).
type forbiddenClaims struct{ t *testing.T }

func (f forbiddenClaims) ClaimsFor(context.Context, string, string) ([]repository.ClaimMatch, error) {
	f.t.Error("the commit's evaluation read the replica's claim index")
	return nil, nil
}

type forbiddenRepos struct {
	repository.Reader
	t *testing.T
}

func (f forbiddenRepos) ByKey(ctx context.Context, key string) (repository.Repository, error) {
	f.t.Errorf("the commit's evaluation read the replica's repository snapshot for %s", key)
	return f.Reader.ByKey(ctx, key)
}

func (f forbiddenRepos) List(ctx context.Context) ([]repository.Repository, error) {
	f.t.Error("the commit's evaluation listed the replica's repository snapshot")
	return f.Reader.List(ctx)
}

// R-VULN-010, R-VULN-011: the commit hook reads the claims rule and the repository in its own transaction. With a claim index and a
// snapshot that fail the test when they are read, a name that a claim of another repository covers is internal (the claim is
// written to the table after the replica's index loaded, so only a read in the transaction sees it), and the name is matched
// again once the claim is gone.
func TestR_VULN_011_TheCommitEvaluationReadsTheClaimsAndTheRepositoryInItsTransaction(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	svc, db := e.serviceDB(evaluate.Options{Claims: forbiddenClaims{t}, Repos: forbiddenRepos{Reader: e.rep.App.Repositories(), t: t}}, nil)
	ctx := context.Background()
	const name = "trove-test-txclaim"
	e.publish("npm-noclaim", name, "1.0.0")
	item := e.st.Number(`SELECT pv.lead_item_id FROM package_versions pv JOIN packages p ON p.id = pv.package_id JOIN repositories r ON r.id = p.repo_id
		WHERE r.key = 'npm-noclaim' AND p.name = $1`, name)
	repoID := e.st.Number(`SELECT id FROM repositories WHERE key = 'npm-noclaim'`)
	commit := func() {
		t.Helper()
		if err := db.InTx(ctx, func(tx meta.Tx) error {
			return svc.OnVersionCommitted(ctx, tx, vuln.CommittedVersion{RepoID: repoID, RepoKey: "npm-noclaim", Format: "npm", ItemID: item})
		}); err != nil {
			t.Fatal(err)
		}
	}
	e.addAdvisory("npm", "MAL-2026-9101", "", "malicious", "critical")
	e.addAffected("npm", "MAL-2026-9101", 0, name, everyVersionRange, nil, true, nil)
	e.st.Exec(`INSERT INTO namespace_claims (repo_id, pattern, source, scope) SELECT id, 'trove-test-txclaim*', 'manual', 'members' FROM repositories WHERE key = 'npm-local'`)
	commit()
	if got := e.findings("npm-noclaim", name, "", ""); got != 0 {
		t.Errorf("%d findings of a name that a claim of npm-local covers", got)
	}
	if got := e.st.Number(`SELECT count(*) FROM vuln_claimed_match WHERE name = $1`, name); got != 1 {
		t.Errorf("%d claimed-name matches, want 1", got)
	}
	e.st.Exec(`DELETE FROM namespace_claims WHERE pattern = 'trove-test-txclaim*'`)
	commit()
	if got := e.findings("npm-noclaim", name, "", ""); got != 1 {
		t.Errorf("%d findings once the claim is gone, want 1", got)
	}
}

// R-VULN-010, Q-VULN-65 (C6): the scope question the data rule asks, answered by the rule of the miss: a remote of a public
// upstream and a local repository that holds public packages are in scope, an unrecognised upstream and a claimed name are not.
func TestR_VULN_010_InScopeIsTheRuleOfTheMiss(t *testing.T) {
	e := newEnv(t)
	e.publish("npm-local", "trove-test-scope", "1.0.0")
	svc := e.service(evaluate.Options{Claims: fixedClaims{"trove-test-scope": "npm-local"}})
	ctx := context.Background()
	for _, tc := range []struct {
		repo, name string
		want       bool
	}{
		{"npm-remote", "left-pad", true},
		{"npm-mirror", "left-pad", false},
		{"npm-vendor", "left-pad", true},
		{"npm-noclaim", "left-pad", true},
		{"npm-local", "trove-test-scope", false},
		{"files-internal", "left-pad", false},
	} {
		repo, err := e.rep.App.Repositories().ByKey(ctx, tc.repo)
		if err != nil {
			t.Fatal(err)
		}
		got, err := svc.InScope(ctx, repo, tc.name)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("%s/%s in scope = %v, want %v", tc.repo, tc.name, got, tc.want)
		}
	}
}

// R-VULN-015, Q-VULN-67 (C4): a tarball that a remote repository cached before versions were recorded gets its package and version
// rows when the ecosystem's full evaluation runs, and is evaluated by it; a file that is no artifact stays without a version.
func TestR_VULN_015_ACachedTarballWithoutAVersionGetsItsRowsInTheFullEvaluation(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	e.st.Exec(`INSERT INTO blobs (sha256, sha1, md5, sha512, size) VALUES (decode(repeat('ab', 32), 'hex'), decode(repeat('cd', 20), 'hex'),
		decode(repeat('ef', 16), 'hex'), decode(repeat('12', 64), 'hex'), 10)`)
	for _, path := range []string{"trove-test-old/-/trove-test-old-1.0.0.tgz", "trove-test-old/-/other.tgz", "trove-test-old"} {
		e.st.Exec(`INSERT INTO items (repo_id, path, parent_path, kind, blob_sha256, size) SELECT id, $2, $3, 'file', decode(repeat('ab', 32), 'hex'), 10
			FROM repositories WHERE key = $1`, "npm-remote", path, path[:max(strings.LastIndexByte(path, '/'), 0)])
	}
	e.addAdvisory("npm", "MAL-2026-9001", "", "malicious", "critical")
	e.addAffected("npm", "MAL-2026-9001", 0, "trove-test-old", everyVersionRange, nil, true, nil)
	e.st.Exec(`UPDATE vuln_source_state SET full_evaluation_requested_at = now() WHERE source = 'osv' AND ecosystem = 'npm'`)
	e.evaluate()

	if got := e.st.Text(`SELECT COALESCE(pv.version, '-') FROM items i LEFT JOIN package_versions pv ON pv.id = i.version_id WHERE i.path = 'trove-test-old/-/trove-test-old-1.0.0.tgz'`); got != "1.0.0" {
		t.Errorf("the tarball's version: %q", got)
	}
	if got := e.st.Number(`SELECT count(*) FROM items WHERE path IN ('trove-test-old/-/other.tgz', 'trove-test-old') AND version_id IS NOT NULL`); got != 0 {
		t.Errorf("%d files that are no artifact got a version", got)
	}
	if got := e.st.Text(`SELECT kind || ' ' || status FROM vuln_finding WHERE package_name = 'trove-test-old' AND version = '1.0.0'`); got != "malicious needs_triage" {
		t.Errorf("the finding of the backfilled version: %q", got)
	}
	// Idempotent: a second full evaluation links nothing and changes nothing.
	e.st.Exec(`UPDATE vuln_source_state SET full_evaluation_requested_at = now() WHERE source = 'osv' AND ecosystem = 'npm'`)
	e.evaluate()
	if got := e.st.Number(`SELECT count(*) FROM package_versions pv JOIN packages p ON p.id = pv.package_id WHERE p.name = 'trove-test-old'`); got != 1 {
		t.Errorf("%d version rows after the second run, want 1", got)
	}
}
