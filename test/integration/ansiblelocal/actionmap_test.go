//go:build integration

package ansiblelocal

import (
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The action map of R-AUTHZ-007 for the Ansible table of R-ANS-007 (local class), one case per row: a principal who holds exactly the
// action of the row succeeds, and one who lacks it fails. Ownership is off in the repository, so what is shown is the action and
// nothing else. The grants are seeded before the replica starts, because its grant index is built from what exists then.

type actors struct{ readOnly, deployOnly, readDeploy, readDelete, readManage, noRead string }

func newActionEnv(t *testing.T) (*env, *actors) {
	t.Helper()
	a := &actors{}
	e := newEnvOpts(t, envOpts{seed: func(e *env) {
		grant := func(name string, actions ...string) string {
			p := e.st.User(name)
			e.st.Policy("act-"+name, actions, []string{repo}, p)
			return e.st.Token(p, scope.Default())
		}
		a.readOnly = grant("reader", "read")
		a.deployOnly = grant("deploy-only", "deploy")
		a.readDeploy = grant("deployer", "read", "deploy")
		a.readDelete = grant("deleter", "read", "delete")
		a.readManage = grant("manager", "read", "manage")
		a.noRead = grant("blind", "delete", "manage")
	}})
	return e, a
}

func refused(a testenv.Answer) bool { return a.Status == 403 || a.Status == 404 }

func TestR_AUTHZ_007_AnsibleActionMap(t *testing.T) {
	e, a := newActionEnv(t)
	e.mustPublish("1.0.0")
	e.mustPublish("1.1.0")
	reads := []string{"", "v3/collections/acme/tools/", "v3/collections/acme/tools/versions/", "v3/collections/acme/tools/versions/1.0.0/", "v3/artifacts/collections/acme-tools-1.0.0.tar.gz"}

	t.Run("R-AUTHZ-007 discovery, collection, version list, version and download: read", func(t *testing.T) {
		for _, p := range reads {
			if r := e.get(a.readOnly, p); r.Status != 200 {
				t.Errorf("%q with read: %d", p, r.Status)
			}
			if r := e.get(a.noRead, p); !refused(r) {
				t.Errorf("%q without read: %d", p, r.Status)
			}
			if r := e.get(a.deployOnly, p); !refused(r) {
				t.Errorf("%q with deploy alone: %d", p, r.Status)
			}
		}
	})
	t.Run("R-AUTHZ-007 the import task: read", func(t *testing.T) {
		pub := e.publish(e.tokA, specBytes(spec("1.2.0")), true)
		u, _ := pub.JSON(t)["task"].(string)
		path := u[len(u)-len("v3/imports/collections/6f1c0000-0000-4000-8000-000000000002/"):]
		if r := e.get(a.readOnly, path); r.Status != 200 {
			t.Errorf("task with read: %d", r.Status)
		}
		if r := e.get(a.noRead, path); !refused(r) {
			t.Errorf("task without read: %d", r.Status)
		}
	})
	t.Run("R-AUTHZ-007 the publish: deploy", func(t *testing.T) {
		if r := e.publish(a.deployOnly, specBytes(spec("1.3.0")), true); r.Status != 202 {
			t.Errorf("with deploy: %d %s", r.Status, r.Body)
		}
		if r := e.publish(a.readOnly, specBytes(spec("1.4.0")), true); !refused(r) {
			t.Errorf("with read: %d", r.Status)
		}
		if r := e.publish(a.readDelete, specBytes(spec("1.4.0")), true); !refused(r) {
			t.Errorf("with delete: %d", r.Status)
		}
	})
	t.Run("R-AUTHZ-007 the removal of a version: delete", func(t *testing.T) {
		file := "/files/" + project + "/" + repo + "/" + archivePath("1.1.0")
		if r := e.st.Do(e.r, "DELETE", file, a.readDeploy, nil); !refused(r) {
			t.Errorf("with deploy: %d", r.Status)
		}
		if r := e.st.Do(e.r, "DELETE", file, a.readDelete, nil); r.Status/100 != 2 {
			t.Errorf("with delete: %d %s", r.Status, r.Body)
		}
	})
	t.Run("R-AUTHZ-007 the tombstones: manage", func(t *testing.T) {
		if r := e.st.Do(e.r, "GET", mgmt+"tombstones", a.readManage, nil); r.Status != 200 {
			t.Errorf("list with manage: %d %s", r.Status, r.Body)
		}
		if r := e.st.Do(e.r, "GET", mgmt+"tombstones", a.readDelete, nil); !refused(r) {
			t.Errorf("list with delete: %d", r.Status)
		}
		if r := e.st.Do(e.r, "DELETE", mgmt+"tombstones/acme.tools/1.1.0", a.readDelete, nil); !refused(r) {
			t.Errorf("clear with delete: %d", r.Status)
		}
		if r := e.st.Do(e.r, "DELETE", mgmt+"tombstones/acme.tools/1.1.0", a.readManage, nil); r.Status != 204 {
			t.Errorf("clear with manage: %d %s", r.Status, r.Body)
		}
	})
	t.Run("R-AUTHZ-007 the import task list: read", func(t *testing.T) {
		if r := e.st.Do(e.r, "GET", mgmt+"imports", a.readOnly, nil); r.Status != 200 {
			t.Errorf("with read: %d %s", r.Status, r.Body)
		}
		if r := e.st.Do(e.r, "GET", mgmt+"imports", a.noRead, nil); !refused(r) {
			t.Errorf("without read: %d", r.Status)
		}
	})
}

// TestR_ANS_028_PublishAndReadWorkOnTwoReplicasWithoutValkey: with Valkey unavailable (the state of R-HA-001) a publish, a read and the
// task of a publish work on every replica, and what one replica published is listed by the other at once (R-ANS-028).
func TestR_ANS_028_PublishAndReadWorkOnTwoReplicasWithoutValkey(t *testing.T) {
	e := newEnvOpts(t, envOpts{replicas: 2})
	for i, v := range []string{"1.0.0", "1.1.0", "1.2.0", "1.3.0"} {
		r := e.rs[i%2]
		body, ct := form(specBytes(spec(v)), true, "")
		pub := e.st.Do(r, "POST", base+"v3/artifacts/collections/", e.tokA, body, "Content-Type", ct)
		if pub.Status != 202 {
			t.Fatalf("publish %s on %s: %d %s", v, r.Name, pub.Status, pub.Body)
		}
		// The task is read on the other replica: a row, not a replica's memory.
		u, _ := pub.JSON(t)["task"].(string)
		other := e.rs[(i+1)%2]
		task := e.st.Do(other, "GET", u[len("http://trove.test"):], e.tokA, nil)
		if task.Status != 200 || task.JSON(t)["state"] != "completed" {
			t.Errorf("the task of %s on the other replica: %d %s", v, task.Status, task.Body)
		}
	}
	for _, r := range e.rs {
		list := e.st.Do(r, "GET", base+"v3/collections/acme/tools/versions/", e.tokC, nil)
		if list.Status != 200 || list.JSON(t)["meta"].(map[string]any)["count"] != float64(4) {
			t.Errorf("%s lists %s", r.Name, list.Body)
		}
	}
}
