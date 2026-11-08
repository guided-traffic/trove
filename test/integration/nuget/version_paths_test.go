//go:build integration

package nuget

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// A path pattern finer than an ID's folder on NuGet (R-AUTHZ-005, Q-AUTHZ-255, Q-NUGET-120): a version is decided on the
// path of its .nupkg, so a grant or a token scope that excludes `{id}/{version}/**` hides that version from the version
// list, every registration document and the search, and a write is decided on every file it writes or removes.
//
//	nuget-ver: local, packageOwnership off, deleteMode delete
//	  Acme.Secret 1.0.0 ("Secret one"), Acme.Secret 2.0.0 ("TOPSECRET two"); Acme.Sym 1.0.0 with a symbol package
//
//	setup:  read, deploy, delete; no pattern
//	ver:    read, paths.exclude acme.secret/2.0.0/**
//	scoped: read without patterns, a token whose scope excludes acme.secret/2.0.0/**
//	nup:    read, paths.exclude acme.secret/2.0.0/acme.secret.2.0.0.nupkg (the .nupkg alone)
//	snu:    read, deploy and delete, paths.exclude **/*.snupkg and **/*.nuspec
//	emb:    read, deploy and delete, paths.exclude **/.embedded/**

type versionEnv struct {
	*wenv
	r                                 *testenv.Replica
	setup, ver, scoped, nup, snu, emb string
}

func newVersionEnv(t *testing.T) *versionEnv {
	t.Helper()
	st := testenv.NewStack(t, testenv.StackOptions{Name: "nugetver"})
	e := &versionEnv{wenv: &wenv{t: t, st: st}}
	st.Repository("nuget-ver", "local", "nuget", `{"local":{"packageOwnership":"off"},"nuget":{"deleteMode":"delete"}}`)
	repos := []string{"nuget-ver"}
	pattern := func(policy, p string) {
		st.Exec(`INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern) SELECT id, 'path', 'exclude', $2 FROM authz_policy WHERE name = $1`, policy, p)
	}
	setup, ver, scoped, nup, snu := st.User("setup"), st.User("ver"), st.User("scoped"), st.User("nup"), st.User("snu")
	st.Policy("ver-setup", []string{"read", "deploy", "delete"}, repos, setup)
	st.Policy("ver-ver", []string{"read"}, repos, ver)
	pattern("ver-ver", "acme.secret/2.0.0/**")
	st.Policy("ver-scoped", []string{"read"}, repos, scoped)
	st.Policy("ver-nup", []string{"read"}, repos, nup)
	pattern("ver-nup", "acme.secret/2.0.0/acme.secret.2.0.0.nupkg")
	st.Policy("ver-snu", []string{"read", "deploy", "delete"}, repos, snu)
	pattern("ver-snu", "**/*.snupkg")
	pattern("ver-snu", "**/*.nuspec")
	emb := st.User("emb")
	st.Policy("ver-emb", []string{"read", "deploy", "delete"}, repos, emb)
	pattern("ver-emb", "**/.embedded/**")
	e.setup, e.ver, e.snu = st.Token(setup, scope.Default()), st.Token(ver, scope.Default()), st.Token(snu, scope.Default())
	e.nup, e.emb = st.Token(nup, scope.Default()), st.Token(emb, scope.Default())
	e.scoped = st.Token(scoped, scope.Scope{Repositories: []scope.Grant{{
		Repositories: []string{"it/nuget-ver"}, Paths: scope.Patterns{Exclude: []string{"acme.secret/2.0.0/**"}}, Actions: []string{"read"},
	}}})
	e.r = e.replica("web-ver")
	e.mustPush(e.r, "nuget-ver", e.setup, nupkg(t, pkgSpec{id: "Acme.Secret", version: "1.0.0", description: "Secret one"}))
	e.mustPush(e.r, "nuget-ver", e.setup, nupkg(t, pkgSpec{id: "Acme.Secret", version: "2.0.0", description: "TOPSECRET two"}))
	e.mustPush(e.r, "nuget-ver", e.setup, nupkg(t, pkgSpec{id: "Acme.Sym", version: "1.0.0"}))
	if a := e.pushSymbols(e.r, "nuget-ver", e.setup, snupkg(t, "Acme.Sym", "1.0.0", "x", nil)); a.Status != http.StatusCreated {
		t.Fatalf("symbols: %d %s", a.Status, a.Body)
	}
	return e
}

func (e *versionEnv) get(path, token string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(e.r, "GET", "/api/nuget/it/nuget-ver/"+path, token, nil)
}

// flatVersions is the version list of the flat container.
func (e *versionEnv) flatVersions(t *testing.T, token string) []string {
	t.Helper()
	a := e.get("v3/flatcontainer/acme.secret/index.json", token)
	var l struct {
		Versions []string `json:"versions"`
	}
	if a.Status != http.StatusOK || json.Unmarshal(a.Body, &l) != nil {
		t.Fatalf("flat index: %d %s", a.Status, a.Body)
	}
	return l.Versions
}

// searchHits are the ID@version and versions of a search's results.
func (e *versionEnv) searchHits(t *testing.T, token, q string) (int, []string) {
	t.Helper()
	a := e.get("v3/search?q="+q+"&prerelease=true&semVerLevel=2.0.0", token)
	var f struct {
		TotalHits int `json:"totalHits"`
		Data      []struct {
			ID       string `json:"id"`
			Version  string `json:"version"`
			Versions []struct {
				Version string `json:"version"`
			} `json:"versions"`
		} `json:"data"`
	}
	if a.Status != http.StatusOK || json.Unmarshal(a.Body, &f) != nil {
		t.Fatalf("search %s: %d %s", q, a.Status, a.Body)
	}
	var out []string
	for _, h := range f.Data {
		vs := make([]string, 0, len(h.Versions))
		for _, v := range h.Versions {
			vs = append(vs, v.Version)
		}
		out = append(out, strings.ToLower(h.ID)+"@"+h.Version+"["+strings.Join(vs, ",")+"]")
	}
	return f.TotalHits, out
}

// expectOnlyFirst asserts that a caller whose grant hides acme.secret 2.0.0 meets the ID with the one version 1.0.0.
func (e *versionEnv) expectOnlyFirst(t *testing.T, who, token string) {
	t.Helper()
	for _, p := range []string{"v3/flatcontainer/acme.secret/2.0.0/acme.secret.2.0.0.nupkg", "v3/flatcontainer/acme.secret/2.0.0/acme.secret.nuspec"} {
		if a := e.get(p, token); a.Status != http.StatusNotFound {
			t.Errorf("%s, %s: %d, want 404", who, p, a.Status)
		}
	}
	if got := e.flatVersions(t, token); !slices.Equal(got, []string{"1.0.0"}) {
		t.Errorf("%s, flat container lists %v, want [1.0.0]", who, got)
	}
	for _, hive := range []string{"registration", "registration-gz", "registration-semver2"} {
		a := e.get("v3/"+hive+"/acme.secret/index.json", token)
		if a.Status != http.StatusOK {
			t.Errorf("%s, %s index: %d, want 200\n%s", who, hive, a.Status, a.Body)
			continue
		}
		if b := string(a.Body); strings.Contains(b, "TOPSECRET") || strings.Contains(b, "2.0.0") {
			t.Errorf("%s, %s index names the hidden version or its description:\n%s", who, hive, b)
		}
		if a := e.get("v3/"+hive+"/acme.secret/2.0.0.json", token); a.Status != http.StatusNotFound {
			t.Errorf("%s, %s leaf of 2.0.0: %d, want 404\n%s", who, hive, a.Status, a.Body)
		}
		if a := e.get("v3/"+hive+"/acme.secret/1.0.0.json", token); a.Status != http.StatusOK {
			t.Errorf("%s, %s leaf of 1.0.0: %d, want 200", who, hive, a.Status)
		}
	}
	if total, hits := e.searchHits(t, token, "TOPSECRET"); total != 0 || len(hits) != 0 {
		t.Errorf("%s, search for the hidden description: %d %v, want nothing", who, total, hits)
	}
	if total, hits := e.searchHits(t, token, "acme.secret"); total != 1 || !slices.Equal(hits, []string{"acme.secret@1.0.0[1.0.0]"}) {
		t.Errorf("%s, search for the ID: %d %v, want acme.secret@1.0.0[1.0.0]", who, total, hits)
	}
}

func TestR_AUTHZ_005_AVersionPathPatternHidesTheVersionFromNuGetDocuments(t *testing.T) {
	e := newVersionEnv(t)

	t.Run("R-AUTHZ-005 a grant that excludes a version's folder hides the version from every document and the search", func(t *testing.T) {
		// The unrestricted caller first and last, so the replica's renditions of the whole documents are cached when the
		// narrowed caller asks and a filtered one is never served back (R-NUGET-030).
		if got := e.flatVersions(t, e.setup); !slices.Equal(got, []string{"1.0.0", "2.0.0"}) {
			t.Errorf("setup, flat container lists %v", got)
		}
		e.expectOnlyFirst(t, "ver", e.ver)
		if got := e.flatVersions(t, e.setup); !slices.Equal(got, []string{"1.0.0", "2.0.0"}) {
			t.Errorf("setup, flat container lists %v after ver", got)
		}
		if a := e.get("v3/registration-semver2/acme.secret/index.json", e.setup); !strings.Contains(string(a.Body), "TOPSECRET") {
			t.Errorf("setup, registration index lacks 2.0.0: %d %s", a.Status, a.Body)
		}
		if total, _ := e.searchHits(t, e.setup, "TOPSECRET"); total != 1 {
			t.Errorf("setup, search for the description: %d hits, want 1", total)
		}
	})

	t.Run("R-AUTHZ-005 a token scope that excludes a version's folder hides it the same way", func(t *testing.T) {
		e.expectOnlyFirst(t, "scoped token", e.scoped)
	})

	t.Run("R-AUTHZ-005 a grant that excludes the version's .nupkg alone hides the version and its .nuspec", func(t *testing.T) {
		e.expectOnlyFirst(t, "nup", e.nup)
		if a := e.get("v3/flatcontainer/acme.secret/1.0.0/acme.secret.nuspec", e.nup); a.Status != http.StatusOK {
			t.Errorf("nup, the .nuspec of 1.0.0: %d, want 200", a.Status)
		}
	})
}

func TestR_AUTHZ_005_ANuGetWriteIsDecidedOnEveryFileItWritesOrRemoves(t *testing.T) {
	e := newVersionEnv(t)

	t.Run("R-NUGET-013 a delete that would remove a file the caller may not delete is refused whole", func(t *testing.T) {
		before := e.files("nuget-ver", "acme.sym", "1.0.0")
		if a := e.send(e.r, "DELETE", "nuget-ver", "acme.sym/1.0.0", e.snu); a.Status != http.StatusForbidden {
			t.Errorf("snu, DELETE of acme.sym 1.0.0: %d, want 403\n%s", a.Status, a.Body)
		}
		if after := e.files("nuget-ver", "acme.sym", "1.0.0"); after != before || before < 3 {
			t.Errorf("acme.sym 1.0.0 holds %d files after the refused delete, %d before; want them all", after, before)
		}
	})

	t.Run("R-NUGET-013 a push whose generated .nuspec the caller may not write is refused whole", func(t *testing.T) {
		if a := e.push(e.r, "nuget-ver", e.snu, nupkg(t, pkgSpec{id: "Acme.Nusp", version: "1.0.0"})); a.Status != http.StatusForbidden {
			t.Errorf("snu, push of Acme.Nusp: %d, want 403\n%s", a.Status, a.Body)
		}
		if n := e.files("nuget-ver", "acme.nusp", "1.0.0"); n != 0 {
			t.Errorf("acme.nusp 1.0.0 holds %d files after the refused push, want 0", n)
		}
	})

	t.Run("R-NUGET-013 a delete decides every file the version's folder holds, one the format does not name included", func(t *testing.T) {
		// An embedded file below the version's folder, as R-NUGET-024 will store one (no route stores it yet, so the row is
		// written here, as a copy of the .nuspec's item). The delete names the folder; the host decides its files.
		e.mustPush(e.r, "nuget-ver", e.setup, nupkg(t, pkgSpec{id: "Acme.Fold", version: "1.0.0"}))
		e.st.Exec(`INSERT INTO items (repo_id, path, parent_path, kind, blob_sha256, size, media_type)
			SELECT i.repo_id, 'acme.fold/1.0.0/.embedded/icon', 'acme.fold/1.0.0/.embedded', 'file', i.blob_sha256, i.size, i.media_type
			  FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = 'nuget-ver' AND i.path = 'acme.fold/1.0.0/acme.fold.nuspec'`)
		before := e.files("nuget-ver", "acme.fold", "1.0.0")
		if a := e.send(e.r, "DELETE", "nuget-ver", "acme.fold/1.0.0", e.emb); a.Status != http.StatusForbidden {
			t.Errorf("emb, DELETE of acme.fold 1.0.0: %d, want 403\n%s", a.Status, a.Body)
		}
		if after := e.files("nuget-ver", "acme.fold", "1.0.0"); after != before || before != 3 {
			t.Errorf("acme.fold 1.0.0 holds %d files after the refused delete, %d before; want the three", after, before)
		}
		if n := e.versions("nuget-ver", "acme.fold"); n != 1 {
			t.Errorf("acme.fold holds %d versions after the refused delete, want 1", n)
		}
	})
}
