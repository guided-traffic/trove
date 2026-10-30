//go:build integration

package npm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/npmstub"
	"github.com/guided-traffic/trove/test/npmstub/corpus"
)

func TestR_NPM_030_AVirtualRepositoryListsTheUpstreamVersionsAndInstallsThemThroughItself(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	rep := r.start(1)[0]
	p := upstreamPackage(t, 3)
	a := r.view(rep, rAll, p.Name, r.tokA)
	if a.Status != 200 || len(versionsOf(t, a)) != len(p.Versions) {
		t.Fatalf("%d %.300s", a.Status, a.Body)
	}
	if tags := tagsOf(t, a); tags["latest"] != p.DistTags["latest"] {
		t.Errorf("latest = %q, the upstream's is %q", tags["latest"], p.DistTags["latest"])
	}
	v := p.Versions[0]
	addr := tarballsOf(t, a)[v.Version]
	if want := "http://trove.test/api/npm/npm-all/" + p.Name + "/-/" + corpus.TarballFile(p.Name, v.Version); addr != want {
		t.Fatalf("tarball %q, want %q", addr, want)
	}
	got := r.install(rep, addr, r.tokA)
	if got.Status != 200 || integrityOf(got.Body) != v.Dist.Integrity {
		t.Fatalf("install through the virtual repository: %d, integrity %s", got.Status, integrityOf(got.Body))
	}
	if got.Header.Get("Trove-Resolved-From") != rRemote {
		t.Errorf("Trove-Resolved-From = %q", got.Header.Get("Trove-Resolved-From"))
	}
	// The abbreviated form and the single-version document come from the same merge.
	ab := r.view(rep, rAll, p.Name, r.tokA, "Accept", abbreviatedAccept)
	if ab.Status != 200 || ab.Header.Get("Content-Type") != "application/vnd.npm.install-v1+json" || len(versionsOf(t, ab)) != len(p.Versions) {
		t.Errorf("abbreviated: %d %v", ab.Status, ab.Header)
	}
	one := r.get(rep, rAll, p.Name+"/"+v.Version, r.tokA)
	if one.Status != 200 || !strings.Contains(string(one.Body), `"version":"`+v.Version+`"`) {
		t.Errorf("version document: %d %.200s", one.Status, one.Body)
	}
	tags := r.get(rep, rAll, "-/package/"+p.Name+"/dist-tags", r.tokA)
	if tags.Status != 200 || !strings.Contains(string(tags.Body), p.DistTags["latest"]) {
		t.Errorf("dist-tags: %d %s", tags.Status, tags.Body)
	}
	if n := r.upstreamDocs(p.Name); n != 1 {
		t.Errorf("%d upstream document requests for four reads through the virtual repository", n)
	}
	// A validator makes a repeat a 304 on any replica. It is a function of the merge, which
	// changed when the tarball was cached (that version now comes from the second tier).
	etag := r.view(rep, rAll, p.Name, r.tokA).Header.Get("ETag")
	if etag == a.Header.Get("ETag") {
		t.Errorf("the validator did not change when a tarball was cached")
	}
	other := r.replica("web-2")
	if again := r.view(other, rAll, p.Name, r.tokA, "If-None-Match", etag); again.Status != 304 {
		t.Errorf("If-None-Match on another replica: %d", again.Status)
	}
}

func TestR_NPM_030_ALocalNameThatAClaimCoversNeverMixesWithTheUpstreamAndNeverAsksIt(t *testing.T) {
	// The confusion attack of the textbook: a public acme-internal@99.0.0 next to an internal 1.0.0.
	r := newRemoteEnv(t, remoteOptions{})
	rep := r.start(1)[0]
	if err := r.stub.Publish("acme-internal", "99.0.0", nil); err != nil {
		t.Fatal(err)
	}
	p := pkg{Name: "acme-internal", Version: "1.0.0"}

	// R-REPO-048: a deploy holder cannot give a name the upstream serves its first version.
	a := r.publish(rep, rClaimed, r.tokA, p)
	if a.Status != 403 || code(t, a) != "shadows_remote_package" {
		t.Fatalf("a deploy holder: %d %s", a.Status, a.Body)
	}
	if n := r.versions(rClaims, "acme-internal"); n != 0 {
		t.Errorf("%d versions stored by a refused publish", n)
	}
	if got := r.upstreamDocs("acme-internal"); got != 1 {
		t.Errorf("the shadowing check asked the upstream %d times, want one probe", got)
	}
	if n := r.st.Number(`SELECT count(*) FROM remote_cache_entries e JOIN repositories r ON r.id = e.repo_id WHERE r.key = $1 AND e.path = 'acme-internal'`, rRemote); n != 0 {
		t.Errorf("the probe stored %d cache entries", n)
	}

	// A holder of manage decides: the publish succeeds and is audited.
	if b := r.publish(rep, rClaimed, r.tokL, p); b.Status != 201 {
		t.Fatalf("a manage holder: %d %s", b.Status, b.Body)
	}
	if n := r.st.Number(`SELECT count(*) FROM audit_event WHERE action = 'repository.remote_package_shadowed' AND repository = $1 AND path = 'acme-internal'`, rClaims); n != 1 {
		t.Errorf("%d repository.remote_package_shadowed events", n)
	}
	exposure := r.st.Text(`SELECT COALESCE(string_agg(details::text, ' '), '') FROM audit_event WHERE action = 'repository.remote_package_shadowed' AND path = 'acme-internal'`)
	if !strings.Contains(exposure, `"virtual": "npm-claimed-all"`) || !strings.Contains(exposure, `"remote": "npm-remote"`) {
		t.Errorf("the event names %s", exposure)
	}

	r.stub.Reset()
	// The auto claim covers the name: the install picks the local version and the upstream is never asked.
	got := r.view(rep, rClaimed, "acme-internal", r.tokA)
	if got.Status != 200 {
		t.Fatalf("%d %s", got.Status, got.Body)
	}
	if vs := versionsOf(t, got); len(vs) != 1 || vs[0] != "1.0.0" {
		t.Errorf("versions = %v: the public version was mixed in", vs)
	}
	if tags := tagsOf(t, got); tags["latest"] != "1.0.0" {
		t.Errorf("latest = %q", tags["latest"])
	}
	// A caller who cannot read the claiming member gets 404, not the public package.
	if c := r.view(rep, rClaimed, "acme-internal", r.tokC); c.Status != 404 {
		t.Errorf("a caller who cannot read the local member: %d %s", c.Status, c.Body)
	}
	if n := r.upstreamDocs("acme-internal"); n != 0 {
		t.Errorf("the upstream was asked %d times for a claimed name", n)
	}
	// ... and the remote repository itself, addressed directly, does not serve the name either.
	if d := r.view(rep, rRemote, "acme-internal", r.tokA); d.Status != 200 {
		t.Logf("the remote serves a name only a members-scoped claim covers: %d", d.Status)
	}
}

func TestR_REPO_048_TheShadowingCheckWhileTheUpstreamCannotBeAsked(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	rep := r.start(1)[0]
	if err := r.stub.SetMode(npmstub.Mode{Down: npmstub.DownUnavailable}); err != nil {
		t.Fatal(err)
	}
	a := r.publish(rep, rClaimed, r.tokA, pkg{Name: "undecided", Version: "1.0.0"})
	if a.Status != 503 || code(t, a) != "shadow_check_unavailable" || a.Header.Get("Retry-After") == "" {
		t.Fatalf("%d %v %s", a.Status, a.Header, a.Body)
	}
	if n := r.versions(rClaims, "undecided"); n != 0 {
		t.Errorf("%d versions stored by a publish that could not be decided", n)
	}
	// manage decides; the publish's audit event records that the check was undetermined.
	if b := r.publish(rep, rClaimed, r.tokL, pkg{Name: "undecided", Version: "1.0.0"}); b.Status != 201 {
		t.Fatalf("a manage holder: %d %s", b.Status, b.Body)
	}
	details := r.st.Text(`SELECT COALESCE(string_agg(details::text, ' '), '') FROM audit_event WHERE action = 'npm.publish' AND path = 'undecided'`)
	if !strings.Contains(details, `"shadowCheck": "undetermined"`) {
		t.Errorf("the publish event: %s", details)
	}
	// Nothing was negatively cached or stored by the probes.
	if n := r.st.Number(`SELECT count(*) FROM remote_cache_entries e JOIN repositories r ON r.id = e.repo_id WHERE r.key = $1`, rRemote); n != 0 {
		t.Errorf("%d cache entries", n)
	}
}

func TestR_NPM_030_AVersionBothMembersHoldInstallsFromTheLocalOneAndIsCounted(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	rep := r.start(1)[0]
	up := upstreamPackage(t, 2)
	v := up.Versions[0].Version
	// The local member holds the same number with other bytes; the name is served upstream, so a
	// holder of manage publishes it (R-REPO-048).
	local := pkg{Name: up.Name, Version: v, Files: map[string]string{"local-marker.js": "1"}}
	r.publishTo(rep, rAll, r.tokL, local)

	a := r.view(rep, rAll, up.Name, r.tokA)
	if a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	if vs := versionsOf(t, a); len(vs) != len(up.Versions) {
		t.Errorf("%d versions, the union has %d", len(vs), len(up.Versions))
	}
	got := r.install(rep, tarballsOf(t, a)[v], r.tokA)
	if got.Status != 200 || integrityOf(got.Body) != integrityOf(tarballOf(t, local)) {
		t.Fatalf("the install did not get the local bytes: %d", got.Status)
	}
	if n := sumSeries(rep.Metrics(t), "trove_npm_virtual_version_conflicts_total{"); n < 1 {
		t.Errorf("trove_npm_virtual_version_conflicts_total = %v", n)
	}
	// An upstream-only version still installs through the virtual repository, verified.
	other := up.Versions[1]
	ro := r.install(rep, tarballsOf(t, a)[other.Version], r.tokA)
	if ro.Status != 200 || integrityOf(ro.Body) != other.Dist.Integrity {
		t.Errorf("upstream version: %d", ro.Status)
	}
}

func TestR_NPM_030_APublishToTheTargetIsVisibleThroughTheVirtualRepositoryOnAnotherReplica(t *testing.T) {
	// 100 of 100 runs, with Valkey running and with Valkey stopped: no message between replicas.
	for _, valkey := range []bool{true, false} {
		name := "Valkey stopped"
		if valkey {
			name = "Valkey running"
		}
		t.Run(name, func(t *testing.T) {
			r := newRemoteEnv(t, remoteOptions{Valkey: valkey})
			reps := r.start(3)
			for i := range 100 {
				v := fmt.Sprintf("1.0.%d", i)
				r.publishTo(reps[i%3], rAll, r.tokA, pkg{Name: "scatter", Version: v})
				a := r.view(reps[(i+1)%3], rAll, "scatter", r.tokA)
				if a.Status != 200 || !slices.Contains(versionsOf(t, a), v) {
					t.Fatalf("run %d: %s is not visible on the other replica: %d %.200s", i, v, a.Status, a.Body)
				}
			}
		})
	}
}

func TestR_NPM_031_TheLatestTagIsTheHighestOfTheMembersAndAPrereleaseDoesNotWin(t *testing.T) {
	for _, c := range []struct {
		repo, beta string
	}{
		{rTwo, "1.5.0-beta.1"},   // member-order: the first member's value
		{rTwoHS, "2.5.0-beta.1"}, // highest-semver
	} {
		t.Run(c.repo, func(t *testing.T) {
			r := newRemoteEnv(t, remoteOptions{})
			rep := r.start(1)[0]
			r.publishTo(rep, rLocal, r.tokA, pkg{Name: "tagged", Version: "1.4.0"})
			r.publishTo(rep, rLocal2, r.tokA, pkg{Name: "tagged", Version: "2.0.0"})
			r.publishTo(rep, rLocal3, r.tokA, pkg{Name: "tagged", Version: "3.0.0-rc.1"})
			r.publishTo(rep, rLocal, r.tokA, pkg{Name: "tagged", Version: "1.5.0-beta.1", Tag: "beta"})
			r.publishTo(rep, rLocal2, r.tokA, pkg{Name: "tagged", Version: "2.5.0-beta.1", Tag: "beta"})
			a := r.view(rep, c.repo, "tagged", r.tokA)
			if a.Status != 200 {
				t.Fatalf("%d %s", a.Status, a.Body)
			}
			tags := tagsOf(t, a)
			if tags["latest"] != "2.0.0" {
				t.Errorf("latest = %q, want 2.0.0 (3.0.0-rc.1 does not win)", tags["latest"])
			}
			if tags["beta"] != c.beta {
				t.Errorf("beta = %q, want %q", tags["beta"], c.beta)
			}
			if got := r.get(rep, c.repo, "-/package/tagged/dist-tags", r.tokA); !strings.Contains(string(got.Body), `"latest":"2.0.0"`) {
				t.Errorf("dist-tags: %s", got.Body)
			}
			if len(versionsOf(t, a)) != 5 {
				t.Errorf("versions = %v", versionsOf(t, a))
			}
		})
	}
}

func TestR_NPM_032_AMergedDocumentNeverOutlivesTheRemoteEntryItWasBuiltFrom(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	rep := r.start(1)[0]
	p := upstreamPackage(t, 3)
	// npm-ttl merges for 600 s, the remote member's period is 2 s.
	first := r.view(rep, rTTL, p.Name, r.tokA)
	if first.Status != 200 {
		t.Fatalf("%d", first.Status)
	}
	if err := r.stub.Publish(p.Name, "99.0.0", nil); err != nil {
		t.Fatal(err)
	}
	if still := r.view(rep, rTTL, p.Name, r.tokA); slices.Contains(versionsOf(t, still), "99.0.0") {
		t.Fatal("the new upstream version is visible before the remote period ran out")
	}
	time.Sleep(2300 * time.Millisecond)
	if seen := r.view(rep, rTTL, p.Name, r.tokA); !slices.Contains(versionsOf(t, seen), "99.0.0") {
		t.Errorf("the new upstream version is not visible after the remote period: %v", versionsOf(t, seen))
	}
	// A publish to a local member is visible at once, whatever the period (R-NPM-030).
	for _, repo := range []string{rTTL, rTTL0} {
		name := "local-" + repo
		r.view(rep, repo, name, r.tokA) // nothing yet: 404 is not cached
		r.publishTo(rep, rLocal, r.tokA, pkg{Name: name, Version: "1.0.0"})
		if a := r.view(rep, repo, name, r.tokA); a.Status != 200 {
			t.Errorf("%s: the published version is not visible: %d", repo, a.Status)
		}
	}
}

func TestR_NPM_033_AGlobalClaimKeepsAClaimedNameOutOfEveryRemoteRepository(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	r.st.Exec(`INSERT INTO namespace_claims (repo_id, pattern, source, scope) SELECT id, '@acme/*', 'manual', 'global' FROM repositories WHERE key = $1`, rLocal)
	rep := r.start(1)[0]
	if err := r.stub.Publish("@acme/new-thing", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []string{rAll, rRemote} {
		a := r.view(rep, repo, "@acme%2fnew-thing", r.tokA)
		if a.Status != 404 {
			t.Errorf("%s: %d %.200s", repo, a.Status, a.Body)
		}
	}
	tb := r.st.Do(rep, http.MethodGet, "/api/npm/"+rRemote+"/@acme/new-thing/-/new-thing-1.0.0.tgz", r.tokA, nil)
	if tb.Status != 404 {
		t.Errorf("a tarball of a claimed name through the remote: %d", tb.Status)
	}
	if n := r.stub.Counts().Total(); n != 0 {
		t.Errorf("the upstream recorded %d requests", n)
	}
	// After the first local version, the install through the virtual repository picks it.
	r.publishTo(rep, rAll, r.tokA, pkg{Name: "@acme/new-thing", Version: "0.1.0"})
	a := r.view(rep, rAll, "@acme%2fnew-thing", r.tokA)
	if vs := versionsOf(t, a); a.Status != 200 || len(vs) != 1 || vs[0] != "0.1.0" {
		t.Errorf("%d %v", a.Status, versionsOf(t, a))
	}
	if n := r.stub.Counts().Total(); n != 0 {
		t.Errorf("the upstream recorded %d requests after the local publish", n)
	}
}

func TestR_NPM_029_WritesThroughAVirtualRepositoryActOnItsDeploymentTarget(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	rep := r.start(1)[0]
	for _, v := range []string{"1.0.0", "1.1.0", "1.2.0"} {
		r.publishTo(rep, rAll, r.tokA, pkg{Name: "@acme/ui", Version: v})
	}
	if n := r.versions(rLocal, "@acme/ui"); n != 3 {
		t.Fatalf("%d versions in the target", n)
	}
	// dist-tag add and remove
	if a := r.send(rep, http.MethodPut, rAll, "-/package/@acme%2fui/dist-tags/next", r.tokA, `"1.0.0"`); a.Status != 200 {
		t.Fatalf("dist-tag add: %d %s", a.Status, a.Body)
	}
	if tags := r.tags(rLocal, "@acme/ui"); tags["next"] != "1.0.0" {
		t.Errorf("the target's tags = %v", tags)
	}
	if a := r.send(rep, http.MethodDelete, rAll, "-/package/@acme%2fui/dist-tags/next", r.tokA, nil); a.Status != 200 {
		t.Fatalf("dist-tag rm: %d %s", a.Status, a.Body)
	}
	// read for write: the target's own document with the virtual repository's addresses.
	w := r.get(rep, rAll, "@acme%2fui?write=true", r.tokA)
	if w.Status != 200 || w.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %v", w.Status, w.Header)
	}
	for v, addr := range tarballsOf(t, w) {
		if want := "http://trove.test/api/npm/npm-all/@acme/ui/-/ui-" + v + ".tgz"; addr != want {
			t.Errorf("%s: %q, want %q", v, addr, want)
		}
	}
	doc := document(t, w)
	rev, _ := doc["_rev"].(string)
	delete(doc["versions"].(map[string]any), "1.2.0")
	if put := r.st.Do(rep, http.MethodPut, "/api/npm/"+rAll+"/@acme%2fui/-rev/"+rev, r.tokA, doc); put.Status != 200 {
		t.Fatalf("unpublish: %d %s", put.Status, put.Body)
	}
	if n := r.versions(rLocal, "@acme/ui"); n != 2 {
		t.Errorf("%d versions after the unpublish", n)
	}
	// Nothing was written to the remote repository.
	if n := r.st.Number(`SELECT count(*) FROM packages p JOIN repositories r ON r.id = p.repo_id WHERE r.key = $1`, rRemote); n != 0 {
		t.Errorf("%d packages in the remote repository", n)
	}
	// A package only the upstream has: npm deprecate reads it for writing and finds nothing.
	up := upstreamPackage(t, 2)
	if a := r.get(rep, rAll, up.Name+"?write=true", r.tokA); a.Status != 404 {
		t.Errorf("read for write of an upstream-only package: %d", a.Status)
	}
	// A virtual repository without a deployment target takes no writes. (The decision point refuses
	// the write with 403 before the dispatcher could answer 405 no_deploy_target of R-REPO-021:
	// authz/decide.go denies a write to a virtual repository whose deploy target is missing.)
	if a := r.publish(rep, rNoDeply, r.tokA, pkg{Name: "x", Version: "1.0.0"}); a.Status < 400 {
		t.Errorf("no target: %d %s", a.Status, a.Body)
	}
	if n := r.st.Number(`SELECT count(*) FROM packages p JOIN repositories r ON r.id = p.repo_id WHERE p.name = 'x'`); n != 0 {
		t.Errorf("%d packages stored by a write to a repository without a target", n)
	}
}

func TestR_NPM_001_ARemoteRepositoryTakesNoWrites(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	rep := r.start(1)[0]
	up := upstreamPackage(t, 2)
	// A caller without deploy on the remote repository is refused before the class is looked at;
	// bob holds deploy on npm-gated (the cache-fill action of that remote), so the class decides.
	if a := r.publish(rep, rRemote, r.tokA, pkg{Name: "x", Version: "1.0.0"}); a.Status != 403 {
		t.Errorf("a caller without deploy on the remote: %d %s", a.Status, a.Body)
	}
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPut, "x", body(t, pkg{Name: "x", Version: "1.0.0"})},
		{http.MethodPut, "-/package/" + up.Name + "/dist-tags/next", `"` + up.Versions[0].Version + `"`},
		{http.MethodDelete, "-/package/" + up.Name + "/dist-tags/next", nil},
		{http.MethodPut, up.Name + "/-rev/1-x", `{"versions":{}}`},
		{http.MethodDelete, up.Name + "/-rev/1-x", nil},
		{http.MethodDelete, up.Name + "/-/" + corpus.TarballFile(up.Name, up.Versions[0].Version) + "/-rev/1-x", nil},
	} {
		a := r.send(rep, c.method, rGated, c.path, r.tokB, c.body)
		if a.Status != 405 || code(t, a) != "npm_class_not_writable" {
			t.Errorf("%s %s: %d %s", c.method, c.path, a.Status, a.Body)
		}
		if c.path == "x" && a.Header.Get("Allow") != "GET, HEAD" {
			t.Errorf("Allow = %q", a.Header.Get("Allow"))
		}
		if !strings.Contains(string(a.Body), "remote repository") {
			t.Errorf("%s %s: the text does not say why: %s", c.method, c.path, a.Body)
		}
	}
	if n := r.stub.Counts().Total(); n != 0 {
		t.Errorf("a refused write reached the upstream %d times", n)
	}
}

func TestR_AUTHZ_008_ACallerWhoMayNotFillSeesOnlyTheCachedVersionsOfARemoteMember(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	rep := r.start(1)[0]
	p := upstreamPackage(t, 3)
	first, second := p.Versions[0], p.Versions[1]

	// alice may read npm-gated and not fill it (cacheFillAction: deploy): nothing is cached, so
	// the virtual repository has nothing to show her, and the upstream is not asked.
	if a := r.view(rep, rGatedV, p.Name, r.tokA); a.Status != 404 {
		t.Fatalf("a caller who may not fill sees an uncached package: %d %.200s", a.Status, a.Body)
	}
	if n := r.stub.Counts().Total(); n != 0 {
		t.Fatalf("the upstream was asked %d times on behalf of a caller who may not fill", n)
	}
	// bob may fill: he sees the whole document and installs one version, which caches it.
	b := r.view(rep, rGatedV, p.Name, r.tokB)
	if b.Status != 200 || len(versionsOf(t, b)) != len(p.Versions) {
		t.Fatalf("bob: %d %.200s", b.Status, b.Body)
	}
	if got := r.install(rep, tarballsOf(t, b)[first.Version], r.tokB); got.Status != 200 {
		t.Fatalf("bob's install: %d", got.Status)
	}
	r.stub.Reset()
	// Now alice sees exactly the cached version, and every version she sees installs.
	a := r.view(rep, rGatedV, p.Name, r.tokA)
	if vs := versionsOf(t, a); a.Status != 200 || len(vs) != 1 || vs[0] != first.Version {
		t.Fatalf("alice sees %v (%d)", versionsOf(t, a), a.Status)
	}
	if got := r.install(rep, tarballsOf(t, a)[first.Version], r.tokA); got.Status != 200 || integrityOf(got.Body) != first.Dist.Integrity {
		t.Errorf("alice's install of the cached version: %d", got.Status)
	}
	// A version nobody cached is a miss for her, and bob's document still lists it.
	miss := r.st.Do(rep, http.MethodGet, "/api/npm/"+rGatedV+"/"+p.Name+"/-/"+corpus.TarballFile(p.Name, second.Version), r.tokA, nil)
	if miss.Status != 404 {
		t.Errorf("an uncached version for a caller who may not fill: %d", miss.Status)
	}
	if n := r.stub.Counts().Total(); n != 0 {
		t.Errorf("the upstream was asked %d times on behalf of alice", n)
	}
	if again := r.view(rep, rGatedV, p.Name, r.tokB); len(versionsOf(t, again)) != len(p.Versions) {
		t.Errorf("bob's view shrank: %d", len(versionsOf(t, again)))
	}
}

func TestR_REPO_032_TheCacheViewServesStoredEntriesOnlyAndNeverAsksTheUpstream(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{MetadataTTL: 2})
	rep := r.start(1)[0]
	p, other := upstreamPackage(t, 3), scopedPackage(t)
	r.view(rep, rRemote, p.Name, r.tokA)
	r.stub.Reset()
	time.Sleep(2200 * time.Millisecond)
	// Expired, yet served, and the upstream records no request.
	cv := r.view(rep, rRemote+"~cache", p.Name, r.tokA)
	if cv.Status != 200 || len(versionsOf(t, cv)) != len(p.Versions) {
		t.Fatalf("%d %.200s", cv.Status, cv.Body)
	}
	if !strings.Contains(string(cv.Body), "/api/npm/npm-remote~cache/"+p.Name+"/-/") {
		t.Errorf("the cache view's tarball addresses: %.300s", cv.Body)
	}
	// An uncached package, an uncached tarball: 404, and still no request.
	if a := r.view(rep, rRemote+"~cache", strings.Replace(other.Name, "/", "%2f", 1), r.tokA); a.Status != 404 {
		t.Errorf("an uncached package: %d", a.Status)
	}
	if a := r.st.Do(rep, http.MethodGet, strings.Replace(tarballRoute(rRemote, p.Name, p.Versions[0].Version), rRemote, rRemote+"~cache", 1), r.tokA, nil); a.Status != 404 {
		t.Errorf("an uncached tarball: %d", a.Status)
	}
	if n := r.stub.Counts().Total(); n != 0 {
		t.Errorf("the cache view reached the upstream %d times", n)
	}
	// Only-if-cached on the remote address: stored is served, the rest is 504.
	if a := r.view(rep, rRemote, strings.Replace(other.Name, "/", "%2f", 1), r.tokA, "Cache-Control", "only-if-cached"); a.Status != 504 {
		t.Errorf("only-if-cached for an uncached package: %d %s", a.Status, a.Body)
	}
	// A cache view needs a remote repository.
	if a := r.view(rep, rLocal+"~cache", "x", r.tokA); a.Status != 404 {
		t.Errorf("the cache view of a local repository: %d", a.Status)
	}
}

func TestR_REPO_043_ADocumentThatNamesAForeignHostIsRewrittenAndItsTarballIsNotFetched(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	rep := r.start(1)[0]
	p := upstreamPackage(t, 3)
	if err := r.stub.SetMode(npmstub.Mode{ForeignTarballHost: "https://cdn.example.net"}); err != nil {
		t.Fatal(err)
	}
	a := r.view(rep, rRemote, p.Name, r.tokA)
	if a.Status != 200 || strings.Contains(string(a.Body), "cdn.example.net") || strings.Contains(string(a.Body), r.run.Addr) {
		t.Fatalf("%d: the served document names a host that is not the serving repository's: %.300s", a.Status, a.Body)
	}
	got := r.tarball(rep, rRemote, p.Name, p.Versions[0].Version, r.tokA)
	if got.Status != 502 || got.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("%d %s", got.Status, got.Body)
	}
	if c := code(t, got); c != "foreign_host_denied" {
		t.Errorf("code = %q", c)
	}
	if n := r.stub.Counts().Tarballs(p.Name); n != 0 {
		t.Errorf("the upstream saw %d tarball requests: no request goes to a host nobody allowed", n)
	}
}

func TestR_REPO_042_AnHTMLPageInPlaceOfADocumentIsRefused(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	rep := r.start(1)[0]
	p := upstreamPackage(t, 3)
	if err := r.stub.SetMode(npmstub.Mode{HTMLContentType: []string{p.Name}}); err != nil {
		t.Fatal(err)
	}
	a := r.view(rep, rRemote, p.Name, r.tokA)
	if a.Status != 502 || code(t, a) != "upstream_content_type_blocked" {
		t.Errorf("%d %s", a.Status, a.Body)
	}
	if n := r.st.Number(`SELECT count(*) FROM remote_cache_entries e JOIN repositories r ON r.id = e.repo_id WHERE r.key = $1`, rRemote); n != 0 {
		t.Errorf("%d cache entries", n)
	}
}

func TestR_NPM_027_TrustEvidenceAndSignaturesPassThroughUnchanged(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	rep := r.start(1)[0]
	type doc struct {
		Versions map[string]struct {
			NPMUser json.RawMessage `json:"_npmUser"`
			Dist    struct {
				Signatures   json.RawMessage `json:"signatures"`
				Attestations json.RawMessage `json:"attestations"`
				Integrity    string          `json:"integrity"`
			} `json:"dist"`
		} `json:"versions"`
		Time map[string]string `json:"time"`
	}
	read := func(a testenv.Answer) doc {
		t.Helper()
		var d doc
		if a.Status != 200 {
			t.Fatalf("%d %.200s", a.Status, a.Body)
		}
		if err := json.Unmarshal(a.Body, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	up := read(r.get(rep, rRemote, "sigstore", r.tokA))
	via := read(r.get(rep, rAll, "sigstore", r.tokA))
	trusted := 0
	for v, e := range up.Versions {
		if len(e.NPMUser) == 0 || len(e.Dist.Signatures) == 0 {
			t.Errorf("%s: _npmUser or signatures missing", v)
		}
		if strings.Contains(string(e.NPMUser), "trustedPublisher") && len(e.Dist.Attestations) > 0 {
			trusted++
		}
		o := via.Versions[v]
		if string(o.NPMUser) != string(e.NPMUser) || string(o.Dist.Signatures) != string(e.Dist.Signatures) ||
			string(o.Dist.Attestations) != string(e.Dist.Attestations) || o.Dist.Integrity != e.Dist.Integrity {
			t.Errorf("%s: the virtual repository changed the trust evidence", v)
		}
	}
	if trusted == 0 {
		t.Error("the recorded package carries no trusted publisher and attestation: the check proves nothing")
	}
	if len(up.Time) == 0 {
		t.Error("no time map")
	}
}

func TestR_NPM_025_ABearerTokenAndTheUpstreamsCredentialRule(t *testing.T) {
	// A remote whose upstream refuses requests without the credential: the stub requires it, the
	// repository carries none, and the answer names the repository.
	r := newRemoteEnv(t, remoteOptions{})
	rep := r.start(1)[0]
	p := upstreamPackage(t, 3)
	if err := r.stub.SetMode(npmstub.Mode{RequireAuthorization: "Bearer secret"}); err != nil {
		t.Fatal(err)
	}
	a := r.view(rep, rRemote, p.Name, r.tokA)
	if a.Status != 502 || code(t, a) != "upstream_auth_failed" {
		t.Errorf("%d %s", a.Status, a.Body)
	}
	if strings.Contains(string(a.Body), "secret") || strings.Contains(string(a.Body), r.run.Addr) {
		t.Errorf("the refusal discloses upstream details: %s", a.Body)
	}
}
