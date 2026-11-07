package engine

import (
	"net/http"
	"testing"
)

// TestR_BAS_018_TheAddressedRepositoryIsTheQualifiedName: a protocol path names format, project and key, a raw path
// project and key; the same key in two projects is two repositories, and the answer key of one is not the other's
// (Q-REPO-212).
func TestR_BAS_018_TheAddressedRepositoryIsTheQualifiedName(t *testing.T) {
	rows := []struct {
		path, format, repo string
	}{
		{"/api/npm/web/npm-all/lodash", "npm", "web/npm-all"},
		{"/api/npm/data/npm-all/lodash", "npm", "data/npm-all"},
		{"/api/nuget/web/nuget-all/v3/index.json", "nuget", "web/nuget-all"},
		{"/files/web/raw-all/a/b.txt", "files", "web/raw-all"},
		{"/files/data/raw-all", "files", "data/raw-all"},
	}
	for _, row := range rows {
		format, repo, ok := classifyPath(row.path)
		if !ok || format != row.format || repo != row.repo {
			t.Errorf("classifyPath(%q) = %q %q %v, want %q %q", row.path, format, repo, ok, row.format, row.repo)
		}
	}
	if shapeKey("cred", "web/npm-all") == shapeKey("cred", "data/npm-all") {
		t.Error("the shape answer of one project's repository is the answer of another project's repository of the same key")
	}
}

// TestR_BAS_018_ARepositoryOfAnotherProjectIsAnotherRepository: a caller who reads web/npm-all in full has no answer
// for data/npm-all, which the main installation's check answers as a missing repository; the Bastion neither reuses
// the shape nor serves the cached bytes of the first (Q-REPO-212).
func TestR_BAS_018_ARepositoryOfAnotherProjectIsAnotherRepository(t *testing.T) {
	s := newSiteHarness(t, options{})
	const tgz = "/api/npm/web/npm-all/lodash/-/lodash-1.0.0.tgz"
	s.site.artifacts[tgz] = []byte("bytes")
	for i := 0; i < 2; i++ {
		if w := s.get(tgz, "Authorization", bearer(s.full)); w.Code != http.StatusOK {
			t.Fatalf("the repository of the site: %d %s", w.Code, w.Body)
		}
	}
	w := s.get("/api/npm/data/npm-all/lodash/-/lodash-1.0.0.tgz", "Authorization", bearer(s.full))
	if w.Code != http.StatusNotFound || problemCode(w) == "bastion_not_served" {
		t.Fatalf("the same key in another project: %d %s", w.Code, w.Body)
	}
}
