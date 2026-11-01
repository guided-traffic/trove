//go:build integration

package npm

import (
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/npmstub/corpus"
)

// TestR_AUTHZ_013_ASetupTokenOfAVirtualRepositoryReadsThroughIt is the token the client
// setup page creates for a virtual repository: read on that repository only
// (Q-AUTHZ-163). Through the virtual repository it finds the package and its tarball
// in the local member; the member addressed directly stays outside the scope, and the
// token never reads what its owner cannot.
func TestR_AUTHZ_013_ASetupTokenOfAVirtualRepositoryReadsThroughIt(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	rep := r.start(1)[0]
	p := pkg{Name: "setup-probe", Version: "1.0.0"}
	r.publishTo(rep, rLocal, r.tokA, p)

	onlyAll := scope.Scope{Repositories: []scope.Grant{{Repositories: []string{rAll}, Actions: []string{"read"}}}}
	tok := r.st.Token(r.alice, onlyAll)

	doc := r.view(rep, rAll, p.Name, tok)
	if doc.Status != 200 {
		t.Fatalf("the document through %s: %d %s", rAll, doc.Status, doc.Body)
	}
	if vs := versionsOf(t, doc); len(vs) != 1 || vs[0] != "1.0.0" {
		t.Fatalf("versions %v", vs)
	}
	addr := tarballsOf(t, doc)["1.0.0"]
	if want := "/api/npm/" + rAll + "/" + p.Name + "/-/" + corpus.TarballFile(p.Name, "1.0.0"); !strings.HasSuffix(addr, want) {
		t.Fatalf("tarball %q", addr)
	}
	if got := r.install(rep, addr, tok); got.Status != 200 {
		t.Fatalf("the tarball through %s: %d %s", rAll, got.Status, got.Body)
	}

	// The scope names the virtual repository, not its members.
	if direct := r.view(rep, rLocal, p.Name, tok); direct.Status != 404 {
		t.Errorf("the member addressed directly: %d %s", direct.Status, direct.Body)
	}
	// It does not lift the rights of the owner: carol cannot read npm-local, so her token
	// finds nothing through npm-all either.
	carol := r.st.Token(r.carol, onlyAll)
	if got := r.view(rep, rAll, p.Name, carol); got.Status != 404 {
		t.Errorf("a token of a user who cannot read the member: %d %s", got.Status, got.Body)
	}
}
