//go:build integration

package registry

import (
	"net/http"
	"strings"
	"testing"
)

// R-OCI-029: on a bound host the links a handler writes name the image alone (EndpointFacts.Bound comes from the match), on the shared
// host they carry the project and the repository key.
func TestLinksOnABoundHostAreTheShortForm(t *testing.T) {
	e := newEnv(t, nil)
	img := newImage("links")
	for _, tag := range []string{"1", "2"} {
		if a := e.push(bearer(e.admin), "web/docker-local/app", tag, img); a.Code != http.StatusCreated {
			t.Fatalf("push %s: %d %s", tag, a.Code, a.Body.String())
		}
	}
	bound := e.send("mirror", req{method: "GET", path: "/v2/app/tags/list?n=1", host: mirrorHost, auth: bearer(e.admin)})
	link := bound.Header().Get("Link")
	if bound.Code != http.StatusOK || !strings.HasPrefix(link, "</v2/app/tags/list?") || strings.Contains(link, "web/docker-local") {
		t.Fatalf("bound host: %d Link %q", bound.Code, link)
	}
	shared := e.send("default", req{method: "GET", path: "/v2/web/docker-local/app/tags/list?n=1", auth: bearer(e.admin)})
	if link := shared.Header().Get("Link"); shared.Code != http.StatusOK || !strings.HasPrefix(link, "</v2/web/docker-local/app/tags/list?") {
		t.Fatalf("shared host: %d Link %q", shared.Code, link)
	}
}
