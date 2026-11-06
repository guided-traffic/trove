package claims_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/ci/reason"
)

// R-CI-007: the pure scope grammar, the part of the scope parameter that needs no grant index; the exchange
// applies it (internal/ci/exchange_scope.go).
func TestR_CI_007_TheScopeGrammar(t *testing.T) {
	ok := []struct {
		scope string
		want  []claims.ScopeItem
	}{
		{"read:web/npm-all deploy:web/npm-internal", []claims.ScopeItem{{Action: claims.Read, Selector: "web/npm-all"}, {Action: claims.Deploy, Selector: "web/npm-internal"}}},
		{"read", []claims.ScopeItem{{Action: claims.Read}}},
		{"read:@any-local:npm", []claims.ScopeItem{{Action: claims.Read, Selector: "@any-local:npm"}}},
		{"read:web/npm-team-* read:web/npm-team-*", []claims.ScopeItem{{Action: claims.Read, Selector: "web/npm-team-*"}}},
		{"annotate:web/x1 overwrite:web/x1 delete:web/x1", []claims.ScopeItem{{Action: claims.Annotate, Selector: "web/x1"}, {Action: claims.Overwrite, Selector: "web/x1"}, {Action: claims.Delete, Selector: "web/x1"}}},
		{"", nil},
	}
	for _, c := range ok {
		t.Run(strconv.Quote(c.scope)+" parses", func(t *testing.T) {
			got, err := claims.ParseScope(c.scope)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("%+v", got)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("%+v", got)
				}
			}
		})
	}
	var fifty, fiftyOne []string
	for i := range 51 {
		fiftyOne = append(fiftyOne, "read:web/repo-"+strconv.Itoa(i))
	}
	fifty = append(append(fifty, fiftyOne[:50]...), fiftyOne[0])
	if items, err := claims.ParseScope(strings.Join(fifty, " ")); err != nil || len(items) != 50 {
		t.Fatalf("R-CI-007 50 distinct items and a duplicate: %d %v", len(items), err)
	}
	bad := []string{
		"manage:web/npm-internal", "manage", "write:web/npm-internal", "Read:web/npm-internal", "read:", "read:NPM-internal",
		"read:web/npm-internal:npm", "read:@any-thing", "read:@any-local:", "read  deploy", " read", "read ",
		`read:"x"`, "read:x\\y", "read:é", strings.Join(fiftyOne, " "),
	}
	for _, s := range bad {
		t.Run(strconv.Quote(s[:min(len(s), 40)])+" is scope_malformed", func(t *testing.T) {
			_, err := claims.ParseScope(s)
			if r, _ := reasonOf(t, err); r != reason.ScopeMalformed {
				t.Fatalf("%s", r)
			}
		})
	}
}

// R-CI-007: the pure scope grammar, the part of the scope parameter that needs no grant index; the exchange
// applies it (internal/ci/exchange_scope.go).
func TestR_CI_007_TheSelectorSyntaxIsTheAuthorizationEngines(t *testing.T) {
	for _, sel := range []string{
		"web/npm-internal", "web/ab", "w/a", "web/npm-team-*", "w*/npm-*", "web/n?m", "*/*", "*/npm-remote", "@any", "@any-local",
		"@any-remote", "@any-virtual", "@any-local:npm", "web/npm-*:npm", "*/*:nuget", "web/npm-internal:npm", "@any:", "@anything",
		"web/NPM", "web/-npm", "web/npm_x", "web/" + strings.Repeat("a", 64), "web/" + strings.Repeat("a", 65), "web/a**b",
		"@any-local:npm:x", "npm-internal", "ab", "a", "npm-team-*", "*", "web/", "/npm", "web/npm/x", "WEB/npm", "-web/npm",
	} {
		_, err := claims.ParseScope("read:" + sel)
		ours := err == nil
		theirs := authz.ValidateSelector(sel) == nil
		if ours != theirs {
			t.Errorf("R-CI-007 selector %q: the scope grammar says %v, the authorization engine %v", sel, ours, theirs)
		}
	}
}

func inline(repos []string, actions ...claims.Action) claims.InlineGrant {
	return claims.InlineGrant{Repositories: repos, Packages: &claims.PatternSet{Include: []string{"@acme-billing/*"}}, Actions: actions, Ownership: claims.OwnershipBindProject}
}

// R-CI-007: the pure scope grammar, the part of the scope parameter that needs no grant index; the exchange
// applies it (internal/ci/exchange_scope.go).
func TestR_CI_007_NarrowingWithTheMatchedGrants(t *testing.T) {
	r := claims.Rendered{
		Policies: []string{"ci-read-internal"},
		Inline:   []claims.InlineGrant{inline([]string{"web/npm-internal"}, claims.Read, claims.Deploy), inline([]string{"web/nuget-internal", "web/nuget-team-*"}, claims.Read, claims.Deploy)},
	}
	noPolicy := func(claims.Action, string) bool { return false }
	items := func(s string) []claims.ScopeItem {
		it, err := claims.ParseScope(s)
		if err != nil {
			t.Fatal(err)
		}
		return it
	}
	t.Run("an item the grants do not hold is scope_not_granted", func(t *testing.T) {
		npmOnly := claims.Rendered{Inline: r.Inline[:1]}
		_, err := claims.Narrow(npmOnly, items("deploy:web/nuget-internal"), noPolicy)
		if rr, _ := reasonOf(t, err); rr != reason.ScopeNotGranted {
			t.Fatalf("%s", rr)
		}
		_, err = claims.Narrow(npmOnly, items("read:web/npm-internal annotate:web/npm-internal"), noPolicy)
		if rr, _ := reasonOf(t, err); rr != reason.ScopeNotGranted {
			t.Fatalf("no partial token: %s", rr)
		}
	})
	t.Run("deploy is narrowed away by a read scope", func(t *testing.T) { // R-CI-007
		got, err := claims.Narrow(r, items("read:web/npm-internal"), noPolicy)
		if err != nil {
			t.Fatal(err)
		}
		if claims.FormatScope(got, nil) != "read:web/npm-internal" || got.Policies[0] != "ci-read-internal" {
			t.Fatalf("%q %+v", claims.FormatScope(got, nil), got)
		}
	})
	t.Run("a key pattern grant holds a key item; the narrowed grant keeps only what may overlap", func(t *testing.T) { // R-CI-007
		got, err := claims.Narrow(r, items("deploy:web/nuget-team-x"), noPolicy)
		if err != nil {
			t.Fatal(err)
		}
		if s := claims.FormatScope(got, nil); s != "deploy:web/nuget-team-*" {
			t.Fatalf("%q", s)
		}
	})
	t.Run("a class item the text cannot decide is asked through held", func(t *testing.T) { // R-CI-007
		asked := ""
		held := func(a claims.Action, sel string) bool {
			asked = string(a) + ":" + sel
			return sel == "@any-local:npm" && a == claims.Read
		}
		got, err := claims.Narrow(r, items("read:@any-local:npm"), held)
		if err != nil || asked != "read:@any-local:npm" {
			t.Fatalf("%v, asked %q", err, asked)
		}
		if s := claims.FormatScope(got, nil); s != "read:web/npm-internal read:web/nuget-internal read:web/nuget-team-*" {
			t.Fatalf("every inline grant may overlap a class, only the read survives: %q", s)
		}
		if _, err := claims.Narrow(r, items("deploy:@any-local:npm"), held); err == nil {
			t.Fatal("held refused, the inline grants could not decide, and it was granted")
		}
	})
	t.Run("an item without selector is @any", func(t *testing.T) { // R-CI-007
		got, err := claims.Narrow(r, items("read"), func(_ claims.Action, sel string) bool { return sel == "@any" })
		if err != nil || claims.FormatScope(got, nil) != "read:web/npm-internal read:web/nuget-internal read:web/nuget-team-*" {
			t.Fatalf("%q %v", claims.FormatScope(got, nil), err)
		}
	})
	t.Run("no scope is no narrowing; the response lists every item in grant order", func(t *testing.T) { // R-CI-007
		got, err := claims.Narrow(r, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		want := "read:web/npm-internal deploy:web/npm-internal read:web/nuget-internal deploy:web/nuget-internal read:web/nuget-team-* deploy:web/nuget-team-*"
		if s := claims.FormatScope(got, nil); s != want {
			t.Fatalf("%q", s)
		}
	})
}

func TestR_CI_012_TheCrossProjectClassification(t *testing.T) {
	cases := []struct {
		name   string
		claims claims.Claims
		cross  bool
	}{
		{"the synthetic token: project_id 900, job_project_id 42", claims.Claims{"project_id": "900", "job_project_id": "42"}, true},
		{"the same project in all three", claims.Claims{"project_id": "42", "job_project_id": "42", "source_project_id": "42"}, false},
		{"the source project differs", claims.Claims{"project_id": "42", "job_project_id": "42", "source_project_id": "900"}, true},
		{"only job and source differ", claims.Claims{"job_project_id": "42", "source_project_id": "900"}, true},
		{"one project claim alone", claims.Claims{"project_id": "42"}, false},
		{"older GitLab without job_project_id", claims.Claims{"project_id": "900"}, false},
		{"a string and a number of the same ID", claims.Claims{"project_id": "42", "job_project_id": json.Number("42")}, false},
		{"a null job_project_id is absent", claims.Claims{"project_id": "42", "job_project_id": nil}, false},
		{"an array project claim fails closed", claims.Claims{"project_id": "42", "job_project_id": []any{"42"}}, true},
		{"an object project claim fails closed", claims.Claims{"project_id": map[string]any{"id": "42"}}, true},
	}
	for _, c := range cases {
		t.Run("R-CI-012 "+c.name, func(t *testing.T) {
			if got := c.claims.CrossProject(); got != c.cross {
				t.Fatalf("cross-project %v, want %v", got, c.cross)
			}
		})
	}
	t.Run("R-CI-012 a read_only opt-in caps the issued scope to read items", func(t *testing.T) {
		r := claims.Rendered{Inline: []claims.InlineGrant{inline([]string{"web/npm-internal"}, claims.Read, claims.Deploy), inline([]string{"web/npm-x"}, claims.Deploy)}}
		got := claims.FormatScope(r, []claims.Cap{claims.CapRead})
		if got != "read:web/npm-internal" {
			t.Fatalf("%q", got)
		}
		capped := claims.ApplyCaps(r, []claims.Cap{claims.CapRead})
		if len(capped.Inline) != 1 || len(capped.Inline[0].Actions) != 1 || r.Inline[0].Actions[1] != claims.Deploy {
			t.Fatalf("ApplyCaps: %+v (the input must stay unchanged: %+v)", capped, r)
		}
	})
}

func frozenOf(t *testing.T, ms ...claims.FrozenMapping) claims.Frozen {
	t.Helper()
	f := claims.Frozen{Version: claims.FrozenVersion, Issuer: "gitlab-com", IssuerID: "i-1", ProjectID: "4242", ProjectPath: "acme/billing/invoice-api"}
	for _, m := range ms {
		f.Add(m)
	}
	return f
}

func TestR_CI_012_ACapBoundsOnlyItsOwnMapping(t *testing.T) {
	sys := claims.Freeze("ci:gitlab-com/acme-read", "m-1", "system", nil,
		claims.Rendered{Groups: []string{"readers"}, Inline: []claims.InlineGrant{inline([]string{"web/npm-all"}, claims.Read)}}, nil)
	proj := claims.Freeze("ci:gitlab-com/web-publish", "m-2", "project:web", &claims.FrozenProject{Key: "web", ID: "p-7"},
		claims.Rendered{Inline: []claims.InlineGrant{inline([]string{"npm-local"}, claims.Read, claims.Deploy)}}, []claims.Cap{claims.CapRead})
	f := frozenOf(t, sys, proj)
	if err := f.Check(); err != nil {
		t.Fatal(err)
	}
	if got := f.InlineScope(); got != "read:web/npm-all read:web/npm-local" {
		t.Fatalf("%q", got)
	}
	if f.Contributions[1].HasCap(claims.CapRead) == false || f.Contributions[0].HasCap(claims.CapRead) {
		t.Fatal("the cap is per mapping")
	}
	if len(f.Contributions[1].Inline[0].Actions) != 1 {
		t.Fatalf("a frozen grant holds an action its own cap removes: %+v", f.Contributions[1].Inline)
	}
	t.Run("R-CI-012 a deploy item is not granted by a capped mapping", func(t *testing.T) {
		raw := claims.Freeze("ci:gitlab-com/web-publish", "m-2", "project:web", &claims.FrozenProject{Key: "web", ID: "p-7"},
			claims.Rendered{Inline: []claims.InlineGrant{inline([]string{"npm-local"}, claims.Read, claims.Deploy)}}, nil)
		raw.Caps = []claims.Cap{claims.CapRead} // a bundle built without Freeze
		items, _ := claims.ParseScope("deploy:web/npm-local")
		_, err := claims.NarrowFrozen(frozenOf(t, sys, raw), items, nil)
		if r, _ := reasonOf(t, err); r != reason.ScopeNotGranted {
			t.Fatalf("%s", r)
		}
	})
	t.Run("narrowing a bundle narrows every mapping", func(t *testing.T) { // R-CI-007
		items, _ := claims.ParseScope("read:web/npm-local")
		got, err := claims.NarrowFrozen(f, items, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got.InlineScope() != "read:web/npm-local" || len(f.Contributions[0].Inline) != 1 {
			t.Fatalf("%q (the input must stay unchanged)", got.InlineScope())
		}
	})
}

func TestTheFrozenBundleOfATokenIsVersionedAndChecked(t *testing.T) {
	sys := claims.Freeze("ci:gitlab-com/acme-read", "m-1", "system", nil, claims.Rendered{Groups: []string{"readers", "ci"}}, nil)
	proj := claims.Freeze("ci:gitlab-com/web-read", "m-2", "project:web", &claims.FrozenProject{Key: "web", ID: "p-7"}, claims.Rendered{Groups: []string{"ci"}}, nil)
	f := frozenOf(t, sys, proj)
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	back, err := claims.DecodeFrozen(raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(back.Mappings, ",") != "ci:gitlab-com/acme-read,ci:gitlab-com/web-read" || strings.Join(back.Groups(), ",") != "readers,ci" || back.OwnerRef() != "ci:gitlab-com:project:4242" {
		t.Fatalf("%+v", back)
	}
	if back.Contributions[1].Project.ID != "p-7" || back.Contributions[0].Project != nil {
		t.Fatalf("%+v", back.Contributions)
	}
	broken := []struct {
		name string
		mut  func(*claims.Frozen)
	}{
		{"another version", func(f *claims.Frozen) { f.Version = 3 }},
		{"no version", func(f *claims.Frozen) { f.Version = 0 }},
		{"no issuer", func(f *claims.Frozen) { f.IssuerID = "" }},
		{"no contribution", func(f *claims.Frozen) { f.Contributions, f.Mappings, f.MappingIDs = nil, nil, nil }},
		{"mappings out of step", func(f *claims.Frozen) { f.Mappings = f.Mappings[:1] }},
		{"a mapping ref out of step", func(f *claims.Frozen) { f.Mappings[1] = "ci:gitlab-com/other" }},
		{"a project tier without its project", func(f *claims.Frozen) { f.Contributions[1].Project = nil }},
		{"a project tier with another project's key", func(f *claims.Frozen) { f.Contributions[1].Project = &claims.FrozenProject{Key: "shop", ID: "p-8"} }},
		{"the system tier with a project", func(f *claims.Frozen) { f.Contributions[0].Project = &claims.FrozenProject{Key: "web", ID: "p-7"} }},
		{"an unknown tier", func(f *claims.Frozen) { f.Contributions[0].Tier = "delegation:x" }},
	}
	for _, b := range broken {
		t.Run("a bundle with "+b.name+" is refused", func(t *testing.T) {
			c, _ := claims.DecodeFrozen(raw)
			b.mut(c)
			again, _ := json.Marshal(c)
			if _, err := claims.DecodeFrozen(again); err == nil {
				t.Fatal("decoded")
			}
		})
	}
	if _, err := claims.DecodeFrozen([]byte(`{"version":1,"contributions":"x"}`)); err == nil {
		t.Fatal("a bundle of the wrong shape decoded")
	}
}

func TestR_CI_013_TheExampleGrantRenders(t *testing.T) {
	spec := claims.Spec{
		Policies: []string{"ci-read-internal"},
		Groups:   []string{"ci-readers"},
		Inline: []claims.InlineGrant{
			{Repositories: []string{"web/npm-internal"}, Packages: &claims.PatternSet{Include: []string{"@{{ namespace_path | slug }}/*"}, Exclude: []string{}}, Actions: []claims.Action{claims.Read, claims.Deploy}, Ownership: claims.OwnershipBindProject},
			{Repositories: []string{"web/nuget-internal"}, Packages: &claims.PatternSet{Include: []string{
				`{{ project_path | replace "/" "." | replace "-" "" }}`, `{{ project_path | replace "/" "." | replace "-" "" }}.*`,
			}}, Actions: []claims.Action{claims.Read, claims.Deploy}},
			{Repositories: []string{"generic-local"}, Paths: &claims.PatternSet{Include: []string{"builds/{{ project_path | segment -1 }}/**"}}, Actions: []claims.Action{claims.Read}},
		},
	}
	r, err := spec.Render(tokenClaims())
	if err != nil {
		t.Fatal(err)
	}
	if r.Inline[0].Packages.Include[0] != "@acme-billing/*" || r.Inline[1].Packages.Include[1] != "acme.billing.invoiceapi.*" || r.Inline[2].Paths.Include[0] != "builds/invoice-api/**" {
		t.Fatalf("%+v", r.Inline)
	}
	if r.Inline[1].Ownership != claims.OwnershipBindProject || r.Inline[2].Ownership != claims.OwnershipNone || r.Policies[0] != "ci-read-internal" || r.Groups[0] != "ci-readers" {
		t.Fatalf("R-CI-013 the rendered grants carry the resolved ownership: %+v", r)
	}
	if !nameMatches(t, r.Inline[0].Packages.Include[0], "@acme-billing/invoice-client") || nameMatches(t, r.Inline[0].Packages.Include[0], "@acme-shipping/x") {
		t.Fatal("R-CI-013 the rendered pattern admits the wrong names")
	}
	t.Run("R-CI-013 a template on an absent claim fails the whole render at its pointer", func(t *testing.T) {
		c := tokenClaims()
		delete(c, "namespace_path")
		_, err := spec.Render(c)
		if r, ptr := reasonOf(t, err); r != reason.GrantRenderFailed || ptr != "/inline/0/packages/include/0" {
			t.Fatalf("%s at %q", r, ptr)
		}
	})
}

func TestTheClaimSnapshotIsAllowlisted(t *testing.T) {
	c := tokenClaims()
	c["user_email"] = "jdoe@acme.example"
	c["user_identities"] = []any{"x"}
	c["pad"] = strings.Repeat("a", 4000)
	s := c.Snapshot()
	for _, dropped := range []string{"user_email", "user_identities", "pad"} {
		if _, ok := s[dropped]; ok {
			t.Fatalf("%s is in the snapshot", dropped)
		}
	}
	for _, kept := range []string{"sub", "project_id", "ref_protected", "runner_id", "exp", "aud"} {
		if s[kept] != c[kept] {
			t.Fatalf("%s: %v", kept, s[kept])
		}
	}
	if v, ok := c.First("project_id"); !ok || v != "4242" {
		t.Fatal("First")
	}
	if _, ok := (claims.Claims{"aud": []any{}}).First("aud"); ok {
		t.Fatal("First of an empty array")
	}
}
