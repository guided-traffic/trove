//go:build integration

package projects

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/secrets"
	"github.com/guided-traffic/trove/test/integration/stack"
)

// answers is the prober of the replicas of this test: the upstream test of an Owner reaches it only when the
// rules of a create let the section through.
type answers struct{}

func (answers) TestSaved(context.Context, repository.Repository, repository.RemoteSettings) repository.UpstreamTestResult {
	return repository.UpstreamTestResult{}
}

func (answers) TestUnsaved(context.Context, repository.UnsavedProbe) repository.UpstreamTestResult {
	return repository.UpstreamTestResult{}
}

// ownerFixture is the fixture with a sealing ring and a prober on the repository service of both replicas.
func ownerFixture(t *testing.T) *fixture {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	path := filepath.Join(t.TempDir(), "k1.key")
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
	ring, err := secrets.Load(config.Secrets{EncryptionKeyFiles: []string{path}, ActiveKeyID: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	e := stack.New(t)
	f := &fixture{t: t, e: e}
	f.admin = e.User("sec-root")
	e.Admin(f.admin)
	f.alice = e.User("alice")
	o := stack.Options{Validator: apitest.New(t, v1.EmbeddedYAML()), Remote: repository.RemoteDeps{Ring: ring, Prober: answers{}}}
	f.a, f.b = e.NewReplica("web-a", o), e.NewReplica("web-b", o)
	f.cl = stack.NewCluster(f.a, f.b)
	return f
}

// wantProblem asserts the status and the top-level code of a problem: a violation of the rules of an Owner is
// answered with its own code (secret_value_required, outbound_path_restricted, mirror_restricted) and the field in errors[].
func wantProblem(t *testing.T, r stack.Response, status int, code string) {
	t.Helper()
	if r.Status != status || r.Code() != code {
		t.Fatalf("answered %d %q, want %d %q\n%s", r.Status, r.Code(), status, code, r.Body)
	}
}

// TestR_AUTHZ_026_AnOwnerWritesRemoteRepositoriesWithinTheRules: an Owner of a project creates and changes the
// remote repositories of its project through the role alone, but only inside the rules of Q-REPO-127, Q-REPO-128,
// Q-REPO-142 and Q-REPO-193, and it does not weaken the member vulnerabilities (Q-VULN-69); an administrator (who
// holds repositories:write-config) is bound by none of them.
func TestR_AUTHZ_026_AnOwnerWritesRemoteRepositoriesWithinTheRules(t *testing.T) {
	f := ownerFixture(t)
	f.create("web")
	wantStatus(t, f.member("web", "user:alice", "owner"), 201, "")

	repos := base + "/web/repositories"
	// The role reaches both replicas through the change log: wait until each one answers the Owner.
	deadline := time.Now().Add(15 * time.Second)
	for streak := 0; streak < 6; {
		if got := f.do(f.alice, "GET", base+"/web", nil); got.Status == 200 && got.JSON(t)["role"] == "owner" {
			streak++
		} else if streak = 0; time.Now().After(deadline) {
			t.Fatal("the Owner is not known to both replicas")
		}
	}
	const upstream = "https://registry.example.test/"
	remote := func(key string, section map[string]any, extra map[string]any) map[string]any {
		m := map[string]any{"key": key, "class": "remote", "format": "npm", "remote": section}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	patch := func(who stack.Person, key string, body map[string]any) stack.Response {
		return f.do(who, "PATCH", repos+"/"+key, body, "Content-Type", "application/merge-patch+json", "If-Match", "*")
	}
	reference := map[string]any{"url": upstream, "authMode": "bearer", "password": map[string]any{"secretRef": map[string]any{"file": "partner"}}}

	t.Run("Q-REPO-127 an Owner creates a remote with a secret as a value, never with a reference of the installation", func(t *testing.T) {
		value := map[string]any{"url": upstream, "authMode": "bearer", "password": map[string]any{"value": "mine"}}
		wantStatus(t, f.do(f.alice, "POST", repos, remote("owner-value", value, nil)), 201, "")
		wantProblem(t, f.do(f.alice, "POST", repos, remote("owner-ref", reference, nil)), 422, "secret_value_required")
		if n := f.e.Number("SELECT count(*) FROM repositories WHERE key = 'owner-ref'"); n != 0 {
			t.Fatalf("%d repositories", n)
		}
	})
	t.Run("Q-REPO-128 an Owner chooses no outbound path of its own", func(t *testing.T) {
		wantProblem(t, f.do(f.alice, "POST", repos,
			remote("owner-net", map[string]any{"url": upstream, "sourceNetwork": "10.99.0.0/24"}, nil)), 422, "outbound_path_restricted")
		wantProblem(t, f.do(f.alice, "POST", repos,
			remote("owner-direct", map[string]any{"url": upstream, "outboundProxy": map[string]any{"mode": "direct"}}, nil)), 422, "outbound_path_restricted")
	})
	t.Run("Q-REPO-142 an Owner does not make a remote a mirror", func(t *testing.T) {
		wantStatus(t, f.do(f.alice, "POST", repos,
			remote("owner-mirror", map[string]any{"url": upstream, "mode": "mirror"}, nil)), 422, "mirror_restricted")
	})
	t.Run("Q-REPO-127 the unsaved upstream test of an Owner has the rules of a create", func(t *testing.T) {
		probe := base + "/web/upstream-tests"
		wantProblem(t, f.do(f.alice, "POST", probe, map[string]any{"format": "npm", "remote": reference}), 422, "secret_value_required")
		wantProblem(t, f.do(f.alice, "POST", probe,
			map[string]any{"format": "npm", "remote": map[string]any{"url": upstream, "sourceNetwork": "10.99.0.0/24"}}), 422, "outbound_path_restricted")
		wantStatus(t, f.do(f.alice, "POST", probe, map[string]any{"format": "npm", "remote": map[string]any{"url": upstream}}), 200, "")
		wantStatus(t, f.do(f.admin, "POST", probe, map[string]any{"format": "npm", "remote": reference}), 200, "")
	})
	t.Run("Q-REPO-128 an administrator creates the same repositories", func(t *testing.T) {
		wantStatus(t, f.do(f.admin, "POST", repos, remote("admin-ref", reference, nil)), 201, "")
		routed := map[string]any{"url": upstream, "sourceNetwork": "10.99.0.0/24", "outboundProxy": map[string]any{"mode": "direct"}}
		wantStatus(t, f.do(f.admin, "POST", repos, remote("admin-net", routed, nil)), 201, "")
		wantStatus(t, f.do(f.admin, "POST", repos, remote("admin-mirror", map[string]any{"url": upstream, "mode": "mirror"},
			map[string]any{"includePatterns": []string{"@acme/*"}})), 201, "")
	})
	t.Run("Q-REPO-193 an Owner does not widen the scope of a mirror or move its source, and may pause it", func(t *testing.T) {
		wantStatus(t, patch(f.alice, "admin-mirror", map[string]any{"includePatterns": []string{"*"}}), 422, "mirror_restricted")
		wantStatus(t, patch(f.alice, "admin-mirror", map[string]any{"remote": map[string]any{"url": "https://evil.example.test/"}}), 422, "mirror_restricted")
		wantStatus(t, patch(f.alice, "admin-mirror", map[string]any{"remote": map[string]any{"mirror": map[string]any{"paused": true}}}), 200, "")
	})
	t.Run("Q-REPO-127 an Owner's change does not send a stored credential or path to another url", func(t *testing.T) {
		evil := map[string]any{"remote": map[string]any{"url": "https://evil.example.test/"}}
		wantProblem(t, patch(f.alice, "admin-ref", evil), 422, "secret_value_required")
		wantProblem(t, patch(f.alice, "admin-net", evil), 422, "outbound_path_restricted")
	})
	t.Run("Q-VULN-69 an Owner strengthens the member vulnerabilities and does not weaken it; an administrator does", func(t *testing.T) {
		wantStatus(t, patch(f.alice, "owner-value", map[string]any{"vulnerabilities": map[string]any{"onMissingData": "block"}}), 200, "")
		for _, member := range []map[string]any{{"enabled": false}, {"blockMalicious": false}, {"matchPublicAdvisories": "never"}, {"onMissingData": "allow"}} {
			wantStatus(t, patch(f.alice, "owner-value", map[string]any{"vulnerabilities": member}), 403, "vulnerability_weakening_refused")
		}
		wantStatus(t, patch(f.admin, "owner-value", map[string]any{"vulnerabilities": map[string]any{"blockMalicious": false}}), 200, "")
	})
}
