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
	// families under /api/<format>/<key>/ and /files/<key>/.
	mux.HandleFunc("/api/v1/{rest...}", routed("management"))
	mux.HandleFunc("/api/{format}/{rest...}", routed("package"))
	mux.HandleFunc("/files/{key}/{rest...}", routed("package"))
	mux.HandleFunc("/", routed("other"))

	rows := []struct {
		path   string
		served bool
	}{
		{"/api/npm/npm-all/lodash", true},
		{"/api/npm/npm-all/%40acme%2Fsdk", true},
		{"/api/nuget/nuget-all/v3/index.json", true},
		{"/files/raw-all/a/b.txt", true},
		{"/files/raw-all/a%20b.txt", true},
		{"/api/v1/users", false},
		{"/api/%761/users", false},
		{"/api/%76%31/users", false},
		{"/api/%761/bastion/handshake", false},
		{"/api/v%31/bastion/feed", false},
		{"/api/V1/users", true}, // the router is case-sensitive: this is a package path there as well
		{"/api/np%6d/npm-all/lodash", false},
		{"/api/npm/npm%2Dall/lodash", false},
		{"/api/npm/%6Epm-all/lodash", false},
		{"/%61pi/npm/npm-all/lodash", false},
		{"/%66iles/raw-all/a", false},
		{"/files/raw%2Dall/a", false},
		{"/api/./v1/users", false},
		{"/api/../api/v1/users", false},
		{"/api/npm/npm-all/../../v1/users", false},
		{"/api//v1/users", false},
		{"/api/%2e/v1/users", false},
		{"/api/%2e%2e/v1/users", false},
		{"/api/v1", false},
		{"/files/raw-all/%2e%2e/%2e%2e/x", true}, // the router does not clean an encoded dot segment
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
