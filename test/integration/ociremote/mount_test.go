//go:build integration

package ociremote

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// A cross-repository mount whose source is a Docker Hub remote's cache is decided on both spellings of the source's name, like a read
// (Q-OCI-129, R3-3): an exclusion written for either spelling of the source holds for a mount whichever spelling `from` names.
//
// Mechanism of R-OCI-012, not a proof of its criteria: it checks one rule of the mount source or one refusal; the criteria are
// the grant-limited 201 and 202 outcomes, the mount without from and the absence of uploaded bytes.
func TestAMountFromADockerHubRemoteIsDecidedOnBothSpellingsOfTheSource(t *testing.T) {
	e := newEnv(t, options{})
	e.st.Repository("oci-local", "local", "oci", "{}")
	person := func(name string, exclude ...string) string {
		p := e.st.User(name)
		e.st.Policy("r-"+name, []string{"read"}, []string{rHub}, p)
		e.st.Policy("w-"+name, []string{"read", "deploy"}, []string{"oci-local"}, p)
		return e.st.Token(p, scope.Scope{Repositories: []scope.Grant{
			{Repositories: []string{"it/" + rHub}, Packages: scope.Patterns{Include: []string{"*", "*/*"}, Exclude: exclude}, Actions: []string{"read"}},
			{Repositories: []string{"it/oci-local"}, Packages: scope.Patterns{Include: []string{"*", "*/*"}}, Actions: []string{"read", "deploy"}},
		}})
	}
	tokens := map[string]string{
		"free": person("mountfree"), "exclude library/nginx": person("mountexlib", "library/nginx"), "exclude nginx": person("mountexshort", "nginx"),
	}
	rep := e.start(1)[0]
	e.pull(rep, rHub, "library/nginx", "1") // the cache holds the image
	img, err := e.hub.Image("library/nginx", "1")
	if err != nil {
		t.Fatal(err)
	}
	digest := img.Blobs()[len(img.Blobs())-1]

	mount := func(who, from string) int {
		q := url.Values{"scope": {"repository:it/oci-local/app:pull,push", "repository:" + from + ":pull"}}
		a := e.st.Do(rep, http.MethodGet, "/v2/_token?"+q.Encode(), tokens[who], nil)
		if a.Status != http.StatusOK {
			t.Fatalf("token for %s: %d %s", who, a.Status, a.Body)
		}
		var tok struct{ Token string }
		if err := json.Unmarshal(a.Body, &tok); err != nil || tok.Token == "" {
			t.Fatalf("token: %v %s", err, a.Body)
		}
		path := "/v2/" + testenv.DefaultProject + "/oci-local/app/blobs/uploads/?mount=" + digest + "&from=" + from
		return e.st.Do(rep, http.MethodPost, path, tok.Token, nil).Status
	}
	for _, from := range []string{"it/" + rHub + "/library/nginx", "it/" + rHub + "/nginx"} {
		// Only the spelling the cache holds finds the blob; the other falls back to an upload session whatever the rules say. What the
		// rules decide is told by the same mount with the person who is not excluded.
		if got := mount("free", from); got != http.StatusCreated && got != http.StatusAccepted {
			t.Errorf("free, from %s: %d", from, got)
		}
	}
	if got := mount("free", "it/"+rHub+"/library/nginx"); got != http.StatusCreated {
		t.Fatalf("a mount of a cached blob of the remote: %d, want 201", got)
	}
	for _, who := range []string{"exclude library/nginx", "exclude nginx"} {
		if got := mount(who, "it/"+rHub+"/library/nginx"); got != http.StatusAccepted {
			t.Errorf("%s, from the canonical spelling: %d, want the fallback 202", who, got)
		}
	}
}
