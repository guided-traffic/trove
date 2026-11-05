//go:build integration

package mirror

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/mirror"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/npmstub"
	"github.com/guided-traffic/trove/test/npmstub/corpus"
)

// TestR_REPO_055_AWorkerRunsAPassThroughItsOwnEngineAgainstTheNpmUpstream: a replica with the role worker that claims the queue
// mirror and has NO host override builds the background engine of Q-ARCH-316 on its first job and runs a pass end to end against the
// in-process npm stub: names, plan, the artifacts through the format's gate and the engine, the populate events of the system.
func TestR_REPO_055_AWorkerRunsAPassThroughItsOwnEngineAgainstTheNpmUpstream(t *testing.T) {
	stub, err := npmstub.New(npmstub.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ss, err := npmstub.NewSelfSigned("localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	run, err := stub.Start("127.0.0.1:0", ss.ServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Close() })
	ca := filepath.Join(t.TempDir(), "stub-ca.pem")
	if err := os.WriteFile(ca, ss.CAPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	var pkg *corpus.Package
	for _, p := range corpus.Default().Packages {
		if !strings.HasPrefix(p.Name, "@") && len(p.Versions) >= 2 && len(p.Versions) < 30 && !strings.ContainsAny(p.Name, "*?") {
			pkg = p
			break
		}
	}
	if pkg == nil {
		t.Fatal("the corpus has no package")
	}

	st := testenv.NewStack(t, testenv.StackOptions{Name: "mirrorw", Valkey: true, Env: []string{
		"TROVE_PROXY_OUTBOUND_TLS_CA_FILES=[" + strconv.Quote(ca) + "]",
		`TROVE_PROXY_OUTBOUND_ADDRESS_POLICY_ALLOW_CIDRS=["127.0.0.1/32"]`,
		"TROVE_VULNERABILITIES_SOURCES_OSV_MODE=off",
	}})
	doc := map[string]any{
		"includePatterns": []string{pkg.Name}, "excludePatterns": []string{},
		"remote": map[string]any{
			"url": run.URL + "/", "extraHosts": []any{}, "authMode": "none", "username": "", "password": nil,
			"clientCertificate": nil, "headers": []any{}, "forwardAuthOnRedirect": false, "outboundProxy": map[string]any{"mode": "direct"},
			"connectTimeoutMs": 5000, "readTimeoutMs": 15000, "sourceNetwork": nil, "cacheFillAction": "read",
			"cache": map[string]any{
				"storeArtifacts": true, "metadataTtlSeconds": 600, "metadataRefreshTimeoutSeconds": 10, "notFoundTtlSeconds": 60,
				"maxStaleSeconds": 0, "unusedRetentionHours": 0,
			},
			"integrity":    map[string]any{"checksumPolicy": "verify-if-available", "blockUnexpectedContentTypes": true, "allowedContentTypes": []any{}},
			"availability": map[string]any{"mode": "online", "failureThreshold": 3, "circuitOpenSeconds": 30},
			"limits":       map[string]any{"maxArtifactBytes": nil, "maxMetadataBytes": nil, "maxConcurrentFetches": 16},
			"mode":         "mirror", "mirror": map[string]any{"concurrency": 2},
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	st.Repository(repoKey, "remote", "npm", string(raw))

	// No MirrorOverride: the host is the one the composition root builds for a worker. The worker claims the queue mirror
	// for the pass and maintenance for the dispatch, which runs there (Q-ARCH-321).
	rep := st.Start(t, testenv.ReplicaOptions{
		Name: "worker-1", Role: "worker", Env: append(append([]string{}, fastJobs...), `TROVE_JOBS_QUEUES=["mirror","maintenance"]`),
	})
	dispatch := func() {
		if _, err := rep.App.Queue().EnqueueNow(context.Background(), jobs.EnqueueSpec{Kind: mirror.KindDispatch}); err != nil {
			t.Fatal(err)
		}
	}
	dispatch() // creates the state row of the repository; the first pass is due one interval later
	testenv.WaitFor(t, "the state row", 30*time.Second, func() bool {
		n, err := st.TryNumber(`SELECT count(*) FROM mirror_state`)
		return err == nil && n == 1
	})
	st.Exec(`UPDATE mirror_state SET next_pass_at = now() - interval '1 second'`)
	dispatch()
	files := func() int64 {
		return st.Number(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.kind = 'file'`, repoKey)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("jobs: %s", st.Text(`SELECT coalesce(string_agg(kind || ' ' || state || ' ' || coalesce(result::text, '') || ' ' || coalesce(error::text, ''), E'\n'), '') FROM job`))
			t.Logf("mirror_state: %s", st.Text(`SELECT coalesce(string_agg(row_to_json(s)::text, E'\n'), '') FROM mirror_state s`))
		}
	})
	testenv.WaitFor(t, "the pass to store the package's tarballs", 90*time.Second, func() bool { return files() >= 2 })
	testenv.WaitFor(t, "the pass to end complete", 60*time.Second, func() bool {
		n, err := st.TryNumber(`SELECT count(*) FROM mirror_state s JOIN repositories r ON r.id = s.repo_id WHERE r.key = $1 AND s.last_pass->>'outcome' = 'complete'`, repoKey)
		return err == nil && n == 1
	})
	if n := st.Number(`SELECT count(*) FROM audit_event WHERE action = 'authz.write.allowed' AND reason = 'mirror' AND repository = $1`, repoKey); n < 2 {
		t.Fatalf("%d populate events of the mirror for the stored artifacts", n)
	}
	if n := st.Number(`SELECT count(*) FROM audit_event WHERE action = 'authz.write.allowed' AND reason = 'mirror' AND actor_type = 'system' AND repository = $1`, repoKey); n < 2 {
		t.Fatalf("%d of them name the system as actor", n)
	}
	if strings.Contains(rep.Logs.String(), "registered twice") {
		t.Fatal("a cache was registered twice")
	}
}
