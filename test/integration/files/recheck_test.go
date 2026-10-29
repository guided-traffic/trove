//go:build integration

package files

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
)

// TestR_AUTHZ_015_ADeployIsDecidedAgainWhenItCommits removes the grant of a
// deploy while its body is still arriving: the commit-time re-check refuses the
// write, for a new file and for an identical re-upload alike, and nothing is
// stored (R-AUTHZ-015, docs/security/permissions.md H-104).
func TestR_AUTHZ_015_ADeployIsDecidedAgainWhenItCommits(t *testing.T) {
	e := newEnv(t)
	e.breakGlass()
	r := e.newReplica("a", replicaOpts{})
	r.createRepository("generic-up", nil)
	e.user("uploader")
	grant := func() {
		e.grant(r, "up-deploy", []string{"generic-up"}, authz.PatternLists{}, map[string][]string{"user:uploader": {"read", "deploy"}})
		waitFor(t, "the grant taking effect", 10*time.Second, func() bool {
			return r.get("uploader", "/files/generic-up/").status == 200
		})
	}
	// upload sends the first part of the body, removes the grant, waits until the
	// replica's index refuses the uploader, then sends the rest.
	upload := func(path, content string) response {
		pr, pw := io.Pipe()
		done := make(chan response, 1)
		go func() { done <- r.do("uploader", "PUT", path, pr) }()
		half := len(content) / 2
		if _, err := pw.Write([]byte(content[:half])); err != nil {
			t.Fatal(err)
		}
		if _, err := r.policy.Delete(context.Background(), e.actor(), "up-deploy", errs.AnyRevision); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the removal reaching the replica's index", 10*time.Second, func() bool {
			return r.get("uploader", "/files/generic-up/").status == 404
		})
		if _, err := pw.Write([]byte(content[half:])); err != nil {
			t.Fatal(err)
		}
		_ = pw.Close()
		return <-done
	}
	content := strings.Repeat("trove-", 4096)

	t.Run("R-AUTHZ-015 a new file is refused at commit", func(t *testing.T) {
		grant()
		res := upload("/files/generic-up/rel/new.bin", content)
		if res.status == 201 || res.status/100 != 4 {
			t.Fatalf("the deploy committed after its grant was removed: %d %s", res.status, res.body)
		}
		if n := e.number("SELECT count(*) FROM items WHERE path = 'rel/new.bin'"); n != 0 {
			t.Fatalf("%d items were written", n)
		}
	})
	t.Run("R-AUTHZ-015 an identical re-upload is refused at commit", func(t *testing.T) {
		grant()
		if res := r.put("uploader", "/files/generic-up/rel/same.bin", content); res.status != 201 {
			t.Fatalf("%d %s", res.status, res.body)
		}
		res := upload("/files/generic-up/rel/same.bin", content)
		if res.status/100 != 4 {
			t.Fatalf("the re-upload was answered %d after its grant was removed", res.status)
		}
	})
}
