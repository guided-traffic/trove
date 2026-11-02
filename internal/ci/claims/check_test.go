package claims_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/ci/reason"
)

var gitlabPins = []string{"project_id", "namespace_id", "job_project_id", "job_namespace_id"}

var gitlabRequired = []string{
	"iss", "aud", "exp", "iat", "jti", "sub", "project_id", "namespace_id", "project_path",
	"namespace_path", "ref", "ref_type", "ref_protected", "pipeline_source", "pipeline_id", "job_id", "user_id", "user_login",
}

func publicIssuer() claims.IssuerFacts {
	return claims.IssuerFacts{Kind: "gitlab", PublicIssuer: true, PinClaims: gitlabPins, RequiredClaims: gitlabRequired}
}

func privateIssuer() claims.IssuerFacts {
	f := publicIssuer()
	f.PublicIssuer = false
	return f
}

// publishMatch is the condition of the reference mapping acme-release-publish (R-CI-019).
const publishMatch = `{"all": [
	{"claim": "namespace_id", "in": ["4711", "4712"]},
	{"claim": "ref_type", "equals": "tag"},
	{"claim": "ref_protected", "equals": true},
	{"claim": "pipeline_source", "in": ["push", "web"]}
]}`

func npmPublish() claims.InlineGrant {
	return claims.InlineGrant{
		Repositories: []string{"npm-internal"},
		Packages:     &claims.PatternSet{Include: []string{"@{{ namespace_path | slug }}/*"}},
		Actions:      []claims.Action{claims.Read, claims.Deploy},
	}
}

func input(t testing.TB, match string, g claims.Spec) claims.SaveInput {
	t.Helper()
	return claims.SaveInput{Name: "acme-release-publish", Match: parse(t, match), Grants: g}
}

func check(t testing.TB, f claims.IssuerFacts, in claims.SaveInput) (claims.Checked, []claims.Warning, error) {
	t.Helper()
	return claims.Check(f, in, claims.DefaultLimits())
}

func refused(t testing.TB, f claims.IssuerFacts, in claims.SaveInput, want reason.Reason, pointer string) {
	t.Helper()
	_, _, err := check(t, f, in)
	if err == nil {
		t.Fatalf("saved, want %s", want)
	}
	r, ptr := reasonOf(t, err)
	if r != want || ptr != pointer {
		t.Fatalf("%s at %q, want %s at %q (%v)", r, ptr, want, pointer, err)
	}
}

func codes(ws []claims.Warning) []string {
	var out []string
	for _, w := range ws {
		out = append(out, string(w.Code))
	}
	return out
}

func TestTheReferenceMappingSaves(t *testing.T) {
	checked, warnings, err := check(t, publicIssuer(), input(t, publishMatch, claims.Spec{Policies: []string{"ci-read-internal"}, Inline: []claims.InlineGrant{npmPublish()}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings %v", codes(warnings))
	}
	if checked.Grants.Inline[0].Ownership != claims.OwnershipBindProject || checked.Options.CrossProjectSource != claims.CrossProjectDeny {
		t.Fatalf("%+v", checked)
	}
	if _, ok := checked.Templates["/grants/inline/0/packages/include/0"]; !ok {
		t.Fatalf("templates %v", checked.Templates)
	}
}

func TestR_CI_010_APublicIssuerNeedsAPin(t *testing.T) {
	g := claims.Spec{Policies: []string{"ci-read-internal"}}
	refused(t, publicIssuer(), input(t, `{"claim": "namespace_path", "glob": "acme/**"}`, g), reason.PinRequired, "/match")
	refused(t, publicIssuer(), input(t, `{"all":[{"any":[{"claim":"project_id","equals":"1"}]},{"claim":"ref","present":true}]}`, g), reason.PinRequired, "/match")
	refused(t, publicIssuer(), input(t, `{"all":[{"claim":"project_path","equals":"acme/app"}]}`, g), reason.PinRequired, "/match")
	if _, _, err := check(t, publicIssuer(), input(t, `{"claim":"project_id","equals":"4242"}`, g)); err != nil {
		t.Fatalf("a root pin leaf: %v", err)
	}
}

func TestR_CI_010_UnpinnedWriteAndUnpinnedCondition(t *testing.T) {
	cond := `{"all": [{"claim": "project_path", "glob": "acme/**"}, {"claim": "ref_protected", "equals": true}, {"claim":"pipeline_source","equals":"push"}]}`
	t.Run("R-CI-010 a policy that grants deploy without a pin is unpinned_write", func(t *testing.T) {
		in := input(t, cond, claims.Spec{Policies: []string{"ci-deploy"}})
		in.PolicyActions = []claims.Action{claims.Read, claims.Deploy}
		refused(t, privateIssuer(), in, reason.UnpinnedWrite, "/match")
	})
	t.Run("R-CI-010 a group that grants delete without a pin is unpinned_write", func(t *testing.T) {
		in := input(t, cond, claims.Spec{Groups: []string{"ci-writers"}})
		in.PolicyActions = []claims.Action{claims.Delete}
		refused(t, privateIssuer(), in, reason.UnpinnedWrite, "/match")
	})
	t.Run("R-CI-010 an inline write with ownership none without a pin is unpinned_write", func(t *testing.T) {
		g := npmPublish()
		g.Ownership = claims.OwnershipNone
		refused(t, privateIssuer(), input(t, cond, claims.Spec{Inline: []claims.InlineGrant{g}}), reason.UnpinnedWrite, "/match")
	})
	t.Run("R-CI-010 the only write an inline bind_project grant: saved with unpinned_condition", func(t *testing.T) {
		_, warnings, err := check(t, privateIssuer(), input(t, cond, claims.Spec{Inline: []claims.InlineGrant{npmPublish()}}))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(codes(warnings), []string{string(reason.WarnUnpinnedCondition)}) {
			t.Fatalf("warnings %v", codes(warnings))
		}
	})
	t.Run("R-CI-010 a read-only mapping without a pin saves with unpinned_condition", func(t *testing.T) {
		in := input(t, `{"claim": "project_path", "glob": "acme/**"}`, claims.Spec{Policies: []string{"ci-read"}})
		in.PolicyActions = []claims.Action{claims.Read}
		_, warnings, err := check(t, privateIssuer(), in)
		if err != nil || !slices.Contains(codes(warnings), string(reason.WarnUnpinnedCondition)) {
			t.Fatalf("%v %v", codes(warnings), err)
		}
	})
	t.Run("R-CI-010 a pin makes the policy write saveable on a private issuer", func(t *testing.T) {
		in := input(t, publishMatch, claims.Spec{Policies: []string{"ci-deploy"}})
		in.PolicyActions = []claims.Action{claims.Deploy}
		if _, _, err := check(t, privateIssuer(), in); err != nil {
			t.Fatal(err)
		}
	})
}

func TestR_CI_011_UnprotectedWrite(t *testing.T) {
	noRef := `{"all":[{"claim":"namespace_id","in":["4711"]},{"claim":"pipeline_source","equals":"push"}]}`
	g := claims.Spec{Inline: []claims.InlineGrant{npmPublish()}}
	t.Run("R-CI-011 deploy without ref_protected is unprotected_write", func(t *testing.T) {
		refused(t, publicIssuer(), input(t, noRef, g), reason.UnprotectedWrite, "/match")
	})
	t.Run("R-CI-011 ref_protected under any or not does not count", func(t *testing.T) {
		for _, cond := range []string{
			`{"all":[{"claim":"namespace_id","in":["4711"]},{"any":[{"claim":"ref_protected","equals":true}]}]}`,
			`{"all":[{"claim":"namespace_id","in":["4711"]},{"claim":"ref_protected","equals":false}]}`,
			`{"all":[{"claim":"namespace_id","in":["4711"]},{"claim":"ref_protected","present":true}]}`,
		} {
			refused(t, publicIssuer(), input(t, cond, g), reason.UnprotectedWrite, "/match")
		}
	})
	t.Run("R-CI-011 a policy write counts too", func(t *testing.T) {
		in := input(t, noRef, claims.Spec{Policies: []string{"ci-deploy"}})
		in.PolicyActions = []claims.Action{claims.Overwrite}
		refused(t, publicIssuer(), in, reason.UnprotectedWrite, "/match")
	})
	t.Run("R-CI-011 allowUnprotectedRefs saves with a warning", func(t *testing.T) {
		in := input(t, noRef, g)
		in.Options.AllowUnprotectedRefs = true
		_, warnings, err := check(t, publicIssuer(), in)
		if err != nil || !slices.Equal(codes(warnings), []string{string(reason.WarnUnprotectedRefsAllowed)}) {
			t.Fatalf("%v %v", codes(warnings), err)
		}
	})
	t.Run("R-CI-011 a write mapping without a pipeline_source condition warns", func(t *testing.T) {
		cond := `{"all":[{"claim":"namespace_id","in":["4711"]},{"claim":"ref_protected","equals":true}]}`
		_, warnings, err := check(t, publicIssuer(), input(t, cond, g))
		if err != nil || !slices.Equal(codes(warnings), []string{string(reason.WarnNoPipelineSource)}) {
			t.Fatalf("%v %v", codes(warnings), err)
		}
		read := npmPublish()
		read.Actions = []claims.Action{claims.Read}
		_, warnings, err = check(t, publicIssuer(), input(t, `{"claim":"namespace_id","in":["4711"]}`, claims.Spec{Inline: []claims.InlineGrant{read}}))
		if err != nil || len(warnings) != 0 {
			t.Fatalf("a read mapping: %v %v", codes(warnings), err)
		}
	})
}

func TestR_CI_013_TheGrantSpecification(t *testing.T) {
	t.Run("R-CI-013 manage is action_not_allowed", func(t *testing.T) {
		g := npmPublish()
		g.Actions = []claims.Action{"manage"}
		refused(t, publicIssuer(), input(t, publishMatch, claims.Spec{Inline: []claims.InlineGrant{g}}), reason.ActionNotAllowed, "/grants/inline/0/actions/0")
		g.Actions = []claims.Action{claims.Read, "write"}
		refused(t, publicIssuer(), input(t, publishMatch, claims.Spec{Inline: []claims.InlineGrant{g}}), reason.ActionNotAllowed, "/grants/inline/0/actions/1")
	})
	t.Run("R-CI-013 a listed policy that grants manage is action_not_allowed", func(t *testing.T) {
		in := input(t, publishMatch, claims.Spec{Policies: []string{"admins"}})
		in.PolicyActions = []claims.Action{claims.Read, "manage"}
		refused(t, publicIssuer(), in, reason.ActionNotAllowed, "/grants/policies")
	})
	t.Run("R-CI-013 an empty grant specification is empty_grant", func(t *testing.T) {
		refused(t, publicIssuer(), input(t, publishMatch, claims.Spec{}), reason.EmptyGrant, "/grants")
		g := npmPublish()
		g.Actions = nil
		refused(t, publicIssuer(), input(t, publishMatch, claims.Spec{Inline: []claims.InlineGrant{g}}), reason.EmptyGrant, "/grants/inline/0/actions")
		g = npmPublish()
		g.Repositories = nil
		refused(t, publicIssuer(), input(t, publishMatch, claims.Spec{Inline: []claims.InlineGrant{g}}), reason.EmptyGrant, "/grants/inline/0/repositories")
	})
	t.Run("R-CI-013 the ownership default is resolved and stored", func(t *testing.T) {
		read := npmPublish()
		read.Actions = []claims.Action{claims.Read}
		checked, warnings, err := check(t, publicIssuer(), input(t, publishMatch, claims.Spec{Inline: []claims.InlineGrant{npmPublish(), read}}))
		if err != nil || len(warnings) != 0 {
			t.Fatalf("%v %v", codes(warnings), err)
		}
		if checked.Grants.Inline[0].Ownership != claims.OwnershipBindProject || checked.Grants.Inline[1].Ownership != claims.OwnershipNone {
			t.Fatalf("%+v", checked.Grants.Inline)
		}
	})
	t.Run("R-CI-013 an explicit none on a writing grant saves with shared_ownership", func(t *testing.T) {
		g := npmPublish()
		g.Ownership = claims.OwnershipNone
		checked, warnings, err := check(t, publicIssuer(), input(t, publishMatch, claims.Spec{Inline: []claims.InlineGrant{g}}))
		if err != nil || !slices.Equal(codes(warnings), []string{string(reason.WarnSharedOwnership)}) || warnings[0].Pointer != "/grants/inline/0/ownership" {
			t.Fatalf("%+v %v", warnings, err)
		}
		if checked.Grants.Inline[0].Ownership != claims.OwnershipNone {
			t.Fatal("none was not kept")
		}
	})
	t.Run("R-CI-013 an inline grant names exactly one of packages and paths", func(t *testing.T) {
		g := npmPublish()
		g.Paths = &claims.PatternSet{}
		refused(t, publicIssuer(), input(t, publishMatch, claims.Spec{Inline: []claims.InlineGrant{g}}), reason.InvalidValue, "/grants/inline/0")
		g.Packages, g.Paths = nil, nil
		refused(t, publicIssuer(), input(t, publishMatch, claims.Spec{Inline: []claims.InlineGrant{g}}), reason.InvalidValue, "/grants/inline/0")
	})
	t.Run("R-CI-013 an unknown ownership is refused", func(t *testing.T) {
		g := npmPublish()
		g.Ownership = "shared"
		refused(t, publicIssuer(), input(t, publishMatch, claims.Spec{Inline: []claims.InlineGrant{g}}), reason.InvalidValue, "/grants/inline/0/ownership")
	})
}

func TestR_CI_014_TemplatesOnSave(t *testing.T) {
	spec := func(repos []string, pkgs ...string) claims.Spec {
		return claims.Spec{Inline: []claims.InlineGrant{{Repositories: repos, Packages: &claims.PatternSet{Include: pkgs}, Actions: []claims.Action{claims.Read}}}}
	}
	t.Run("R-CI-014 an unknown filter is template_invalid", func(t *testing.T) {
		refused(t, publicIssuer(), input(t, publishMatch, spec([]string{"npm-internal"}, "{{ project_path | upper }}")), reason.TemplateInvalid, "/grants/inline/0/packages/include/0")
	})
	t.Run("R-CI-014 a repositories template without a literal anchor is template_unanchored", func(t *testing.T) {
		for _, tpl := range []string{"{{ namespace_path | slug }}", "{{ namespace_path | slug }}*"} {
			refused(t, publicIssuer(), input(t, publishMatch, spec([]string{"npm-all", tpl}, "x")), reason.TemplateUnanchored, "/grants/inline/0/repositories/1")
		}
	})
	t.Run("R-CI-014 a repositories template never renders a class", func(t *testing.T) {
		refused(t, publicIssuer(), input(t, publishMatch, spec([]string{"@any-{{ x | require \"local\" }}"}, "x")), reason.TemplateInvalid, "/grants/inline/0/repositories/0")
	})
	t.Run("R-CI-014 a literal selector has the syntax of R-AUTHZ-004", func(t *testing.T) {
		for _, sel := range []string{"NPM", "@any-thing", "npm-internal:npm", "a"} {
			refused(t, publicIssuer(), input(t, publishMatch, spec([]string{sel}, "x")), reason.InvalidPattern, "/grants/inline/0/repositories/0")
		}
		if _, _, err := check(t, publicIssuer(), input(t, publishMatch, spec([]string{"@any-local:npm", "npm-team-*", "@any"}, "x"))); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-CI-014 an anchored repositories template without require warns repository_template_unconstrained", func(t *testing.T) {
		_, warnings, err := check(t, publicIssuer(), input(t, publishMatch, spec([]string{"{{ namespace_path | slug }}-releases"}, "x")))
		if err != nil || !slices.Equal(codes(warnings), []string{string(reason.WarnRepositoryTemplateUnconstraint)}) {
			t.Fatalf("%v %v", codes(warnings), err)
		}
		_, warnings, err = check(t, publicIssuer(), input(t, publishMatch, spec([]string{`{{ project_path | segment 0 | require "acme" }}-releases`}, "x")))
		if err != nil || len(warnings) != 0 {
			t.Fatalf("with require: %v %v", codes(warnings), err)
		}
	})
	t.Run("R-CI-010 a template claim that is neither required nor guarded warns", func(t *testing.T) {
		_, warnings, err := check(t, publicIssuer(), input(t, publishMatch, spec([]string{"npm-internal"}, "@acme/{{ environment }}")))
		if err != nil || !slices.Equal(codes(warnings), []string{string(reason.WarnUnguardedTemplateClaim)}) || !strings.Contains(warnings[0].Detail, "environment") {
			t.Fatalf("%+v %v", warnings, err)
		}
		guarded := `{"all":[{"claim":"namespace_id","in":["4711"]},{"claim":"environment","present":true}]}`
		_, warnings, err = check(t, publicIssuer(), input(t, guarded, spec([]string{"npm-internal"}, "@acme/{{ environment }}")))
		if err != nil || len(warnings) != 0 {
			t.Fatalf("guarded: %v %v", codes(warnings), err)
		}
	})
	t.Run("R-CI-014 a pattern list over 256 is invalid_pattern", func(t *testing.T) {
		many := make([]string, 257)
		for i := range many {
			many[i] = "a"
		}
		refused(t, publicIssuer(), input(t, publishMatch, spec([]string{"npm-internal"}, many...)), reason.InvalidPattern, "/grants/inline/0/packages/include")
	})
}

func TestR_CI_010_TheConditionWarnings(t *testing.T) {
	g := claims.Spec{Policies: []string{"ci-read"}}
	cases := []struct {
		cond string
		want reason.Reason
		ptr  string
	}{
		{`{"all":[{"claim":"project_id","equals":"1"},{"claim":"sub","glob":"project_path:acme/*"}]}`, reason.WarnSubjectPattern, "/match/all/1"},
		{`{"all":[{"claim":"project_id","equals":"1"},{"not":{"claim":"sub","regex":".*:ref_type:branch:.*"}}]}`, reason.WarnSubjectPattern, "/match/all/1/not"},
		{`{"all":[{"claim":"project_id","equals":"1"},{"claim":"user_login","equals":"jdoe"}]}`, reason.WarnMutableUserClaim, "/match/all/1"},
		{`{"all":[{"claim":"project_id","equals":"1"},{"any":[{"claim":"user_email","glob":"*@acme.example"}]}]}`, reason.WarnMutableUserClaim, "/match/all/1/any/0"},
	}
	for _, c := range cases {
		t.Run("R-CI-010 "+string(c.want)+" at "+c.ptr, func(t *testing.T) {
			_, warnings, err := check(t, publicIssuer(), input(t, c.cond, g))
			if err != nil || len(warnings) != 1 || warnings[0].Code != c.want || warnings[0].Pointer != c.ptr {
				t.Fatalf("%+v %v", warnings, err)
			}
		})
	}
	t.Run("R-CI-010 sub with equals does not warn", func(t *testing.T) {
		_, warnings, err := check(t, publicIssuer(), input(t, `{"all":[{"claim":"project_id","equals":"1"},{"claim":"sub","equals":"x"}]}`, g))
		if err != nil || len(warnings) != 0 {
			t.Fatalf("%v %v", codes(warnings), err)
		}
	})
	t.Run("R-CI-010 condition errors carry the pointer of the body", func(t *testing.T) {
		refused(t, publicIssuer(), input(t, `{"all":[{"claim":"project_id","equals":"1"},{"claim":"ref","glob":"a**"}]}`, g), reason.ConditionInvalid, "/match/all/1/glob")
		refused(t, publicIssuer(), input(t, `{}`, g), reason.CatchAllCondition, "/match")
	})
}

func TestR_CI_012_TheReadOnlyOptInIsVisible(t *testing.T) {
	in := input(t, publishMatch, claims.Spec{Policies: []string{"ci-read"}})
	in.Options.CrossProjectSource = claims.CrossProjectReadOnly
	checked, warnings, err := check(t, publicIssuer(), in)
	if err != nil || !slices.Equal(codes(warnings), []string{string(reason.WarnCrossProjectReadOnly)}) || checked.Options.CrossProjectSource != claims.CrossProjectReadOnly {
		t.Fatalf("%v %v", codes(warnings), err)
	}
	in.Options.CrossProjectSource = "allow"
	refused(t, publicIssuer(), in, reason.InvalidValue, "/options/crossProjectSource")
}

func TestTheJobTokenSubjectKindIsRefused(t *testing.T) {
	in := input(t, publishMatch, claims.Spec{Policies: []string{"ci-read"}})
	in.SubjectKinds = []string{"id_token", "job_token"}
	refused(t, publicIssuer(), in, reason.JobTokenFallbackNotAvailable, "/subjectKinds/1")
	in.SubjectKinds = []string{"id_token"}
	if _, _, err := check(t, publicIssuer(), in); err != nil {
		t.Fatal(err)
	}
}

func TestTheBoundsOfAProjectMapping(t *testing.T) {
	f := publicIssuer()
	f.Project = "web"
	g := claims.Spec{Policies: []string{"web-read"}}
	t.Run("a project mapping's name starts with the project key and a hyphen", func(t *testing.T) {
		for _, name := range []string{"release", "web", "web-", "shop-release", "webrelease"} {
			in := input(t, publishMatch, g)
			in.Name = name
			refused(t, f, in, reason.ProjectPrefixRequired, "/name")
		}
		in := input(t, publishMatch, g)
		in.Name = "web-release"
		if _, _, err := check(t, f, in); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("groups are refused in a project", func(t *testing.T) {
		in := input(t, publishMatch, claims.Spec{Policies: []string{"web-read"}, Groups: []string{"developers"}})
		in.Name = "web-release"
		refused(t, f, in, reason.GroupsNotAllowedInProject, "/grants/groups")
	})
	t.Run("the rules of R-CI-010 and R-CI-011 apply unchanged in a project", func(t *testing.T) {
		in := input(t, `{"claim":"namespace_path","glob":"acme/**"}`, g)
		in.Name = "web-release"
		refused(t, f, in, reason.PinRequired, "/match")
		in = input(t, `{"claim":"namespace_id","in":["4711"]}`, claims.Spec{Inline: []claims.InlineGrant{npmPublish()}})
		in.Name = "web-release"
		refused(t, f, in, reason.UnprotectedWrite, "/match")
		in = input(t, `{}`, g)
		in.Name = "web-release"
		refused(t, f, in, reason.CatchAllCondition, "/match")
		g := npmPublish()
		g.Actions = []claims.Action{"manage"}
		in = input(t, publishMatch, claims.Spec{Inline: []claims.InlineGrant{g}})
		in.Name = "web-release"
		refused(t, f, in, reason.ActionNotAllowed, "/grants/inline/0/actions/0")
	})
}
