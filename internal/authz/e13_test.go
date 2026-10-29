package authz

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/errs"
)

// E1.3: the authorization cases at unit level, against the engine on a compiled
// index. The integration test of test/integration/authz repeats a subset against
// PostgreSQL.

type authzCase struct {
	name    string
	sub     Subject
	c       Coordinate
	want    Outcome
	reason  string
	missing Action
}

func read(repo, pkg, path string) Coordinate {
	return Coordinate{Repository: repo, Package: pkg, Path: path, Kind: KindArtifact, Actions: []Action{Read}}
}

func metadata(repo string) Coordinate {
	return Coordinate{Repository: repo, Kind: KindMetadata, Actions: []Action{Read}}
}

func write(repo, pkg, path string, actions ...Action) Coordinate {
	return Coordinate{Repository: repo, Package: pkg, Path: path, Kind: KindWrite, Actions: actions}
}

var (
	anonSub   = Subject{Anonymous: true, Kind: principal.KindAnonymous}
	aliceSub  = userSub(idAlice, "alice", idDevs, idWeb)
	bobSub    = userSub(idBob, "bob", idDevs)
	carolSub  = userSub(idCarol, "carol")
	botSub    = Subject{Kind: principal.KindServiceAccount, ID: idBot, Name: "ci-bot"}
	ciSub     = Subject{Kind: principal.KindCI, Name: "ci", Mappings: []string{"ci:gitlab-internal/acme"}}
	adminSub  = userSub(idAudit, "ops-admin", idOps)
	breakSub  = Subject{Kind: principal.KindBreakGlass, ID: "user-root", Name: "root"}
	tokenScop = func(s Subject, sc token.Scope) Subject { s.Scope = &sc; return s }
)

func runAuthzCases(t *testing.T, e *Engine, cases []authzCase, count *int) {
	t.Helper()
	for _, tc := range cases {
		*count++
		t.Run(tc.name, func(t *testing.T) {
			d := e.Decide(t.Context(), tc.sub, tc.c)
			if d.Outcome != tc.want {
				t.Fatalf("outcome = %d (%s, missing %q), want %d", d.Outcome, d.Reason, d.Missing, tc.want)
			}
			if tc.reason != "" && d.Reason != tc.reason {
				t.Fatalf("reason = %q, want %q", d.Reason, tc.reason)
			}
			if tc.missing != "" && d.Missing != tc.missing {
				t.Fatalf("missing = %q, want %q", d.Missing, tc.missing)
			}
		})
	}
}

func TestE1_3_AuthorizationCases(t *testing.T) {
	n := 0

	t.Run("R-AUTHZ-001 R-AUTHZ-016 deny by default on a fresh installation", func(t *testing.T) {
		f := newFixture().repo("generic-local", "local", "generic", "").repo("files-internal", "local", "generic", "").repo("npm-internal", "local", "npm", "")
		off := newTestEngine(t, f, nil)
		on := newTestEngine(t, f, func(o *Options) { o.AnonymousAccess = func() bool { return true } })
		subjects := []struct {
			name string
			sub  Subject
			anon bool
		}{{"user", aliceSub, false}, {"service account", botSub, false}, {"CI token", ciSub, false}, {"administrator without a binding", adminSub, false}}
		repos := []string{"generic-local", "files-internal", "npm-internal", "ghost"}
		ops := []struct {
			name string
			c    func(repo string) Coordinate
			read bool
			act  Action
		}{
			{"artifact read", func(r string) Coordinate { return read(r, "", "a/b.bin") }, true, Read},
			{"metadata read", metadata, true, Read},
			{"deploy", func(r string) Coordinate { return write(r, "", "a/b.bin", Deploy) }, false, Deploy},
			{"delete", func(r string) Coordinate { return write(r, "", "a/b.bin", Delete) }, false, Delete},
		}
		for _, repo := range repos {
			for _, op := range ops {
				var cs []authzCase
				cs = append(cs, authzCase{name: fmt.Sprintf("anonymous off %s %s", op.name, repo), sub: anonSub, c: op.c(repo), want: DenyUnauthenticated, reason: ReasonUnauthenticated})
				for _, s := range subjects {
					want, missing := DenyVisible, op.act
					if op.read {
						want = DenyHidden
					}
					cs = append(cs, authzCase{name: fmt.Sprintf("%s %s %s", s.name, op.name, repo), sub: s.sub, c: op.c(repo), want: want, reason: ReasonNoGrant, missing: missing})
				}
				runAuthzCases(t, off, cs, &n)
				runAuthzCases(t, on, []authzCase{{name: fmt.Sprintf("anonymous on without a grant %s %s", op.name, repo), sub: anonSub, c: op.c(repo), want: DenyUnauthenticated, reason: ReasonNoGrant}}, &n)
			}
		}
	})

	t.Run("R-AUTHZ-001 R-AUTHZ-015 policies union and an exclude removes nothing another policy grants", func(t *testing.T) {
		f := newFixture().repo("npm-internal", "local", "npm", "")
		f.policy(policyDef{name: "readers", selectors: []string{"npm-internal"}, entries: map[string][]string{idDevs: {"read"}}})
		f.policy(policyDef{name: "web", selectors: []string{"npm-internal"}, entries: map[string][]string{idWeb: {"annotate", "deploy"}}})
		f.policy(policyDef{name: "web-read-narrow", selectors: []string{"npm-internal"}, pkgInc: []string{"@web/*"}, pkgExc: []string{"@web/internal-*"}, entries: map[string][]string{idWeb: {"delete"}}})
		f.policy(policyDef{name: "web-read-wide", selectors: []string{"npm-internal"}, entries: map[string][]string{idWeb: {"overwrite"}}})
		e := newTestEngine(t, f, nil)
		runAuthzCases(t, e, []authzCase{
			{"alice reads through the devs policy", aliceSub, read("npm-internal", "left-pad", ""), Allow, ReasonAllowGrant, ""},
			{"alice deploys through the web policy", aliceSub, write("npm-internal", "@web/x", "", Deploy), Allow, "", ""},
			{"alice annotates through the web policy", aliceSub, write("npm-internal", "@web/x", "", Annotate), Allow, "", ""},
			{"alice needs both read and deploy for a combined operation", aliceSub, write("npm-internal", "@web/x", "", Read, Deploy), Allow, "", ""},
			{"alice has no manage", aliceSub, write("npm-internal", "@web/x", "", Manage), DenyVisible, ReasonNoGrant, Manage},
			{"bob reads", bobSub, read("npm-internal", "left-pad", ""), Allow, "", ""},
			{"bob cannot deploy", bobSub, write("npm-internal", "@web/x", "", Deploy), DenyVisible, ReasonNoGrant, Deploy},
			{"bob cannot annotate", bobSub, write("npm-internal", "@web/x", "", Annotate), DenyVisible, ReasonNoGrant, Annotate},
			{"carol cannot read", carolSub, read("npm-internal", "left-pad", ""), DenyHidden, ReasonNoGrant, Read},
			{"a combined operation reports the first refused action", bobSub, write("npm-internal", "@web/x", "", Read, Deploy, Delete), DenyVisible, ReasonNoGrant, Deploy},
			{"an exclude of one policy leaves the other policy's right", aliceSub, write("npm-internal", "@web/internal-keys", "", Overwrite), Allow, "", ""},
			{"the narrow policy grants delete inside its include", aliceSub, write("npm-internal", "@web/button", "", Delete), Allow, "", ""},
			{"the exclude removes delete inside the same set", aliceSub, write("npm-internal", "@web/internal-keys", "", Delete), DenyVisible, ReasonNoGrant, Delete},
			{"the include does not cover another scope", aliceSub, write("npm-internal", "@pay/core", "", Delete), DenyVisible, ReasonNoGrant, Delete},
			{"deploy does not imply read", userSub("u-dep", "dep", idWeb), read("npm-internal", "x", ""), DenyHidden, ReasonNoGrant, Read},
			{"manage does not imply deploy", func() Subject { return userSub("u-mgr", "mgr") }(), write("npm-internal", "x", "", Deploy), DenyVisible, ReasonNoGrant, Deploy},
		}, &n)
	})

	t.Run("R-AUTHZ-005 include and exclude patterns", func(t *testing.T) {
		f := newFixture().repo("npm-internal", "local", "npm", "").repo("nuget-internal", "local", "nuget", "").repo("files-internal", "local", "generic", "")
		f.policy(policyDef{name: "team-web-publish", selectors: []string{"npm-internal"}, pkgInc: []string{"@web/*"}, pkgExc: []string{"@web/internal-*"}, entries: map[string][]string{idWeb: {"write"}}})
		f.policy(policyDef{name: "nuget-acme", selectors: []string{"nuget-internal"}, pkgInc: []string{"Acme.*"}, entries: map[string][]string{idDevs: {"read"}}})
		f.policy(policyDef{name: "releases", selectors: []string{"files-internal"}, pathInc: []string{"releases/**"}, pathExc: []string{"releases/secret/**"}, entries: map[string][]string{idDevs: {"read"}, idBot: {"read", "deploy"}}})
		f.policy(policyDef{name: "escaped", selectors: []string{"npm-internal"}, pkgInc: []string{`@acme/ui\*`}, entries: map[string][]string{idBot: {"deploy"}}})
		e := newTestEngine(t, f, nil)
		runAuthzCases(t, e, []authzCase{
			{"scope include matches its scope", aliceSub, read("npm-internal", "@web/button", ""), Allow, "", ""},
			{"scope include does not match another scope", aliceSub, read("npm-internal", "@pay/core", ""), DenyHidden, ReasonNoGrant, Read},
			{"scope include does not match a sibling scope prefix", aliceSub, read("npm-internal", "@web-tools/x", ""), DenyHidden, ReasonNoGrant, Read},
			{"exclude beats include", aliceSub, read("npm-internal", "@web/internal-keys", ""), DenyHidden, ReasonNoGrant, Read},
			{"deploy inside the include", aliceSub, write("npm-internal", "@web/button", "", Deploy), Allow, "", ""},
			{"deploy on an excluded name", aliceSub, write("npm-internal", "@web/internal-keys", "", Deploy), DenyVisible, ReasonNoGrant, Deploy},
			{"a raw file without a package name never matches a name include", aliceSub, read("npm-internal", "", "readme.txt"), DenyHidden, ReasonNoGrant, Read},
			{"a repository-level search matches whatever the patterns say", aliceSub, Coordinate{Repository: "npm-internal", Kind: KindSearch, Actions: []Action{Read}}, Allow, "", ""},
			{"a repository-level coordinate does not widen deploy for another principal", bobSub, Coordinate{Repository: "npm-internal", Kind: KindWrite, Actions: []Action{Deploy}}, DenyVisible, ReasonNoGrant, Deploy},
			{"NuGet include matches in any spelling", bobSub, read("nuget-internal", "acme.core", ""), Allow, "", ""},
			{"NuGet include matches upper case", bobSub, read("nuget-internal", "ACME.Core.Tests", ""), Allow, "", ""},
			{"NuGet include needs the dot", bobSub, read("nuget-internal", "AcmeCore", ""), DenyHidden, ReasonNoGrant, Read},
			{"path include matches nested files", bobSub, read("files-internal", "", "releases/1.0/app.zip"), Allow, "", ""},
			{"path include does not match another directory", bobSub, read("files-internal", "", "snapshots/app.zip"), DenyHidden, ReasonNoGrant, Read},
			{"path exclude beats include", bobSub, read("files-internal", "", "releases/secret/key"), DenyHidden, ReasonNoGrant, Read},
			{"a path include never matches a coordinate without a path", bobSub, read("files-internal", "pkg", ""), DenyHidden, ReasonNoGrant, Read},
			{"path include applies to deploy as well", botSub, write("files-internal", "", "releases/app.zip", Deploy), Allow, "", ""},
			{"deploy outside the path include", botSub, write("files-internal", "", "tmp/app.zip", Deploy), DenyVisible, ReasonNoGrant, Deploy},
			{"a rendered escape is a literal", botSub, write("npm-internal", `@acme/ui*`, "", Deploy), Allow, "", ""},
			{"a rendered escape cannot widen to a sibling", botSub, write("npm-internal", "@acme/ui-kit", "", Deploy), DenyVisible, ReasonNoGrant, Deploy},
		}, &n)
	})

	t.Run("R-AUTHZ-006 deploy without overwrite on files-internal", func(t *testing.T) {
		f := newFixture().repo("files-internal", "local", "generic", "").repo("npm-internal", "local", "npm", "")
		f.policy(policyDef{name: "uploaders", selectors: []string{"files-internal"}, entries: map[string][]string{idBot: {"read", "deploy"}, idWeb: {"read", "deploy", "overwrite"}}})
		f.policy(policyDef{name: "cleaners", selectors: []string{"npm-internal"}, entries: map[string][]string{idBob: {"delete"}, idCarol: {"deploy"}}})
		e := newTestEngine(t, f, nil)
		runAuthzCases(t, e, []authzCase{
			{"the first upload to a path that holds nothing needs deploy", botSub, write("files-internal", "", "releases/app.zip", Deploy), Allow, "", ""},
			{"the same bytes need deploy only", botSub, write("files-internal", "", "releases/app.zip", Deploy), Allow, "", ""},
			{"different bytes need deploy and overwrite", botSub, write("files-internal", "", "releases/app.zip", Deploy, Overwrite), DenyVisible, ReasonNoGrant, Overwrite},
			{"read stays allowed", botSub, read("files-internal", "", "releases/app.zip"), Allow, "", ""},
			{"delete is refused", botSub, write("files-internal", "", "releases/app.zip", Delete), DenyVisible, ReasonNoGrant, Delete},
			{"with overwrite the replacement is allowed", aliceSub, write("files-internal", "", "releases/app.zip", Deploy, Overwrite), Allow, "", ""},
			{"overwrite alone does not give deploy", userSub("u-ov", "ov"), write("files-internal", "", "releases/app.zip", Deploy, Overwrite), DenyVisible, ReasonNoGrant, Deploy},
			{"delete without deploy unpublishes", bobSub, write("npm-internal", "@acme/ui", "", Delete), Allow, "", ""},
			{"delete without deploy cannot publish", bobSub, write("npm-internal", "@acme/ui", "", Deploy), DenyVisible, ReasonNoGrant, Deploy},
			{"deploy without read cannot view the package", carolSub, read("npm-internal", "@acme/new", ""), DenyHidden, ReasonNoGrant, Read},
			{"deploy without read can publish", carolSub, write("npm-internal", "@acme/new", "", Deploy), Allow, "", ""},
			{"a write that is refused names the action and not the existence", bobSub, write("files-internal", "", "does/not/exist", Deploy), DenyVisible, ReasonNoGrant, Deploy},
		}, &n)
	})

	t.Run("R-AUTHZ-013 a token scope is a ceiling and never a grant", func(t *testing.T) {
		f := newFixture().repo("npm-internal", "local", "npm", "").repo("npmjs", "remote", "npm", "").repo("npm-all", "virtual", "npm", `{"virtual":{"members":["npm-internal","npmjs"],"deployTarget":"npm-internal"}}`)
		f.policy(policyDef{name: "consumers", selectors: []string{"@any-virtual:npm", "@any-local:npm", "@any-remote:npm"}, entries: map[string][]string{idDevs: {"read"}}})
		f.policy(policyDef{name: "web", selectors: []string{"npm-internal"}, pkgInc: []string{"@web/*"}, entries: map[string][]string{idWeb: {"read", "deploy"}}})
		e := newTestEngine(t, f, nil)
		readOnly := token.Scope{Repositories: []token.ScopeGrant{{Repositories: []string{"npm-all", "@any-local:npm", "@any-remote:npm"}, Actions: []string{"read"}}}}
		narrow := token.Scope{Repositories: []token.ScopeGrant{{Repositories: []string{"npm-internal"}, Packages: token.Patterns{Include: []string{"@web/*"}}, Actions: []string{"read"}}}}
		wide := token.Scope{Repositories: []token.ScopeGrant{{Repositories: []string{"@any"}, Actions: []string{"read", "deploy", "delete", "manage"}}}}
		runAuthzCases(t, e, []authzCase{
			{"a session has no ceiling", aliceSub, write("npm-internal", "@web/x", "", Deploy), Allow, "", ""},
			{"inherit admits every right of the owner", tokenScop(aliceSub, token.Scope{Inherit: true}), write("npm-internal", "@web/x", "", Deploy), Allow, "", ""},
			{"a read-only scope still reads through the virtual repository", tokenScop(aliceSub, readOnly), read("npm-all", "left-pad", ""), Allow, "", ""},
			{"a read-only scope reads the local repository", tokenScop(aliceSub, readOnly), read("npm-internal", "@web/x", ""), Allow, "", ""},
			{"a read-only scope refuses deploy that the grant allows", tokenScop(aliceSub, readOnly), write("npm-internal", "@web/x", "", Deploy), DenyVisible, ReasonScope, Deploy},
			{"a read-only scope refuses a write through the virtual repository", tokenScop(aliceSub, readOnly), func() Coordinate { c := write("npm-all", "@web/x", "", Deploy); return c }(), DenyVisible, ReasonScope, Deploy},
			{"a name scope admits its names", tokenScop(aliceSub, narrow), read("npm-internal", "@web/button", ""), Allow, "", ""},
			{"a name scope hides what the grants would allow", tokenScop(aliceSub, narrow), read("npm-internal", "@pay/core", ""), DenyHidden, ReasonScope, Read},
			{"a name scope does not admit deploy", tokenScop(aliceSub, narrow), write("npm-internal", "@web/button", "", Deploy), DenyVisible, ReasonScope, Deploy},
			{"a scope that names more than the owner holds yields nothing for the rest", tokenScop(bobSub, wide), write("npm-internal", "@web/x", "", Deploy), DenyVisible, ReasonNoGrant, Deploy},
			{"a wide scope does not widen a read the owner has", tokenScop(bobSub, wide), read("npm-internal", "x", ""), Allow, "", ""},
			{"a scope never grants manage", tokenScop(aliceSub, wide), write("npm-internal", "@web/x", "", Manage), DenyVisible, ReasonNoGrant, Manage},
			{"a scope without a matching selector admits nothing", tokenScop(aliceSub, token.Scope{Repositories: []token.ScopeGrant{{Repositories: []string{"other-repo"}, Actions: []string{"read"}}}}), read("npm-internal", "x", ""), DenyHidden, ReasonScope, Read},
			{"an empty restricted scope admits nothing", tokenScop(aliceSub, token.Scope{}), read("npm-internal", "x", ""), DenyHidden, ReasonScope, Read},
			{"a scope with an invalid selector admits nothing", tokenScop(aliceSub, token.Scope{Repositories: []token.ScopeGrant{{Repositories: []string{"NOT A KEY"}, Actions: []string{"read"}}}}), read("npm-internal", "x", ""), DenyHidden, ReasonScope, Read},
			{"a scope with an invalid pattern admits nothing", tokenScop(aliceSub, token.Scope{Repositories: []token.ScopeGrant{{Repositories: []string{"@any"}, Packages: token.Patterns{Include: []string{`x\`}}, Actions: []string{"read"}}}}), read("npm-internal", "x", ""), DenyHidden, ReasonScope, Read},
			{"a scope with an unknown action admits nothing for it", tokenScop(aliceSub, token.Scope{Repositories: []token.ScopeGrant{{Repositories: []string{"@any"}, Actions: []string{"publish"}}}}), read("npm-internal", "x", ""), DenyHidden, ReasonScope, Read},
			{"a scope class selector with a format admits the format", tokenScop(aliceSub, token.Scope{Repositories: []token.ScopeGrant{{Repositories: []string{"@any-local:npm"}, Actions: []string{"read"}}}}), read("npm-internal", "x", ""), Allow, "", ""},
			{"a scope class selector with another format does not", tokenScop(aliceSub, token.Scope{Repositories: []token.ScopeGrant{{Repositories: []string{"@any-local:nuget"}, Actions: []string{"read"}}}}), read("npm-internal", "x", ""), DenyHidden, ReasonScope, Read},
			{"a service-account token carries only what the account holds", tokenScop(botSub, wide), read("npm-internal", "x", ""), DenyHidden, ReasonNoGrant, Read},
			{"a scope does not help an anonymous caller", anonSub, read("npm-internal", "x", ""), DenyUnauthenticated, ReasonUnauthenticated, ""},
		}, &n)
	})

	t.Run("R-AUTHZ-016 hidden existence is indistinguishable from a missing resource", func(t *testing.T) {
		f := newFixture().repo("npm-internal", "local", "npm", "").repo("open-hide", "local", "generic", `{"hideExistence":"false"}`).repo("force-hide", "local", "generic", `{"hideExistence":"true"}`)
		e := newTestEngine(t, f, nil)
		runAuthzCases(t, e, []authzCase{
			{"an existing repository without a grant is hidden", carolSub, read("npm-internal", "@web/button", ""), DenyHidden, ReasonNoGrant, Read},
			{"a missing repository is hidden the same way", carolSub, read("ghost", "@web/button", ""), DenyHidden, ReasonNoGrant, Read},
			{"hideExistence false turns the refusal of a read into 403", carolSub, read("open-hide", "", "a"), DenyVisible, ReasonNoGrant, Read},
			{"hideExistence true keeps 404", carolSub, read("force-hide", "", "a"), DenyHidden, ReasonNoGrant, Read},
			{"a refused write is 403 whether or not the target exists", carolSub, write("npm-internal", "@web/nope", "", Deploy), DenyVisible, ReasonNoGrant, Deploy},
			{"a refused write of an existing package is 403 as well", carolSub, write("npm-internal", "@web/button", "", Deploy), DenyVisible, ReasonNoGrant, Deploy},
			{"a refused write on a missing repository is 403", carolSub, write("ghost", "x", "", Deploy), DenyVisible, ReasonNoGrant, Deploy},
		}, &n)

		d1 := e.Decide(t.Context(), carolSub, read("npm-internal", "@web/button", ""))
		d2 := e.Decide(t.Context(), carolSub, read("ghost", "@web/nope", ""))
		p1, _ := errs.ProblemOf(d1.Err(), "req-1", "/api/npm/x/y")
		p2, _ := errs.ProblemOf(NotFound(), "req-1", "/api/npm/x/y")
		b1, _ := json.Marshal(p1)
		b2, _ := json.Marshal(p2)
		if string(b1) != string(b2) || d1.Status() != 404 || d2.Status() != 404 {
			t.Errorf("R-AUTHZ-016 hidden answer %s differs from the missing one %s", b1, b2)
		}
		n++

		global := newTestEngine(t, f, func(o *Options) { o.Config.HideExistence = false })
		runAuthzCases(t, global, []authzCase{
			{"authz.hide_existence false makes an inherited repository answer 403", carolSub, read("npm-internal", "x", ""), DenyVisible, ReasonNoGrant, Read},
			{"a repository that forces hiding still answers 404", carolSub, read("force-hide", "", "a"), DenyHidden, ReasonNoGrant, Read},
		}, &n)
	})

	t.Run("R-AUTHZ-030 the authenticated principal", func(t *testing.T) {
		f := newFixture().repo("npm-all", "virtual", "npm", `{"virtual":{"members":["npm-internal"]}}`).repo("npm-internal", "local", "npm", "").repo("npmjs", "remote", "npm", "").repo("generic-local", "local", "generic", "")
		f.policy(policyDef{name: "everyone-reads-npm", selectors: []string{"@any-virtual:npm", "@any-remote:npm"}, entries: map[string][]string{AuthenticatedID: {"read"}}})
		f.policy(policyDef{name: "everyone-reads-generic", selectors: []string{"generic-local"}, entries: map[string][]string{AuthenticatedID: {"read"}}})
		f.policy(policyDef{name: "bot", selectors: []string{"npm-internal"}, entries: map[string][]string{idBot: {"read"}}})
		e := newTestEngine(t, f, nil)
		scoped := token.Scope{Repositories: []token.ScopeGrant{{Repositories: []string{"npmjs"}, Actions: []string{"read"}}}}
		scopedAll := token.Scope{Repositories: []token.ScopeGrant{{Repositories: []string{"npm-all", "generic-local"}, Actions: []string{"read"}}}}
		runAuthzCases(t, e, []authzCase{
			{"a user in no group reads through authenticated", carolSub, metadata("npm-all"), Allow, "", ""},
			{"a user in no group reads a remote repository", carolSub, read("npmjs", "left-pad", ""), Allow, "", ""},
			{"a user in no group downloads from generic-local", carolSub, read("generic-local", "", "a/b.bin"), Allow, "", ""},
			{"a service-account token without its own grant gets 404 on the virtual repository", botSub, metadata("npm-all"), DenyHidden, ReasonVirtualGate, Read},
			{"a service-account token without its own grant gets 404 on generic-local", botSub, read("generic-local", "", "a/b.bin"), DenyHidden, ReasonNoGrant, Read},
			{"a service account never counts as authenticated", botSub, read("npmjs", "left-pad", ""), DenyHidden, ReasonNoGrant, Read},
			{"a CI token never counts as authenticated", ciSub, metadata("npm-all"), DenyHidden, ReasonVirtualGate, Read},
			{"anonymous is not authenticated", anonSub, metadata("npm-all"), DenyUnauthenticated, ReasonUnauthenticated, ""},
			{"authenticated cannot deploy", carolSub, write("generic-local", "", "a", Deploy), DenyVisible, ReasonNoGrant, Deploy},
			{"authenticated gives no read on an unselected repository", carolSub, read("npm-internal", "x", ""), DenyHidden, ReasonNoGrant, Read},
			{"a token whose scope does not admit the repository does not read it through authenticated", tokenScop(carolSub, scoped), metadata("npm-all"), DenyHidden, ReasonVirtualGate, Read},
			{"a token whose scope admits the repository reads through authenticated", tokenScop(carolSub, scopedAll), metadata("npm-all"), Allow, "", ""},
			{"a break-glass session is authenticated as well", breakSub, read("generic-local", "", "a"), Allow, "", ""},
			{"a service account reads with its own grant", botSub, read("npm-internal", "x", ""), Allow, "", ""},
		}, &n)
	})

	t.Run("R-AUTHZ-003 anonymous access", func(t *testing.T) {
		f := newFixture().repo("npm-public", "local", "npm", "").repo("npm-private", "local", "npm", "").repo("strict", "local", "npm", `{"requireAuthentication":true}`)
		f.policy(policyDef{name: "public", selectors: []string{"npm-public", "strict"}, entries: map[string][]string{AnonymousID: {"read"}}})
		off := newTestEngine(t, f, nil)
		on := newTestEngine(t, f, func(o *Options) { o.AnonymousAccess = func() bool { return true } })
		runAuthzCases(t, off, []authzCase{
			{"anonymous access off refuses a granted read with 401", anonSub, read("npm-public", "left-pad", ""), DenyUnauthenticated, ReasonUnauthenticated, ""},
			{"anonymous access off refuses a publish with 401", anonSub, write("npm-public", "x", "", Deploy), DenyUnauthenticated, ReasonUnauthenticated, ""},
			{"an authenticated user does not get the anonymous grant", carolSub, read("npm-public", "x", ""), DenyHidden, ReasonNoGrant, Read},
		}, &n)
		runAuthzCases(t, on, []authzCase{
			{"anonymous access on allows the granted read", anonSub, read("npm-public", "left-pad", ""), Allow, ReasonAllowGrant, ""},
			{"anonymous lacking a grant is challenged and never answered 404", anonSub, read("npm-private", "x", ""), DenyUnauthenticated, ReasonNoGrant, ""},
			{"anonymous write is challenged even where read is granted", anonSub, write("npm-public", "x", "", Deploy), DenyUnauthenticated, ReasonNoGrant, ""},
			{"requireAuthentication challenges anonymous whatever the policies grant", anonSub, read("strict", "x", ""), DenyUnauthenticated, ReasonUnauthenticated, ""},
			{"a missing repository is challenged for anonymous", anonSub, read("ghost", "x", ""), DenyUnauthenticated, ReasonNoGrant, ""},
			{"a user is not affected by requireAuthentication", carolSub, read("strict", "x", ""), DenyHidden, ReasonNoGrant, Read},
		}, &n)
	})

	t.Run("R-AUTHZ-009 R-AUTHZ-015 virtual repositories and the worked trace", func(t *testing.T) {
		f := newFixture().repo("npm-internal", "local", "npm", `{}`).repo("npmjs", "remote", "npm", `{}`).
			repo("npm-all", "virtual", "npm", `{"virtual":{"members":["npm-internal","npmjs"],"deployTarget":"npm-internal"}}`).
			repo("npm-nodeploy", "virtual", "npm", `{"virtual":{"members":["npm-internal"]}}`)
		f.policy(policyDef{name: "npm-consumers", selectors: []string{"@any-virtual:npm", "@any-local:npm", "@any-remote:npm"}, entries: map[string][]string{idDevs: {"read"}}})
		f.policy(policyDef{name: "team-web-publish", selectors: []string{"npm-internal"}, pkgInc: []string{"@web/*"}, entries: map[string][]string{idWeb: {"annotate", "deploy", "read"}}})
		f.policy(policyDef{name: "anon-partial", selectors: []string{"npm-all", "npmjs"}, entries: map[string][]string{AnonymousID: {"read"}}})
		f.policy(policyDef{name: "virtual-deploy", selectors: []string{"@any-virtual"}, entries: map[string][]string{idBob: {"deploy", "manage"}}})
		e := newTestEngine(t, f, func(o *Options) { o.AnonymousAccess = func() bool { return true } })
		off := newTestEngine(t, f, nil)
		readOnlyTok := tokenScop(aliceSub, token.Scope{Repositories: []token.ScopeGrant{{Repositories: []string{"@any"}, Actions: []string{"read"}}}})
		runAuthzCases(t, e, []authzCase{
			{"trace 1 alice views through npm-all", aliceSub, read("npm-all", "@web/button", ""), Allow, "", ""},
			{"trace 2 alice publishes through npm-all", aliceSub, write("npm-all", "@web/button", "", Deploy), Allow, "", ""},
			{"trace 3 alice publishes a name she has no grant for", aliceSub, write("npm-all", "@pay/core", "", Deploy), DenyVisible, ReasonNoGrant, Deploy},
			{"trace 4 bob has no deploy on the target", bobSub, write("npm-all", "@web/button", "", Deploy), DenyVisible, ReasonNoGrant, Deploy},
			{"trace 5 carol has no read on npm-all", carolSub, read("npm-all", "left-pad", ""), DenyHidden, ReasonVirtualGate, Read},
			{"trace 7 alice with a read-only token cannot publish", readOnlyTok, write("npm-all", "@web/button", "", Deploy), DenyVisible, ReasonScope, Deploy},
			{"the gate applies to writes as well and a refused write answers 403", carolSub, write("npm-all", "@web/button", "", Deploy), DenyVisible, ReasonVirtualGate, Read},
			{"a virtual repository without a deploy target refuses a write", aliceSub, write("npm-nodeploy", "@web/button", "", Deploy), DenyVisible, ReasonNoGrant, Deploy},
			{"a grant of deploy on the virtual repository itself has no effect", bobSub, write("npm-nodeploy", "@web/button", "", Deploy), DenyVisible, ReasonNoGrant, Deploy},
			{"manage on the virtual repository itself is decided on it", bobSub, write("npm-all", "", "", Manage), Allow, "", ""},
			{"anonymous reads a virtual repository whose members it reads entirely", anonSub, read("npm-nodeploy", "x", ""), DenyUnauthenticated, ReasonVirtualGate, ""},
			{"trace 6 anonymous through npm-all is challenged when a member is not readable", anonSub, read("npm-all", "left-pad", ""), DenyUnauthenticated, ReasonAnonymousPartialVirtual, ""},
		}, &n)
		runAuthzCases(t, off, []authzCase{
			{"trace 6 without anonymous access npm-all answers 401", anonSub, read("npm-all", "left-pad", ""), DenyUnauthenticated, ReasonUnauthenticated, ""},
		}, &n)

		f2 := newFixture().repo("npm-internal", "local", "npm", "").repo("npmjs", "remote", "npm", "").
			repo("npm-all", "virtual", "npm", `{"virtual":{"members":["npm-internal","npmjs"]}}`)
		f2.policy(policyDef{name: "anon-all", selectors: []string{"npm-all", "npm-internal", "npmjs"}, entries: map[string][]string{AnonymousID: {"read"}}})
		e2 := newTestEngine(t, f2, func(o *Options) { o.AnonymousAccess = func() bool { return true } })
		runAuthzCases(t, e2, []authzCase{
			{"anonymous reads a virtual repository when it reads the virtual repository and every member", anonSub, read("npm-all", "left-pad", ""), Allow, "", ""},
		}, &n)
	})

	t.Run("R-AUTHZ-010 R-AUTHZ-013 system roles and capabilities", func(t *testing.T) {
		f := newFixture().repo("npm-internal", "local", "npm", "")
		f.bind(RoleAdmin, idOps).bind(RoleAuditor, "user-auditor-direct").bind(RoleSecurityAdmin, "user-secadm").bind(RoleRepositoryAdmin, "user-repoadm").bind(RoleWebhookAdmin, "user-hookadm")
		e := newTestEngine(t, f, nil)
		auditor := userSub("user-auditor-direct", "auditor")
		secadm := userSub("user-secadm", "secadm")
		repoadm := userSub("user-repoadm", "repoadm")
		hookadm := userSub("user-hookadm", "hookadm")
		adminTok := func(sc token.Scope) Subject { return tokenScop(adminSub, sc) }

		type capCase struct {
			name string
			sub  Subject
			perm string
			ok   bool
		}
		cases := []capCase{
			{"admin holds settings:read through its group", adminSub, "settings:read", true},
			{"admin holds policies:write", adminSub, "policies:write", true},
			{"admin holds roles:write", adminSub, "roles:write", true},
			{"admin holds content:bypass", adminSub, "content:bypass", true},
			{"admin holds repositories:write-config", adminSub, "repositories:write-config", true},
			{"admin holds webhooks:write", adminSub, "webhooks:write", true},
			{"admin holds endpoints:write", adminSub, "endpoints:write", true},
			{"auditor reads policies", auditor, "policies:read", true},
			{"auditor cannot write policies", auditor, "policies:write", false},
			{"auditor reads identities", auditor, "identities:read", true},
			{"auditor cannot write identities", auditor, "identities:write", false},
			{"auditor reads the audit log", auditor, "audit:read", true},
			{"auditor reads webhooks", auditor, "webhooks:read", true},
			{"auditor has no content bypass", auditor, "content:bypass", false},
			{"auditor reads settings", auditor, "settings:read", true},
			{"auditor cannot write settings", auditor, "settings:write", false},
			{"security-admin writes policies", secadm, "policies:write", true},
			{"security-admin reads roles", secadm, "roles:read", true},
			{"security-admin cannot write roles", secadm, "roles:write", false},
			{"security-admin writes identities", secadm, "identities:write", true},
			{"security-admin writes security settings", secadm, "security-settings:write", true},
			{"security-admin cannot write repository configuration", secadm, "repositories:write-config", false},
			{"security-admin reads repository configuration", secadm, "repositories:read-config", true},
			{"security-admin revokes tokens", secadm, "tokens:revoke", true},
			{"security-admin cannot write settings", secadm, "settings:write", false},
			{"repository-admin writes repository configuration", repoadm, "repositories:write-config", true},
			{"repository-admin cannot read policies", repoadm, "policies:read", false},
			{"repository-admin reads projects", repoadm, "projects:read", true},
			{"repository-admin cannot write projects", repoadm, "projects:write", false},
			{"webhook-admin writes webhooks", hookadm, "webhooks:write", true},
			{"webhook-admin cannot read the audit log", hookadm, "audit:read", false},
			{"a user without a role holds nothing", carolSub, "settings:read", false},
			{"a user without a role cannot write policies", carolSub, "policies:write", false},
			{"anonymous holds no capability", anonSub, "settings:read", false},
			{"the break-glass administrator holds admin without a binding", breakSub, "roles:write", true},
			{"the break-glass administrator holds content:bypass", breakSub, "content:bypass", true},
			{"a CI token never holds a capability", ciSub, "settings:read", false},
			{"a service account without a binding holds nothing", botSub, "settings:read", false},
			{"an unknown capability is refused", adminSub, "everything:write", false},
			{"the default scope of an administrator's token exercises no role", adminTok(token.Scope{Inherit: true}), "policies:write", false},
			{"admin: true exercises every capability of admin", adminTok(token.Scope{Inherit: true, Admin: true}), "policies:write", true},
			{"admin: true includes content:bypass", adminTok(token.Scope{Inherit: true, Admin: true}), "content:bypass", true},
			{"a system item grants a capability the owner's role holds", adminTok(token.Scope{System: []string{"policies:read"}}), "policies:read", true},
			{"a system item grants only its own capability", adminTok(token.Scope{System: []string{"policies:read"}}), "policies:write", false},
			{"a system item lists no other capability", adminTok(token.Scope{System: []string{"policies:read"}}), "identities:read", false},
			{"content:bypass is never a system item", adminTok(token.Scope{System: []string{"content:bypass"}}), "content:bypass", false},
			{"an auditor's token with policies:read reads policies", tokenScop(auditor, token.Scope{System: []string{"policies:read"}}), "policies:read", true},
			{"an auditor's token cannot read users", tokenScop(auditor, token.Scope{System: []string{"policies:read"}}), "identities:read", false},
			{"a system item the owner's roles do not grant yields nothing", tokenScop(auditor, token.Scope{System: []string{"policies:write"}}), "policies:write", false},
			{"admin: true of a non-admin exercises no admin capability", tokenScop(auditor, token.Scope{Admin: true}), "policies:write", false},
			{"admin: true of a non-admin still exercises its system items", tokenScop(auditor, token.Scope{Admin: true, System: []string{"audit:read"}}), "audit:read", true},
			{"a service-account token with a system item and no role holds nothing", tokenScop(botSub, token.Scope{System: []string{"policies:read"}}), "policies:read", false},
			{"the role form admin is held by an administrator", adminSub, "role:admin", true},
			{"the role form admin is not held by an auditor", auditor, "role:admin", false},
			{"the role form auditor is held by the auditor", auditor, "role:auditor", true},
			{"the role form is not held through a default-scope token", adminTok(token.Scope{Inherit: true}), "role:admin", false},
			{"the role form is held through an admin token", adminTok(token.Scope{Admin: true}), "role:admin", true},
			{"the role form admin is held by break-glass", breakSub, "role:admin", true},
			{"an unknown role is refused", adminSub, "role:root", false},
		}
		for _, tc := range cases {
			n++
			t.Run("R-AUTHZ-010 "+tc.name, func(t *testing.T) {
				err := e.Require(t.Context(), tc.sub, tc.perm)
				if (err == nil) != tc.ok {
					t.Fatalf("Require(%s) = %v, want ok=%v", tc.perm, err, tc.ok)
				}
				if err != nil {
					ee, _ := errs.As(err)
					want := 403
					if tc.sub.Anonymous {
						want = 401
					}
					if ee == nil || ee.Status() != want {
						t.Fatalf("status = %v, want %d", err, want)
					}
				}
			})
		}

		runAuthzCases(t, e, []authzCase{
			{"content:bypass lets an administrator read anything", adminSub, read("npm-internal", "x", ""), Allow, ReasonAllowRole, ""},
			{"content:bypass lets an administrator write anything", adminSub, write("npm-internal", "x", "", Deploy, Overwrite, Delete, Manage), Allow, ReasonAllowRole, ""},
			{"the break-glass administrator reads anything", breakSub, read("npm-internal", "x", ""), Allow, ReasonAllowRole, ""},
			{"an administrator's default-scope token has no content bypass", tokenScop(adminSub, token.Scope{Inherit: true}), read("npm-internal", "x", ""), DenyHidden, ReasonNoGrant, Read},
			{"an administrator's admin token has content bypass", tokenScop(adminSub, token.Scope{Admin: true}), read("npm-internal", "x", ""), Allow, ReasonAllowRole, ""},
			{"an auditor has no content rights through its role", auditor, read("npm-internal", "x", ""), DenyHidden, ReasonNoGrant, Read},
			{"a missing repository is bypassed by nobody", adminSub, read("ghost", "x", ""), DenyHidden, ReasonNoGrant, Read},
		}, &n)
	})

	t.Run("R-EP-013 the endpoint ceiling hook binds every subject", func(t *testing.T) {
		f := newFixture().repo("npm-internal", "local", "npm", "").repo("npm-hidden", "local", "npm", "")
		f.policy(policyDef{name: "all", selectors: []string{"@any"}, entries: map[string][]string{idDevs: {"read", "deploy"}}})
		f.bind(RoleAdmin, idOps)
		e := newTestEngine(t, f, func(o *Options) { o.Ceiling = stubCeiling{} })
		runAuthzCases(t, e, []authzCase{
			{"an exposed repository is readable", aliceSub, func() Coordinate { c := read("npm-internal", "x", ""); c.Endpoint = "customers"; return c }(), Allow, "", ""},
			{"a read the exposure does not admit is a hidden 404", aliceSub, func() Coordinate { c := read("npm-hidden", "x", ""); c.Endpoint = "customers"; return c }(), DenyHidden, ReasonExposure, Read},
			{"the same read on the default endpoint is allowed", aliceSub, read("npm-hidden", "x", ""), Allow, "", ""},
			{"a write on an endpoint with exposure rules is 403 read-only", aliceSub, func() Coordinate { c := write("npm-internal", "x", "", Deploy); c.Endpoint = "customers"; return c }(), DenyVisible, ReasonEndpointReadOnly, ""},
			{"the ceiling binds an administrator", adminSub, func() Coordinate { c := read("npm-hidden", "x", ""); c.Endpoint = "customers"; return c }(), DenyHidden, ReasonExposure, Read},
			{"the ceiling binds the break-glass administrator's writes", breakSub, func() Coordinate { c := write("npm-internal", "x", "", Deploy); c.Endpoint = "customers"; return c }(), DenyVisible, ReasonEndpointReadOnly, ""},
		}, &n)
	})

	if n < 200 {
		t.Fatalf("E1.3 needs at least 200 authorization cases, ran %d", n)
	}
	t.Logf("E1.3: %d authorization cases", n)
}

// stubCeiling hides npm-hidden and makes every write read-only on the endpoint
// "customers".
type stubCeiling struct{}

func (stubCeiling) Admits(_ *Index, c Coordinate, a Action) CeilingVerdict {
	if c.Endpoint != "customers" {
		return CeilingAdmits
	}
	if a != Read {
		return CeilingReadOnly
	}
	if c.Repository == "npm-hidden" {
		return CeilingHidesRead
	}
	return CeilingAdmits
}
