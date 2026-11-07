//go:build e2e

package e2e

// The reference pipeline of R-CI-028 on kind (roadmap M4): the steps of the specification's Examples run with the real npm and .NET
// clients of the client pods against the issuer double, in the scatter profile (three packages replicas, every request to another
// one), so a job's exchange, installs, publish and self-revocation reach different replicas. docs/developer/e2e.md,
// "The CI tests", describes the set.

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// steps returns the steps of a job of the Examples: the hidden job's before_script, then the job's own script.
func (fx *ciFixture) steps(t testing.TB, job string) []string {
	t.Helper()
	j, ok := fx.spec.pipeline.Jobs[job]
	if !ok {
		t.Fatalf("the Examples have no job %s", job)
	}
	return append(append([]string{}, fx.spec.pipeline.Before...), j.Script...)
}

func (fx *ciFixture) npmJob(t testing.TB, pod int, name string, claims *ciClaims, vars map[string]string) ciJobResult {
	t.Helper()
	return fx.runJob(t, ciJob{kind: "npm", pod: pod, name: name, claims: claims, vars: vars, files: fx.npmRepoFiles(), steps: fx.steps(t, "publish-npm")})
}

func (fx *ciFixture) nugetJob(t testing.TB, pod int, name string, claims *ciClaims, vars map[string]string) ciJobResult {
	t.Helper()
	return fx.runJob(t, ciJob{kind: "nuget", pod: pod, name: name, claims: claims, vars: vars, files: fx.nugetRepoFiles(), steps: fx.steps(t, "push-nuget")})
}

// noSecrets fails when the job's log holds a CI token or the ID token (R-CI-028, E4.1).
func noSecrets(t testing.TB, what string, r ciJobResult) {
	t.Helper()
	if strings.Contains(r.Out, "trv_cit_") {
		t.Errorf("%s: the job log holds a trv_cit_ value:\n%s", what, tailLines(r.Out, 40))
	}
	for _, p := range append(strings.Split(r.IDToken, "."), r.IDToken) {
		if len(p) > 20 && strings.Contains(r.Out, p) {
			t.Errorf("%s: the job log holds a part of the ID token", what)
		}
	}
	if r.Token != "" && strings.Contains(r.Out, r.Token) {
		t.Errorf("%s: the job log holds the CI token", what)
	}
}

// tokenGets probes a path with a bearer token and returns the status.
func (fx *ciFixture) tokenGets(t testing.TB, token, path string) int {
	t.Helper()
	r, err := newAPI(t).withToken(token).send(http.MethodGet, path, nil)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return r.Status
}

// versionsOfNPM lists the versions of a package in a repository, read with the setup account's token (nil for a package that
// does not exist).
func (fx *ciFixture) versionsOfNPM(t testing.TB, repo, name string) []string {
	t.Helper()
	r, err := newAPI(t).withToken(fx.setupTok).send(http.MethodGet, npmAddr(repo)+"/"+strings.Replace(name, "/", "%2F", 1), nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status == 404 {
		return nil
	}
	r.want(t, "packument of "+name, 200)
	var doc struct {
		Versions map[string]json.RawMessage `json:"versions"`
	}
	r.JSON(t, &doc)
	var out []string
	for v := range doc.Versions {
		out = append(out, v)
	}
	return out
}

// versionsOfNuGet lists the versions of a package in a repository (flat container).
func (fx *ciFixture) versionsOfNuGet(t testing.TB, repo, id string) []string {
	t.Helper()
	r, err := newAPI(t).withToken(fx.setupTok).send(http.MethodGet, nugetAddr(repo)+"/v3/flatcontainer/"+strings.ToLower(id)+"/index.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status == 404 {
		return nil
	}
	r.want(t, "versions of "+id, 200)
	var doc struct {
		Versions []string `json:"versions"`
	}
	r.JSON(t, &doc)
	return doc.Versions
}

func contains(l []string, s string) bool { return slicesContains(l, s) }

var writeAction = regexp.MustCompile(`(?i)publish|push|upload|deploy|created`)

var forbiddenRE = regexp.MustCompile(`(?i)\b403\b|forbidden`)

// pipelineRun is what a scenario of TestCIPipeline leaves for the dry run of E4.5.
type pipelineRun struct {
	name     string
	claims   map[string]any
	record   issuance
	crossing bool
}

// TestCIPipeline runs the reference pipeline (R-CI-028): E4.1 on a protected tag and on an unprotected branch, E4.4 for a
// fork's merge request run in the parent, E4.5 against the issuances those runs left.
func TestCIPipeline(t *testing.T) {
	fx := ciFx(t)
	var runs []pipelineRun

	t.Run("R-CI-028 R-CI-020 E4.1 a protected tag publishes both packages, the issuance list shows project_id, ref and sha, no log holds a token, the publish's audit entry carries the token_id", func(t *testing.T) {
		if got := fx.spec.pipeline.Jobs["publish-npm"].Rules; len(got) != 1 || !ruleHolds(t, got[0], map[string]string{"CI_COMMIT_TAG": ciNpmVersionTag, "CI_COMMIT_REF_PROTECTED": "true"}) {
			t.Fatalf("the rules of the Examples do not select a protected tag: %v", got)
		}
		claims := protectedTag(t, ciNpmVersionTag)
		npm := fx.npmJob(t, 0, "tag-npm", claims, nil)
		nuget := fx.nugetJob(t, 0, "tag-nuget", claims, nil)
		jobs := map[string]ciJobResult{"npm job": npm, "NuGet job": nuget}
		for what, r := range jobs {
			if r.Code != 0 {
				t.Errorf("%s exited %d:\n%s", what, r.Code, tailLines(r.Out, 60))
			}
			noSecrets(t, what, r)
			t.Logf("%s took %s", what, r.Dur.Round(time.Millisecond))
		}
		if !contains(fx.versionsOfNPM(t, ciNpmInt, npmPackageName), "1.4.0") {
			t.Errorf("%s 1.4.0 is not in %s", npmPackageName, ciNpmInt)
		}
		if !contains(fx.versionsOfNuGet(t, ciNugetInt, nugetPackageID), "1.4.0") {
			t.Errorf("%s 1.4.0 is not in %s", nugetPackageID, ciNugetInt)
		}
		for what, r := range jobs {
			recs := fx.issuancesOf(t, r.JobID)
			if len(recs) != 1 {
				t.Fatalf("%s: %d issuance records for job %s, want 1", what, len(recs), r.JobID)
			}
			rec := recs[0]
			claimsOfJob := decodeJWTClaims(t, r.IDToken)
			if rec.ProjectID != "1234" || rec.Ref != ciNpmVersionTag || rec.Sha == "" || rec.Sha != str(claimsOfJob["sha"]) {
				t.Errorf("%s: record project_id %q ref %q sha %q", what, rec.ProjectID, rec.Ref, rec.Sha)
			}
			if !contains(rec.MappingNames, "acme-release-publish") {
				t.Errorf("%s: mappings %v, want acme-release-publish", what, rec.MappingNames)
			}
			// the closing step of the job revoked the token although its expires_in has not passed (R-CI-017, R-CI-028)
			if rec.Active || rec.RevokedAt == nil || !rec.RevokedAt.Before(rec.ExpiresAt) {
				t.Errorf("%s: the token of a finished job is still active: %+v", what, rec)
			}
			if !strings.HasPrefix(r.Token, "trv_cit_") {
				t.Fatalf("%s: the harness did not capture the job's CI token", what)
			}
			if got := fx.tokenGets(t, r.Token, npmAddr(ciNpmAll)+"/-/whoami"); got != 401 {
				t.Errorf("%s: the revoked token answered %d, want 401", what, got)
			}
			// R-CI-020: the publish's audit entry in the repository carries the same token_id; the revocation is audited with reason owner
			repo := ciNpmInt
			if what == "NuGet job" {
				repo = ciNugetInt
			}
			waitFor(t, what+": the audit entry of the publish in "+repo, 30*time.Second, time.Second, func() bool {
				for _, e := range fx.audit(t, "repository="+repoRef(repo)) {
					if e.Actor.TokenID == rec.TokenID && e.Outcome == "success" && writeAction.MatchString(e.Action) {
						t.Logf("%s: audit entry %s of token %s", what, e.Action, rec.TokenID)
						return true
					}
				}
				return false
			})
			waitFor(t, what+": the audit entry of the revocation", 30*time.Second, time.Second, func() bool {
				for _, e := range fx.audit(t, "action=token.revoked") {
					if e.Resource.ID == rec.TokenID && (e.Reason == "owner" || e.Details["reason"] == "owner") {
						return true
					}
				}
				return false
			})
			runs = append(runs, pipelineRun{name: "protected tag, " + what, claims: claimsOfJob, record: rec})
		}
	})

	t.Run("R-CI-028 R-CI-011 E4.1 an unprotected branch installs but both publishes get 403", func(t *testing.T) {
		claims := fixtureClaims(t, "push-unprotected-branch")
		// the rules: are removed; the version still comes from a variable, as a pipeline variable would give it
		vars := map[string]string{"CI_COMMIT_TAG": "v1.4.1"}
		npm := fx.npmJob(t, 1, "branch-npm", claims, vars)
		nuget := fx.nugetJob(t, 1, "branch-nuget", claims, vars)
		for what, r := range map[string]ciJobResult{"npm job": npm, "NuGet job": nuget} {
			if r.Code == 0 {
				t.Errorf("%s: the publish of an unprotected branch succeeded:\n%s", what, tailLines(r.Out, 40))
			}
			noSecrets(t, what, r)
			recs := fx.issuancesOf(t, r.JobID)
			if len(recs) != 1 || len(recs[0].MappingNames) != 1 || recs[0].MappingNames[0] != "acme-read" {
				t.Fatalf("%s: the exchange should succeed through the read-only mapping acme-read: %+v", what, recs)
			}
			if strings.Contains(recs[0].GrantedScope, "deploy") {
				t.Errorf("%s: granted scope %q", what, recs[0].GrantedScope)
			}
			runs = append(runs, pipelineRun{name: "unprotected branch, " + what, claims: decodeJWTClaims(t, r.IDToken), record: recs[0]})
			fx.revokeJob(t, r.JobID)
		}
		if !forbiddenRE.MatchString(npm.Out) {
			t.Errorf("npm publish should fail with 403:\n%s", tailLines(npm.Out, 30))
		}
		if !forbiddenRE.MatchString(nuget.Out) {
			t.Errorf("dotnet nuget push should fail with 403:\n%s", tailLines(nuget.Out, 30))
		}
		// the installs of the same job went through the read grant of the policy
		if !strings.Contains(npm.Out, "added 1 package") {
			t.Errorf("npm ci of the unprotected job did not install the dependency:\n%s", tailLines(npm.Out, 30))
		}
		if contains(fx.versionsOfNPM(t, ciNpmInt, npmPackageName), "1.4.1") || contains(fx.versionsOfNuGet(t, ciNugetInt, nugetPackageID), "1.4.1") {
			t.Error("version 1.4.1 exists although both publishes were refused")
		}
	})

	parent := t
	t.Run("R-CI-012 R-CI-028 E4.4 a fork's merge request pipeline run in the parent cannot publish to npm-internal", func(t *testing.T) {
		claims := fixtureClaims(t, "merge-request-fork-in-parent")
		if !crossProject(claims) {
			t.Fatal("the fixture's claim set is not a cross-project one")
		}
		vars := map[string]string{"CI_COMMIT_TAG": "v1.4.2"}
		exchangeOnly := func(name string) ciJobResult {
			return fx.runJob(t, ciJob{kind: "npm", pod: 0, name: name, claims: claims, vars: vars, files: fx.npmRepoFiles(), steps: fx.spec.pipeline.Before})
		}
		// 1. the fork's namespace (4712) is pinned by no mapping
		wantRefusal(t, exchangeOnly("fork-nomatch"), "no_matching_mapping")
		// 2. a mapping that pins the fork's namespace and keeps the default for pipelines of other projects
		mapping := map[string]any{
			"name": "partner-read", "description": "the partner namespace reads", "priority": 800,
			"match":  map[string]any{"all": []any{map[string]any{"claim": "namespace_id", "equals": "4712"}}},
			"grants": map[string]any{"policies": []string{ciPolicyName}}, "maxTtlSeconds": 600,
		}
		fx.adm.do(http.MethodPost, ciMappingsPath(), mapping).want(t, "create partner-read", 201)
		parent.Cleanup(func() { fx.adm.do(http.MethodDelete, ciMappingsPath()+"/partner-read", nil) })
		wantRefusal(t, exchangeOnly("fork-denied"), "cross_project_source_denied")
		// 3. the opt-in read_only: the exchange works, the token reads and cannot publish
		g := fx.adm.get(ciMappingsPath()+"/partner-read").want(t, "read partner-read", 200)
		mapping["options"] = map[string]any{"crossProjectSource": "read_only"}
		fx.adm.do(http.MethodPut, ciMappingsPath()+"/partner-read", mapping, "If-Match", g.Header.Get("ETag")).want(t, "opt partner-read in", 200)
		r := fx.npmJob(t, 0, "fork-readonly", claims, vars)
		noSecrets(t, "fork npm job", r)
		if r.Code == 0 || !forbiddenRE.MatchString(r.Out) {
			t.Errorf("npm publish of the fork's pipeline should fail with 403 (exit %d):\n%s", r.Code, tailLines(r.Out, 30))
		}
		if !strings.Contains(r.Out, "added 1 package") {
			t.Errorf("the read-only token should install:\n%s", tailLines(r.Out, 30))
		}
		recs := fx.issuancesOf(t, r.JobID)
		if len(recs) != 1 || !recs[0].CrossProject || strings.Contains(recs[0].GrantedScope, "deploy") {
			t.Fatalf("issuance of the fork's job: %+v", recs)
		}
		if contains(fx.versionsOfNPM(t, ciNpmInt, npmPackageName), "1.4.2") {
			t.Error("the fork's pipeline published version 1.4.2")
		}
		runs = append(runs, pipelineRun{name: "fork merge request in the parent", claims: decodeJWTClaims(t, r.IDToken), record: recs[0], crossing: true})
		fx.revokeJob(t, r.JobID)
	})

	t.Run("R-CI-019 R-UI-036 E4.5 the dry run and the claim tester answer the grants the real exchange issued", func(t *testing.T) {
		if len(runs) < 5 {
			t.Fatalf("the earlier scenarios left %d runs, want 5", len(runs))
		}
		for _, run := range runs {
			t.Run(run.name, func(t *testing.T) {
				var res struct {
					Selected  []string       `json:"selected"`
					Scope     string         `json:"scope"`
					Failure   string         `json:"failure"`
					CrossProj bool           `json:"crossProject"`
					Grants    map[string]any `json:"grants"`
				}
				fx.adm.do(http.MethodPost, "/api/v1/ci/issuers/"+ciIssuerName+"/evaluate", map[string]any{"claims": run.claims}).want(t, "dry run", 200).JSON(t, &res)
				if res.Failure != "" {
					t.Fatalf("the dry run says the exchange fails (%s), the real one issued %+v", res.Failure, run.record)
				}
				if strings.Join(res.Selected, ",") != strings.Join(run.record.MappingNames, ",") {
					t.Errorf("selected %v, the exchange used %v", res.Selected, run.record.MappingNames)
				}
				// the dry run reads no policy (R-CI-019): its scope is the inline grants' and the issued scope adds what the policies of the
				// mapping grant, which are reads
				issued := map[string]bool{}
				for _, item := range strings.Fields(run.record.GrantedScope) {
					issued[item] = true
				}
				inDry := map[string]bool{}
				for _, item := range strings.Fields(res.Scope) {
					inDry[item] = true
					if !issued[item] {
						t.Errorf("the dry run's scope item %q was not issued (issued: %q)", item, run.record.GrantedScope)
					}
				}
				for item := range issued {
					if !inDry[item] && !strings.HasPrefix(item, "read:") {
						t.Errorf("the issued scope item %q is neither in the dry run's scope nor a read of a policy", item)
					}
				}
				if res.CrossProj != run.crossing || run.record.CrossProject != run.crossing {
					t.Errorf("crossProject: dry run %v, issuance %v, want %v", res.CrossProj, run.record.CrossProject, run.crossing)
				}
				var dryInline, issuedInline []string
				for _, g := range asList(res.Grants["inline"]) {
					raw, _ := json.Marshal(g)
					dryInline = append(dryInline, string(raw))
				}
				for _, c := range asList(run.record.Grants["contributions"]) {
					for _, g := range asList(c.(map[string]any)["inline"]) {
						raw, _ := json.Marshal(g)
						issuedInline = append(issuedInline, string(raw))
					}
				}
				sort.Strings(dryInline)
				sort.Strings(issuedInline)
				if strings.Join(dryInline, "\n") != strings.Join(issuedInline, "\n") {
					t.Errorf("rendered inline grants differ:\n dry run %s\n issued  %s", strings.Join(dryInline, "\n"), strings.Join(issuedInline, "\n"))
				}
			})
		}
	})
}

// crossProject says whether a claim set is cross-project in the sense of R-CI-012: project_id and job_project_id differ.
func crossProject(c *ciClaims) bool {
	return str(c.set["project_id"]) != "" && str(c.set["job_project_id"]) != "" && str(c.set["project_id"]) != str(c.set["job_project_id"])
}

var refusalField = regexp.MustCompile(`"(reason|requestId)":\s*"([^"]*)"`)

// wantRefusal checks the refused exchange of a job: it stops, prints error, error_description, reason and requestId, and the
// requestId leads to the denial's audit event (R-CI-028, R-CI-020).
func wantRefusal(t testing.TB, r ciJobResult, reason string) {
	t.Helper()
	if r.Code == 0 {
		t.Fatalf("the exchange should be refused with %s, the job passed:\n%s", reason, tailLines(r.Out, 30))
	}
	fields := map[string]string{}
	for _, m := range refusalField.FindAllStringSubmatch(r.Out, -1) {
		fields[m[1]] = m[2]
	}
	for _, k := range []string{"error", "error_description"} {
		if !strings.Contains(r.Out, `"`+k+`"`) {
			t.Errorf("the refusal should print %s:\n%s", k, r.Out)
		}
	}
	if fields["reason"] != reason || fields["requestId"] == "" {
		t.Fatalf("the refusal prints reason %q requestId %q, want %s:\n%s", fields["reason"], fields["requestId"], reason, r.Out)
	}
	noSecrets(t, "refused job", r)
}

// revokeJob revokes the tokens of a job through the bulk operation (the cleanup of a job that stopped before its closing step).
func (fx *ciFixture) revokeJob(t testing.TB, jobID string) {
	t.Helper()
	fx.adm.do(http.MethodPost, "/api/v1/ci/issuances/revoke", map[string]any{"issuer": ciIssuerName, "jobId": jobID}).want(t, "revoke job "+jobID, 200)
}

// asList reads a JSON array member (nil when it is absent).
func asList(v any) []any {
	l, _ := v.([]any)
	return l
}
