//go:build integration

package bastion

import (
	"net/http"
	"testing"
)

// TestR_BAS_018_R_BAS_026_TheSameKeyInTwoProjectsIsTwoRepositoriesAtTheBastion: a Bastion keeps the answers and the cached
// bytes of it/npm-all apart from those of data/npm-all, a repository of the same key in another project that the
// caller holds no grant on (Q-REPO-212). The first is served from the cache; the second is asked at the main
// installation and answers as a repository the caller cannot see; the first stays served.
func TestR_BAS_018_R_BAS_026_TheSameKeyInTwoProjectsIsTwoRepositoriesAtTheBastion(t *testing.T) {
	w := newWorld(t, worldOptions{})
	w.content()
	w.st.Repository("data/npm-all", "local", "npm", "{}")
	b := w.newBastion(t, bastionOptions{}).wait()
	r := b.reps[0]
	c := callOpts{token: w.full.token, addr: "203.0.113.31"}
	doc := "/api/npm/" + npmAll + "/" + escapeNPM("@acme/sdk")
	other := "/api/npm/data/npm-all/" + escapeNPM("@acme/sdk")

	for i := 0; i < 2; i++ {
		if got := r.do(t, "GET", doc, c); got.Status != http.StatusOK {
			t.Fatalf("%s, request %d: %d %.200s", npmAll, i+1, got.Status, got.Body)
		}
	}
	m := w.front.Mark()
	if got := r.do(t, "GET", other, c); got.Status != http.StatusNotFound {
		t.Fatalf("data/npm-all for a caller without a grant there: %d %.200s", got.Status, got.Body)
	}
	if n := len(w.front.Forwards(m, "/api/")); n == 0 {
		t.Fatal("the Bastion answered data/npm-all from what it kept of it/npm-all")
	}
	if got := r.do(t, "GET", doc, c); got.Status != http.StatusOK {
		t.Fatalf("%s after the other project's refusal: %d", npmAll, got.Status)
	}
}
