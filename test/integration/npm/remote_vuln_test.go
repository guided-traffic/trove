//go:build integration

package npm

import (
	"net/http"
	"testing"
)

// R-VULN-001, R-VULN-011, R-VULN-012 (Q-VULN-35): the tarball a remote repository caches is recorded as a version of its package, the item
// linked to it as the lead item, and the version is evaluated in the fill's commit; removing the cached item removes the
// version row, which resolves the finding with version_removed.
func TestR_VULN_011_ARemoteFillRecordsTheVersionAndTheEvictionRemovesIt(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	p := upstreamPackage(t, 2)
	v := p.Versions[0].Version
	r.st.Exec(`UPDATE repositories SET config = jsonb_set(config, '{vulnerabilities}', '{"matchPublicAdvisories":"always"}') WHERE key = $1`, rRemote)
	r.st.Exec(`INSERT INTO vuln_advisory (ecosystem, id, canonical_id, modified, kind, summary, severity, severity_source, record_sha256)
		VALUES ('npm', 'GHSA-aaaa-aaaa-0001', 'GHSA-aaaa-aaaa-0001', now(), 'vulnerability', 'test', 'high', 'cvss', '\x00')`)
	r.st.Exec(`INSERT INTO vuln_affected (ecosystem, advisory_id, ord, name, ranges, versions, every_version, fixed)
		VALUES ('npm', 'GHSA-aaaa-aaaa-0001', 0, $1, '[{"type":"SEMVER","events":[{"introduced":"0"}]}]', '{}', true, '{}')`, p.Name)
	rep := r.start(1)[0]

	if a := r.tarball(rep, rRemote, p.Name, v, r.tokA); a.Status != 200 {
		t.Fatalf("fill: %d %s", a.Status, a.Body)
	}
	const rows = `SELECT count(*) FROM package_versions pv JOIN packages p ON p.id = pv.package_id JOIN repositories r ON r.id = p.repo_id
		JOIN items i ON i.version_id = pv.id AND i.id = pv.lead_item_id WHERE r.key = $1 AND p.name = $2 AND pv.version = $3`
	if got := r.st.Number(rows, rRemote, p.Name, v); got != 1 {
		t.Fatalf("%d version rows with the cached tarball as lead item, want 1", got)
	}
	if got := r.st.Number(`SELECT count(*) FROM vuln_finding WHERE package_name = $1 AND version = $2 AND status = 'needs_triage'`, p.Name, v); got != 1 {
		t.Fatalf("%d findings after the fill, want 1", got)
	}
	if a := r.tarball(rep, rRemote, p.Name, v, r.tokA); a.Status != 200 {
		t.Fatalf("hit: %d", a.Status)
	}
	if got := r.st.Number(rows, rRemote, p.Name, v); got != 1 {
		t.Errorf("a cache hit changed the version rows: %d", got)
	}

	path := "/files/" + rRemote + "/" + p.Name + "/-/" + tarballFile(p.Name, v)
	if a := r.st.Do(rep, http.MethodDelete, path, r.opsPAT, nil); a.Status != 204 {
		t.Fatalf("evict: %d %s", a.Status, a.Body)
	}
	if got := r.st.Number(`SELECT count(*) FROM package_versions pv JOIN packages p ON p.id = pv.package_id WHERE p.name = $1 AND pv.version = $2`, p.Name, v); got != 0 {
		t.Errorf("the evicted version keeps %d rows", got)
	}
	if got := r.st.Text(`SELECT status || ' ' || resolved_reason FROM vuln_finding WHERE package_name = $1 AND version = $2`, p.Name, v); got != "resolved version_removed" {
		t.Errorf("the finding after the eviction: %q", got)
	}
	id := r.st.Text(`SELECT id::text FROM vuln_finding WHERE package_name = $1`, p.Name)
	if a := r.tarball(rep, rRemote, p.Name, v, r.tokA); a.Status != 200 {
		t.Fatalf("refill: %d", a.Status)
	}
	if got := r.st.Text(`SELECT id::text || ' ' || status FROM vuln_finding WHERE package_name = $1`, p.Name); got != id+" needs_triage" {
		t.Errorf("after the refill: %q, want the same finding re-detected", got)
	}
}

func tarballFile(name, version string) string {
	base := name
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '/' {
			base = name[i+1:]
			break
		}
	}
	return base + "-" + version + ".tgz"
}
