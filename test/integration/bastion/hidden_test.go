//go:build integration

package bastion

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// hiddenRule is an exposure rule of the default endpoint on npm-all that hides the versions in exclude.
func hiddenRule(exclude ...string) map[string]any {
	if exclude == nil {
		exclude = []string{}
	}
	return map[string]any{
		"name": "bastion-it", "repository": npmAll, "enabled": true,
		"packages":    map[string]any{"include": []string{"@acme/*", "plain-lib"}, "exclude": []string{}},
		"versions":    map[string]any{"include": []string{}, "exclude": exclude, "range": ""},
		"prereleases": true,
		"distTags":    map[string]any{"include": []string{}, "exclude": []string{}},
	}
}

func hasVersion(t *testing.T, doc []byte, version string) bool {
	t.Helper()
	var d struct {
		Versions map[string]json.RawMessage `json:"versions"`
	}
	if err := json.Unmarshal(doc, &d); err != nil {
		t.Fatalf("%v: %.200s", err, doc)
	}
	_, ok := d.Versions[version]
	return ok
}

// TestR_BAS_022_R_BAS_023_R_BAS_026_AVersionHiddenByAnExposureRuleIsHiddenAtTheBastion: the default endpoint of
// the main installation serves npm-all by exposure rules. A rule hides version 2.0.0 after the Bastion has cached
// the package document and the tarball: within the bound the document no longer lists it and the tarball is
// 404, and a Bastion that never saw them is told the same.
func TestR_BAS_022_R_BAS_023_R_BAS_026_AVersionHiddenByAnExposureRuleIsHiddenAtTheBastion(t *testing.T) {
	w := newWorld(t, worldOptions{})
	w.content()
	w.restartMain(`{"default":{"exposure":"rules"}}`)
	rules := "/api/v1/endpoints/default/exposure-rules"
	w.mustAPI("POST", rules, 201, hiddenRule())
	b := w.newBastion(t, bastionOptions{Replicas: 2}).wait()
	doc := "/api/npm/" + npmAll + "/" + escapeNPM("@acme/sdk")
	tgz2 := tarballPath(npmAll, "@acme/sdk", "2.0.0")
	c := callOpts{token: w.full.token, addr: "198.51.100.171"}

	for _, r := range b.reps {
		if got := r.do(t, "GET", doc, c); got.Status != http.StatusOK || !hasVersion(t, got.Body, "2.0.0") {
			t.Fatalf("%s: before the rule: %d %.200s", r.name, got.Status, got.Body)
		}
	}
	warm(t, b, c, tgz2)

	cur := w.mustAPI("GET", rules+"/bastion-it", 200, nil)
	made := time.Now()
	w.mustAPI("PUT", rules+"/bastion-it", 200, hiddenRule("2.0.0"), "If-Match", cur.Header.Get("ETag"))
	refusedSince(t, made, b, tgz2, c, http.StatusNotFound)
	for _, r := range b.reps {
		testWait(t, r.name+" lists the version no more", func() bool {
			got := r.do(t, "GET", doc, c)
			return got.Status == http.StatusOK && !hasVersion(t, got.Body, "2.0.0") && hasVersion(t, got.Body, "1.0.0")
		})
		if got := r.do(t, "GET", tarballPath(npmAll, "@acme/sdk", "1.0.0"), c); got.Status != http.StatusOK {
			t.Fatalf("%s: the version that stays: %d", r.name, got.Status)
		}
	}

	// A Bastion that has cached nothing.
	fresh := w.newBastion(t, bastionOptions{}).wait().reps[0]
	if got := fresh.do(t, "GET", doc, callOpts{token: w.part.token, addr: "198.51.100.172"}); got.Status != http.StatusOK || bytes.Contains(got.Body, []byte(`"2.0.0":`)) {
		t.Fatalf("a cold Bastion lists the hidden version: %d", got.Status)
	}
	if got := fresh.do(t, "GET", tgz2, c); got.Status != http.StatusNotFound {
		t.Fatalf("a cold Bastion serves the hidden tarball: %d", got.Status)
	}
}
