//go:build integration

package ociremote

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/ocistub"
)

// Phase B of WP8: the remote class through the real /v2/ mount, and the findings of the re-audit.

// scoped is a trove token whose grant reads the repository only for the packages that match: the patterns of include and exclude.
func (e *renv) scoped(name, repo string, include, exclude []string) string {
	e.t.Helper()
	p := e.st.User(name)
	e.st.Policy("p-"+name, []string{"read"}, []string{repo}, p)
	return e.st.Token(p, scope.Scope{Repositories: []scope.Grant{{
		Repositories: []string{"it/" + repo}, Packages: scope.Patterns{Include: include, Exclude: exclude}, Actions: []string{"read"},
	}}})
}

// On a Docker Hub remote a read is decided on both spellings of the name, `nginx` and `library/nginx` (Q-OCI-129): the cache, the populate
// check and the screens use the second, the client types either. An exclusion written for one spelling holds whichever is typed, in both
// directions, for an image that is not cached yet and for one that is; a grant has to name both, and one that names one refuses
// consistently, cold or warm.
func TestR_OCI_025_ADecisionOnADockerHubRemoteIsMadeOnBothSpellings(t *testing.T) {
	e := newEnv(t, options{})
	// The people are made before the replica starts: its policy snapshot holds them from the first request.
	tokens := map[string]string{
		"exclude library/nginx": e.scoped("hubexlib", rHub, []string{"*", "*/*"}, []string{"library/nginx"}),
		"exclude nginx":         e.scoped("hubexshort", rHub, []string{"*", "*/*"}, []string{"nginx"}),
		"grant nginx":           e.scoped("hubshort", rHub, []string{"nginx"}, nil),
		"grant library/*":       e.scoped("hublib", rHub, []string{"library/*"}, nil),
		"grant both":            e.scoped("hubboth", rHub, []string{"nginx", "library/nginx"}, nil),
	}
	rep := e.start(1)[0]
	check := func(phase string, whos ...string) {
		t.Helper()
		for _, who := range whos {
			tok := tokens[who]
			for _, name := range []string{"nginx", "library/nginx"} {
				for _, path := range []string{"/manifests/1", "/tags/list"} {
					a := e.doAs(rep, tok, "pull", http.MethodGet, rHub, name+path)
					if want := who == "grant both"; (a.Status == 200) != want {
						t.Errorf("%s: %s, %s%s: %d %s, want ok %v", phase, who, name, path, a.Status, a.Body, want)
					}
				}
			}
		}
	}
	refused := []string{"exclude library/nginx", "exclude nginx", "grant nginx", "grant library/*"}
	check("cold", refused...)
	if got := e.hub.Counts().ManifestGets("library/nginx", "1"); got != 0 {
		t.Errorf("%d upstream requests for a caller who may not read the image", got)
	}
	check("cold", "grant both")
	// Someone who may read both fills the cache; the same answers hold for the cached copy.
	if a := e.doAs(rep, e.reader, "pull", http.MethodGet, rHub, "library/nginx/manifests/1"); a.Status != 200 {
		t.Fatalf("the reader: %d %s", a.Status, a.Body)
	}
	check("warm", append(refused, "grant both")...)
	if got := e.hub.Counts().ManifestGets("library/nginx", "1"); got != 1 {
		t.Errorf("%d upstream requests, want the reader's one", got)
	}
}

// A rule written for either spelling holds for both: an exclude pattern for nginx hides library/nginx as well, and the reverse.
func TestR_OCI_025_AnExcludeForOneSpellingHoldsForTheOther(t *testing.T) {
	e := newEnv(t, options{})
	rep := e.start(1)[0]
	for _, name := range []string{"nginx", "library/nginx"} {
		for _, path := range []string{"/manifests/1", "/tags/list", "/blobs/" + sha("x")} {
			if a := e.get(rep, rHubX, name+path); a.Status != http.StatusNotFound {
				t.Errorf("%s%s: %d %s, want 404", name, path, a.Status, a.Body)
			}
		}
	}
	if n := e.hub.Counts().Total(); n != 0 {
		t.Errorf("the upstream got %d requests for an excluded name", n)
	}
	// Another name of the same repository is served.
	if a := e.get(rep, rHubX, "alpine/manifests/3.19"); a.Status != 200 {
		t.Errorf("alpine: %d %s", a.Status, a.Body)
	}
}

func sha(string) string { return "sha256:" + strings.Repeat("0", 64) }

// A stood 401 of the revalidation does not mark the image gone: the stored copy keeps being served, and so do the manifests and blobs
// of the image (R2-1, R-REPO-029).
func TestR_OCI_021_AnImageTheRegistryDeniesOnRevalidationKeepsServingFromTheCache(t *testing.T) {
	e := newEnv(t, options{})
	rep := e.start(1)[0]
	first := e.get(rep, rFast, "acme/a/manifests/1.0")
	if first.Status != 200 {
		t.Fatalf("%d %s", first.Status, first.Body)
	}
	img, _ := e.stub.Image("acme/a", "1.0")
	e.blob(rep, rFast, "acme/a", img.Layers[0].Digest)
	if err := e.stub.SetMode(ocistub.Mode{PrivateRepos: []string{"acme/a"}}); err != nil {
		t.Fatal(err)
	}
	e.stub.RevokeTokens()
	eventually(t, 30*time.Second, "the expired tag to be revalidated against a registry that denies the image", func() bool {
		a := e.get(rep, rFast, "acme/a/manifests/1.0")
		return a.Status == 200 && e.stub.Counts().WithStatus(http.StatusUnauthorized) >= 2
	})
	if a := e.get(rep, rFast, "acme/a/manifests/1.0"); a.Status != 200 || string(a.Body) != string(first.Body) {
		t.Errorf("the stored tag: %d", a.Status)
	}
	if a := e.get(rep, rFast, "acme/a/manifests/"+first.Header.Get("Docker-Content-Digest")); a.Status != 200 {
		t.Errorf("the manifest by digest: %d", a.Status)
	}
	if a := e.get(rep, rFast, "acme/a/blobs/"+img.Layers[0].Digest); a.Status != 200 {
		t.Errorf("the blob: %d %s", a.Status, a.Body)
	}
}
