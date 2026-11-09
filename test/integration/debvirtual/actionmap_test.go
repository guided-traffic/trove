//go:build integration

package debvirtual

import (
	"net/http"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// denied reports an answer that refuses: 403, or 404 for a caller who may not even read the repository (R-AUTHZ-016).
func denied(a testenv.Answer) bool { return a.Status == 403 || a.Status == 404 }

// The action map of R-AUTHZ-007 for the virtual rows of the Debian table of R-DEB-004, one subtest per row: a principal who holds exactly
// the actions of the row succeeds, and principals who lack one of them fail. Grants of deploy and delete on the virtual repository
// itself have no effect (R-AUTHZ-009 rule 5).
func TestR_AUTHZ_007_DebActionMapOfTheVirtualClass(t *testing.T) {
	e := newEnv(t, options{})
	e.ready("map-tool")
	idx := "dists/stable/main/binary-amd64/Packages.gz"
	pool := "pool/m/map-tool/map-tool_1.0-1_amd64.deb"
	state := "/api/v1/projects/" + project + "/repositories/" + rApt + "/deb/index"
	keys := "/api/v1/projects/" + project + "/repositories/" + rApt + "/deb/signing-keys"

	t.Run("R-AUTHZ-007 apt-get update on a virtual repository: read on it and on every member", func(t *testing.T) {
		for _, p := range []string{"dists/stable/InRelease", "dists/stable/Release.gpg", idx, "signing-key.asc"} {
			if a := e.get(rApt, p, e.alice); a.Status != 200 {
				t.Errorf("%s with read on the virtual repository and every member: %d", p, a.Status)
			}
			if a := e.get(rApt, p, e.bob); a.Status != 403 || codeOf(a) != "deb_members_unreadable" {
				t.Errorf("%s without read on one member: %d %s", p, a.Status, codeOf(a))
			}
			if a := e.get(rApt, p, e.erin); !denied(a) {
				t.Errorf("%s with read on every member and none on the virtual repository: %d", p, a.Status)
			}
			if a := e.get(rApt, p, ""); a.Status != 401 {
				t.Errorf("%s anonymous: %d", p, a.Status)
			}
		}
	})
	t.Run("R-AUTHZ-007 package file download through a virtual repository: read on it and on the member", func(t *testing.T) {
		if a := e.get(rApt, pool, e.alice); a.Status != 200 {
			t.Errorf("with read on the member: %d", a.Status)
		}
		if a := e.get(rApt, pool, e.carol); !denied(a) {
			t.Errorf("with read on the virtual repository and no member: %d", a.Status)
		}
		if a := e.get(rApt, pool, e.erin); !denied(a) {
			t.Errorf("with read on the members and not on the virtual repository: %d", a.Status)
		}
	})
	t.Run("R-AUTHZ-007 members/<memberKey>/<path>: read on the virtual repository and on the member", func(t *testing.T) {
		path := "members/" + rLocal + "/" + pool
		if a := e.get(rApt, path, e.bob); a.Status != 200 {
			t.Errorf("bob reads the local member: %d", a.Status)
		}
		if a := e.get(rApt, path, e.carol); !denied(a) {
			t.Errorf("carol, no read on the member: %d", a.Status)
		}
		if a := e.get(rApt, path, e.erin); !denied(a) {
			t.Errorf("erin, no read on the virtual repository: %d", a.Status)
		}
	})
	t.Run("R-AUTHZ-007 index state of a virtual repository: read", func(t *testing.T) {
		if a := e.st.Do(e.r, "GET", state, e.carol, nil); a.Status != 200 {
			t.Errorf("with read: %d", a.Status)
		}
		if a := e.st.Do(e.r, "GET", state, e.erin, nil); !denied(a) {
			t.Errorf("without read: %d", a.Status)
		}
	})
	t.Run("R-AUTHZ-007 signing keys of a virtual repository: read lists, manage changes", func(t *testing.T) {
		if a := e.st.Do(e.r, "GET", keys, e.alice, nil); a.Status != 200 {
			t.Errorf("list with read: %d", a.Status)
		}
		if a := e.st.Do(e.r, "POST", keys, e.alice, map[string]any{"generate": true}); a.Status != 403 {
			t.Errorf("create with read only: %d", a.Status)
		}
	})
	t.Run("R-AUTHZ-007 upload through a virtual repository: deploy on the target, read on the virtual repository", func(t *testing.T) {
		if a := e.upload(rApt, e.alice, "via-virtual", "1.0-1", "stable"); a.Status != 201 {
			t.Errorf("deploy on the target and read on the virtual repository: %d %s", a.Status, a.Body)
		}
		if a := e.upload(rApt, e.bob, "via-bob", "1.0-1", "stable"); !denied(a) {
			t.Errorf("read only: %d", a.Status)
		}
		if a := e.upload(rApt, e.dave, "via-dave", "1.0-1", "stable"); !denied(a) {
			t.Errorf("deploy on the target and no read on the virtual repository (the gate, rule 1): %d", a.Status)
		}
		if a := e.upload(rApt, e.frank, "via-frank", "1.0-1", "stable"); !denied(a) {
			t.Errorf("deploy on the virtual repository itself has no effect (rule 5): %d", a.Status)
		}
	})
	testenv.WaitFor(t, "the virtual index to list the upload", 90*time.Second, func() bool {
		_, ok := find(e.packages(rApt, "stable", e.admin), "via-virtual", "")
		return ok
	})
	t.Run("R-AUTHZ-007 delete through a virtual repository: delete on the target", func(t *testing.T) {
		del := func(token string) testenv.Answer {
			return e.st.Do(e.r, http.MethodDelete, e.url(rApt, "pool/v/via-virtual/via-virtual_1.0-1_amd64.deb"), token, nil)
		}
		if a := del(e.frank); !denied(a) {
			t.Errorf("delete on the virtual repository itself has no effect: %d", a.Status)
		}
		if a := del(e.bob); !denied(a) {
			t.Errorf("read only: %d", a.Status)
		}
		if a := del(e.alice); a.Status != 204 {
			t.Errorf("delete on the target: %d %s", a.Status, a.Body)
		}
	})
	t.Run("R-AUTHZ-007 the index follows the target: the upload is listed and the deleted file is not", func(t *testing.T) {
		testenv.WaitFor(t, "the virtual index to drop the deleted package", 90*time.Second, func() bool {
			list := e.packages(rApt, "stable", e.admin)
			_, gone := find(list, "via-virtual", "")
			_, kept := find(list, "map-tool", "")
			return !gone && kept
		})
	})
}
