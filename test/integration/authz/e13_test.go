//go:build integration

package authz

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
)

type itCase struct {
	name    string
	sub     authz.Subject
	c       authz.Coordinate
	want    authz.Outcome
	reason  string
	missing authz.Action
}

// TestE1_3_AuthorizationAgainstPostgreSQL is the subset of E1.3 that runs against
// PostgreSQL: the policies are written through the service (so the 422 rules, the
// expansion of presets and the change log are part of the path), and every
// decision is made on an index built from the database, named by
// Trove-Authz-Min-Epoch.
func TestE1_3_AuthorizationAgainstPostgreSQL(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{})
	alice, bob, carol := e.user("alice"), e.user("bob"), e.user("carol")
	devs, web := e.group("developers", false), e.group("team-web", false)
	bot := e.serviceAccount("ci-bot")
	ci := e.ciMapping("gitlab-internal/acme-publish")
	for _, r := range []struct{ key, class, format, cfg string }{
		{"generic-local", "local", "generic", ""},
		{"files-internal", "local", "generic", ""},
		{"npm-internal", "local", "npm", ""},
		{"nuget-internal", "local", "nuget", ""},
		{"npmjs", "remote", "npm", `{"remote":{"cacheFillAction":"read"}}`},
		{"npm-all", "virtual", "npm", `{"virtual":{"members":["npm-internal","npmjs"],"deployTarget":"npm-internal"}}`},
		{"strict", "local", "generic", `{"requireAuthentication":true}`},
	} {
		e.repo(r.key, r.class, r.format, r.cfg)
	}
	aliceSub, bobSub, carolSub := sub(alice, devs, web), sub(bob, devs), sub(carol)
	botSub := sub(bot)
	ciSub := authz.Subject{Kind: ci.Kind, ID: ci.ID, Name: "ci", Mappings: []string{"ci:" + ci.Name}}
	anon := authz.Subject{Anonymous: true}

	var epoch int64
	create := func(in authz.PolicyInput) {
		t.Helper()
		_, ep, err := a.svc.Create(t.Context(), sub(alice), in)
		if err != nil {
			t.Fatalf("create %s: %v", in.Name, err)
		}
		epoch = ep
	}
	run := func(cases []itCase) {
		t.Helper()
		// An anonymous request ignores Trove-Authz-Min-Epoch (R-AUTHZ-032), so the
		// replica has to have applied the writes before such a case runs.
		waitFor(t, "the replica applying the last write", 5*time.Second, func() bool { return a.engine.Epoch() >= epoch })
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				s := tc.sub
				if !s.Anonymous {
					s.MinEpoch = epoch
				}
				d := a.engine.Decide(t.Context(), s, tc.c)
				if d.Outcome != tc.want || (tc.reason != "" && d.Reason != tc.reason) || (tc.missing != "" && d.Missing != tc.missing) {
					t.Fatalf("decision = outcome %d reason %q missing %q, want outcome %d reason %q missing %q", d.Outcome, d.Reason, d.Missing, tc.want, tc.reason, tc.missing)
				}
			})
		}
	}

	t.Run("R-AUTHZ-001 R-AUTHZ-016 deny by default on a fresh installation", func(t *testing.T) {
		var cs []itCase
		for _, repo := range []string{"generic-local", "files-internal", "npm-internal", "ghost"} {
			cs = append(cs,
				itCase{"anonymous " + repo + " read", anon, read(repo, "", "a"), authz.DenyUnauthenticated, authz.ReasonUnauthenticated, ""},
				itCase{"anonymous " + repo + " write", anon, write(repo, "", "a", authz.Deploy), authz.DenyUnauthenticated, authz.ReasonUnauthenticated, ""},
				itCase{"user " + repo + " read", aliceSub, read(repo, "", "a"), authz.DenyHidden, authz.ReasonNoGrant, authz.Read},
				itCase{"user " + repo + " write", aliceSub, write(repo, "", "a", authz.Deploy), authz.DenyVisible, authz.ReasonNoGrant, authz.Deploy},
				itCase{"service account " + repo + " read", botSub, read(repo, "", "a"), authz.DenyHidden, authz.ReasonNoGrant, authz.Read},
				itCase{"CI token " + repo + " read", ciSub, read(repo, "", "a"), authz.DenyHidden, authz.ReasonNoGrant, authz.Read},
			)
		}
		run(cs)
		e.anonymous.Store(true)
		run([]itCase{{"anonymous access on, no grant: still 401", anon, read("generic-local", "", "a"), authz.DenyUnauthenticated, authz.ReasonNoGrant, ""}})
		e.anonymous.Store(false)
	})

	t.Run("R-AUTHZ-021 a policy is written with expanded presets", func(t *testing.T) {
		p, ep, err := a.svc.Create(t.Context(), sub(alice), authz.PolicyInput{
			Name: "files-writers", Resources: authz.Resources{Repositories: []string{"files-internal"}},
			Entries: []authz.Entry{{Principal: "group:team-web", Actions: []string{"write"}}, {Principal: "sa:ci-bot", Actions: []string{"read", "deploy"}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		epoch = ep
		if len(p.Entries) != 2 || p.Entries[0].Principal != "group:team-web" || len(p.Entries[0].Actions) != 3 ||
			p.Entries[0].Actions[0] != "annotate" || p.Entries[0].Actions[1] != "deploy" || p.Entries[0].Actions[2] != "read" {
			t.Fatalf("entries = %+v", p.Entries)
		}
		if p.Version != 1 || p.Inert || p.CreatedBy != "user:alice" {
			t.Fatalf("policy = %+v", p)
		}
	})

	t.Run("R-AUTHZ-006 deploy without overwrite on files-internal", func(t *testing.T) {
		run([]itCase{
			{"first upload", botSub, write("files-internal", "", "releases/app.zip", authz.Deploy), authz.Allow, authz.ReasonAllowGrant, ""},
			{"same bytes", botSub, write("files-internal", "", "releases/app.zip", authz.Deploy), authz.Allow, "", ""},
			{"different bytes", botSub, write("files-internal", "", "releases/app.zip", authz.Deploy, authz.Overwrite), authz.DenyVisible, authz.ReasonNoGrant, authz.Overwrite},
			{"read", botSub, read("files-internal", "", "releases/app.zip"), authz.Allow, "", ""},
			{"delete", botSub, write("files-internal", "", "releases/app.zip", authz.Delete), authz.DenyVisible, authz.ReasonNoGrant, authz.Delete},
			{"team-web has deploy", aliceSub, write("files-internal", "", "x", authz.Deploy), authz.Allow, "", ""},
			{"others on files-internal", bobSub, write("files-internal", "", "x", authz.Deploy), authz.DenyVisible, authz.ReasonNoGrant, authz.Deploy},
		})
	})

	t.Run("R-AUTHZ-001 R-AUTHZ-005 policy union, include and exclude", func(t *testing.T) {
		create(authz.PolicyInput{
			Name: "npm-consumers", Resources: authz.Resources{Repositories: []string{"@any-virtual:npm", "@any-local:npm", "@any-remote:npm"}},
			Entries: []authz.Entry{{Principal: "group:developers", Actions: []string{"read"}}},
		})
		create(authz.PolicyInput{
			Name: "team-web-publish", Resources: authz.Resources{
				Repositories: []string{"npm-internal"},
				Packages:     authz.PatternLists{Include: []string{"@web/*"}, Exclude: []string{"@web/internal-*"}},
			},
			Entries: []authz.Entry{{Principal: "group:team-web", Actions: []string{"write"}}},
		})
		create(authz.PolicyInput{
			Name: "nuget-acme", Resources: authz.Resources{Repositories: []string{"nuget-internal"}, Packages: authz.PatternLists{Include: []string{"Acme.*"}}},
			Entries: []authz.Entry{{Principal: "group:developers", Actions: []string{"read"}}},
		})
		create(authz.PolicyInput{
			Name: "releases", Resources: authz.Resources{Repositories: []string{"generic-local"}, Paths: authz.PatternLists{Include: []string{"releases/**"}, Exclude: []string{"releases/secret/**"}}},
			Entries: []authz.Entry{{Principal: "group:developers", Actions: []string{"read"}}},
		})
		run([]itCase{
			{"devs read npm-internal", bobSub, read("npm-internal", "left-pad", ""), authz.Allow, "", ""},
			{"devs read through the virtual repository", bobSub, read("npm-all", "left-pad", ""), authz.Allow, "", ""},
			{"devs cannot deploy", bobSub, write("npm-internal", "@web/x", "", authz.Deploy), authz.DenyVisible, authz.ReasonNoGrant, authz.Deploy},
			{"team-web publishes inside its scope", aliceSub, write("npm-internal", "@web/button", "", authz.Deploy), authz.Allow, "", ""},
			{"team-web cannot publish another scope", aliceSub, write("npm-internal", "@pay/core", "", authz.Deploy), authz.DenyVisible, authz.ReasonNoGrant, authz.Deploy},
			{"the exclude removes the name from the set", aliceSub, write("npm-internal", "@web/internal-keys", "", authz.Deploy), authz.DenyVisible, authz.ReasonNoGrant, authz.Deploy},
			{"publishing through the virtual repository lands on the target", aliceSub, write("npm-all", "@web/button", "", authz.Deploy), authz.Allow, "", ""},
			{"a user in no group is refused at the gate", carolSub, read("npm-all", "left-pad", ""), authz.DenyHidden, authz.ReasonVirtualGate, authz.Read},
			{"NuGet names compare case-insensitively", bobSub, read("nuget-internal", "acme.core", ""), authz.Allow, "", ""},
			{"NuGet prefix needs the dot", bobSub, read("nuget-internal", "AcmeCore", ""), authz.DenyHidden, authz.ReasonNoGrant, authz.Read},
			{"a path include matches", bobSub, read("generic-local", "", "releases/1.0/app.zip"), authz.Allow, "", ""},
			{"a path exclude beats the include", bobSub, read("generic-local", "", "releases/secret/key"), authz.DenyHidden, authz.ReasonNoGrant, authz.Read},
			{"another directory is outside the include", bobSub, read("generic-local", "", "snapshots/a"), authz.DenyHidden, authz.ReasonNoGrant, authz.Read},
		})
	})

	t.Run("R-AUTHZ-013 a token scope is a ceiling", func(t *testing.T) {
		readOnly := token.Scope{Repositories: []token.ScopeGrant{{Repositories: []string{"npm-all", "@any-local:npm", "@any-remote:npm"}, Actions: []string{"read"}}}}
		narrow := token.Scope{Repositories: []token.ScopeGrant{{Repositories: []string{"npm-internal"}, Packages: token.Patterns{Include: []string{"@web/*"}}, Actions: []string{"read"}}}}
		scoped := func(s authz.Subject, sc token.Scope) authz.Subject { s.Scope = &sc; return s }
		run([]itCase{
			{"inherit keeps the rights", scoped(aliceSub, token.Scope{Inherit: true}), write("npm-internal", "@web/x", "", authz.Deploy), authz.Allow, "", ""},
			{"a read-only scope still installs", scoped(aliceSub, readOnly), read("npm-all", "left-pad", ""), authz.Allow, "", ""},
			{"a read-only scope cannot publish", scoped(aliceSub, readOnly), write("npm-internal", "@web/x", "", authz.Deploy), authz.DenyVisible, authz.ReasonScope, authz.Deploy},
			{"a name scope reads inside its names", scoped(aliceSub, narrow), read("npm-internal", "@web/button", ""), authz.Allow, "", ""},
			{"a name scope hides what the grants would allow", scoped(aliceSub, narrow), read("npm-internal", "@pay/core", ""), authz.DenyHidden, authz.ReasonScope, authz.Read},
			{"a scope never grants", scoped(bobSub, token.Scope{Repositories: []token.ScopeGrant{{Repositories: []string{"@any"}, Actions: []string{"deploy"}}}}), write("npm-internal", "@web/x", "", authz.Deploy), authz.DenyVisible, authz.ReasonNoGrant, authz.Deploy},
		})
	})

	t.Run("R-AUTHZ-016 a hidden answer is the answer for a missing resource", func(t *testing.T) {
		d1 := a.engine.Decide(t.Context(), carolSub, read("npm-internal", "@web/button", ""))
		d2 := a.engine.Decide(t.Context(), carolSub, read("ghost", "@web/nope", ""))
		if d1.Outcome != authz.DenyHidden || d2.Outcome != authz.DenyHidden {
			t.Fatalf("%v %v", d1, d2)
		}
		e1, _ := errs.ProblemOf(d1.Err(), "r", "/x")
		e2, _ := errs.ProblemOf(authz.NotFound(), "r", "/x")
		b1, _ := json.Marshal(e1)
		b2, _ := json.Marshal(e2)
		if string(b1) != string(b2) {
			t.Fatalf("hidden %s differs from missing %s", b1, b2)
		}
		run([]itCase{
			{"a refused write names the action whether or not the package exists", carolSub, write("npm-internal", "@web/nope", "", authz.Deploy), authz.DenyVisible, authz.ReasonNoGrant, authz.Deploy},
			{"and for the existing one", carolSub, write("npm-internal", "@web/button", "", authz.Deploy), authz.DenyVisible, authz.ReasonNoGrant, authz.Deploy},
		})
	})

	t.Run("R-AUTHZ-030 the authenticated principal", func(t *testing.T) {
		_, _, err := a.svc.Create(t.Context(), sub(alice), authz.PolicyInput{
			Name: "bad", Resources: authz.Resources{Repositories: []string{"generic-local"}},
			Entries: []authz.Entry{{Principal: "authenticated", Actions: []string{"read", "deploy"}}},
		})
		problem(t, err, 422, errs.CodeAuthenticatedWriteAction)
		create(authz.PolicyInput{
			Name: "everyone-reads-generic", Resources: authz.Resources{Repositories: []string{"@any-local:generic"}},
			Entries: []authz.Entry{{Principal: "authenticated", Actions: []string{"read"}}},
		})
		run([]itCase{
			{"a user in no group downloads", carolSub, read("generic-local", "", "a/b.bin"), authz.Allow, authz.ReasonAllowGrant, ""},
			{"a user in no group cannot upload", carolSub, write("generic-local", "", "a", authz.Deploy), authz.DenyVisible, authz.ReasonNoGrant, authz.Deploy},
			{"a service-account token without its own grant gets 404", botSub, read("generic-local", "", "a/b.bin"), authz.DenyHidden, authz.ReasonNoGrant, authz.Read},
			{"a CI token gets 404", ciSub, read("generic-local", "", "a/b.bin"), authz.DenyHidden, authz.ReasonNoGrant, authz.Read},
			{"anonymous gets 401", anon, read("generic-local", "", "a/b.bin"), authz.DenyUnauthenticated, authz.ReasonUnauthenticated, ""},
		})
	})

	t.Run("R-AUTHZ-003 anonymous access", func(t *testing.T) {
		create(authz.PolicyInput{
			Name: "public", Resources: authz.Resources{Repositories: []string{"npm-internal", "strict"}},
			Entries: []authz.Entry{{Principal: "anonymous", Actions: []string{"read"}}},
		})
		_, _, err := a.svc.Create(t.Context(), sub(alice), authz.PolicyInput{
			Name: "public-bad", Resources: authz.Resources{Repositories: []string{"@any-remote"}},
			Entries: []authz.Entry{{Principal: "anonymous", Actions: []string{"read"}}},
		})
		problem(t, err, 422, errs.CodeAnonymousDynamicSelector)
		_, _, err = a.svc.Create(t.Context(), sub(alice), authz.PolicyInput{
			Name: "public-write", Resources: authz.Resources{Repositories: []string{"npm-internal"}},
			Entries: []authz.Entry{{Principal: "anonymous", Actions: []string{"read", "deploy"}}},
		})
		problem(t, err, 422, errs.CodeAnonymousWriteAction)
		run([]itCase{{"anonymous access off: 401 although the policy grants", anon, read("npm-internal", "x", ""), authz.DenyUnauthenticated, authz.ReasonUnauthenticated, ""}})
		e.anonymous.Store(true)
		defer e.anonymous.Store(false)
		run([]itCase{
			{"anonymous access on: the grant works", anon, read("npm-internal", "x", ""), authz.Allow, "", ""},
			{"anonymous access on: writes are challenged", anon, write("npm-internal", "x", "", authz.Deploy), authz.DenyUnauthenticated, authz.ReasonNoGrant, ""},
			{"requireAuthentication challenges anonymous whatever the policies grant", anon, read("strict", "", "a"), authz.DenyUnauthenticated, authz.ReasonUnauthenticated, ""},
		})
	})

	t.Run("R-AUTHZ-010 system roles", func(t *testing.T) {
		admins := e.group("platform-admins", false)
		if created, ep, err := a.svc.BindRole(t.Context(), sub(alice), authz.RoleAdmin, "group:platform-admins"); err != nil || !created {
			t.Fatal(created, err)
		} else {
			epoch = ep
		}
		adminSub := sub(e.user("root-admin"), admins)
		for _, tc := range []struct {
			perm string
			sub  authz.Subject
			ok   bool
		}{
			{"policies:write", adminSub, true}, {"settings:read", adminSub, true}, {"policies:write", aliceSub, false}, {"settings:read", carolSub, false},
		} {
			s := tc.sub
			s.MinEpoch = epoch
			if err := a.engine.Require(t.Context(), s, tc.perm); (err == nil) != tc.ok {
				t.Errorf("Require(%s) for %s = %v", tc.perm, s.Name, err)
			}
		}
		adminSub.MinEpoch = epoch
		if d := a.engine.Decide(t.Context(), adminSub, read("npm-internal", "anything", "")); d.Outcome != authz.Allow || d.Reason != authz.ReasonAllowRole {
			t.Errorf("content:bypass: %v", d)
		}
		tok := adminSub
		tok.Scope = &token.Scope{Inherit: true}
		if err := a.engine.Require(t.Context(), tok, "policies:write"); err == nil {
			t.Error("the default scope of an administrator's token exercises no role")
		}
		tok.Scope = &token.Scope{Inherit: true, Admin: true}
		if err := a.engine.Require(t.Context(), tok, "policies:write"); err != nil {
			t.Errorf("admin: true: %v", err)
		}
	})
}
