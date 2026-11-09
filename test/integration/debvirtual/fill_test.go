//go:build integration

package debvirtual

import "testing"

// R-DEB-027, R-AUTHZ-008: with cacheFillAction deploy on the remote member, a reader (who holds read only) gets 404 for a member path of a
// file the member has not stored, and the file is served once an administrator, who may fill, has stored it.
func TestR_DEB_027_AReaderWhoMayNotFillGetsA404ForAFileTheRemoteMemberHasNotStored(t *testing.T) {
	e := newEnv(t, options{fillAction: "deploy"})
	e.ready("local-tool")
	hello, _ := find(e.packages(rApt, "stable", e.admin), "trove-hello", "")
	path := hello.Value("Filename")
	if a := e.get(rApt, path, e.alice); a.Status != 404 {
		t.Errorf("a reader, file not stored: %d", a.Status)
	}
	if a := e.get(rApt, path, e.admin); a.Status != 200 {
		t.Fatalf("an administrator fills the cache: %d %s", a.Status, a.Body)
	}
	if a := e.get(rApt, path, e.alice); a.Status != 200 {
		t.Errorf("a reader, file stored: %d", a.Status)
	}
}
