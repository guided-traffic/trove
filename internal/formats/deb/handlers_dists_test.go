package deb

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
)

// R-DEB-003, R-DEB-004: a path below dists/ that no generation published is 404; the read is decided on a path that belongs to no
// package.
func TestR_DEB_003_DistsOfALocalRepository(t *testing.T) {
	h, _ := newHost(t)
	for _, path := range []string{
		"dists/stable/InRelease", "dists/stable/main/binary-amd64/Packages.gz",
		"dists/stable/main/binary-amd64/by-hash/SHA256/" + strings.Repeat("0", 64),
	} {
		r, err := h.Serve(context.Background(), http.MethodGet, path, nil, false)
		if err != nil || r.Code != http.StatusNotFound {
			t.Fatalf("%s = %v %v", path, r, err)
		}
	}
	calls := h.Authorizations()
	if len(calls) != 3 {
		t.Fatalf("%d authorizations", len(calls))
	}
	for _, a := range calls {
		if a.Target.Package != "" || len(a.Target.Actions) != 1 || a.Target.Actions[0] != formats.ActionRead {
			t.Errorf("authorized %+v", a.Target)
		}
	}
}
