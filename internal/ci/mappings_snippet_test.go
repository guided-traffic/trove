package ci

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/guided-traffic/trove/internal/ci/claims"
)

func snippetFor(format string, g claims.Spec, protected bool) string {
	m := Mapping{Name: "acme-publish", Grants: g}
	if protected {
		eq := []byte(`true`)
		m.Match = claims.Node{All: []claims.Node{{Claim: "ref_protected", Equals: eq}}}
	}
	return renderSnippet(snippetInput{
		BaseURL: "https://packages.example.com", Audience: "https://packages.example.com", Issuer: "gitlab-main", Mapping: m, Format: format,
		Formats: map[string]string{"npm-internal": "npm", "nuget-internal": "nuget", "npm-other": "npm"},
	})
}

func grantOn(repo string, actions ...claims.Action) claims.InlineGrant {
	return claims.InlineGrant{Repositories: []string{repo}, Packages: &claims.PatternSet{Include: []string{"*"}}, Actions: actions}
}

// R-API-035: the snippet is a YAML document a pipeline can include, for every format and every shape of a
// mapping's grants.
func TestTheSnippetIsYAML(t *testing.T) {
	for name, tc := range map[string]struct {
		format    string
		grants    claims.Spec
		protected bool
	}{
		"npm with a writing grant":       {FormatNPM, claims.Spec{Inline: []claims.InlineGrant{grantOn("npm-internal", claims.Read, claims.Deploy)}}, true},
		"nuget with a writing grant":     {FormatNuGet, claims.Spec{Inline: []claims.InlineGrant{grantOn("nuget-internal", claims.Read, claims.Deploy)}}, true},
		"npm with a read-only grant":     {FormatNPM, claims.Spec{Inline: []claims.InlineGrant{grantOn("npm-internal", claims.Read)}}, false},
		"nuget without a literal key":    {FormatNuGet, claims.Spec{Inline: []claims.InlineGrant{grantOn("@any", claims.Read)}}, false},
		"npm with policies only":         {FormatNPM, claims.Spec{Policies: []string{"ci-read"}}, false},
		"npm with two repositories":      {FormatNPM, claims.Spec{Inline: []claims.InlineGrant{grantOn("npm-other", claims.Read), grantOn("npm-internal", claims.Deploy)}}, true},
		"a template names no repository": {FormatNPM, claims.Spec{Inline: []claims.InlineGrant{grantOn("{{ project_path | slug }}-npm", claims.Read)}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			text := snippetFor(tc.format, tc.grants, tc.protected)
			var doc map[string]any
			if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
				t.Fatalf("not YAML: %v\n%s", err, text)
			}
			trove, ok := doc[".trove"].(map[string]any)
			if !ok {
				t.Fatalf("no hidden job .trove:\n%s", text)
			}
			tokens := trove["id_tokens"].(map[string]any)["TROVE_ID_TOKEN"].(map[string]any)
			if tokens["aud"] != "https://packages.example.com" {
				t.Errorf("aud = %v", tokens["aud"])
			}
			for _, secret := range []string{"trv_", "eyJ"} {
				if strings.Contains(text, secret) {
					t.Errorf("the snippet holds %q", secret)
				}
			}
			jobName := "publish-npm"
			if tc.format == FormatNuGet {
				jobName = "push-nuget"
			}
			job, ok := doc[jobName].(map[string]any)
			if !ok {
				t.Fatalf("no job %s:\n%s", jobName, text)
			}
			script := job["script"].([]any)
			if last := script[len(script)-1].(string); !strings.Contains(last, "DELETE") || !strings.Contains(last, "/api/v1/tokens/current") {
				t.Errorf("the last script line is %q", last)
			}
			if _, has := job["rules"]; has != tc.protected {
				t.Errorf("rules present %v, want %v (the mapping requires a protected ref: %v)", has, tc.protected, tc.protected)
			}
		})
	}
}

// R-CI-028: the failure line of the exchange prints the reason and the request ID, so an administrator finds
// the audit event of the denial (a finding of the run).
func TestTheExchangeFailurePrintsTheRequestID(t *testing.T) {
	text := snippetFor(FormatNPM, claims.Spec{Inline: []claims.InlineGrant{grantOn("npm-internal", claims.Read)}}, false)
	if !strings.Contains(text, "jq '{error, error_description, reason, requestId}'") {
		t.Errorf("the failure line of the exchange lacks requestId:\n%s", text)
	}
}
