//go:build integration

package registry

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_AUTHZ_005_APathPatternOnATagHidesItFromThePackageAPI: a version of an image is a tag, and the decision point decides it
// on the tag's path "<name>/_tags/<tag>" as well as on the tags list (Q-AUTHZ-255, Q-AUTHZ-258). A caller whose grant excludes one
// tag's path meets the image with the other tag in the package API, as the tags list of /v2/ shows it; a caller without the
// pattern meets both.
func TestR_AUTHZ_005_APathPatternOnATagHidesItFromThePackageAPI(t *testing.T) {
	var tagexc string
	e := newEnv(t, func(e *env, _ *testenv.ReplicaOptions) {
		p := e.st.User("tagexc")
		e.st.Policy("oci-tag-exclude", []string{"read"}, []string{"web/docker-local"}, p)
		e.st.Exec(`INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern) SELECT id, 'path', 'exclude', 'team/app/_tags/v2' FROM authz_policy WHERE name = 'oci-tag-exclude'`)
		tagexc = e.st.Token(p, scope.Default())
	})
	for _, tag := range []string{"v1", "v2"} {
		if a := e.push(bearer(e.admin), "web/docker-local/team/app", tag, newImage("team/app "+tag)); a.Code != http.StatusCreated {
			t.Fatalf("push %s: %d %s", tag, a.Code, a.Body.String())
		}
	}
	versions := func(token string) []string {
		t.Helper()
		a := e.st.Do(e.rep, "GET", "/api/v1/packages/lookup?format=oci&name=team/app", token, nil)
		var p struct {
			ID string `json:"id"`
		}
		if a.Status != http.StatusOK || json.Unmarshal(a.Body, &p) != nil || p.ID == "" {
			t.Fatalf("lookup: %d %s", a.Status, a.Body)
		}
		l := e.st.Do(e.rep, "GET", "/api/v1/packages/"+p.ID+"/versions", token, nil)
		var vs struct {
			Items []struct {
				Version string `json:"version"`
			} `json:"items"`
		}
		if l.Status != http.StatusOK || json.Unmarshal(l.Body, &vs) != nil {
			t.Fatalf("versions: %d %s", l.Status, l.Body)
		}
		var out []string
		for _, v := range vs.Items {
			out = append(out, v.Version)
		}
		slices.Sort(out)
		return out
	}
	if got := versions(tagexc); !slices.Equal(got, []string{"v1"}) {
		t.Errorf("the caller whose grant excludes team/app/_tags/v2 sees the versions %v, want [v1]", got)
	}
	if got := versions(e.reader); !slices.Equal(got, []string{"v1", "v2"}) {
		t.Errorf("a caller without the pattern sees the versions %v, want [v1 v2]", got)
	}
}
