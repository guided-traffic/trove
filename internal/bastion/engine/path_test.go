package engine

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestR_BAS_003_R_BAS_018_APathIsClassifiedAsTheMainInstallationsRouterWillRouteIt: the router of the main
// installation decodes every segment before it compares it, so /api/%761/users reaches the management API there.
// The Bastion classifies by the same reading or refuses what it cannot read the same way: for every spelling,
// a path the Bastion serves is a path the router sends to the package families (Q-BAS-121, security finding P1).
func TestR_BAS_003_R_BAS_018_APathIsClassifiedAsTheMainInstallationsRouterWillRouteIt(t *testing.T) {
	mux := http.NewServeMux()
	routed := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(name)) }
	}
	// The shape of the main installation's routes that matter: the management API under /api/v1/, the package
	// families under /api/<format>/<project>/<key>/ and
	// /files/<project>/<key>/.
	mux.HandleFunc("/api/v1/{rest...}", routed("management"))
	for _, format := range []string{"npm", "nuget", "mk", "np%m", "np"} {
		mux.HandleFunc("/api/"+format+"/{project}/{key}/{rest...}", routed("package"))
	}
	mux.HandleFunc("/files/{project}/{key}/{rest...}", routed("package"))
	mux.HandleFunc("/", routed("other"))

	rows := []struct {
		path   string
		served bool
	}{
		{"/api/npm/web/npm-all/lodash", true},
		{"/api/npm/web/npm-all/%40acme%2Fsdk", true},
		{"/api/nuget/web/nuget-all/v3/index.json", true},
		{"/api/npm/other/npm-all/lodash", true}, // the same key in a second project is another repository
		{"/files/web/raw-all/a/b.txt", true},
		{"/files/web/raw-all/a%20b.txt", true},
		{"/api/npm/web/npm-all/", true},
		{"/api/npm/npm-all/l", false}, // three segments after /api/npm: a protocol path needs the project and the key
		{"/api/npm/web", false},
		{"/api/npm/web/", false},
		{"/api/npm/web/npm-all~cache/lodash", false},
		{"/files/raw-all/a/b.txt", false},
		{"/files/web", false},
		{"/api/v1/users", false},
		{"/api/v1/web/users", false},
		{"/api/%761/web/users", false},
		{"/api/%76%31/web/users", false},
		{"/api/%761/bastion/handshake", false},
		{"/api/v%31/bastion/feed", false},
		{"/api/np%6d/web/npm-all/lodash", false},
		{"/api/npm/we%62/npm-all/lodash", false},
		{"/api/npm/web/npm%2Dall/lodash", false},
		{"/api/npm/web/%6Epm-all/lodash", false},
		{"/%61pi/npm/web/npm-all/lodash", false},
		{"/%66iles/web/raw-all/a", false},
		{"/files/we%62/raw-all/a", false},
		{"/files/web/raw%2Dall/a", false},
		{"/api/./v1/users", false},
		{"/api/../api/v1/users", false},
		{"/api/npm/web/npm-all/../../v1/users", false},
		{"/api//v1/users", false},
		{"/api/%2e/v1/users", false},
		{"/api/%2e%2e/v1/users", false},
		{"/api/v1", false},
		{"/files/web/raw-all/%2e%2e/%2e%2e/x", true}, // the router does not clean an encoded dot segment
	}
	for _, row := range rows {
		t.Run(row.path, func(t *testing.T) {
			_, _, ok := classifyPath(row.path)
			if ok != row.served {
				t.Fatalf("classifyPath(%q) = %v, want %v", row.path, ok, row.served)
			}
			if !ok {
				return
			}
			req := httptest.NewRequest(http.MethodGet, row.path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || rec.Body.String() != "package" {
				t.Fatalf("the Bastion serves %q, the router answers %d %q", row.path, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestR_BAS_003_AnEncodedSpellingOfTheManagementAPIIsNeverForwarded: the request flow answers 404
// bastion_not_served and sends nothing to the main installation, for the spellings of the proof of concept.
func TestR_BAS_003_AnEncodedSpellingOfTheManagementAPIIsNeverForwarded(t *testing.T) {
	s := newSiteHarness(t, options{})
	for _, p := range []string{"/api/%761/users", "/api/%76%31/users", "/api/%761/bastion/handshake", "/api/%761/bastion/feed"} {
		w := s.get(p, "Authorization", bearer(s.full))
		if w.Code != http.StatusNotFound || problemCode(w) != "bastion_not_served" {
			t.Fatalf("%s: %d %s", p, w.Code, w.Body)
		}
	}
	if n := s.main.count(); n != 0 {
		t.Fatalf("the main installation saw %d requests", n)
	}
}
