//go:build integration

package nuget

import (
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/nugetstub"
)

// mixedVirtual creates the virtual repository key with the configuration cfg and the members
// given, in order, and the first one as its deployment target.
func (e *renv) mixedVirtual(key, cfg string, members ...string) {
	e.t.Helper()
	e.st.Repository(key, "virtual", "nuget", cfg)
	for i, m := range members {
		e.st.Exec(`INSERT INTO virtual_members (virtual_id, position, member_id)
			SELECT v.id, $2, m.id FROM repositories v, repositories m WHERE v.key = $1 AND m.key = $3`, key, i, m)
	}
	e.st.Exec(`INSERT INTO virtual_deploy_targets (virtual_id, target_id)
		SELECT v.id, t.id FROM repositories v, repositories t WHERE v.key = $1 AND t.key = $2`, key, members[0])
}

// TestR_NUGET_017_AVirtualRepositoryMergesARemoteMemberThatGzipsAndPagesItsRegistration puts a
// local repository and a remote of the counting upstream behind one virtual repository with the
// local one as its deployment target, while the upstream codes every document with gzip whatever
// trove asks for, as nuget.org's registration hive does (Q-NUGET-82). A package only the remote serves
// comes through the virtual repository with every address on the virtual repository, an ID with
// 200 versions in pages the upstream lists by address is merged whole, a version shows the same
// entry through the virtual repository as through the remote, and a push through the virtual
// repository lands in the local member (R-NUGET-015 to R-NUGET-018, R-REPO-021). Before, the
// merge read neither gzip nor pages by address, and every such ID answered 502.
func TestR_NUGET_017_AVirtualRepositoryMergesARemoteMemberThatGzipsAndPagesItsRegistration(t *testing.T) {
	e := newRemoteEnv(t, remoteOptions{Valkey: true})
	if err := e.stub.SetMode(nugetstub.Mode{GzipAlways: true}); err != nil {
		t.Fatal(err)
	}
	const mixed = "nuget-mixed"
	e.mixedVirtual(mixed, `{}`, rLocal, rRemote)
	a := e.start(1)[0]
	get := func(repo, path string, hdr ...string) testenv.Answer {
		return e.st.Do(a, "GET", "/api/nuget/"+repo+"/"+path, e.tokAdmin, nil, hdr...)
	}

	t.Run("R-NUGET-017 a package only the remote member serves comes through the virtual repository", func(t *testing.T) {
		flat := get(mixed, "v3/flatcontainer/newtonsoft.json/index.json")
		if flat.Status != 200 {
			t.Fatalf("flat container: %d %s", flat.Status, flat.Body)
		}
		if got := versionsOf(t, flat); !slices.Equal(got, []string{"13.0.1", "13.0.3"}) {
			t.Errorf("versions = %v", got)
		}
		reg := get(mixed, "v3/registration-semver2/newtonsoft.json/index.json", "Accept-Encoding", "gzip")
		if reg.Status != 200 {
			t.Fatalf("registration: %d %s", reg.Status, reg.Body)
		}
		leaves := 0
		for _, p := range registrationOf(t, reg).Items {
			for _, l := range p.Items {
				leaves++
				if !strings.HasPrefix(l.PackageContent, feed(mixed)+"flatcontainer/newtonsoft.json/") {
					t.Errorf("packageContent %q is not an address of the virtual repository", l.PackageContent)
				}
			}
		}
		if leaves != 2 {
			t.Errorf("%d leaves, want 2", leaves)
		}
		pkg := get(mixed, "v3/flatcontainer/newtonsoft.json/13.0.1/newtonsoft.json.13.0.1.nupkg")
		if pkg.Status != 200 || len(pkg.Body) == 0 {
			t.Errorf("package through the virtual repository: %d, %d bytes", pkg.Status, len(pkg.Body))
		}
	})

	t.Run("R-NUGET-017 an ID with 200 versions in pages the upstream lists by address is merged whole", func(t *testing.T) {
		id := strings.ToLower(nugetstub.ManyVersionsID)
		idx := get(mixed, "v3/registration-semver2/"+id+"/index.json")
		if idx.Status != 200 {
			t.Fatalf("index: %d %s", idx.Status, idx.Body)
		}
		d := registrationOf(t, idx)
		total := 0
		for _, p := range d.Items {
			total += p.Count
			if !strings.HasPrefix(p.ID, feed(mixed)+"registration-semver2/"+id+"/page/") {
				t.Errorf("page @id %q is no page of the virtual repository", p.ID)
			}
		}
		if len(d.Items) != 4 || total != nugetstub.ManyVersions {
			t.Fatalf("%d pages holding %d versions, want 4 holding %d", len(d.Items), total, nugetstub.ManyVersions)
		}
		page := get(mixed, strings.TrimPrefix(d.Items[2].ID, feedBase+"/api/nuget/"+mixed+"/"))
		if pd := registrationOf(t, page); page.Status != 200 || len(pd.Items) != 64 {
			t.Errorf("page: %d, %d leaves", page.Status, len(pd.Items))
		}
		if flat := get(mixed, "v3/flatcontainer/"+id+"/index.json"); len(versionsOf(t, flat)) != nugetstub.ManyVersions {
			t.Errorf("version list: %d %.200s", flat.Status, flat.Body)
		}
	})

	t.Run("R-NUGET-017 a version shows the same entry through the virtual repository as through the remote", func(t *testing.T) {
		through := get(mixed, "v3/registration-semver2/newtonsoft.json/index.json")
		direct := get(rRemote, "v3/registration-semver2/newtonsoft.json/index.json")
		if through.Status != 200 || direct.Status != 200 {
			t.Fatalf("%d %d", through.Status, direct.Status)
		}
		if got, want := strings.ReplaceAll(string(through.Body), feed(mixed), feed(rRemote)), string(direct.Body); got != want {
			t.Errorf("the virtual repository renders another entry than the remote:\n%s\n%s", got, want)
		}
		if !strings.Contains(string(through.Body), `"vulnerabilities":[{"advisoryUrl":"`+nugetstub.Advisory+`","severity":"2"}]`) {
			t.Errorf("the advisory of 13.0.1 is not passed through: %s", through.Body)
		}
	})

	t.Run("R-NUGET-019 a push through the virtual repository lands in its deployment target", func(t *testing.T) {
		body, ct := form(t, nupkg(t, pkgSpec{id: "Acme.Local", version: "1.0.0"}))
		ans := e.st.Do(a, "PUT", "/api/nuget/"+mixed+"/v3/package", e.tokAdmin, body, "Content-Type", ct)
		if ans.Status != 201 {
			t.Fatalf("push: %d %s", ans.Status, ans.Body)
		}
		in := func(repo string) int64 {
			return e.st.Number(`SELECT count(*) FROM nuget_version v JOIN repositories r ON r.id = v.repository_id WHERE r.key = $1 AND v.lower_id = 'acme.local'`, repo)
		}
		if in(rLocal) != 1 || in(rRemote) != 0 {
			t.Errorf("rows: local %d, remote %d, want 1 and 0", in(rLocal), in(rRemote))
		}
		flat := get(mixed, "v3/flatcontainer/acme.local/index.json")
		if flat.Status != 200 || !slices.Equal(versionsOf(t, flat), []string{"1.0.0"}) {
			t.Errorf("merged flat container: %d %s", flat.Status, flat.Body)
		}
		if remote := get(rRemote, "v3/flatcontainer/acme.local/index.json"); remote.Status != 404 {
			t.Errorf("the remote member answers %d for the pushed package, want 404", remote.Status)
		}
	})
}

// TestR_AUTHZ_008_AVersionWhoseOnlyCachedFileIsItsNuspecIsNotListedToACallerWhoMayNotFill
// (Q-NUGET-82): a caller who may read but not fill a remote member sees, through a virtual
// repository, only the versions whose package the member has cached, so that every listed version
// can be downloaded. A cached .nuspec is no package; before, any cached file of a version listed it.
func TestR_AUTHZ_008_AVersionWhoseOnlyCachedFileIsItsNuspecIsNotListedToACallerWhoMayNotFill(t *testing.T) {
	e := newRemoteEnv(t, remoteOptions{})
	const guarded, deployOnly = "nuget-guarded", "nuget-deploy-fill"
	content := []any{map[string]any{"pattern": "localhost", "sendCredentials": false}}
	e.st.Repository(deployOnly, "remote", "nuget", strings.Replace(e.remoteConfig(300, content, nil), `"cacheFillAction":"read"`, `"cacheFillAction":"deploy"`, 1))
	// No merged-metadata cache, so that the viewer's second request merges what is cached then.
	e.mixedVirtual(guarded, `{"virtual":{"mergedMetadataTtlSeconds":0}}`, deployOnly)
	viewer := e.st.User("viewer")
	e.st.Policy("nuget-viewers", []string{"read"}, []string{deployOnly, guarded}, viewer)
	tokViewer := e.st.Token(viewer, scope.Default())
	a := e.start(1)[0]
	as := func(tok, repo, path string) testenv.Answer {
		return e.st.Do(a, "GET", "/api/nuget/"+repo+"/"+path, tok, nil)
	}

	// The administrator, who may fill, brings the version list (through the virtual repository,
	// which asks the member for its own variant of it) and the .nuspec of 13.0.3 into the member's
	// cache, and no package.
	if r := as(e.tokAdmin, guarded, "v3/flatcontainer/newtonsoft.json/index.json"); r.Status != 200 {
		t.Fatalf("version list: %d %s", r.Status, r.Body)
	}
	if r := as(e.tokAdmin, deployOnly, "v3/flatcontainer/newtonsoft.json/13.0.3/newtonsoft.json.nuspec"); r.Status != 200 {
		t.Fatalf(".nuspec: %d %s", r.Status, r.Body)
	}
	if r := as(tokViewer, deployOnly, "v3/flatcontainer/newtonsoft.json/13.0.3/newtonsoft.json.13.0.3.nupkg"); r.Status != 403 {
		t.Fatalf("the viewer filled the package: %d %s", r.Status, r.Body)
	}
	if r := as(tokViewer, guarded, "v3/flatcontainer/newtonsoft.json/index.json"); r.Status != 404 {
		t.Errorf("a version whose .nuspec alone is cached is listed: %d %s, want 404 (no version can be downloaded)", r.Status, r.Body)
	}
	if r := as(e.tokAdmin, deployOnly, "v3/flatcontainer/newtonsoft.json/13.0.1/newtonsoft.json.13.0.1.nupkg"); r.Status != 200 {
		t.Fatalf("package: %d %s", r.Status, r.Body)
	}
	r := as(tokViewer, guarded, "v3/flatcontainer/newtonsoft.json/index.json")
	if r.Status != 200 || !slices.Equal(versionsOf(t, r), []string{"13.0.1"}) {
		t.Errorf("after the package of 13.0.1 was cached: %d %s, want 13.0.1 alone", r.Status, r.Body)
	}
}

// TestR_NUGET_015_TheFirstAnswerOfACacheMissCarriesTheHeadersOfAStoredAnswer (Q-NUGET-86, E3.6): a file
// that fits in the withheld tail, as a .nuspec does, is answered from the stored entry on the
// request that fetched it, with the validator and the headers every later answer carries; before,
// the first answer streamed the upstream's response without ETag, Accept-Ranges or Cache-Control,
// directly and through a virtual repository.
func TestR_NUGET_015_TheFirstAnswerOfACacheMissCarriesTheHeadersOfAStoredAnswer(t *testing.T) {
	e := newRemoteEnv(t, remoteOptions{})
	const mixed = "nuget-mixed"
	e.mixedVirtual(mixed, `{}`, rLocal, rRemote)
	rep := e.start(1)[0]
	for repo, id := range map[string]string{rRemote: "newtonsoft.json", mixed: "contoso.util"} {
		v := "13.0.3"
		if id == "contoso.util" {
			v = "2.1.0"
		}
		path := "/api/nuget/" + repo + "/v3/flatcontainer/" + id + "/" + v + "/" + id + ".nuspec"
		first, second := e.st.Do(rep, "GET", path, e.tokAdmin, nil), e.st.Do(rep, "GET", path, e.tokAdmin, nil)
		if first.Status != 200 || second.Status != 200 {
			t.Fatalf("%s: %d %d", repo, first.Status, second.Status)
		}
		if !strings.Contains(first.Header.Get("Cache-Status"), "fwd=uri-miss") {
			t.Fatalf("%s: the first request did not fill: %q", repo, first.Header.Get("Cache-Status"))
		}
		for _, h := range []string{"ETag", "Accept-Ranges", "Cache-Control", "Last-Modified", "Content-Type", "Content-Disposition"} {
			if first.Header.Get(h) == "" || first.Header.Get(h) != second.Header.Get(h) {
				t.Errorf("%s %s: first %q, stored %q", repo, h, first.Header.Get(h), second.Header.Get(h))
			}
		}
	}
}
