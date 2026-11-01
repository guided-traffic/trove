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

// TestR_AUTHZ_013_ASetupTokenFillsAndReadsARemoteMemberThroughTheVirtualRepository: the
// populate check of a remote member decides on the virtual repository the client
// addressed, so the token of the setup page installs a package whose document and tarball
// are not cached yet, from the upstream, through the virtual repository (Q-AUTHZ-163). The
// remote member addressed directly stays outside the scope.
func TestR_AUTHZ_013_ASetupTokenFillsAndReadsARemoteMemberThroughTheVirtualRepository(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	rep := r.start(1)[0]
	p := upstreamPackage(t, 2)
	tok := r.st.Token(r.alice, scope.Scope{Repositories: []scope.Grant{{Repositories: []string{rAll}, Actions: []string{"read"}}}})

	doc := r.view(rep, rAll, p.Name, tok)
	if doc.Status != 200 || len(versionsOf(t, doc)) != len(p.Versions) {
		t.Fatalf("the document through %s: %d %.300s", rAll, doc.Status, doc.Body)
	}
	v := p.Versions[0]
	got := r.install(rep, tarballsOf(t, doc)[v.Version], tok)
	if got.Status != 200 || integrityOf(got.Body) != v.Dist.Integrity {
		t.Fatalf("the uncached tarball through %s: %d %.300s", rAll, got.Status, got.Body)
	}
	if from := got.Header.Get("Trove-Resolved-From"); from != rRemote {
		t.Errorf("Trove-Resolved-From = %q", from)
	}
	if direct := r.view(rep, rRemote, p.Name, tok); direct.Status != 404 {
		t.Errorf("the remote member addressed directly: %d %.200s", direct.Status, direct.Body)
	}
}

// TestR_AUTHZ_013_ASetupTokenRunsTheAuditThroughTheVirtualRepository: the forwarding member of a
// virtual repository is asked for read on behalf of the virtual repository the client
// addressed, so the token of the setup page, scoped to that repository by its exact key, runs
// `npm audit` through it. The member addressed directly stays outside the scope (Q-ARCH-252).
func TestR_AUTHZ_013_ASetupTokenRunsTheAuditThroughTheVirtualRepository(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	rep := r.start(1)[0]
	if a := r.view(rep, rRemote, "lodash", r.tokA); a.Status != 200 {
		t.Fatalf("fetch lodash: %d %s", a.Status, a.Body)
	}
	r.stub.Reset()
	tok := r.st.Token(r.alice, scope.Scope{Repositories: []scope.Grant{{Repositories: []string{rAll}, Actions: []string{"read"}}}})

	a := r.postAudit(rep, rAll, tok, `{"lodash":["4.17.20"]}`, false)
	if a.Status != 200 {
		t.Fatalf("the audit through %s: %d %s", rAll, a.Status, a.Body)
	}
	if got := askedOf(t, r.stub.Audits(), 0); strings.Join(got, ",") != "lodash" {
		t.Fatalf("the provider was asked about %v", got)
	}
	if direct := r.postAudit(rep, rRemote, tok, `{"lodash":["4.17.20"]}`, false); direct.Status != 404 {
		t.Errorf("the audit addressed to the remote member directly: %d %s", direct.Status, direct.Body)
	}
}
