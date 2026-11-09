//go:build integration

package debvirtual

import (
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// R-DEB-025, R-DEB-026: the virtual repository's index lists the paragraphs of its local member and of its remote member, in member order,
// each with the member path as its Filename, and it is signed with the virtual repository's own key; a suite no member has contributes
// nothing and is no failure.
func TestR_DEB_025_TheIndexMergesTheMembersAndIsSignedWithItsOwnKey(t *testing.T) {
	e := newEnv(t, options{})
	if a := e.upload(rLocal, e.admin, "local-tool", "1.0-1", "stable"); a.Status != 201 {
		t.Fatalf("upload = %d %s", a.Status, a.Body)
	}
	testenv.WaitFor(t, "the virtual index to list the local package", 90*time.Second, func() bool {
		s, ok := e.suite(rApt, "stable")
		if !ok || s.Generation == 0 {
			return false
		}
		_, found := find(e.packages(rApt, "stable", e.admin), "local-tool", "1.0-1")
		return found
	})

	list := e.packages(rApt, "stable", e.admin)
	lp, _ := find(list, "local-tool", "1.0-1")
	if got := lp.Value("Filename"); got != "members/deb-local/pool/l/local-tool/local-tool_1.0-1_amd64.deb" {
		t.Errorf("the local member's Filename = %q", got)
	}
	hello, ok := find(list, "trove-hello", "")
	if !ok {
		t.Fatalf("the stub's trove-hello is not listed; %d paragraphs", len(list))
	}
	if !strings.HasPrefix(hello.Value("Filename"), "members/debian/pool/") || hello.Value("SHA256") == "" {
		t.Errorf("the remote member's paragraph = %v", hello.Fields)
	}
	// Member order: the local member's paragraphs come before the remote's.
	firstRemote, lastLocal := -1, -1
	for i, p := range list {
		switch {
		case strings.HasPrefix(p.Value("Filename"), "members/deb-local/"):
			lastLocal = i
		case strings.HasPrefix(p.Value("Filename"), "members/debian/") && firstRemote < 0:
			firstRemote = i
		}
	}
	if lastLocal < 0 || firstRemote < 0 || lastLocal > firstRemote {
		t.Errorf("the members are not in member order: last local %d, first remote %d", lastLocal, firstRemote)
	}

	rel := e.verifies(rApt, "stable")
	if rel.Value("Suite") != "stable" || rel.Value("Components") != "main" || rel.Value("Architectures") != "amd64" || rel.Value("Acquire-By-Hash") != "yes" {
		t.Errorf("the Release = %v", rel.Fields)
	}
	if _, has := rel.Get("Valid-Until"); has {
		t.Error("the Release has a Valid-Until although no member release has one")
	}
	// The virtual repository's key is its own: the local member's key does not verify its release.
	if s, ok := e.suite(rApt, "stable"); !ok || s.State != "current" {
		t.Errorf("state = %+v", s)
	}
}
