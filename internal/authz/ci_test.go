package authz

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The CI evaluator (R-AUTHZ-014, R-CI-012, R-CI-013, R-CI-015) on the grant index, without PostgreSQL: the
// bundle comes with the subject, as it comes with the token cache entry (Q-CI-17).

const (
	idProjWeb   = "project-web"
	idProjShop  = "project-shop"
	idMapRead   = "ci-gitlab-internal-read"
	ciIssuerUID = "0190a4c2-0000-7000-8000-000000000001"
)

// laterEpoch is the authorization epoch of a bundle exchanged after the fixtures' index was built (its label is at
// epoch 1): such an index cannot know the bundle's mappings yet, so every contribution counts (Q-CI-47). The tests of
// the active mappings use bundles at the index's epoch.
const laterEpoch = 2

// frozenOf is a bundle of the issuer gitlab-internal for a GitLab project with the contributions given, exchanged
// after the fixture's index was built.
func frozenOf(projectID string, contributions ...claims.FrozenMapping) *claims.Frozen {
	f := &claims.Frozen{
		Version: claims.FrozenVersion, Issuer: "gitlab-internal", IssuerID: ciIssuerUID,
		ProjectID: projectID, ProjectPath: "acme/p" + projectID, AuthzEpoch: laterEpoch,
	}
	for _, m := range contributions {
		f.Add(m)
	}
	if err := f.Check(); err != nil {
		panic(err)
	}
	return f
}

// systemTier is a contribution of the system tier; name is "<issuer>/<mapping>".
func systemTier(name string, groups []string, inline ...claims.InlineGrant) claims.FrozenMapping {
	return claims.FrozenMapping{Ref: "ci:" + name, ID: "m-" + name, Tier: "system", Groups: groups, Inline: inline}
}

// projectTier is a contribution of the tier of project key, whose trove ID is id.
func projectTier(name, key, id string, inline ...claims.InlineGrant) claims.FrozenMapping {
	return claims.FrozenMapping{Ref: "ci:" + name, ID: "m-" + name, Tier: "project:" + key, Project: &claims.FrozenProject{Key: key, ID: id}, Inline: inline}
}

func capped(m claims.FrozenMapping) claims.FrozenMapping {
	m.Caps = []claims.Cap{claims.CapRead}
	return m
}

// ciSubject is the subject a CI token's request carries (edge.SubjectOf with Principal.CI).
func ciSubject(f *claims.Frozen) Subject {
	return Subject{Kind: principal.KindCI, ID: idMapAcm, Name: strings.TrimPrefix(f.Mappings[0], "ci:"), Mappings: f.Mappings, CI: f, TokenID: "tok-1"}
}

func inline(repos []string, pkgs []string, own claims.Ownership, acts ...claims.Action) claims.InlineGrant {
	g := claims.InlineGrant{Repositories: repos, Actions: acts, Ownership: own}
	if pkgs != nil {
		g.Packages = &claims.PatternSet{Include: pkgs}
	}
	if own == "" {
		g.Ownership = g.ResolvedOwnership()
	}
	return g
}

// ciFixture: npm-internal and npm-loose are global, npm-web is the project web's, npm-shop the project shop's;
// npm-all reads npm-internal and npm-web. ci-read-internal gives the mapping gitlab-internal/read read on
// npm-all and npm-internal; the group ci-readers reads npm-loose; web-ci is a project policy of web.
func ciFixture() *fixture {
	f := newFixture().
		repo("npm-internal", "local", "npm", `{"local":{"packageOwnership":"enforce"}}`).
		repo("npm-loose", "local", "npm", `{"local":{"packageOwnership":"off"}}`).
		repo("npm-web", "local", "npm", `{"local":{"packageOwnership":"enforce"}}`).
		repo("npm-shop", "local", "npm", `{"local":{"packageOwnership":"enforce"}}`).
		repo("npm-all", "virtual", "npm", `{"virtual":{"members":["npm-internal","npm-web"],"deployTarget":"npm-internal"}}`)
	f.project(idProjWeb, "web").project(idProjShop, "shop")
	f.policy(policyDef{name: "ci-read-internal", selectors: []string{"npm-all", "npm-internal", "npm-web"}, entries: map[string][]string{idMapAcm: {"read"}}})
	f.policy(policyDef{name: "ci-readers-loose", selectors: []string{"npm-loose"}, entries: map[string][]string{idWeb: {"read"}}})
	f.policy(policyDef{name: "ci-manage", selectors: []string{"npm-loose"}, entries: map[string][]string{idMapAcm: {"read", "manage"}}})
	f.policy(policyDef{name: "web-ci", project: idProjWeb, selectors: []string{"npm-web"}, entries: map[string][]string{idMapAcm: {"read", "deploy"}}})
	return f
}

func ciEngine(t *testing.T) (*Engine, *Index) {
	t.Helper()
	e := newTestEngine(t, ciFixture(), nil)
	idx, err := e.Begin(t.Context(), aliceSub)
	if err != nil {
		t.Fatal(err)
	}
	return e, idx
}

func ciAllows(e *Engine, idx *Index, sub Subject, c Coordinate) bool {
	return e.evaluate(idx, sub, c).Outcome == Allow
}

func npmRead(repo, pkg string) Coordinate {
	return Coordinate{Repository: qr(repo), Format: "npm", Kind: KindMetadata, Package: pkg, Actions: []Action{Read}}
}

// R-AUTHZ-014: the rights of a CI token are the union of the policies naming its mappings, the policies
// naming the groups they list and the frozen inline grants.
func TestR_AUTHZ_014_TheRightsOfACITokenAreTheUnionOfItsThreeKinds(t *testing.T) {
	e, idx := ciEngine(t)
	sub := ciSubject(frozenOf("101", systemTier("gitlab-internal/acme", []string{"team-web"},
		inline([]string{"it/npm-internal"}, []string{"@acme-billing/*"}, "", claims.Read, claims.Deploy))))
	for _, tc := range []struct {
		name string
		c    Coordinate
		want bool
	}{
		{"a policy naming the mapping reads npm-all", npmRead("npm-all", "left-pad"), true},
		{"a policy naming a listed group reads npm-loose", npmRead("npm-loose", "left-pad"), true},
		{"the inline grant publishes the project's name", publish("npm-internal", "@acme-billing/invoice-client"), true},
		{"publishing another namespace gets nothing", publish("npm-internal", "@acme-shipping/x"), false},
		{"a policy's manage never reaches a CI token", write("npm-loose", "left-pad", "", Manage), false},
		{"a project's policy does not grant a system mapping on another repository", publish("npm-shop", "x"), false},
	} {
		t.Run("R-AUTHZ-014 "+tc.name, func(t *testing.T) {
			if got := ciAllows(e, idx, sub, tc.c); got != tc.want {
				t.Fatalf("allowed %v, want %v (%+v)", got, tc.want, e.evaluate(idx, sub, tc.c))
			}
		})
	}
	t.Run("R-AUTHZ-014 the inline grant is named in the sources of the decision", func(t *testing.T) {
		d := e.evaluate(idx, sub, publish("npm-internal", "@acme-billing/invoice-client"))
		if !slices.Contains(d.Sources, "ci-inline:gitlab-internal/acme#0") {
			t.Fatalf("sources %v", d.Sources)
		}
	})
	t.Run("R-AUTHZ-014 a CI subject without its bundle holds nothing", func(t *testing.T) {
		bare := Subject{Kind: principal.KindCI, ID: idMapAcm, Name: "gitlab-internal/acme", Mappings: []string{"ci:gitlab-internal/acme"}}
		if ciAllows(e, idx, bare, npmRead("npm-all", "left-pad")) {
			t.Fatal("a CI subject without a bundle was allowed")
		}
	})
	t.Run("R-AUTHZ-014 a CI token holds no capability and administers nothing", func(t *testing.T) {
		if e.HoldsOn(idx, sub, "tokens:read") || len(e.MembershipsOn(idx, sub)) != 0 {
			t.Fatal("a CI token holds a capability or a project")
		}
	})
}

// R-AUTHZ-014: a claim value cannot widen a pattern: the escaped rendering of team/ui* matches the literal name.
func TestR_AUTHZ_014_AnEscapedRenderingMatchesOnlyTheLiteralName(t *testing.T) {
	e, idx := ciEngine(t)
	spec := claims.Spec{Inline: []claims.InlineGrant{{
		Repositories: []string{"it/npm-internal"}, Packages: &claims.PatternSet{Include: []string{"@acme/{{ project_path | segment -1 }}"}},
		Actions: []claims.Action{claims.Read, claims.Deploy},
	}}}
	r, err := spec.Render(claims.Claims{"project_path": "team/ui*"})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Inline[0].Packages.Include[0]; got != `@acme/ui\*` {
		t.Fatalf("rendered %q", got)
	}
	sub := ciSubject(frozenOf("7", systemTier("gitlab-internal/acme", nil, r.Inline...)))
	if !ciAllows(e, idx, sub, publish("npm-internal", "@acme/ui*")) {
		t.Error("the literal name is refused")
	}
	if ciAllows(e, idx, sub, publish("npm-internal", "@acme/ui-kit")) {
		t.Error("the escaped pattern matched @acme/ui-kit")
	}
}

// R-CI-012: the cap read of a mapping bounds its own grants only.
func TestR_CI_012_TheCapBoundsOnlyItsOwnMappingsGrants(t *testing.T) {
	e, idx := ciEngine(t)
	writer := systemTier("gitlab-internal/writer", nil, inline([]string{"it/npm-internal"}, []string{"@acme/*"}, "", claims.Read, claims.Deploy))
	reader := capped(projectTier("gitlab-internal/web-read", "web", idProjWeb, inline([]string{"@any"}, nil, "", claims.Read, claims.Deploy)))
	sub := ciSubject(frozenOf("900", writer, reader))
	if !ciAllows(e, idx, sub, publish("npm-internal", "@acme/x")) {
		t.Error("the uncapped mapping's deploy was capped by the other mapping's cap")
	}
	if ciAllows(e, idx, sub, publish("npm-web", "anything")) {
		t.Error("the capped mapping's deploy passed its cap")
	}
	if !ciAllows(e, idx, sub, npmRead("npm-web", "anything")) {
		t.Error("the capped mapping's read was removed")
	}
	only := ciSubject(frozenOf("900", capped(systemTier("gitlab-internal/acme", []string{"team-web"},
		inline([]string{"it/npm-internal"}, nil, "", claims.Read, claims.Deploy)))))
	if ciAllows(e, idx, only, publish("npm-internal", "x")) || !ciAllows(e, idx, only, npmRead("npm-loose", "x")) {
		t.Error("the cap of the only mapping does not cap its inline grants or keeps its group's read from it")
	}
}

// R-AUTHZ-014, Q-CI-26, Q-AUTHZ-179: the inline grants of a project tier are compiled against the project's
// repositories only, and only the project's own policies grant its mapping.
func TestR_AUTHZ_014_AProjectTiersGrantsStayInsideItsProject(t *testing.T) {
	e, idx := ciEngine(t)
	m := projectTier("gitlab-internal/acme", "web", idProjWeb,
		inline([]string{"@any"}, []string{"@web/*"}, "", claims.Read, claims.Deploy),
		inline([]string{"npm-shop", "npm-internal"}, nil, "", claims.Read, claims.Deploy))
	m.Groups = []string{"team-web"} // a project tier lists no groups (Q-CI-26); a bundle that does is not trusted with them
	sub := ciSubject(frozenOf("101", m))
	for _, tc := range []struct {
		name string
		c    Coordinate
		want bool
	}{
		{"@any covers the project's repository", publish("npm-web", "@web/button"), true},
		{"@any does not cover another project's repository", publish("npm-shop", "@web/button"), false},
		{"a key outside the project is inert", publish("npm-internal", "@web/button"), false},
		{"another project's key is inert", npmRead("npm-shop", "x"), false},
		{"the project's own policy grants the project mapping", publish("npm-web", "other"), true},
		{"a global policy naming the project mapping grants nothing", npmRead("npm-all", "x"), false},
		{"the groups of a project tier grant nothing", npmRead("npm-loose", "x"), false},
	} {
		t.Run("Q-CI-26 "+tc.name, func(t *testing.T) {
			if got := ciAllows(e, idx, sub, tc.c); got != tc.want {
				t.Fatalf("allowed %v, want %v", got, tc.want)
			}
		})
	}
}

// R-CI-016, R-CI-017, Q-CI-47: a contribution gives its grants until its own expiry and while its mapping is active;
// a contribution that ended or whose mapping was disabled or deleted takes none of the other contributions' grants
// with it.
func TestQ_CI_47_AContributionEndsWithoutTheOthers(t *testing.T) {
	f := ciFixture()
	// The index knows the system mapping acme and the project mapping shop-reader as active; shop-gone was disabled or
	// deleted before the index was built. The project shop's repository npm-shop is the project tier's alone.
	f.s.CIMappings = []string{"m-gitlab-internal/acme", "m-gitlab-internal/shop-reader"}
	e := newTestEngine(t, f, nil)
	idx, err := e.Begin(t.Context(), aliceSub)
	if err != nil {
		t.Fatal(err)
	}
	sys := systemTier("gitlab-internal/acme", nil, inline([]string{"it/npm-internal"}, []string{"@acme/*"}, "", claims.Read))
	reader := projectTier("gitlab-internal/shop-reader", "shop", idProjShop, inline([]string{"@any"}, []string{"*"}, "", claims.Read))
	gone := projectTier("gitlab-internal/shop-gone", "shop", idProjShop, inline([]string{"@any"}, []string{"*"}, "", claims.Read))
	atIndex := func(ms ...claims.FrozenMapping) Subject {
		b := frozenOf("101", ms...)
		b.AuthzEpoch = idx.Epoch()
		return ciSubject(b)
	}
	internal, web := npmRead("npm-internal", "@acme/x"), npmRead("npm-shop", "x")

	t.Run("Q-CI-47 a contribution past its own expiry gives nothing, the others still do", func(t *testing.T) {
		past, future := time.Now().Add(-time.Second), time.Now().Add(time.Hour)
		ended, open := reader, reader
		ended.ExpiresAt, open.ExpiresAt = &past, &future
		if sub := atIndex(sys, ended); ciAllows(e, idx, sub, web) || !ciAllows(e, idx, sub, internal) {
			t.Error("an ended contribution still reads, or took the system tier's read with it")
		}
		if sub := atIndex(sys, open); !ciAllows(e, idx, sub, web) {
			t.Error("a contribution before its expiry does not read")
		}
	})
	t.Run("Q-CI-47 an index at the bundle's epoch drops the contribution of a mapping it does not hold as active", func(t *testing.T) {
		sub := atIndex(sys, gone)
		if ciAllows(e, idx, sub, web) {
			t.Error("the contribution of a disabled or deleted mapping still reads")
		}
		if !ciAllows(e, idx, sub, internal) {
			t.Error("the system tier's read went with the other mapping")
		}
		if sub := atIndex(sys, reader); !ciAllows(e, idx, sub, web) {
			t.Error("an active mapping's contribution was dropped")
		}
	})
	t.Run("Q-CI-47 an index older than the bundle keeps every contribution, it cannot know a mapping created just before", func(t *testing.T) {
		if sub := ciSubject(frozenOf("101", sys, gone)); !ciAllows(e, idx, sub, web) {
			t.Error("a contribution of a mapping newer than the index was dropped")
		}
	})
	t.Run("Q-CI-47 a bundle of version 1 is decided against every index", func(t *testing.T) {
		old := frozenOf("101", sys, gone)
		old.Version, old.AuthzEpoch = claims.FrozenVersion1, 0
		if err := old.Check(); err != nil {
			t.Fatal(err)
		}
		if sub := ciSubject(old); ciAllows(e, idx, sub, web) || !ciAllows(e, idx, sub, internal) {
			t.Error("the version-1 bundle's inactive contribution still reads, or its active one does not")
		}
	})
}

// R-AUTHZ-013, R-CI-007: the token's scope is the ceiling of its CI grants.
func TestR_AUTHZ_013_TheScopeIsTheCeilingOfACIToken(t *testing.T) {
	e, idx := ciEngine(t)
	sub := ciSubject(frozenOf("101", systemTier("gitlab-internal/acme", nil, inline([]string{"it/npm-internal"}, []string{"@acme/*"}, "", claims.Read, claims.Deploy))))
	sub.Scope = &scope.Scope{Repositories: []scope.Grant{{Repositories: []string{"it/npm-internal"}, Packages: scope.Patterns{Include: []string{"@acme/*"}}, Actions: []string{"read"}}}}
	if ciAllows(e, idx, sub, publish("npm-internal", "@acme/x")) {
		t.Error("the scope did not bound the inline deploy")
	}
	if !ciAllows(e, idx, sub, npmRead("npm-internal", "@acme/x")) {
		t.Error("the scope removed the read it admits")
	}
}

// R-AUTHZ-016: a listing of a CI token shows what its inline grants read.
func TestR_AUTHZ_014_AListingFollowsTheInlineGrants(t *testing.T) {
	e, idx := ciEngine(t)
	sub := ciSubject(frozenOf("101", systemTier("gitlab-internal/none", nil, inline([]string{"it/npm-loose"}, []string{"@acme/*"}, "", claims.Read))))
	st := e.newEvalState(idx, sub)
	if got := e.readShape(st, sub, idx.repo(qr("npm-loose")), true); got != ReadRows {
		t.Fatalf("shape %v, want rows", got)
	}
	if got := e.readShape(st, sub, idx.repo(qr("npm-internal")), true); got != ReadNone {
		t.Fatalf("shape %v, want none", got)
	}
}

// R-CI-015, R-AUTHZ-018: when a CI write is checked and whom its claim records.
func TestR_CI_015_TheClaimOfACIPublish(t *testing.T) {
	e, idx := ciEngine(t)
	o, err := NewOwnership(OwnershipOptions{Engine: e})
	if err != nil {
		t.Fatal(err)
	}
	bind := ciSubject(frozenOf("101", systemTier("gitlab-internal/acme", nil, inline([]string{"it/npm-internal", "it/npm-loose"}, []string{"@acme/*"}, "", claims.Read, claims.Deploy))))
	none := ciSubject(frozenOf("101", systemTier("gitlab-internal/acme", nil, inline([]string{"it/npm-internal"}, []string{"@acme/*"}, claims.OwnershipNone, claims.Read, claims.Deploy))))
	policy := ciSubject(frozenOf("101", projectTier("gitlab-internal/acme", "web", idProjWeb)))
	claimers := func(sub Subject, c Coordinate) []string {
		k, applies, err := o.keyFor(idx, sub, c)
		if err != nil || !applies {
			t.Fatalf("applies %v, %v", applies, err)
		}
		var out []string
		for _, cl := range o.factsOf(idx, sub, k, c).claimers {
			switch {
			case cl.project != nil:
				out = append(out, cl.project.Ref())
			case cl.mapping != "":
				out = append(out, "unresolved:"+cl.mapping)
			default:
				out = append(out, cl.ref.String())
			}
		}
		return out
	}
	t.Run("R-CI-015 a bind_project grant claims for the project only", func(t *testing.T) {
		if got := claimers(bind, publish("npm-internal", "@acme/client")); !slices.Equal(got, []string{"ci:gitlab-internal:project:101"}) {
			t.Fatalf("claimers %v", got)
		}
	})
	t.Run("R-AUTHZ-018 a bind_project grant is checked where packageOwnership is off", func(t *testing.T) {
		if _, applies, _ := o.keyFor(idx, bind, publish("npm-loose", "@acme/client")); !applies {
			t.Fatal("not checked")
		}
		if _, applies, _ := o.keyFor(idx, none, publish("npm-loose", "@acme/client")); applies {
			t.Fatal("an ownership none grant made the off repository checked")
		}
	})
	t.Run("R-AUTHZ-018 an inline grant with ownership none makes the mapping the owner", func(t *testing.T) {
		if got := claimers(none, publish("npm-internal", "@acme/client")); !slices.Equal(got, []string{"ci:gitlab-internal/acme"}) {
			t.Fatalf("claimers %v", got)
		}
	})
	t.Run("R-AUTHZ-018 a project policy entry makes the mapping the owner", func(t *testing.T) {
		if got := claimers(policy, publish("npm-web", "x")); !slices.Equal(got, []string{"ci:gitlab-internal/acme"}) {
			t.Fatalf("claimers %v", got)
		}
	})
	t.Run("R-CI-015 the owner identities are the project, the mappings and their groups", func(t *testing.T) {
		k, _, _ := o.keyFor(idx, bind, publish("npm-internal", "@acme/client"))
		f := o.factsOf(idx, bind, k, publish("npm-internal", "@acme/client"))
		mine := Owner{Owner: "ci:gitlab-internal:project:101", ciIssuerID: ciIssuerUID, ciProjectID: "101"}
		theirs := Owner{Owner: "ci:gitlab-internal:project:202", ciIssuerID: ciIssuerUID, ciProjectID: "202"}
		mapping := Owner{Owner: "ci:gitlab-internal/acme", principalID: "elsewhere"}
		write := OwnershipWrite{Publish: true}
		if ok, _, _ := judge(StateClaimed, []Owner{mine}, write, f); !ok {
			t.Error("the project's own name was refused")
		}
		if ok, _, _ := judge(StateClaimed, []Owner{theirs}, write, f); ok {
			t.Error("another project's name was allowed")
		}
		if ok, _, _ := judge(StateClaimed, []Owner{mapping}, write, f); !ok {
			t.Error("the mapping's name was refused")
		}
		if ok, _, _ := judge(StateUnclaimed, nil, OwnershipWrite{Publish: true, FirstVersion: false}, f); ok {
			t.Error("an unclaimed name with versions was allowed without manage")
		}
	})
}

func projectRow(id, key string) (r q.AuthzBuildProjectsRow) {
	r.ID, r.Key = id, key
	return r
}
