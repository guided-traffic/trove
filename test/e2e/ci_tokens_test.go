//go:build e2e

package e2e

// What a CI token does with the real clients and over time (roadmap M4): R-CI-017's client criteria, E4.6 (expiry and the
// revocation bound) and R-CI-030 (the tasks run on worker replicas), in the scatter profile against the reference setup of
// ci_env_test.go. The exchange here is the test's own POST, not the pipeline's curl: the scenarios need the token in the hands
// of the test.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// citOf exchanges a freshly minted ID token with the claim set and returns the CI token with its issuance record.
func (fx *ciFixture) citOf(t testing.TB, claims *ciClaims, extra map[string]string) (string, issuance, reply) {
	t.Helper()
	jobID, pipelineID := fx.nextIDs()
	c := claims.with(map[string]any{"aud": fx.base, "job_id": jobID, "pipeline_id": pipelineID})
	r := fx.exchange(t, newAPI(t), fx.dbl.mint(t, c.set, c.remove), extra)
	if r.Status != 200 {
		return "", issuance{}, r
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	r.JSON(t, &out)
	recs := fx.issuancesOf(t, jobID)
	if len(recs) != 1 {
		t.Fatalf("%d issuance records for job %s, want 1", len(recs), jobID)
	}
	return out.AccessToken, recs[0], r
}

// TestCITokens covers what the pipeline's own steps do not show: the clients' three ways to present the token, the expiry and the
// bound on a revocation.
func TestCITokens(t *testing.T) {
	fx := ciFx(t)

	t.Run("R-CI-017 R-CI-028 npm whoami with Bearer, a NuGet source with Basic credentials, dotnet nuget push -k, and DELETE /api/v1/tokens/current then 401 on npm view", func(t *testing.T) {
		claims := protectedTag(t, "v1.5.0")
		tok, rec, _ := fx.citOf(t, claims, nil)
		if !strings.HasPrefix(tok, "trv_cit_") {
			t.Fatalf("the exchange returned %q", tok)
		}
		// npm: the token as the Bearer credential of the registry (.npmrc _authToken)
		pod := npmClientPod(t, 1)
		root := "/work/ci-client-npm-" + fx.run
		_, _ = podExec(t, pod, "", time.Minute, "rm", "-rf", root)
		putTree(t, pod, root, map[string]string{".npmrc": fmt.Sprintf("registry=%s/api/npm/%s/\n//%s/api/npm/%s/:_authToken=${TROVE_TOKEN}\n", fx.base, ciNpmAll, fx.host, ciNpmAll)})
		npmEnv := []string{
			"env", "-C", root, "HOME=" + root + "/home", "NODE_EXTRA_CA_CERTS=/etc/trove-ca/ca.crt", "TROVE_TOKEN=" + tok,
			"npm_config_update_notifier=false", "npm_config_cache=" + root + "/cache",
		}
		out, code := podExec(t, pod, "", 2*time.Minute, append(append([]string{}, npmEnv...), "npm11", "whoami", "--registry", fx.base+"/api/npm/"+ciNpmAll+"/")...)
		if code != 0 || strings.TrimSpace(out) == "" {
			t.Errorf("npm whoami with the CI token exited %d: %s", code, out)
		}
		t.Logf("npm whoami: %s", strings.TrimSpace(out))

		// NuGet: the source configured with Basic credentials (the nuget.config of the Examples), a restore, a pack and dotnet nuget push -k
		npod := nugetClientPod(t, 1)
		nroot := "/work/ci-client-nuget-" + fx.run
		_, _ = podExec(t, npod, "", time.Minute, "rm", "-rf", nroot)
		putTree(t, npod, nroot, fx.nugetRepoFiles())
		nenv := []string{
			"env", "-C", nroot, "NUGET_PACKAGES=" + nroot + "/packages", "NUGET_HTTP_CACHE_PATH=" + nroot + "/httpcache",
			"NUGET_PLUGINS_CACHE_PATH=" + nroot + "/plugins", "TROVE_TOKEN=" + tok,
		}
		run := func(argv ...string) (string, int) {
			return podExec(t, npod, "", 10*time.Minute, append(append([]string{}, nenv...), argv...)...)
		}
		if out, code := run("dotnet", "restore"); code != 0 {
			t.Fatalf("dotnet restore with Basic credentials exited %d:\n%s", code, tailLines(out, 30))
		}
		if out, code := run("dotnet", "pack", "src/Acme.Billing.InvoiceApi/Acme.Billing.InvoiceApi.csproj", "-c", "Release", "-o", "out", "--no-restore", "-p:PackageVersion=1.5.0"); code != 0 {
			t.Fatalf("dotnet pack exited %d:\n%s", code, tailLines(out, 30))
		}
		if out, code := run("dotnet", "nuget", "push", "out/"+nugetPackageID+".1.5.0.nupkg", "-s", fx.base+"/api/nuget/"+ciNugetInt+"/v3/index.json", "-k", tok); code != 0 || !pushedRE.MatchString(out) {
			t.Errorf("dotnet nuget push -k exited %d:\n%s", code, tailLines(out, 30))
		}
		if !contains(fx.versionsOfNuGet(t, ciNugetInt, nugetPackageID), "1.5.0") {
			t.Errorf("%s 1.5.0 is not in %s after dotnet nuget push -k", nugetPackageID, ciNugetInt)
		}

		// the job revokes its own token: 204, and the next npm view gets 401 although expires_in has not passed
		cur := newAPI(t).withToken(tok)
		cur.do(http.MethodGet, "/api/v1/tokens/current", nil).want(t, "GET /api/v1/tokens/current with a CI token", 200)
		cur.do(http.MethodDelete, "/api/v1/tokens/current", nil).want(t, "DELETE /api/v1/tokens/current with a CI token", 204)
		out, code = podExec(t, pod, "", 2*time.Minute, append(append([]string{}, npmEnv...), "npm11", "view", npmPackageName, "--registry", fx.base+"/api/npm/"+ciNpmAll+"/")...)
		if code == 0 || !strings.Contains(out, "401") {
			t.Errorf("npm view with the revoked token: exit %d, want a failure with 401:\n%s", code, tailLines(out, 20))
		}
		if recs := fx.issuancesOf(t, rec.JobID); len(recs) != 1 || recs[0].Active || recs[0].RevokedAt == nil {
			t.Errorf("the issuance of a self-revoked token is still active: %+v", recs)
		}
	})

	t.Run("R-CI-016 R-CI-017 E4.6 a CI token used after its expiry gets 401", func(t *testing.T) {
		tok, rec, r := fx.citOf(t, protectedTag(t, "v1.5.1"), map[string]string{"ttl": "30"})
		if tok == "" {
			t.Fatalf("exchange with ttl=30: %s", r)
		}
		var out struct {
			ExpiresIn int `json:"expires_in"`
		}
		r.JSON(t, &out)
		if out.ExpiresIn < 1 || out.ExpiresIn > 30 {
			t.Fatalf("expires_in = %d, want at most the 30 s asked for", out.ExpiresIn)
		}
		a := newAPI(t).withToken(tok)
		a.get("/api/v1/tokens/current").want(t, "the token before its expiry", 200)
		until := time.Until(rec.ExpiresAt.Add(time.Second))
		t.Logf("token %s expires at %s; probing after %s", rec.TokenID, rec.ExpiresAt.Format(time.TimeOnly), until.Round(time.Second))
		time.Sleep(max(until, 0))
		seen := map[string]bool{}
		for range 9 { // every request to another replica (scatter)
			p := a.get("/api/v1/tokens/current")
			if p.Status != 401 {
				t.Errorf("a token used %s after its expiry answered %s, want 401", time.Since(rec.ExpiresAt).Round(time.Millisecond), p)
			}
			seen[p.Replica()] = true
		}
		if len(seen) < 3 {
			t.Errorf("the probes reached %d replicas, want all three: %v", len(seen), seen)
		}
	})

	t.Run("R-CI-017 R-CI-020 E4.6 revoking an issuance makes its token fail on every replica within 2 s", func(t *testing.T) {
		tok, rec, _ := fx.citOf(t, protectedTag(t, "v1.5.2"), nil)
		a := newAPI(t).withToken(tok)
		warm := map[string]bool{}
		for range 9 {
			p := a.get("/api/v1/tokens/current").want(t, "the token before its revocation", 200)
			warm[p.Replica()] = true
		}
		if len(warm) < 3 {
			t.Fatalf("the warm-up reached %d replicas, want all three (the token must be in every replica's cache): %v", len(warm), warm)
		}
		fx.adm.do(http.MethodPost, "/api/v1/ci/issuances/revoke", map[string]any{"issuer": ciIssuerName, "jobId": rec.JobID}).want(t, "revoke the issuance", 200)
		start := time.Now()
		var took time.Duration
		for {
			bad := 0
			replicas := map[string]bool{}
			for range 6 {
				p := a.get("/api/v1/tokens/current")
				replicas[p.Replica()] = true
				if p.Status != 401 {
					bad++
				}
			}
			if bad == 0 && len(replicas) >= 3 {
				took = time.Since(start)
				break
			}
			if time.Since(start) > 10*time.Second {
				t.Fatalf("the revoked token still works after 10 s")
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Logf("revoked token refused on every replica after %s", took.Round(time.Millisecond))
		if took > 2*time.Second {
			t.Errorf("R-AUTH-029: the revoked token failed on every replica only after %s, the bound is 2 s", took.Round(time.Millisecond))
		}
	})
}

// TestCITasks is R-CI-030: the three tasks of the exchange are listed and every run of them is a job of a worker replica.
func TestCITasks(t *testing.T) {
	fx := ciFx(t)
	var tasks struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	fx.adm.get("/api/v1/system/tasks?limit=200").want(t, "list the tasks", 200).JSON(t, &tasks)
	listed := map[string]bool{}
	for _, it := range tasks.Items {
		listed[it.Name] = true
	}
	for _, name := range []string{"ci-keys-refresh", "ci-replay-cleanup", "ci-issuance-retention"} {
		if !listed[name] {
			t.Errorf("GET /api/v1/system/tasks does not list %s", name)
			continue
		}
		r := fx.adm.do(http.MethodPost, "/api/v1/system/tasks/"+name+"/run", nil)
		if r.Status == 409 {
			continue // a scheduled run is going on; the jobs below show its replica
		}
		r.want(t, "run "+name, 202)
		var job struct {
			ID string `json:"id"`
		}
		var env struct {
			Job *struct {
				ID string `json:"id"`
			} `json:"job"`
		}
		if json.Unmarshal(r.Body, &env) == nil && env.Job != nil {
			job.ID = env.Job.ID
		} else {
			r.JSON(t, &job)
		}
		if job.ID == "" {
			t.Fatalf("the run of %s answered no job: %s", name, r)
		}
		waitFor(t, "the job of "+name, 2*time.Minute, 2*time.Second, func() bool {
			var j struct {
				State string `json:"state"`
			}
			fx.adm.get("/api/v1/system/jobs/"+job.ID).want(t, "read the job of "+name, 200).JSON(t, &j)
			return j.State == "succeeded"
		})
	}
	// a job names the instance UID of its replica; /api/v1/system/replicas tells the role of each
	rs, err := replicas()
	if err != nil {
		t.Fatal(err)
	}
	role := map[string]string{}
	for _, r := range rs {
		role[r.InstanceUID], role[r.Name] = r.role(), r.role()
	}
	for _, kind := range []string{"ci-keys-refresh", "ci-replay-cleanup", "ci-issuance-retention"} {
		var l struct {
			Items []struct {
				ID      string  `json:"id"`
				Replica *string `json:"replica"`
			} `json:"items"`
		}
		fx.adm.get("/api/v1/system/jobs?limit=100&kind="+kind).want(t, "list the jobs of "+kind, 200).JSON(t, &l)
		onWorker := 0
		for _, j := range l.Items {
			if j.Replica == nil {
				continue
			}
			switch r, known := role[*j.Replica]; {
			case !known: // a replica that is gone (an earlier rollout): its role is no longer listed
			case strings.Contains(r, "web"):
				t.Errorf("R-CI-030: job %s of %s ran on the api or packages replica %s", j.ID, kind, *j.Replica)
			case strings.Contains(r, "worker"):
				onWorker++
			}
		}
		if onWorker == 0 {
			t.Errorf("R-CI-030: no job of %s is known to have run on a worker replica", kind)
		}
	}
}
