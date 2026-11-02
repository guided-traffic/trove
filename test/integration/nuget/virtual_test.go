//go:build integration

package nuget

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The virtual class (R-NUGET-017, R-NUGET-018, R-NUGET-019) against PostgreSQL, through the whole stack: the
// virtual engine, its merged-metadata cache and the decision point. The members are local; remote
// members are the subject of the remote class's tests (they need the upstream stub).

const (
	vA, vB, vC = "nuget-a", "nuget-b", "nuget-c"
	// vAll merges a and b and deploys to a; vRO has the same members and no deployment target; vPin
	// pins acme.pinned to b; vClaim contains c, which auto-claims what it receives.
	vAll, vRO, vPin, vClaim = "nuget-all", "nuget-ro", "nuget-pin", "nuget-claim"
)

type venv struct {
	*wenv
	tokPartial string
}

func newVirtualEnv(t *testing.T) *venv {
	t.Helper()
	st := testenv.NewStack(t, testenv.StackOptions{Name: "nugetv"})
	e := &wenv{t: t, st: st}
	rita, dev, partial, nobody := st.User("rita"), st.User("dev"), st.User("partial"), st.User("nobody")
	st.Bind("admin", rita)
	e.devID = dev.ID
	e.tokAdmin = st.Token(rita, scope.Scope{Inherit: true, Admin: true})
	e.tokDev = st.Token(dev, scope.Default())
	e.tokNobody = st.Token(nobody, scope.Default())
	v := &venv{wenv: e, tokPartial: st.Token(partial, scope.Default())}

	st.Repository(vA, "local", "nuget", `{"local":{"autoClaim":false}}`)
	st.Repository(vB, "local", "nuget", `{"local":{"autoClaim":false}}`)
	st.Repository(vC, "local", "nuget", `{"local":{"autoClaim":true}}`)
	virtual := func(key, target string, members ...string) {
		st.Repository(key, "virtual", "nuget", `{}`)
		for i, m := range members {
			st.Exec(`INSERT INTO virtual_members (virtual_id, position, member_id)
				SELECT v.id, $2, m.id FROM repositories v, repositories m WHERE v.key = $1 AND m.key = $3`, key, i, m)
		}
		if target != "" {
			st.Exec(`INSERT INTO virtual_deploy_targets (virtual_id, target_id)
				SELECT v.id, t.id FROM repositories v, repositories t WHERE v.key = $1 AND t.key = $2`, key, target)
		}
	}
	virtual(vAll, vA, vA, vB)
	virtual(vRO, "", vA, vB)
	virtual(vPin, vA, vA, vB)
	virtual(vClaim, vC, vC, vB)
	st.Exec(`INSERT INTO virtual_pin_rules (virtual_id, position, package_patterns) SELECT id, 0, ARRAY['acme.pinned'] FROM repositories WHERE key = $1`, vPin)
	st.Exec(`INSERT INTO virtual_pin_members (virtual_id, position, member_id)
		SELECT v.id, 0, m.id FROM repositories v, repositories m WHERE v.key = $1 AND m.key = $2`, vPin, vB)

	all := []string{vA, vB, vC, vAll, vRO, vPin, vClaim}
	st.Policy("devs", []string{"read", "deploy", "delete"}, all, dev)
	// partial reads the virtual repository and one of its two members.
	st.Policy("partial", []string{"read"}, []string{vAll, vA}, partial)
	return v
}

// vfeed is the v3 root of a repository.
func vfeed(repo string) string { return "/api/nuget/" + repo + "/v3/" }

func (e *venv) at(r *testenv.Replica, repo, path, token string, hdr ...string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(r, "GET", vfeed(repo)+path, token, nil, hdr...)
}

// flat returns the version list of an ID through a repository.
func (e *venv) flat(r *testenv.Replica, repo, id, token string) ([]string, int) {
	e.t.Helper()
	ans := e.at(r, repo, "flatcontainer/"+id+"/index.json", token)
	if ans.Status != 200 {
		return nil, ans.Status
	}
	return strs(decode(e.t, ans)["versions"]), ans.Status
}

func (e *venv) mustFlat(r *testenv.Replica, repo, id, token string, want ...string) {
	e.t.Helper()
	got, status := e.flat(r, repo, id, token)
	if status != 200 || !slices.Equal(got, want) {
		e.t.Fatalf("%s %s: status %d, versions %v, want %v", repo, id, status, got, want)
	}
}

// TestR_NUGET_017_AVirtualRepositoryMergesTheDocumentsAndFilesOfItsMembers: the version list is the
// union of the members' versions, the registration entry of a version two members hold is the first
// member's and so is the package the client downloads (Trove-Resolved-From names it), the
// documents carry the address of the virtual repository, and two replicas answer with one body and
// one validator.
func TestR_NUGET_017_AVirtualRepositoryMergesTheDocumentsAndFilesOfItsMembers(t *testing.T) {
	e := newVirtualEnv(t)
	a, b := e.replica("web-a"), e.replica("web-b")
	pk := map[string][]byte{}
	push := func(repo, version, desc string) {
		p := nupkg(t, pkgSpec{id: "Acme.Widgets", version: version, description: desc})
		pk[repo+"/"+version] = p
		e.mustPush(a, repo, e.tokDev, p)
	}
	push(vA, "1.0.0", "from a")
	push(vA, "1.5.0", "from a")
	push(vB, "1.5.0", "from b")
	push(vB, "2.0.0", "from b")

	e.mustFlat(a, vAll, "acme.widgets", e.tokDev, "1.0.0", "1.5.0", "2.0.0")
	e.mustFlat(a, vAll, "Acme.Widgets", e.tokDev, "1.0.0", "1.5.0", "2.0.0")
	e.mustFlat(a, vRO, "acme.widgets", e.tokDev, "1.0.0", "1.5.0", "2.0.0")

	t.Run("the registration entry of a version two members hold is the first member's", func(t *testing.T) {
		ans := e.at(a, vAll, "registration-semver2/acme.widgets/index.json", e.tokDev)
		if ans.Status != 200 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		desc := map[string]string{}
		for _, l := range leaves(t, decode(t, ans)) {
			desc[l["version"].(string)] = l["description"].(string)
			if pc := l["packageContent"].(string); !strings.HasPrefix(pc, feedBase+vfeed(vAll)+"flatcontainer/acme.widgets/") {
				t.Errorf("packageContent = %q", pc)
			}
		}
		if desc["1.5.0"] != "from a" || desc["2.0.0"] != "from b" || desc["1.0.0"] != "from a" {
			t.Errorf("descriptions = %v", desc)
		}
		leaf := e.at(a, vAll, "registration-semver2/acme.widgets/1.5.0.json", e.tokDev)
		if leaf.Status != 200 || !strings.Contains(string(leaf.Body), feedBase+vfeed(vAll)+"flatcontainer/acme.widgets/1.5.0/acme.widgets.1.5.0.nupkg") {
			t.Errorf("leaf %d %s", leaf.Status, leaf.Body)
		}
		if e.at(a, vAll, "registration-semver2/acme.widgets/9.9.9.json", e.tokDev).Status != 404 {
			t.Error("a leaf of a version no member holds is not 404")
		}
	})

	t.Run("the package comes from the member whose entry the registration shows", func(t *testing.T) {
		for version, member := range map[string]string{"1.0.0": vA, "1.5.0": vA, "2.0.0": vB} {
			ans := e.at(a, vAll, "flatcontainer/acme.widgets/"+version+"/acme.widgets."+version+".nupkg", e.tokDev)
			if ans.Status != 200 || !bytes.Equal(ans.Body, pk[member+"/"+version]) || ans.Header.Get("Trove-Resolved-From") != member {
				t.Errorf("%s: %d from %q, %d bytes (want the bytes of %s)", version, ans.Status, ans.Header.Get("Trove-Resolved-From"), len(ans.Body), member)
			}
		}
		ns := e.at(a, vAll, "flatcontainer/acme.widgets/1.5.0/acme.widgets.nuspec", e.tokDev)
		if ns.Status != 200 || !strings.Contains(string(ns.Body), "from a") {
			t.Errorf("nuspec %d %s", ns.Status, ns.Body)
		}
		if e.at(a, vAll, "flatcontainer/acme.widgets/3.0.0/acme.widgets.3.0.0.nupkg", e.tokDev).Status != 404 {
			t.Error("a version no member holds is not 404")
		}
	})

	t.Run("two replicas answer with one body and one validator, and a validator is a 304 on either", func(t *testing.T) {
		for _, path := range []string{"flatcontainer/acme.widgets/index.json", "registration-semver2/acme.widgets/index.json", "registration/acme.widgets/page/1.0.0/2.0.0.json"} {
			x, y := e.at(a, vAll, path, e.tokDev), e.at(b, vAll, path, e.tokDev)
			if x.Status != 200 || !bytes.Equal(x.Body, y.Body) || x.Header.Get("ETag") != y.Header.Get("ETag") || !strings.HasPrefix(x.Header.Get("ETag"), `W/"`) {
				t.Fatalf("%s: %d/%d, same body %v, ETags %q %q", path, x.Status, y.Status, bytes.Equal(x.Body, y.Body), x.Header.Get("ETag"), y.Header.Get("ETag"))
			}
			if n := e.at(b, vAll, path, e.tokDev, "If-None-Match", x.Header.Get("ETag")); n.Status != 304 {
				t.Errorf("%s: If-None-Match on the other replica: %d", path, n.Status)
			}
		}
		if e.at(a, vAll, "flatcontainer/acme.widgets/index.json", e.tokDev).Header.Get("ETag") == e.at(a, vRO, "flatcontainer/acme.widgets/index.json", e.tokDev).Header.Get("ETag") {
			t.Error("two virtual repositories share a validator")
		}
	})
}

// TestR_NUGET_017_APushToAMemberIsVisibleThroughTheVirtualRepositoryAtOnce: the merged documents are
// checked against the revision of every local member on each hit, so a push through the virtual
// repository on one replica shows on the other with no wait, and a validator from before the push
// answers 200.
func TestR_NUGET_017_APushToAMemberIsVisibleThroughTheVirtualRepositoryAtOnce(t *testing.T) {
	e := newVirtualEnv(t)
	a, b := e.replica("web-a"), e.replica("web-b")
	e.mustPush(a, vA, e.tokDev, nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.0.0"}))
	e.mustFlat(b, vAll, "acme.widgets", e.tokDev, "1.0.0")
	before := e.at(b, vAll, "registration-gz/acme.widgets/index.json", e.tokDev).Header.Get("ETag")
	if before == "" {
		t.Fatal("no validator")
	}

	// A push straight to the other member, on the other replica.
	e.mustPush(b, vB, e.tokDev, nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.1.0"}))
	e.mustFlat(a, vAll, "acme.widgets", e.tokDev, "1.0.0", "1.1.0")
	ans := e.at(a, vAll, "registration-gz/acme.widgets/index.json", e.tokDev, "If-None-Match", before)
	if ans.Status != 200 || ans.Header.Get("ETag") == before || !strings.Contains(string(gunzipIfNeeded(t, ans)), "1.1.0") {
		t.Fatalf("after a push: %d, ETag %q", ans.Status, ans.Header.Get("ETag"))
	}
	if again := e.at(b, vAll, "registration-gz/acme.widgets/index.json", e.tokDev, "If-None-Match", ans.Header.Get("ETag")); again.Status != 304 {
		t.Errorf("the new validator on the other replica: %d", again.Status)
	}
}

func gunzipIfNeeded(t *testing.T, a testenv.Answer) []byte {
	t.Helper()
	if a.Header.Get("Content-Encoding") == "gzip" {
		return gunzip(t, a.Body)
	}
	return a.Body
}

// TestR_NUGET_019_PublishingThroughAVirtualRepositoryGoesToItsDeploymentTarget: a push, an unlist, a
// relist and a delete addressed to the virtual repository are executed on the deployment target
// and answer as it would; they never touch the other member; without a deployment target the
// writes are refused (Q-NUGET-59) and the service index lists no publish resource.
func TestR_NUGET_019_PublishingThroughAVirtualRepositoryGoesToItsDeploymentTarget(t *testing.T) {
	e := newVirtualEnv(t)
	a := e.replica("web-a")
	pkgA := nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.4.0"})
	if ans := e.push(a, vAll, e.tokDev, pkgA); ans.Status != 201 {
		t.Fatalf("push through the virtual repository: %d %s", ans.Status, ans.Body)
	}
	if e.versions(vA, "acme.widgets") != 1 || e.versions(vB, "acme.widgets") != 0 || e.versions(vAll, "acme.widgets") != 0 {
		t.Fatalf("the package is in a:%d b:%d all:%d", e.versions(vA, "acme.widgets"), e.versions(vB, "acme.widgets"), e.versions(vAll, "acme.widgets"))
	}
	if e.files(vA, "acme.widgets", "1.4.0") != 2 {
		t.Errorf("%d files in the target, want the package and the .nuspec", e.files(vA, "acme.widgets", "1.4.0"))
	}
	if ans := e.st.Do(a, "GET", "/files/"+vA+"/acme.widgets/1.4.0/acme.widgets.1.4.0.nupkg", e.tokDev, nil); ans.Status != 200 || !bytes.Equal(ans.Body, pkgA) {
		t.Errorf("/files/ of the target: %d", ans.Status)
	}
	// The identical bytes again are a replay, as on the target itself.
	if ans := e.push(a, vAll, e.tokDev, pkgA); ans.Status != 201 {
		t.Errorf("replay: %d %s", ans.Status, ans.Body)
	}
	// The same number in b is another member's: the target decides the 409 for its own.
	e.mustPush(a, vB, e.tokDev, nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.5.0"}))
	e.mustFlat(a, vAll, "acme.widgets", e.tokDev, "1.4.0", "1.5.0")

	t.Run("unlist and relist run on the target and never touch another member", func(t *testing.T) {
		e.mustPush(a, vB, e.tokDev, nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.4.0", description: "b's own"}))
		if ans := e.send(a, "DELETE", vAll, "acme.widgets/1.4.0", e.tokDev); ans.Status != 204 {
			t.Fatalf("unlist: %d %s", ans.Status, ans.Body)
		}
		if e.listed(vA, "acme.widgets", "1.4.0") != "false" || e.listed(vB, "acme.widgets", "1.4.0") != "true" {
			t.Errorf("listed a:%s b:%s", e.listed(vA, "acme.widgets", "1.4.0"), e.listed(vB, "acme.widgets", "1.4.0"))
		}
		// The target's entry wins the version, so the virtual registration shows it unlisted.
		for _, l := range leaves(t, decode(t, e.at(a, vAll, "registration-semver2/acme.widgets/index.json", e.tokDev))) {
			if l["version"] == "1.4.0" && l["listed"] != false {
				t.Errorf("the unlisted version is listed through the virtual repository: %v", l["listed"])
			}
		}
		if ans := e.send(a, "POST", vAll, "acme.widgets/1.4.0", e.tokDev); ans.Status != 200 {
			t.Fatalf("relist: %d %s", ans.Status, ans.Body)
		}
		if e.listed(vA, "acme.widgets", "1.4.0") != "true" {
			t.Error("the relist did not run on the target")
		}
	})

	t.Run("a delete of a version only another member holds is the target's 404", func(t *testing.T) {
		if ans := e.send(a, "DELETE", vAll, "acme.widgets/1.5.0", e.tokDev); ans.Status != 404 {
			t.Errorf("%d %s", ans.Status, ans.Body)
		}
		if e.versions(vB, "acme.widgets") != 2 {
			t.Error("b lost a version")
		}
	})

	t.Run("without a deployment target the writes are refused and the service index lists no publish resource", func(t *testing.T) {
		// The decision point refuses a write to a virtual repository without a target before the
		// dispatcher could answer 405 no_deploy_target (authz/decide.go, Q-NUGET-59), for an
		// administrator as for a developer.
		for name, token := range map[string]string{"developer": e.tokDev, "administrator": e.tokAdmin} {
			if ans := e.push(a, vRO, token, nupkg(t, pkgSpec{id: "Acme.Other", version: "1.0.0"})); ans.Status != 403 || code(t, ans) != "forbidden" {
				t.Errorf("push by the %s: %d %s", name, ans.Status, ans.Body)
			}
			if ans := e.send(a, "DELETE", vRO, "acme.widgets/1.4.0", token); ans.Status != 403 {
				t.Errorf("delete by the %s: %d %s", name, ans.Status, ans.Body)
			}
		}
		ro, rw := e.at(a, vRO, "index.json", e.tokDev), e.at(a, vAll, "index.json", e.tokDev)
		if strings.Contains(string(ro.Body), "PackagePublish") || strings.Contains(string(ro.Body), "SymbolPackagePublish") {
			t.Errorf("the read-only index lists a publish resource: %s", ro.Body)
		}
		if !strings.Contains(string(rw.Body), "PackagePublish/2.0.0") || !strings.Contains(string(rw.Body), feedBase+vfeed(vAll)+"package") ||
			!strings.Contains(string(rw.Body), feedBase+vfeed(vAll)+"flatcontainer/") {
			t.Errorf("the index of the repository with a target: %s", rw.Body)
		}
		if e.versions(vA, "acme.other") != 0 {
			t.Error("the refused push stored something")
		}
	})

	t.Run("a caller without deploy on the target is refused as on the target", func(t *testing.T) {
		if ans := e.push(a, vAll, e.tokPartial, nupkg(t, pkgSpec{id: "Acme.Other", version: "1.0.0"})); ans.Status != 403 {
			t.Errorf("%d %s", ans.Status, ans.Body)
		}
	})
}

// TestR_NUGET_017_PinRulesAndClaimsDecideWhichMembersContributeToAnID: a pin rule selects the
// members for the IDs it names, for the list, the registration and the files alike; a claim of a
// member keeps the other members' versions of its ID away.
func TestR_NUGET_017_PinRulesAndClaimsDecideWhichMembersContributeToAnID(t *testing.T) {
	e := newVirtualEnv(t)
	a := e.replica("web-a")
	e.mustPush(a, vA, e.tokDev, nupkg(t, pkgSpec{id: "Acme.Pinned", version: "1.0.0"}))
	e.mustPush(a, vB, e.tokDev, nupkg(t, pkgSpec{id: "Acme.Pinned", version: "2.0.0"}))
	e.mustFlat(a, vAll, "acme.pinned", e.tokDev, "1.0.0", "2.0.0")
	e.mustFlat(a, vPin, "acme.pinned", e.tokDev, "2.0.0")
	if ans := e.at(a, vPin, "flatcontainer/acme.pinned/1.0.0/acme.pinned.1.0.0.nupkg", e.tokDev); ans.Status != 404 {
		t.Errorf("a package of a member the pin rule leaves out: %d", ans.Status)
	}
	ls := leaves(t, decode(t, e.at(a, vPin, "registration-semver2/acme.pinned/index.json", e.tokDev)))
	if len(ls) != 1 || ls[0]["version"] != "2.0.0" {
		t.Errorf("registration leaves = %v", ls)
	}
	if x, y := e.at(a, vAll, "flatcontainer/acme.pinned/index.json", e.tokDev), e.at(a, vPin, "flatcontainer/acme.pinned/index.json", e.tokDev); x.Header.Get("ETag") == y.Header.Get("ETag") {
		t.Error("the pin rule does not change the validator")
	}
	// Another ID of the same members is not pinned.
	e.mustPush(a, vA, e.tokDev, nupkg(t, pkgSpec{id: "Acme.Free", version: "1.0.0"}))
	e.mustFlat(a, vPin, "acme.free", e.tokDev, "1.0.0")

	t.Run("a claim of a local member keeps the other members away", func(t *testing.T) {
		// b holds a number of its own; c claims the ID with its first push (autoClaim).
		e.mustPush(a, vB, e.tokDev, nupkg(t, pkgSpec{id: "Acme.Claimed", version: "9.9.9"}))
		e.mustFlat(a, vClaim, "acme.claimed", e.tokDev, "9.9.9")
		e.mustPush(a, vClaim, e.tokDev, nupkg(t, pkgSpec{id: "Acme.Claimed", version: "1.0.0"}))
		if e.st.Number(`SELECT count(*) FROM namespace_claims c JOIN repositories r ON r.id = c.repo_id WHERE r.key = $1 AND c.pattern = 'acme.claimed'`, vC) != 1 {
			t.Fatal("the first push did not claim the ID")
		}
		e.mustFlat(a, vClaim, "acme.claimed", e.tokDev, "1.0.0")
		if ans := e.at(a, vClaim, "flatcontainer/acme.claimed/9.9.9/acme.claimed.9.9.9.nupkg", e.tokDev); ans.Status != 404 {
			t.Errorf("a package of a member the claim keeps away: %d", ans.Status)
		}
	})
}

// TestR_NUGET_013_AMemberTheCallerCannotReadTakesNoPartAndLeavesNoTrace: through a virtual
// repository the members the caller may not read are skipped silently in every merge; an ID only such a
// member holds is a 404, and a caller who reads no member gets nothing.
func TestR_NUGET_013_AMemberTheCallerCannotReadTakesNoPartAndLeavesNoTrace(t *testing.T) {
	e := newVirtualEnv(t)
	a := e.replica("web-a")
	e.mustPush(a, vA, e.tokDev, nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.0.0"}))
	e.mustPush(a, vB, e.tokDev, nupkg(t, pkgSpec{id: "Acme.Widgets", version: "2.0.0"}))
	e.mustPush(a, vB, e.tokDev, nupkg(t, pkgSpec{id: "Acme.OnlyB", version: "1.0.0"}))

	e.mustFlat(a, vAll, "acme.widgets", e.tokPartial, "1.0.0")
	e.mustFlat(a, vAll, "acme.widgets", e.tokDev, "1.0.0", "2.0.0")
	if _, status := e.flat(a, vAll, "acme.onlyb", e.tokPartial); status != 404 {
		t.Errorf("an ID only an unreadable member holds: %d", status)
	}
	if ans := e.at(a, vAll, "flatcontainer/acme.widgets/2.0.0/acme.widgets.2.0.0.nupkg", e.tokPartial); ans.Status != 404 {
		t.Errorf("the package of an unreadable member: %d", ans.Status)
	}
	ls := leaves(t, decode(t, e.at(a, vAll, "registration-semver2/acme.widgets/index.json", e.tokPartial)))
	if len(ls) != 1 {
		t.Errorf("registration leaves for a caller who reads one member: %d", len(ls))
	}
	// The merge of one caller is not served to another with different members.
	e.mustFlat(a, vAll, "acme.widgets", e.tokDev, "1.0.0", "2.0.0")
	if _, status := e.flat(a, vAll, "acme.widgets", e.tokNobody); status != 403 && status != 404 {
		t.Errorf("a caller without a grant: %d", status)
	}
}

// vSearch is what the tests read of the search document.
type vSearch struct {
	TotalHits int `json:"totalHits"`
	Data      []struct {
		Registration string `json:"registration"`
		ID           string `json:"id"`
		Version      string `json:"version"`
		Versions     []struct {
			Version string `json:"version"`
		} `json:"versions"`
	} `json:"data"`
}

func (e *venv) search(r *testenv.Replica, repo, query, token string) vSearch {
	e.t.Helper()
	ans := e.at(r, repo, "search?"+query, token)
	if ans.Status != 200 {
		e.t.Fatalf("search %s %s: %d %s", repo, query, ans.Status, ans.Body)
	}
	var out vSearch
	if err := json.Unmarshal(ans.Body, &out); err != nil {
		e.t.Fatalf("search body: %v: %s", err, ans.Body)
	}
	return out
}

func (s vSearch) ids() []string {
	var out []string
	for _, d := range s.Data {
		out = append(out, d.ID)
	}
	return out
}

// TestR_NUGET_018_SearchMergesTheLocalMembers: a package found in two members is one result with the
// union of the versions, the other packages follow in member order, an ID named exactly comes
// first, the links point at the virtual repository, a caller sees what the members it may read
// hold, and a page at or beyond virtual_merge_window is empty.
func TestR_NUGET_018_SearchMergesTheLocalMembers(t *testing.T) {
	e := newVirtualEnv(t)
	a := e.replica("web-a", "TROVE_NUGET_SEARCH_VIRTUAL_MERGE_WINDOW=4")
	for _, p := range []struct {
		repo, id, version string
	}{
		{vA, "Acme.Alpha", "1.0.0"},
		{vA, "Acme.Widgets", "1.0.0"},
		{vA, "Acme.Widgets", "1.5.0"},
		{vB, "Acme.Widgets", "1.5.0"},
		{vB, "Acme.Widgets", "2.0.0"},
		{vB, "Acme.Zeta", "3.0.0"},
		{vB, "Other.Thing", "1.0.0"},
	} {
		e.mustPush(a, p.repo, e.tokDev, nupkg(t, pkgSpec{id: p.id, version: p.version}))
	}

	t.Run("one result per package with the union of the versions", func(t *testing.T) {
		got := e.search(a, vAll, "q=id:acme&take=20", e.tokDev)
		if want := []string{"Acme.Alpha", "Acme.Widgets", "Acme.Zeta"}; !slices.Equal(got.ids(), want) {
			t.Fatalf("results = %v, want %v", got.ids(), want)
		}
		if got.TotalHits != 3 {
			t.Errorf("totalHits = %d, want 3 (Acme.Widgets is in two members)", got.TotalHits)
		}
		w := got.Data[1]
		var versions []string
		for _, v := range w.Versions {
			versions = append(versions, v.Version)
		}
		if w.Version != "2.0.0" || !slices.Equal(versions, []string{"1.0.0", "1.5.0", "2.0.0"}) {
			t.Errorf("Acme.Widgets = %s %v", w.Version, versions)
		}
		if want := feedBase + vfeed(vAll) + "registration/acme.widgets/index.json"; w.Registration != want {
			t.Errorf("registration = %q, want %q", w.Registration, want)
		}
	})

	t.Run("an ID named exactly comes first", func(t *testing.T) {
		got := e.search(a, vAll, "q=packageid:acme.zeta", e.tokDev)
		if !slices.Equal(got.ids(), []string{"Acme.Zeta"}) {
			t.Errorf("results = %v", got.ids())
		}
	})

	t.Run("a caller sees what the members it may read hold", func(t *testing.T) {
		got := e.search(a, vAll, "q=id:acme", e.tokPartial)
		if want := []string{"Acme.Alpha", "Acme.Widgets"}; !slices.Equal(got.ids(), want) || got.Data[1].Version != "1.5.0" || got.TotalHits != 2 {
			t.Errorf("results = %v, widgets %s, totalHits %d", got.ids(), got.Data[1].Version, got.TotalHits)
		}
	})

	t.Run("paging cuts the merge and the window ends it", func(t *testing.T) {
		if got := e.search(a, vAll, "q=&skip=1&take=1", e.tokDev); len(got.Data) != 1 || got.Data[0].ID != "Acme.Widgets" {
			t.Errorf("page 2 = %v", got.ids())
		}
		beyond := e.search(a, vAll, "q=&skip=4&take=20", e.tokDev)
		if len(beyond.Data) != 0 || beyond.TotalHits == 0 {
			t.Errorf("a page at the window: %d results, totalHits %d", len(beyond.Data), beyond.TotalHits)
		}
	})

	t.Run("a caller without a grant learns nothing", func(t *testing.T) {
		if ans := e.at(a, vAll, "search?q=id:acme", e.tokNobody); ans.Status != 403 && ans.Status != 404 {
			t.Errorf("%d %s", ans.Status, ans.Body)
		}
	})
}

// TestR_NUGET_001_AVirtualRepositoryOfTheFormatCanBeCreatedThroughTheManagementAPI: the class is
// registered with the format, the create form's enum offers it, and a virtual repository created
// through the API serves the service index with the publish resources of its deployment target.
func TestR_NUGET_001_AVirtualRepositoryOfTheFormatCanBeCreatedThroughTheManagementAPI(t *testing.T) {
	e := newVirtualEnv(t)
	a := e.replica("web-a")
	create := e.st.Do(a, "POST", "/api/v1/repositories", e.tokAdmin, map[string]any{
		"key": "nuget-made", "class": "virtual", "format": "nuget",
		"virtual": map[string]any{"members": []string{vA, vB}, "deployTarget": vA},
	})
	if create.Status != 201 {
		t.Fatalf("create: %d %s", create.Status, create.Body)
	}
	idx := e.st.Do(a, "GET", vfeed("nuget-made")+"index.json", e.tokAdmin, nil)
	if idx.Status != 200 || !strings.Contains(string(idx.Body), feedBase+vfeed("nuget-made")+"package") {
		t.Errorf("index %d %s", idx.Status, idx.Body)
	}
	if bad := e.st.Do(a, "POST", "/api/v1/repositories", e.tokAdmin, map[string]any{
		"key": "nuget-bad", "class": "virtual", "format": "nuget", "nuget": map[string]any{"deleteMode": "delete"},
		"virtual": map[string]any{"members": []string{vA}},
	}); bad.Status != 422 {
		t.Errorf("deleteMode on a virtual repository: %d %s", bad.Status, bad.Body)
	}
}
