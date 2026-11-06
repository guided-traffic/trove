//go:build integration

package nuget

import (
	"net/http"
	"testing"
)

// documentPaths are one request of each kind of rendered document and file of an ID, in
// every hive (R-NUGET-008, R-NUGET-009).
func documentPaths(id string) []string {
	return []string{
		"v3/flatcontainer/" + id + "/index.json",
		"v3/flatcontainer/" + id + "/1.2.3/" + id + ".1.2.3.nupkg",
		"v3/flatcontainer/" + id + "/1.2.3/" + id + ".nuspec",
		"v3/registration/" + id + "/index.json",
		"v3/registration-gz/" + id + "/index.json",
		"v3/registration-semver2/" + id + "/index.json",
		"v3/registration-gz/" + id + "/page/1.0.0/2.0.0.json",
		"v3/registration-semver2/" + id + "/1.2.3.json",
	}
}

// TestR_NUGET_008_AnIDThatHoldsNoVersionIs404 runs the document routes through the whole
// stack - the dispatcher, the authentication and the decision point, the data port on
// PostgreSQL - for an ID that no repository row holds: a reader gets the 404 of the format
// in its one-line shape, with GET and HEAD alike, and a client without a credential is
// challenged first (R-NUGET-002, R-NUGET-012). The documents of stored versions need a push
// and are asserted with it.
func TestR_NUGET_008_AnIDThatHoldsNoVersionIs404(t *testing.T) {
	e := newEnv(t)
	a := e.replica("web-a")
	for _, path := range documentPaths("acme.widgets") {
		t.Run(path, func(t *testing.T) {
			ans := e.get(a, "nuget-local", path, e.tokReader)
			if ans.Status != http.StatusNotFound || line(t, ans) != "not_found" {
				t.Fatalf("GET: %d %s", ans.Status, ans.Body)
			}
			head := e.st.Do(a, http.MethodHead, "/api/nuget/it/nuget-local/"+path, e.tokReader, nil)
			if head.Status != http.StatusNotFound || len(head.Body) != 0 {
				t.Fatalf("HEAD: %d with %d body bytes", head.Status, len(head.Body))
			}
			if anon := e.get(a, "nuget-local", path, ""); anon.Status != http.StatusUnauthorized || anon.Header.Get("WWW-Authenticate") != `Basic realm="trove"` {
				t.Fatalf("without a credential: %d, WWW-Authenticate %q", anon.Status, anon.Header.Get("WWW-Authenticate"))
			}
			if none := e.get(a, "nuget-local", path, e.tokNobody); none.Status != http.StatusForbidden && none.Status != http.StatusNotFound {
				t.Fatalf("without a grant: %d %s", none.Status, none.Body)
			}
		})
	}
}
