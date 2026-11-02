package ci

import (
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/ci/reason"
)

func leaf(claim string, value any) claims.Node {
	raw := []byte(`"` + value.(string) + `"`)
	return claims.Node{Claim: claim, Equals: raw}
}

func onClaim(claim, value string) claims.Node {
	return claims.Node{All: []claims.Node{leaf(claim, value)}}
}

func readGrant(repo string) claims.Spec {
	return claims.Spec{Inline: []claims.InlineGrant{{Repositories: []string{repo}, Packages: &claims.PatternSet{Include: []string{"*"}}, Actions: []claims.Action{claims.Read, claims.Deploy}}}}
}

func mappingOf(tier string, priority int, name string, match claims.Node, g claims.Spec) Mapping {
	m := Mapping{
		ID: "id-" + name, Issuer: "gitlab-main", Name: name, Ref: claims.MappingRef("gitlab-main", name), Tier: tier, Priority: priority,
		Enabled: true, SubjectKinds: []string{SubjectIDToken}, Match: match, Grants: g, Options: claims.Options{CrossProjectSource: claims.CrossProjectDeny},
	}
	if key, ok := cutProject(tier); ok {
		m.Project, m.ProjectID = key, "pid-"+key
	}
	return m
}

func cutProject(tier string) (string, bool) {
	const p = "project:"
	if len(tier) > len(p) && tier[:len(p)] == p {
		return tier[len(p):], true
	}
	return "", false
}

func names(ms []Mapping) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Name)
	}
	return out
}

// R-CI-009: the first match of a tier decides, in ascending priority, whatever order the set arrives in.
func TestR_CI_009_SelectTakesTheFirstMatchOfATier(t *testing.T) {
	set := MappingSet{Mappings: []Mapping{
		mappingOf(TierSystem, 20, "p20", onClaim("ref_protected", "true"), readGrant("npm-internal")),
		mappingOf(TierSystem, 10, "p10", onClaim("pipeline_source", "merge_request_event"), readGrant("npm-internal")),
	}}
	both := claims.Claims{"ref_protected": "true", "pipeline_source": "merge_request_event"}
	sel := Select(set, SubjectIDToken, both)
	if got := names(sel.Mappings()); !slices.Equal(got, []string{"p10"}) {
		t.Errorf("both match: selected %v, want only p10", got)
	}
	if len(sel.Trace) != 1 {
		t.Errorf("the trace %v names a mapping after the tier's first match", sel.Trace)
	}
	push := claims.Claims{"ref_protected": "true", "pipeline_source": "push"}
	sel = Select(set, SubjectIDToken, push)
	if got := names(sel.Mappings()); !slices.Equal(got, []string{"p20"}) || len(sel.Trace) != 2 || sel.Trace[0].Matched || !sel.Trace[1].Matched {
		t.Errorf("a push: selected %v, trace %+v", got, sel.Trace)
	}
	if sel.Trace[0].FailedLeaf != "/all/0" {
		t.Errorf("the failed leaf is %q", sel.Trace[0].FailedLeaf)
	}
}

// R-CI-009: no match, a disabled mapping and another subject kind.
func TestR_CI_009_SelectSkipsWhatIsNotConsidered(t *testing.T) {
	off := mappingOf(TierSystem, 10, "off", onClaim("project_id", "1"), readGrant("npm-internal"))
	off.Enabled = false
	job := mappingOf(TierSystem, 20, "job", onClaim("project_id", "1"), readGrant("npm-internal"))
	job.SubjectKinds = []string{SubjectJobToken}
	on := mappingOf(TierSystem, 30, "on", onClaim("project_id", "1"), readGrant("npm-internal"))
	sel := Select(MappingSet{Mappings: []Mapping{off, job, on}}, SubjectIDToken, claims.Claims{"project_id": "1"})
	if got := names(sel.Mappings()); !slices.Equal(got, []string{"on"}) || len(sel.Trace) != 1 {
		t.Errorf("selected %v, trace %v", got, sel.Trace)
	}
	none := Select(MappingSet{Mappings: []Mapping{on}}, SubjectIDToken, claims.Claims{"project_id": "2"})
	if none.Failure != reason.NoMatchingMapping || len(none.Contributions) != 0 {
		t.Errorf("failure %q", none.Failure)
	}
}

// R-CI-009: a render failure fails the exchange and does not fall through to a later mapping.
func TestR_CI_009_SelectDoesNotFallThroughOnARenderFailure(t *testing.T) {
	failing := claims.Spec{Inline: []claims.InlineGrant{{Repositories: []string{"npm-internal"}, Packages: &claims.PatternSet{Include: []string{"@{{ namespace_path | slug }}/*"}}, Actions: []claims.Action{claims.Read}}}}
	set := MappingSet{Mappings: []Mapping{
		mappingOf(TierSystem, 10, "p10", onClaim("project_id", "1"), failing),
		mappingOf(TierSystem, 20, "p20", onClaim("project_id", "1"), readGrant("npm-internal")),
	}}
	sel := Select(set, SubjectIDToken, claims.Claims{"project_id": "1"})
	if sel.Failure != reason.GrantRenderFailed || len(sel.Contributions) != 0 {
		t.Fatalf("failure %q, contributions %d", sel.Failure, len(sel.Contributions))
	}
	if len(sel.Trace) != 1 || !sel.Trace[0].Matched || sel.Trace[0].Detail == "" {
		t.Errorf("trace = %+v", sel.Trace)
	}
}

// R-CI-009, Q-CI-26: a match per tier, united; each contribution keeps its own rendering, caps and project.
func TestR_CI_009_SelectUnitesTheTiersAndKeepsTheContributionsApart(t *testing.T) {
	system := mappingOf(TierSystem, 10, "sys", onClaim("project_id", "1"), readGrant("npm-internal"))
	web := mappingOf(ProjectTier("web"), 10, "web-reader", onClaim("project_id", "1"), readGrant("web-npm"))
	other := mappingOf(ProjectTier("data"), 10, "data-reader", onClaim("project_id", "2"), readGrant("data-npm"))
	set := MappingSet{Mappings: []Mapping{web, other, system}}
	sel := Select(set, SubjectIDToken, claims.Claims{"project_id": "1"})
	if got := names(sel.Mappings()); !slices.Equal(got, []string{"sys", "web-reader"}) {
		t.Fatalf("selected %v: the system tier first, then the project's", got)
	}
	if sel.Contributions[0].Rendered.Inline[0].Repositories[0] != "npm-internal" || sel.Contributions[1].Rendered.Inline[0].Repositories[0] != "web-npm" {
		t.Errorf("the contributions do not hold their own grants: %+v", sel.Contributions)
	}
	f := sel.Contributions[1].Frozen()
	if f.Project == nil || f.Project.Key != "web" || f.Project.ID != "pid-web" || f.Tier != "project:web" || f.Ref != "ci:gitlab-main/web-reader" {
		t.Errorf("the project's contribution freezes as %+v", f)
	}
	if sel.Contributions[0].Frozen().Project != nil {
		t.Error("the system contribution names a project")
	}
	var bundle claims.Frozen
	bundle.Version, bundle.Issuer, bundle.IssuerID = claims.FrozenVersion, "gitlab-main", "iid"
	for _, c := range sel.Contributions {
		bundle.Add(c.Frozen())
	}
	if err := bundle.Check(); err != nil {
		t.Errorf("the bundle of the selection is refused: %v", err)
	}
	if u := unionRendered(sel.Contributions); len(u.Inline) != 2 {
		t.Errorf("the display union holds %d inline grants", len(u.Inline))
	}
}

// R-CI-012: a cross-project token gets nothing from a matched mapping that denies, the first match of the tier
// still decides, and a read_only opt-in carries the cap.
func TestR_CI_012_SelectDecidesTheCrossProjectOptInPerMapping(t *testing.T) {
	fork := claims.Claims{"project_id": "900", "job_project_id": "42", "pipeline_source": "merge_request_event"}
	deny := mappingOf(TierSystem, 10, "deny", onClaim("pipeline_source", "merge_request_event"), readGrant("npm-internal"))
	later := mappingOf(TierSystem, 20, "later", onClaim("pipeline_source", "merge_request_event"), readGrant("npm-internal"))
	later.Options.CrossProjectSource = claims.CrossProjectReadOnly

	sel := Select(MappingSet{Mappings: []Mapping{deny, later}}, SubjectIDToken, fork)
	if !sel.CrossProject || sel.Failure != reason.CrossProjectSourceDenied || len(sel.Contributions) != 0 {
		t.Errorf("failure %q: the first match denies and the tier does not fall through to the opt-in", sel.Failure)
	}
	if len(sel.Trace) != 1 || !sel.Trace[0].Matched || sel.Trace[0].Contributes {
		t.Errorf("trace = %+v", sel.Trace)
	}

	deny.Options.CrossProjectSource = claims.CrossProjectReadOnly
	sel = Select(MappingSet{Mappings: []Mapping{deny}}, SubjectIDToken, fork)
	if sel.Failure != "" || len(sel.Contributions) != 1 || !slices.Equal(sel.Contributions[0].Caps, []claims.Cap{claims.CapRead}) {
		t.Fatalf("failure %q, contributions %+v", sel.Failure, sel.Contributions)
	}
	if acts := sel.Contributions[0].Frozen().Inline[0].Actions; !slices.Equal(acts, []claims.Action{claims.Read}) {
		t.Errorf("the frozen grant holds %v, want read only", acts)
	}

	// The opt-in of one mapping covers its own grants only: a system mapping that denies contributes nothing
	// beside a project mapping that opted in.
	sys := mappingOf(TierSystem, 10, "sys", onClaim("pipeline_source", "merge_request_event"), readGrant("npm-internal"))
	web := mappingOf(ProjectTier("web"), 10, "web-fork", onClaim("pipeline_source", "merge_request_event"), readGrant("web-npm"))
	web.Options.CrossProjectSource = claims.CrossProjectReadOnly
	sel = Select(MappingSet{Mappings: []Mapping{sys, web}}, SubjectIDToken, fork)
	if got := names(sel.Mappings()); !slices.Equal(got, []string{"web-fork"}) {
		t.Errorf("selected %v, want only the mapping that opted in", got)
	}
}
