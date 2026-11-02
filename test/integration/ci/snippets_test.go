//go:build integration

package ci

import (
	"strings"
	"testing"
)

// The CI snippet (R-API-035, R-UI-035; R-CI-027 is the broader P1 requirement): built from the reference pipeline
// of R-CI-028, a text/plain answer that holds no secret.

const snippetBase = "http://trove.test"

func TestR_API_035_TheGitLabCISnippetOfAMapping(t *testing.T) {
	e := newEnv(t, options{valkey: true})
	a := e.web("web-a")
	e.issuer(t, a, "gitlab-main", false)
	e.stack.Repository("npm-internal", "local", "npm", "{}")
	e.stack.Repository("npm-all", "virtual", "npm", `{"members":["npm-internal"]}`)
	e.stack.Repository("nuget-internal", "local", "nuget", "{}")
	wantStatus(t, e.do(a, "POST", mappingsOf("gitlab-main"), mapping("acme-publish", 100,
		all(eq("project_id", "4242"), eq("ref_protected", true)),
		grants(inline("npm-internal", []string{"@acme/*"}, "read", "deploy"), inline("nuget-internal", []string{"Acme.*"}, "read", "deploy")))), 201, "")
	snippet := func(format string) string {
		t.Helper()
		path := issuersPath + "/gitlab-main/snippets/gitlab-ci?mapping=acme-publish"
		if format != "" {
			path += "&format=" + format
		}
		r := e.do(a, "GET", path, nil)
		wantStatus(t, r, 200, "")
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Errorf("Content-Type = %q", ct)
		}
		return string(r.Body)
	}

	npm := snippet("")
	for _, want := range []string{
		"id_tokens:", "TROVE_ID_TOKEN:", "aud: " + snippetBase, // the issuer's first allowed audience
		`TROVE_URL: "` + snippetBase + `"`,
		"printf '%s' \"$TROVE_ID_TOKEN\" | curl -sS --retry 3", "subject_token@-",
		"grant_type=urn:ietf:params:oauth:grant-type:token-exchange", "subject_token_type=urn:ietf:params:oauth:token-type:id_token",
		"/api/v1/auth/oidc/token",
		"{error, error_description, reason, requestId}", // a failed exchange prints what the administrator needs
		"//trove.test/api/npm/npm-internal/:_authToken=${TROVE_TOKEN}",
		"npm publish --registry " + snippetBase + "/api/npm/npm-internal/",
		"$CI_COMMIT_REF_PROTECTED", // the mapping requires a protected ref
	} {
		if !strings.Contains(npm, want) {
			t.Errorf("the npm snippet lacks %q:\n%s", want, npm)
		}
	}
	lines := strings.Split(strings.TrimRight(npm, "\n"), "\n")
	if last := lines[len(lines)-1]; !strings.Contains(last, "-X DELETE") || !strings.Contains(last, "/api/v1/tokens/current") {
		t.Errorf("the last line is %q: the revocation closes the script (R-CI-017)", last)
	}
	if strings.Contains(npm, "dotnet") {
		t.Error("the npm snippet carries NuGet commands")
	}

	nuget := snippet("nuget")
	for _, want := range []string{
		"dotnet nuget push", "/api/nuget/nuget-internal/v3/index.json", `--api-key "$TROVE_TOKEN"`, "%TROVE_TOKEN%",
		"{error, error_description, reason, requestId}", "id_tokens:",
	} {
		if !strings.Contains(nuget, want) {
			t.Errorf("the NuGet snippet lacks %q:\n%s", want, nuget)
		}
	}
	for _, s := range []string{npm, nuget} {
		if strings.Contains(s, "trv_") || strings.Contains(s, "eyJ") {
			t.Error("a snippet holds a token")
		}
	}

	t.Run("the refusals", func(t *testing.T) {
		wantStatus(t, e.do(a, "GET", issuersPath+"/gitlab-main/snippets/gitlab-ci?mapping=nobody", nil), 404, "not_found")
		wantStatus(t, e.do(a, "GET", issuersPath+"/nobody/snippets/gitlab-ci?mapping=acme-publish", nil), 404, "not_found")
		if r := e.do(a, "GET", issuersPath+"/gitlab-main/snippets/gitlab-ci?mapping=acme-publish&format=maven", nil); r.Status != 400 && r.Status != 422 {
			t.Errorf("an unknown format answered %d", r.Status)
		}
		if r := e.do(a, "GET", issuersPath+"/gitlab-main/snippets/gitlab-ci", nil); r.Status != 400 && r.Status != 422 {
			t.Errorf("a snippet without a mapping answered %d", r.Status)
		}
		if r := e.as(a, e.alicePAT, "GET", issuersPath+"/gitlab-main/snippets/gitlab-ci?mapping=acme-publish", nil); r.Status != 403 {
			t.Errorf("a user without a role got %d", r.Status)
		}
	})
}

func TestQ_CI_26_AProjectAdministratorReadsTheSnippetOfItsMappings(t *testing.T) {
	p := newProjectEnv(t)
	a := p.a
	wantStatus(t, p.bob(a, "POST", projectMappings("web"), webPublish("web-publish", 100)), 201, "")
	wantStatus(t, p.do(a, "POST", mappingsOf("gitlab-main"), readOnlyMapping("sys-read", 10, all(eq("project_id", "4242")), "other-npm")), 201, "")
	path := "/api/v1/projects/web/ci/issuers/gitlab-main/snippets/gitlab-ci?mapping="

	r := p.bob(a, "GET", path+"web-publish", nil)
	wantStatus(t, r, 200, "")
	if !strings.Contains(string(r.Body), "//trove.test/api/npm/web-npm/:_authToken=${TROVE_TOKEN}") || !strings.Contains(string(r.Body), "id_tokens:") {
		t.Errorf("the snippet of the project's mapping:\n%s", r.Body)
	}
	// A mapping of another tier is not the project administrator's: 404, as a mapping that does not exist.
	wantStatus(t, p.bob(a, "GET", path+"sys-read", nil), 404, "not_found")
	wantStatus(t, p.bob(a, "GET", "/api/v1/projects/data/ci/issuers/gitlab-main/snippets/gitlab-ci?mapping=web-publish", nil), 404, "not_found")
	if r := p.bob(a, "GET", issuersPath+"/gitlab-main/snippets/gitlab-ci?mapping=web-publish", nil); r.Status != 403 {
		t.Errorf("the system route answered %d to a project administrator", r.Status)
	}
}
