//go:build integration

package vulnsync

import (
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/osvstub"
)

// R-VULN-006 acceptance: a record with "id": "../x", one with a details of 100 KiB and one with a javascript: reference: the
// first is rejected, the second stored cut to 16 KiB, the third stored without that reference.
func TestR_VULN_006_ARecordThatFailsARuleIsRejectedOrCut(t *testing.T) {
	// 300 filler records keep the share of the one rejected record below the 1 % of the integrity guard.
	e := newEnv(t, options{Filler: 300})
	for _, name := range []string{osvstub.ToggleInvalidID, osvstub.ToggleBigDetails, osvstub.ToggleJSReference} {
		if err := e.stub.Toggle(name, true); err != nil {
			t.Fatal(err)
		}
	}
	j := e.mustSucceed(taskOSV, nil)
	if !strings.Contains(j.Result, `"rejected":1`) {
		t.Errorf("result %s", j.Result)
	}
	if got := e.stateOf("rejected", "osv", "npm"); got != "1" {
		t.Errorf("rejected %s", got)
	}
	if got := e.number("SELECT count(*) FROM vuln_advisory WHERE ecosystem = 'npm' AND (id LIKE '%/%' OR id LIKE '%x')"); got != 0 {
		t.Errorf("the record with the identifier ../x was stored")
	}
	if got := e.number("SELECT octet_length(details) FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1", osvstub.IDBigDetails); got != 16<<10 {
		t.Errorf("details has %d bytes, want 16384", got)
	}
	if got := e.number(`SELECT count(*) FROM vuln_advisory, jsonb_array_elements("references") r
		WHERE ecosystem = 'npm' AND id = $1 AND r->>'url' LIKE 'javascript:%'`, osvstub.IDJSReference); got != 0 {
		t.Errorf("the javascript: reference was stored")
	}
	if got := e.number(`SELECT jsonb_array_length("references") FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1`, osvstub.IDJSReference); got < 1 {
		t.Errorf("the other references of the record were dropped")
	}
	// The record the source serves singly is refused the same way when the change file lists it.
	if got := e.number("SELECT count(*) FROM vuln_pending_evaluation WHERE ecosystem = 'npm' AND advisory_id = $1", osvstub.IDBigDetails); got != 1 {
		t.Errorf("a written record was not queued for the evaluation")
	}
}

// R-VULN-006 acceptance: GHSA-aaaa-bbbb-cccc aliasing CVE-2026-0001 and a second record CVE-2026-0001 of the same ecosystem are
// one vulnerability with the canonical identifier GHSA-aaaa-bbbb-cccc; a later record that names a member changes the group's
// canonical identifier, and the evaluation is queued for every identifier it concerns.
func TestR_VULN_006_AliasesFormOneGroupWithOneCanonicalIdentifier(t *testing.T) {
	e := newEnv(t, options{})
	e.mustSucceed(taskOSV, nil)
	for _, id := range []string{osvstub.IDAlias, osvstub.IDAliasCVE} {
		if got := e.text("SELECT canonical_id FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1", id); got != osvstub.IDAlias {
			t.Errorf("%s: canonical identifier %q, want %s", id, got, osvstub.IDAlias)
		}
	}
	if got := e.text("SELECT canonical_id FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1", osvstub.IDVuln); got != osvstub.IDVuln {
		t.Errorf("a record without aliases has the canonical identifier %q", got)
	}
	if got := e.text("SELECT array_to_string(aliases, ',') FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1", osvstub.IDAlias); got != osvstub.IDAliasCVE {
		t.Errorf("aliases %q", got)
	}

	// A malicious record that names the vulnerability takes over the group: MAL- before GHSA-.
	e.st.Exec("DELETE FROM vuln_pending_evaluation")
	mal := osvstub.Advisory{
		ID: "MAL-2026-9001", Aliases: []string{osvstub.IDVuln}, Summary: "malicious",
		Affected: []osvstub.Affected{{Package: osvstub.Package{Ecosystem: "npm", Name: osvstub.NPMVuln}, Versions: []string{"1.0.0"}}},
	}
	if _, err := e.stub.UpsertAdvisory("npm", mal); err != nil {
		t.Fatal(err)
	}
	e.mustSucceed(taskOSV, nil)
	for _, id := range []string{osvstub.IDVuln, "MAL-2026-9001"} {
		if got := e.text("SELECT canonical_id FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1", id); got != "MAL-2026-9001" {
			t.Errorf("%s: canonical identifier %q after the MAL- record arrived", id, got)
		}
	}
	// The group's severity is the canonical record's: malicious is critical.
	if got := e.text("SELECT severity || ' ' || severity_source FROM vuln_advisory WHERE ecosystem = 'npm' AND id = 'MAL-2026-9001'"); got != "critical malicious" {
		t.Errorf("severity of the canonical record %q", got)
	}
	// The evaluation re-keys the group's findings: both identifiers are queued.
	for _, id := range []string{osvstub.IDVuln, "MAL-2026-9001"} {
		if got := e.number("SELECT count(*) FROM vuln_pending_evaluation WHERE ecosystem = 'npm' AND advisory_id = $1", id); got != 1 {
			t.Errorf("%s was not queued for the evaluation", id)
		}
	}
}

// R-VULN-006: a record withdrawn by a later sync stays stored with its withdrawn time and is queued for the evaluation, which
// resolves its findings with the reason advisory_withdrawn (R-VULN-012).
func TestR_VULN_006_AWithdrawnRecordStaysStored(t *testing.T) {
	e := newEnv(t, options{})
	e.mustSucceed(taskOSV, nil)
	if got := e.text("SELECT COALESCE(withdrawn::text, '') FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1", osvstub.IDWithdrawn); got == "" {
		t.Errorf("the withdrawn record of the archive has no withdrawn time")
	}
	if got := e.text("SELECT COALESCE(withdrawn::text, '') FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1", osvstub.IDMal); got != "" {
		t.Fatalf("%s is withdrawn before the test withdraws it", osvstub.IDMal)
	}
	e.st.Exec("DELETE FROM vuln_pending_evaluation")
	if err := e.stub.Withdraw("npm", osvstub.IDMal); err != nil {
		t.Fatal(err)
	}
	e.mustSucceed(taskOSV, nil)
	if got := e.text("SELECT COALESCE(withdrawn::text, '') FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1", osvstub.IDMal); got == "" {
		t.Errorf("the withdrawal was not applied")
	}
	if got := e.number("SELECT count(*) FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1", osvstub.IDMal); got != 1 {
		t.Errorf("the withdrawn record was deleted")
	}
	if got := e.number("SELECT count(*) FROM vuln_pending_evaluation WHERE ecosystem = 'npm' AND advisory_id = $1", osvstub.IDMal); got != 1 {
		t.Errorf("the withdrawal was not queued for the evaluation")
	}
}

// R-VULN-006: the affected entries as the matcher reads them: ranges as published, versions and fixed versions, the
// every-version flag, kinds and the ranges the format cannot evaluate.
func TestR_VULN_006_AffectedEntriesAreStoredForTheMatcher(t *testing.T) {
	e := newEnv(t, options{})
	e.mustSucceed(taskOSV, nil)
	if got := e.text("SELECT ranges::text || ' ' || array_to_string(fixed, ',') FROM vuln_affected WHERE ecosystem = 'npm' AND advisory_id = $1 AND name = $2", osvstub.IDVuln, osvstub.NPMVuln); !strings.Contains(got, `"introduced": "0"`) || !strings.Contains(got, `"fixed": "1.0.1"`) || !strings.HasSuffix(got, " 1.0.1") {
		t.Errorf("trove-test-vuln: %s", got)
	}
	if got := e.text("SELECT ranges::text FROM vuln_affected WHERE ecosystem = 'npm' AND advisory_id = $1", osvstub.IDLeftPad); !strings.Contains(got, `"last_affected": "1.3.0"`) {
		t.Errorf("left-pad: %s", got)
	}
	if got := e.text("SELECT array_to_string(versions, ',') FROM vuln_affected WHERE ecosystem = 'npm' AND advisory_id = $1", osvstub.IDList); got != "1.0.0,1.2.0" {
		t.Errorf("versions[] of trove-test-list: %q", got)
	}
	if got := e.text("SELECT array_to_string(versions, ',') || ' ' || every_version::text FROM vuln_affected WHERE ecosystem = 'npm' AND advisory_id = $1 AND name = $2", osvstub.IDMal, osvstub.NPMMal); got != "1.0.0 false" {
		t.Errorf("versions[] of the malicious record: %q", got)
	}
	if got := e.text("SELECT kind FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1", osvstub.IDMal); got != "malicious" {
		t.Errorf("kind %q", got)
	}
	if got := e.text("SELECT kind FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1", osvstub.IDVuln); got != "vulnerability" {
		t.Errorf("kind %q", got)
	}
	// The NuGet names are normalized by the format's adapter.
	if got := e.number("SELECT count(*) FROM vuln_affected WHERE ecosystem = 'NuGet' AND name = 'newtonsoft.json'"); got != 1 {
		t.Errorf("the NuGet name was not normalized: %d entries", got)
	}
	// A range with a fixed event the format cannot parse is counted, stored, and the record's versions[] still count.
	if got := e.stateOf("unevaluable", "osv", "npm"); got != "1" {
		t.Errorf("unevaluable %s, want 1 (trove-test-badrange)", got)
	}
	if got := e.text("SELECT ranges::text FROM vuln_affected WHERE ecosystem = 'npm' AND advisory_id = $1", osvstub.IDBadRange); !strings.Contains(got, "not-a-version") {
		t.Errorf("badrange: %s", got)
	}
}

// R-VULN-007 acceptance: a record with a CVSS 4.0 vector of score 9.3 and a CVSS 3.1 vector of score 7.5 has severity critical,
// score 9.3, version 4.0; a record with only database_specific.severity MODERATE has medium from advisory; a record with
// neither has unknown. A malicious record is critical without a score. An alias with a vector decides for a canonical
// record without one.
func TestR_VULN_007_SeverityIsDecidedOncePerCanonicalIdentifier(t *testing.T) {
	e := newEnv(t, options{})
	e.mustSucceed(taskOSV, nil)
	for _, c := range []struct{ eco, id, want string }{
		{"npm", osvstub.IDCVSS, "critical cvss 4.0 9.3"},
		{"npm", osvstub.IDVuln, "high cvss 3.1 7.5"},
		{"npm", osvstub.IDQualitative, "medium advisory  "},
		{"npm", osvstub.IDNoSeverity, "unknown none  "},
		{"npm", osvstub.IDMal, "critical malicious  "},
		{"NuGet", osvstub.IDNuGetHigh, "high cvss 3.1 7.5"},
		{"NuGet", osvstub.IDNuGetMedium, "medium cvss 3.1 5.3"},
		{"NuGet", osvstub.IDNuGetMal, "critical malicious  "},
	} {
		got := e.text(`SELECT severity || ' ' || severity_source || ' ' || COALESCE(cvss_version, '') || ' ' || COALESCE(cvss_score::text, '')
			FROM vuln_advisory WHERE ecosystem = $1 AND id = $2`, c.eco, c.id)
		if got != c.want {
			t.Errorf("%s %s: %q, want %q", c.eco, c.id, got, c.want)
		}
	}
	if got := e.text("SELECT cvss_vector FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1", osvstub.IDCVSS); !strings.HasPrefix(got, "CVSS:4.0/") {
		t.Errorf("vector %q", got)
	}

	// Rule 3: a canonical record without a vector takes the highest level of its aliases' vectors.
	e.st.Exec("DELETE FROM vuln_pending_evaluation")
	canonical := osvstub.Advisory{
		ID: "GHSA-aaaa-zzzz-0001", Aliases: []string{"CVE-2026-7001", "CVE-2026-7002"},
		DatabaseSpecific: map[string]any{"severity": "LOW"},
		Affected:         []osvstub.Affected{{Package: osvstub.Package{Ecosystem: "npm", Name: "trove-test-rule3"}, Versions: []string{"1.0.0"}}},
	}
	lowAlias := osvstub.Advisory{
		ID: "CVE-2026-7001", Aliases: []string{"GHSA-aaaa-zzzz-0001"},
		Severity: []osvstub.Severity{{Type: "CVSS_V3", Score: "CVSS:3.1/AV:N/AC:H/PR:N/UI:R/S:U/C:L/I:N/A:N"}},
	}
	highAlias := osvstub.Advisory{
		ID: "CVE-2026-7002", Aliases: []string{"GHSA-aaaa-zzzz-0001"},
		Severity: []osvstub.Severity{{Type: "CVSS_V3", Score: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"}},
	}
	for _, a := range []osvstub.Advisory{canonical, lowAlias, highAlias} {
		if _, err := e.stub.UpsertAdvisory("npm", a); err != nil {
			t.Fatal(err)
		}
	}
	e.mustSucceed(taskOSV, nil)
	if got := e.text("SELECT canonical_id FROM vuln_advisory WHERE ecosystem = 'npm' AND id = 'CVE-2026-7002'"); got != "GHSA-aaaa-zzzz-0001" {
		t.Fatalf("canonical identifier %q", got)
	}
	got := e.text(`SELECT severity || ' ' || severity_source || ' ' || cvss_version || ' ' || cvss_score::text FROM vuln_advisory
		WHERE ecosystem = 'npm' AND id = 'GHSA-aaaa-zzzz-0001'`)
	if got != "critical cvss 3.1 9.8" {
		t.Errorf("severity of the canonical record %q, want the highest alias vector", got)
	}
	// The alias records keep their own decisions.
	if got := e.text("SELECT severity FROM vuln_advisory WHERE ecosystem = 'npm' AND id = 'CVE-2026-7001'"); got != "low" {
		t.Errorf("the low alias is %q", got)
	}
}

// R-VULN-008 acceptance: after the test source adds CVE-2026-0001 to its catalog, the next run of vuln-sync-kev sets the
// known-exploited flag of GHSA-aaaa-bbbb-cccc (alias CVE-2026-0001) and its date, and queues the advisory for the evaluation,
// which carries the flag to the findings; taking the entry out of the catalog clears it.
func TestR_VULN_008_TheCatalogFlagsTheAdvisoriesThatNameAListedIdentifier(t *testing.T) {
	e := newEnv(t, options{})
	e.mustSucceed(taskOSV, nil)
	e.st.Exec("DELETE FROM vuln_pending_evaluation")
	flag := func(id string) string {
		return e.text("SELECT known_exploited::text || ' ' || COALESCE(kev_date_added::text, '') FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1", id)
	}
	if got := flag(osvstub.IDAlias); got != "false " {
		t.Fatalf("flag before the catalog lists it: %q", got)
	}
	if err := e.stub.AddKEV(osvstub.KEVEntry{CVEID: osvstub.IDAliasCVE, DateAdded: "2026-10-30"}); err != nil {
		t.Fatal(err)
	}
	e.stub.ResetCounts()
	e.mustSucceed(taskKEV, nil)
	if got := flag(osvstub.IDAlias); got != "true 2026-10-30" {
		t.Errorf("flag after the catalog lists it: %q", got)
	}
	if got := flag(osvstub.IDVuln); got != "false " {
		t.Errorf("an advisory the catalog does not name: %q", got)
	}
	if got := e.number("SELECT count(*) FROM vuln_kev WHERE cve_id = $1", osvstub.IDAliasCVE); got != 1 {
		t.Errorf("vuln_kev has %d rows for the listed identifier", got)
	}
	if got := e.number("SELECT count(*) FROM vuln_pending_evaluation WHERE ecosystem = 'npm' AND advisory_id = $1", osvstub.IDAlias); got != 1 {
		t.Errorf("the advisory whose flag changed was not queued for the evaluation")
	}
	if got := e.number("SELECT count(*) FROM vuln_pending_evaluation WHERE advisory_id <> $1", osvstub.IDAlias); got != 0 {
		t.Errorf("%d advisories whose flag did not change were queued", got)
	}
	if got := e.stateOf("state", "kev", ""); got != "complete" {
		t.Errorf("kev state %q", got)
	}
	if got := e.stub.Counts().Kinds["kev"]; got != 1 || e.stub.Counts().Total != 1 {
		t.Errorf("the catalog was fetched %d times, %d requests in all\n%s", got, e.stub.Counts().Total, e.requestLog())
	}

	// An unchanged catalog is a 304.
	e.stub.ResetCounts()
	j := e.mustSucceed(taskKEV, nil)
	if !strings.Contains(j.Result, `"outcome":"unchanged"`) || e.stub.Counts().Path(kevPathName).NotModified != 1 {
		t.Errorf("an unchanged catalog: %s\n%s", j.Result, e.requestLog())
	}

	e.st.Exec("DELETE FROM vuln_pending_evaluation")
	if err := e.stub.RemoveKEV(osvstub.IDAliasCVE); err != nil {
		t.Fatal(err)
	}
	e.mustSucceed(taskKEV, nil)
	if got := flag(osvstub.IDAlias); got != "false " {
		t.Errorf("flag after the catalog dropped the entry: %q", got)
	}
}

// R-VULN-008: an advisory that arrives after the catalog was read carries the flag from its first write, and a catalog that
// is not a valid document fails the run and keeps the catalog as it was.
func TestR_VULN_008_AnAdvisoryIsFlaggedWhenItIsWritten(t *testing.T) {
	e := newEnv(t, options{})
	if err := e.stub.AddKEV(osvstub.KEVEntry{CVEID: osvstub.IDAliasCVE, DateAdded: "2026-10-30"}); err != nil {
		t.Fatal(err)
	}
	e.mustSucceed(taskKEV, nil)
	e.mustSucceed(taskOSV, nil)
	got := e.text("SELECT known_exploited::text || ' ' || kev_date_added::text FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1", osvstub.IDAlias)
	if got != "true 2026-10-30" {
		t.Errorf("flag of an advisory written after the catalog: %q", got)
	}
	rows := e.number("SELECT count(*) FROM vuln_kev")
	if rows < 1 {
		t.Fatalf("the catalog is empty")
	}
	if err := e.stub.SetFault("kev", osvstub.Fault{Error: 500}); err != nil {
		t.Fatal(err)
	}
	if code := e.mustFail(taskKEV, "kev", "", nil); code != "upstream_unexpected_status" {
		t.Errorf("code %q", code)
	}
	if got := e.number("SELECT count(*) FROM vuln_kev"); got != rows {
		t.Errorf("a failed run changed the catalog: %d rows, were %d", got, rows)
	}
	if got := e.stateOf("failures", "kev", ""); got != "1" {
		t.Errorf("failures %s", got)
	}
}

// R-VULN-011 (the sync's part): a matcher version of an ecosystem lower than the constant of the build requests the full
// evaluation of that ecosystem and stores the constant, also when the run finds nothing to apply.
func TestR_VULN_011_ARaisedMatcherVersionRequestsTheFullEvaluation(t *testing.T) {
	e := newEnv(t, options{})
	e.mustSucceed(taskOSV, nil)
	e.st.Exec("UPDATE vuln_source_state SET matcher_version = 0, full_evaluation_requested_at = NULL WHERE source = 'osv'")
	e.mustSucceed(taskOSV, nil)
	for _, eco := range []string{"npm", "NuGet"} {
		if got := e.stateOf("matcher_version", "osv", eco); got != "1" {
			t.Errorf("%s: matcher_version %s", eco, got)
		}
		if got := e.stateOf("full_evaluation_requested_at", "osv", eco); got == "" {
			t.Errorf("%s: the full evaluation was not requested", eco)
		}
	}
	e.st.Exec("UPDATE vuln_source_state SET full_evaluation_requested_at = NULL WHERE source = 'osv'")
	e.mustSucceed(taskOSV, nil)
	if got := e.stateOf("full_evaluation_requested_at", "osv", "npm"); got != "" {
		t.Errorf("a run at the current matcher version requested the full evaluation again")
	}
}

// R-VULN-027: the replica's view of the state follows the change area vuln_data: a web replica sees an ecosystem complete,
// with the epoch of the run, once the worker's run committed, and a changed run raises the epoch it sees.
func TestR_VULN_027_AWebReplicaFollowsTheStateOfTheWorker(t *testing.T) {
	e := newEnv(t, options{})
	states := e.web.App.VulnSync().States()
	if s := states.State("npm"); s.Complete || s.Epoch != 0 || !s.SyncedAt.IsZero() {
		t.Fatalf("state of an ecosystem never imported: %+v", s)
	}
	e.mustSucceed(taskOSV, nil)
	within(t, "the web replica seeing npm complete", 20*time.Second, func() bool { return states.State("npm").Complete })
	s := states.State("npm")
	if s.Epoch != 1 || s.Stale || s.SyncedAt.IsZero() || time.Since(s.SyncedAt) > time.Minute {
		t.Errorf("state %+v", s)
	}
	if !states.State("NuGet").Complete {
		t.Errorf("NuGet state %+v", states.State("NuGet"))
	}
	e.touch("npm", osvstub.IDVuln)
	e.mustSucceed(taskOSV, nil)
	within(t, "the web replica seeing the raised epoch", 20*time.Second, func() bool { return states.State("npm").Epoch == 2 })
	if got := states.State("NuGet").Epoch; got != 1 {
		t.Errorf("the epoch of NuGet is %d, which no run changed", got)
	}
	if got := states.State("PyPI"); got.Complete || got.Epoch != 0 {
		t.Errorf("an ecosystem the source does not serve: %+v", got)
	}
}
