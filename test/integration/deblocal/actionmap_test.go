//go:build integration

package deblocal

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/debstub/debgen"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The action map of R-AUTHZ-007 for the Debian table of R-DEB-004 (local class), one case per row: a principal who holds exactly the
// action of the row (plus read where the client needs to see the repository at all) succeeds, and one who lacks it fails. Ownership is
// off in the repository, so what is shown is the action and nothing else. The grants are seeded before the replica starts, because its
// grant index is built from what exists then.

// actors are the principals of the action map.
type actors struct {
	setup, readOnly, readDeploy, readDelete, readManage, noRead, acmeReader string
}

func newActEnv(t *testing.T) (*env, *actors) {
	t.Helper()
	a := &actors{}
	e := newEnvOpts(t, envOpts{cfg: `{"local":{"packageOwnership":"off"}}`, seed: func(e *env) {
		grant := func(name string, actions ...string) string {
			p := e.st.User(name)
			e.st.Policy("act-"+name, actions, []string{repo}, p)
			return e.st.Token(p, scope.Default())
		}
		a.setup = grant("setup", "read", "deploy", "delete")
		a.readOnly = grant("reader", "read")
		a.readDeploy = grant("deployer", "read", "deploy")
		a.readDelete = grant("deleter", "read", "delete")
		a.readManage = grant("manager", "read", "manage")
		a.noRead = grant("blind", "deploy", "delete", "manage") // everything but read
		// A reader narrowed to the packages acme-* (R-DEB-004, Q-DEB-20).
		p := e.st.User("acme")
		e.st.Policy("act-acme", []string{"read"}, []string{repo}, p)
		e.st.Exec(`INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern)
			SELECT id, 'package', 'include', 'acme-*' FROM authz_policy WHERE name = 'act-acme'`)
		a.acmeReader = e.st.Token(p, scope.Default())
	}})
	return e, a
}

type pkgFile struct {
	name, version, file, pool string
	body                      []byte
}

var counter int

// fresh builds a package of a new name, not yet uploaded.
func fresh(t *testing.T, prefix string) pkgFile {
	t.Helper()
	counter++
	name := fmt.Sprintf("%s-%d", prefix, counter)
	p := pkgFile{name: name, version: "1.0"}
	p.file = name + "_1.0_amd64.deb"
	p.pool = "pool/" + name[:1] + "/" + name + "/" + p.file
	p.body = build(t, name, "1.0", nil)
	return p
}

// stored uploads a fresh package with the setup principal into stable/main.
func (e *env) stored(a *actors, prefix string) pkgFile {
	e.t.Helper()
	p := fresh(e.t, prefix)
	e.mustUpload(a.setup, p.file, p.body, 201, []string{"stable"})
	return p
}

func ok(a testenv.Answer) bool { return a.Status >= 200 && a.Status < 300 }

// refused reports an answer that denies without telling more: 403, or 404 for a caller who may not even read the repository
// (R-AUTHZ-016).
func refused(a testenv.Answer, want ...int) bool {
	for _, w := range want {
		if a.Status == w {
			return true
		}
	}
	return false
}

func TestR_AUTHZ_007_DebActionMap(t *testing.T) {
	e, a := newActEnv(t)
	ids := e.stored(a, "idx") // the first package of stable: its index is published for the read rows
	e.waitIndex("stable", 1)
	acme := e.stored(a, "acme-tool")
	other := e.stored(a, "other-tool")
	e.waitIndex("stable", e.st.Number(`SELECT change_seq FROM deb_suites s JOIN repositories r ON r.id = s.repo_id WHERE r.key = $1 AND s.suite = 'stable'`, repo))
	keys := "/api/v1/projects/" + project + "/repositories/" + repo + "/deb/signing-keys"
	state := "/api/v1/projects/" + project + "/repositories/" + repo + "/deb/index"

	t.Run("R-AUTHZ-007 apt-get update (release documents, indexes, by-hash, signing-key.asc): read", func(t *testing.T) {
		for _, p := range []string{"dists/stable/InRelease", "dists/stable/Release.gpg", "dists/stable/main/binary-amd64/Packages.gz", "signing-key.asc"} {
			if r := e.get(p, a.readOnly); r.Status != 200 {
				t.Errorf("%s with read: %d", p, r.Status)
			}
			if r := e.get(p, a.noRead); !refused(r, 403, 404) {
				t.Errorf("%s without read: %d", p, r.Status)
			}
		}
	})
	t.Run("R-AUTHZ-007 package file download: read", func(t *testing.T) {
		if r := e.get(ids.pool, a.readOnly); r.Status != 200 {
			t.Errorf("with read: %d", r.Status)
		}
		if r := e.get(ids.pool, a.noRead); !refused(r, 403, 404) {
			t.Errorf("without read: %d", r.Status)
		}
	})
	t.Run("R-AUTHZ-007 a read narrowed to packages.include reads the files it names and no index (Q-DEB-20)", func(t *testing.T) {
		if r := e.get("dists/stable/InRelease", a.acmeReader); r.Status != 404 {
			t.Errorf("InRelease: %d, want 404", r.Status)
		}
		if r := e.get("dists/stable/main/binary-amd64/Packages.gz", a.acmeReader); r.Status != 404 {
			t.Errorf("Packages.gz: %d, want 404", r.Status)
		}
		if r := e.get(acme.pool, a.acmeReader); r.Status != 200 {
			t.Errorf("%s: %d, want 200", acme.pool, r.Status)
		}
		if r := e.get(other.pool, a.acmeReader); r.Status != 404 {
			t.Errorf("%s: %d, want 404", other.pool, r.Status)
		}
	})
	t.Run("R-AUTHZ-007 upload of a new package file: deploy", func(t *testing.T) {
		p := fresh(t, "up")
		if r := e.upload(a.readDelete, p.file, p.body, []string{"stable"}); !refused(r, 403) {
			t.Fatalf("without deploy: %d %s", r.Status, r.Body)
		}
		if r := e.upload(a.readDeploy, p.file, p.body, []string{"stable"}); r.Status != 201 {
			t.Fatalf("with deploy: %d %s", r.Status, r.Body)
		}
	})
	t.Run("R-AUTHZ-007 upload of the same bytes again (adds coordinates): deploy", func(t *testing.T) {
		p := e.stored(a, "again")
		if r := e.upload(a.readDelete, p.file, p.body, []string{"testing"}); !refused(r, 403) {
			t.Fatalf("without deploy: %d %s", r.Status, r.Body)
		}
		if r := e.upload(a.readDeploy, p.file, p.body, []string{"testing"}); r.Status != 200 {
			t.Fatalf("with deploy: %d %s", r.Status, r.Body)
		}
	})
	t.Run("R-AUTHZ-007 upload of other bytes for a stored version: refused by the format with 409", func(t *testing.T) {
		p := e.stored(a, "bytes")
		other := build(t, p.name, "1.0", func(s *debgen.Spec) { s.Line = "other bytes" })
		if r := e.upload(a.readDeploy, p.file, other, []string{"stable"}); r.Status != 409 || codeOf(t, r.Body) != "deb_version_exists" {
			t.Fatalf("= %d %s", r.Status, r.Body)
		}
	})
	t.Run("R-AUTHZ-007 coordinates, adding: deploy", func(t *testing.T) {
		p := e.stored(a, "coadd")
		if r := e.setCoordinates(a.readDelete, p.pool, [2]string{"stable", "main"}, [2]string{"testing", "main"}); !refused(r, 403) {
			t.Fatalf("without deploy: %d %s", r.Status, r.Body)
		}
		if r := e.setCoordinates(a.readDeploy, p.pool, [2]string{"stable", "main"}, [2]string{"testing", "main"}); !ok(r) {
			t.Fatalf("with deploy: %d %s", r.Status, r.Body)
		}
	})
	t.Run("R-AUTHZ-007 coordinates, removing: delete", func(t *testing.T) {
		p := e.stored(a, "corm")
		e.setCoordinates(a.setup, p.pool, [2]string{"stable", "main"}, [2]string{"testing", "main"})
		if r := e.setCoordinates(a.readDeploy, p.pool, [2]string{"stable", "main"}); !refused(r, 403) {
			t.Fatalf("without delete: %d %s", r.Status, r.Body)
		}
		if r := e.setCoordinates(a.readDelete, p.pool, [2]string{"stable", "main"}); !ok(r) {
			t.Fatalf("with delete: %d %s", r.Status, r.Body)
		}
	})
	t.Run("R-AUTHZ-007 delete a package file: delete", func(t *testing.T) {
		p := e.stored(a, "del")
		path := e.base() + p.pool
		if r := e.st.Do(e.r, "DELETE", path, a.readDeploy, nil); !refused(r, 403) {
			t.Fatalf("without delete: %d %s", r.Status, r.Body)
		}
		if r := e.st.Do(e.r, "DELETE", path, a.readDelete, nil); r.Status != 204 {
			t.Fatalf("with delete: %d %s", r.Status, r.Body)
		}
	})
	t.Run("R-AUTHZ-007 index state: read", func(t *testing.T) {
		if r := e.st.Do(e.r, "GET", state, a.readOnly, nil); r.Status != 200 {
			t.Errorf("with read: %d %s", r.Status, r.Body)
		}
		if r := e.st.Do(e.r, "GET", state+"?waitFor=stable:1", a.readOnly, nil); r.Status != 200 {
			t.Errorf("waiting, with read: %d %s", r.Status, r.Body)
		}
		if r := e.st.Do(e.r, "GET", state, a.noRead, nil); !refused(r, 403, 404) {
			t.Errorf("without read: %d", r.Status)
		}
	})
	t.Run("R-AUTHZ-007 reindex: manage (R-REPO-016)", func(t *testing.T) {
		path := "/api/v1/projects/" + project + "/repositories/" + repo + "/reindex"
		if r := e.st.Do(e.r, "POST", path, a.readDeploy, nil); !refused(r, 403) {
			t.Fatalf("without manage: %d %s", r.Status, r.Body)
		}
		if r := e.st.Do(e.r, "POST", path, a.readManage, nil); r.Status != 202 {
			t.Fatalf("with manage: %d %s", r.Status, r.Body)
		}
	})
	t.Run("R-AUTHZ-007 signing keys, list: read", func(t *testing.T) {
		if r := e.st.Do(e.r, "GET", keys, a.readOnly, nil); r.Status != 200 {
			t.Errorf("with read: %d %s", r.Status, r.Body)
		}
		if r := e.st.Do(e.r, "GET", keys, a.noRead, nil); !refused(r, 403, 404) {
			t.Errorf("without read: %d", r.Status)
		}
	})
	t.Run("R-AUTHZ-007 signing keys, create the next key: manage", func(t *testing.T) {
		if r := e.st.Do(e.r, "POST", keys, a.readOnly, map[string]any{"generate": true}); !refused(r, 403) {
			t.Fatalf("with read only: %d %s", r.Status, r.Body)
		}
		r := e.st.Do(e.r, "POST", keys, a.readManage, map[string]any{"generate": true})
		if !ok(r) {
			t.Fatalf("with manage: %d %s", r.Status, r.Body)
		}
		var key struct {
			Fingerprint string `json:"fingerprint"`
		}
		if err := json.Unmarshal(r.Body, &key); err != nil {
			t.Fatal(err)
		}
		if d := e.st.Do(e.r, "DELETE", keys+"/"+key.Fingerprint, a.readDelete, nil); !refused(d, 403) {
			t.Errorf("retiring it without manage: %d %s", d.Status, d.Body)
		}
		if d := e.st.Do(e.r, "DELETE", keys+"/"+key.Fingerprint, a.readManage, nil); !ok(d) {
			t.Errorf("retiring it with manage: %d %s", d.Status, d.Body)
		}
	})
}
