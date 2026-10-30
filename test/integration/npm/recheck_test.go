//go:build integration

package npm

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_AUTHZ_015_APublishIsDecidedAgainWhenItCommits removes the grant of a publish
// while its body is still arriving: the host decides the write again on the index of the
// moment before the commit transaction opens, so the publish is refused and nothing is
// stored, as a deploy through /files/ is (R-AUTHZ-015, Q-ARCH-225,
// docs/security/permissions.md H-104).
func TestR_AUTHZ_015_APublishIsDecidedAgainWhenItCommits(t *testing.T) {
	e := newEnv(t)
	eve, ops := e.st.User("eve"), e.st.User("ops")
	e.st.Bind("admin", ops)
	e.st.Policy("eve-writers", []string{"read", "deploy"}, []string{"npm-local"}, eve)
	tokE, tokOps := e.st.Token(eve, scope.Default()), e.st.Token(ops, scope.Scope{Inherit: true, Admin: true})
	a := e.replica("web-a")
	e.mustPublish(a, "npm-local", tokE, pkg{Name: "eve-seen", Version: "1.0.0"})

	content := body(t, pkg{Name: "eve-late", Version: "1.0.0"})
	pr, pw := io.Pipe()
	done := make(chan testenv.Answer, 1)
	go func() {
		ans, err := rawPutTo(a.Addr("default"), pubPath("npm-local", "eve-late"), tokE, pr, int64(len(content)), false)
		if err != nil {
			ans = testenv.Answer{Status: -1, Body: []byte(err.Error())}
		}
		done <- ans
	}()
	half := len(content) / 2
	if _, err := pw.Write(content[:half]); err != nil {
		t.Fatal(err)
	}
	if ans := e.st.Do(a, "DELETE", "/api/v1/permission-policies/eve-writers", tokOps, nil); ans.Status != 204 {
		t.Fatalf("delete the policy: %d %s", ans.Status, ans.Body)
	}
	testenv.WaitFor(t, "the removal reaching the replica's index", 10*time.Second, func() bool {
		return e.get(a, "npm-local", "eve-seen", tokE).Status == 404
	})
	if _, err := io.Copy(pw, bytes.NewReader(content[half:])); err != nil {
		t.Fatal(err)
	}
	_ = pw.Close()
	ans := <-done
	if ans.Status != 403 {
		t.Fatalf("the publish committed, or failed otherwise, after its grant was removed: %d %s", ans.Status, ans.Body)
	}
	if e.versions("npm-local", "eve-late") != 0 || e.itemCount("npm-local", "eve-late/-/eve-late-1.0.0.tgz") != 0 {
		t.Fatal("the publish stored a version")
	}
	if n := e.st.Number(`SELECT count(*) FROM package_ownership WHERE package_name = 'eve-late'`); n != 0 {
		t.Fatalf("the refused publish claimed the name: %d", n)
	}
}
