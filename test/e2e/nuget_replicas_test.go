//go:build e2e

package e2e

// NuGet on interchangeable replicas (R-NUGET-030 to R-NUGET-032, exit criterion E3.6): every rendered document has the same
// body and the same weak ETag on every web replica of the scatter profile, a conditional request is answered 304 by any of
// them, and a push whose answer the edge drops is repeated by the client and exits 0. The closing run repeats the
// documents with Valkey stopped (nuget_closing_test.go).

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
)

// sameOnEveryReplica fetches one document until all three web replicas have answered it and requires one body and one
// ETag. kind says what the ETag is: a rendered document carries a weak one (R-NUGET-030, Q-NUGET-9), a stored file the
// strong ETag of its blob, the search none (it is volatile). A conditional request is then answered 304 by every replica.
func (fx *nugetFixture) sameOnEveryReplica(t *testing.T, path, kind string) {
	t.Helper()
	type seen struct{ body, etag string }
	byReplica := map[string]seen{}
	for range 60 {
		r := fx.adm.get(path).want(t, path, 200)
		sum := sha256.Sum256(r.Body)
		s := seen{body: hex.EncodeToString(sum[:]), etag: r.Header.Get("ETag")}
		if kind == "weak" && !strings.HasPrefix(s.etag, `W/"`) || kind == "strong" && !strings.HasPrefix(s.etag, `"`) {
			t.Fatalf("%s on %s: ETag %q is not a %s ETag", path, r.Replica(), s.etag, kind)
		}
		for other, o := range byReplica {
			if o != s {
				t.Fatalf("%s: %s answered %+v, %s answered %+v", path, r.Replica(), s, other, o)
			}
		}
		byReplica[r.Replica()] = s
		if len(byReplica) == 3 {
			break
		}
	}
	if len(byReplica) < 3 {
		t.Fatalf("%s: only %d web replica(s) answered in 60 requests; the scatter profile reaches three", path, len(byReplica))
	}
	if kind == "none" {
		return
	}
	var etag string
	for _, s := range byReplica {
		etag = s.etag
	}
	replicas := map[string]bool{}
	for range 60 {
		r := fx.adm.do(http.MethodGet, path, nil, "If-None-Match", etag).want(t, path+" with If-None-Match", 304)
		replicas[r.Replica()] = true
		if len(replicas) == 3 {
			return
		}
	}
	t.Fatalf("%s: the conditional requests reached %d replicas", path, len(replicas))
}

// documentsOnEveryReplica runs sameOnEveryReplica for the documents of one package of a repository: the service index,
// the version list, the index and a leaf of each of the three registration hives, the .nuspec and one search page.
func (fx *nugetFixture) documentsOnEveryReplica(t *testing.T, repo, id, version string) {
	t.Helper()
	lower := strings.ToLower(id)
	base := "/api/nuget/" + repo + "/"
	// path -> the ETag it carries; the .nuspec is not fetched first: the first answer of a cache miss within the
	// withheld tail carries the validator of the stored answer (Q-NUGET-86)
	docs := map[string]string{
		base + "v3/index.json":                                                       "weak",
		base + "v3/flatcontainer/" + lower + "/index.json":                           "weak",
		base + "v3/registration/" + lower + "/index.json":                            "weak",
		base + "v3/registration-gz/" + lower + "/index.json":                         "weak",
		base + "v3/registration-semver2/" + lower + "/index.json":                    "weak",
		base + "v3/registration-semver2/" + lower + "/" + version + ".json":          "weak",
		base + "v3/flatcontainer/" + lower + "/" + version + "/" + lower + ".nuspec": "strong",
		base + "v3/search?q=" + lower + "&take=5":                                    "none",
	}
	for path, kind := range docs {
		t.Run("R-NUGET-030 E3.6 "+strings.TrimPrefix(path, base), func(t *testing.T) { fx.sameOnEveryReplica(t, path, kind) })
	}
}

// replicaScenario pushes two versions of a package of the group and checks its documents on every replica.
func (fx *nugetFixture) replicaScenario(t *testing.T, group string) {
	t.Helper()
	auth := nugetFeed(t, "localhost", nugetAuthKey)
	id := fx.pkgID(group)
	c := fx.client(t, 0, "replicas-setup-"+strings.ToLower(group))
	c.config(auth, true)
	for _, v := range []string{"1.0.0", "1.1.0"} {
		file := c.place(id, v, fx.nugetPack(t, id, v, "replicas "+v))
		c.mustPush("push "+v, "dotnet", "nuget", "push", file, "--source", auth, "--api-key", "az")
	}
	fx.documentsOnEveryReplica(t, nugetAuthKey, id, "1.1.0")
}

func TestNuGetReplicas(t *testing.T) {
	fx := nugetFx(t)
	fx.replicaScenario(t, "Replicas")
}

// E3.6, R-NUGET-032, Q-NUGET-6: the edge drops the answer of a push the replica has committed; the client waits for it,
// gives up on the request, and repeats the push, which reaches another replica and finds the version stored. The identical
// push answers 201, so `dotnet nuget push` exits 0. The evidence that the push was repeated is the counter of the
// replays (trove_nuget_push_replayed_total, summed over the replicas): it rises, and the version exists once.
func TestNuGetPushRetry(t *testing.T) {
	fx := nugetFx(t)
	auth := nugetFeed(t, "localhost", nugetAuthKey)
	t.Run("R-NUGET-032 R-NUGET-030 E3.6 a dotnet nuget push whose answer the edge drops is repeated by the client and exits 0", func(t *testing.T) {
		id := fx.pkgID("Dropped")
		c := fx.client(t, 0, "dropped")
		c.config(auth, true)
		file := c.place(id, "1.0.0", fx.nugetPack(t, id, "1.0.0", "the answer of this push is dropped"))
		before := sumSeries(t, "trove_nuget_push_replayed_total")
		undo := dropOneAnswerOf(t, "path_end /v3/package /v3/package/", "201")
		defer undo()
		out, err := c.dotnetPush(file, auth, "az")
		if err != nil || !pushedRE.MatchString(out) {
			t.Fatalf("dotnet nuget push exited with %v after the edge dropped the answer of its PUT:\n%s", err, out)
		}
		if after := sumSeries(t, "trove_nuget_push_replayed_total"); after < before+1 {
			t.Fatalf("trove_nuget_push_replayed_total rose from %v to %v, want at least one replay (the edge has one stick table per HAProxy pod, so each pod drops one answer): the client did not repeat the push, so the edge dropped nothing and the test proves nothing:\n%s", before, after, out)
		}
		if got := fx.flatVersions(t, nugetAuthKey, id); len(got) != 1 || got[0] != "1.0.0" {
			t.Fatalf("flat container versions = %v, want [1.0.0]", got)
		}
	})
}
