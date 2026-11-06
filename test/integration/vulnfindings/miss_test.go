//go:build integration

package vulnfindings

import (
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/evaluate"
)

type fixedStates map[string]advisory.DataState

func (f fixedStates) State(eco string) advisory.DataState { return f[eco] }

// R-VULN-015 (the evaluation half, "Remote miss"): before an artifact is fetched the version is evaluated and the rule of the
// download applied, with nothing written: a malicious record blocks while blockMalicious is on, a finding that resolved with
// version_removed while blocked counts as blocked, a dismissed finding and an unrecognised upstream do not block, and the
// mode report returns the block with Enforced false.
func TestR_VULN_015_EvaluateMissAppliesTheRuleOfTheDownloadAndWritesNothing(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	complete := fixedStates{"npm": {Complete: true}}
	svc := e.service(evaluate.Options{States: complete})
	ctx := context.Background()
	miss := func(repo, name, version string) vuln.Block {
		t.Helper()
		b, err := svc.EvaluateMiss(ctx, vuln.Miss{Repo: qr(repo), Format: "npm", Name: name, Version: version})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	e.addAdvisory("npm", "MAL-2026-1234", "", "malicious", "critical")
	e.addAffected("npm", "MAL-2026-1234", 0, "trove-test-mal", everyVersionRange, nil, true, nil)
	e.addAdvisory("npm", "GHSA-aaaa-aaaa-0001", "", "vulnerability", "high")
	e.addAffected("npm", "GHSA-aaaa-aaaa-0001", 0, "trove-test-vuln", fixedRange("SEMVER", "1.0.1"), nil, false, nil)

	if b := miss("npm-remote", "trove-test-mal", "1.0.0"); b.Reason != vuln.BlockMalicious || b.AdvisoryID != "MAL-2026-1234" || !b.Enforced || !b.Refused() {
		t.Errorf("malicious record: %+v", b)
	}
	if b := miss("npm-remote", "trove-test-vuln", "1.0.0"); b.Active() {
		t.Errorf("a vulnerability without a triaged block: %+v", b)
	}
	if b := miss("npm-mirror", "trove-test-mal", "1.0.0"); b.Active() {
		t.Errorf("a remote with an unrecognised upstream is not evaluated: %+v", b)
	}
	if b := miss("npm-local", "trove-test-mal", "1.0.0"); b.Active() {
		t.Errorf("a local repository has no miss: %+v", b)
	}
	if got := e.st.Number(`SELECT count(*) FROM vuln_finding`); got != 0 {
		t.Fatalf("the miss wrote %d findings", got)
	}

	// A block set in triage survives the eviction: the finding resolved with version_removed while blocked.
	e.addVersion("npm-remote", "trove-test-vuln", "1.0.0")
	e.rescan("npm-remote")
	e.st.Exec(`UPDATE vuln_finding SET status = 'resolved', status_before_resolution = 'blocked', resolved_reason = 'version_removed', resolved_at = now()
		WHERE package_name = 'trove-test-vuln'`)
	findingID := e.st.Text(`SELECT id::text FROM vuln_finding WHERE package_name = 'trove-test-vuln'`)
	if b := miss("npm-remote", "trove-test-vuln", "1.0.0"); b.Reason != vuln.BlockTriage || b.FindingID != findingID || b.AdvisoryID != "GHSA-aaaa-aaaa-0001" {
		t.Errorf("a blocked finding that was evicted: %+v", b)
	}
	// A dismissed one does not block, a blocked one of another version is not this version's.
	e.st.Exec(`UPDATE vuln_finding SET status_before_resolution = 'dismissed' WHERE package_name = 'trove-test-vuln'`)
	if b := miss("npm-remote", "trove-test-vuln", "1.0.0"); b.Active() {
		t.Errorf("a dismissed finding blocks: %+v", b)
	}
	if got := e.st.Text(`SELECT status || ' ' || resolved_reason FROM vuln_finding WHERE package_name = 'trove-test-vuln'`); got != "resolved version_removed" {
		t.Errorf("the miss changed the finding: %q", got)
	}

	// Without data a blocked finding that resolved with version_removed still counts: the evaluation cannot say the advisory
	// stopped applying (R-VULN-011).
	e.st.Exec(`UPDATE vuln_finding SET status_before_resolution = 'blocked' WHERE package_name = 'trove-test-vuln'`)
	e.st.Exec(`DELETE FROM vuln_advisory WHERE id = 'GHSA-aaaa-aaaa-0001'`)
	noData := e.service(evaluate.Options{})
	if b, err := noData.EvaluateMiss(ctx, vuln.Miss{Repo: qr("npm-remote"), Format: "npm", Name: "trove-test-vuln", Version: "1.0.0"}); err != nil || b.Reason != vuln.BlockTriage {
		t.Errorf("without data: %+v, %v", b, err)
	}
	if b := miss("npm-remote", "trove-test-vuln", "1.0.0"); b.Active() {
		t.Errorf("with complete data that no longer names the version: %+v", b)
	}

	// The mode report returns the same condition without refusing.
	report := e.service(evaluate.Options{States: complete, Enforcement: vuln.EnforceReport})
	if b, err := report.EvaluateMiss(ctx, vuln.Miss{Repo: qr("npm-remote"), Format: "npm", Name: "trove-test-mal", Version: "1.0.0"}); err != nil || !b.Active() || b.Enforced || b.Refused() {
		t.Errorf("report mode: %+v, %v", b, err)
	}
}

// R-VULN-015: blockMalicious false turns the malicious block off in the repository, and onMissingData block refuses a version
// while the ecosystem's data was never imported completely.
func TestR_VULN_015_EvaluateMissReadsTheRepositoryPolicyAndTheDataState(t *testing.T) {
	e := newEnv(t)
	e.addAdvisory("npm", "MAL-2026-1234", "", "malicious", "critical")
	e.addAffected("npm", "MAL-2026-1234", 0, "trove-test-mal", everyVersionRange, nil, true, nil)
	ctx := context.Background()
	svc := e.service(evaluate.Options{States: fixedStates{"npm": {Complete: true}}})
	cold := e.service(evaluate.Options{})

	if a := e.patch("npm-remote", `{"vulnerabilities":{"blockMalicious":false}}`); a.Status != 200 {
		t.Fatalf("patch: %d %s", a.Status, a.Body)
	}
	waitSnapshot(t, e, func() bool {
		b, err := svc.EvaluateMiss(ctx, vuln.Miss{Repo: qr("npm-remote"), Format: "npm", Name: "trove-test-mal", Version: "1.0.0"})
		return err == nil && !b.Active()
	})

	if a := e.patch("npm-remote", `{"vulnerabilities":{"blockMalicious":true,"onMissingData":"block"}}`); a.Status != 200 {
		t.Fatalf("patch: %d %s", a.Status, a.Body)
	}
	waitSnapshot(t, e, func() bool {
		b, err := cold.EvaluateMiss(ctx, vuln.Miss{Repo: qr("npm-remote"), Format: "npm", Name: "left-pad", Version: "1.3.0"})
		return err == nil && b.Reason == vuln.BlockMissingData && b.Code() == "vulnerability_data_missing"
	})
	if b, err := svc.EvaluateMiss(ctx, vuln.Miss{Repo: qr("npm-remote"), Format: "npm", Name: "left-pad", Version: "1.3.0"}); err != nil || b.Active() {
		t.Errorf("with complete data: %+v, %v", b, err)
	}
	if b, err := svc.EvaluateMiss(ctx, vuln.Miss{Repo: qr("npm-remote"), Format: "npm", Name: "trove-test-mal", Version: "1.0.0"}); err != nil || b.Reason != vuln.BlockMalicious {
		t.Errorf("malicious again: %+v, %v", b, err)
	}
}
