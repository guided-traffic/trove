package authz

import (
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/config"
)

// The auditor's read of every repository's metadata, never of an artifact
// (R-AUTHZ-011, Q-AUTHZ-172), at the decision point.

const (
	idAuditorDirect = "user-auditor-direct"
	idAuditors      = "group-auditors"
	idInGroup       = "user-auditor-member"
)

func metadataFixture() *fixture {
	f := newFixture().
		repo("npm-internal", "local", "npm", "").
		repo("nuget-local", "local", "nuget", "").
		repo("generic-local", "local", "generic", "").
		repo("npm-all", "virtual", "npm", `{"virtual":{"members":["npm-internal"],"deployTarget":"npm-internal"}}`)
	f.bind(RoleAuditor, idAuditorDirect).bind(RoleAuditor, idAuditors).bind(RoleAdmin, idOps).bind(RoleSecurityAdmin, "user-secadm")
	return f
}

func search(repo string) Coordinate {
	return Coordinate{Repository: qr(repo), Kind: KindSearch, Actions: []Action{Read}}
}

func packageMetadata(repo, pkg string) Coordinate {
	return Coordinate{Repository: qr(repo), Package: pkg, Kind: KindMetadata, Actions: []Action{Read}}
}

func TestQ_AUTHZ_172_AuditorReadsMetadataNeverArtifacts(t *testing.T) {
	e := newTestEngine(t, metadataFixture(), nil)
	direct := userSub(idAuditorDirect, "auditor")
	member := userSub(idInGroup, "member", idAuditors)
	n := 0
	runAuthzCases(t, e, []authzCase{
		{"R-AUTHZ-011 a user bound to auditor reads a repository's metadata", direct, metadata("npm-internal"), Allow, ReasonAllowRole, ""},
		{"R-AUTHZ-011 a member of a group bound to auditor reads it too", member, metadata("nuget-local"), Allow, ReasonAllowRole, ""},
		{"R-AUTHZ-011 the auditor reads a package document", direct, packageMetadata("npm-internal", "left-pad"), Allow, ReasonAllowRole, ""},
		{"R-AUTHZ-011 the auditor searches", member, search("nuget-local"), Allow, ReasonAllowRole, ""},
		{"R-AUTHZ-011 the auditor reads the metadata of every repository without a policy", direct, metadata("generic-local"), Allow, ReasonAllowRole, ""},
		{"Q-AUTHZ-172 an artifact is refused like a missing one", direct, read("npm-internal", "left-pad", "left-pad/-/left-pad-1.0.0.tgz"), DenyHidden, ReasonNoGrant, Read},
		{"Q-AUTHZ-172 a NuGet package download is refused", member, read("nuget-local", "acme.widgets", "acme.widgets/1.0.0/acme.widgets.1.0.0.nupkg"), DenyHidden, ReasonNoGrant, Read},
		{"Q-AUTHZ-172 a raw file is refused", direct, read("generic-local", "", "a/b.bin"), DenyHidden, ReasonNoGrant, Read},
		{"Q-AUTHZ-172 a read the caller did not classify is no metadata read", direct, Coordinate{Repository: qr("npm-internal"), Actions: []Action{Read}}, DenyHidden, ReasonNoGrant, Read},
		{"Q-AUTHZ-172 the auditor publishes nothing", direct, write("npm-internal", "left-pad", "", Deploy), DenyVisible, ReasonNoGrant, Deploy},
		{"Q-AUTHZ-172 the auditor deletes nothing", member, write("nuget-local", "acme.widgets", "", Delete), DenyVisible, ReasonNoGrant, Delete},
		{"Q-AUTHZ-172 the auditor tags nothing", direct, write("npm-internal", "left-pad", "", Annotate), DenyVisible, ReasonNoGrant, Annotate},
		{"Q-AUTHZ-172 the auditor overwrites nothing", direct, write("generic-local", "", "a/b.bin", Overwrite), DenyVisible, ReasonNoGrant, Overwrite},
		{"Q-AUTHZ-172 the auditor manages nothing", direct, write("npm-internal", "left-pad", "", Manage), DenyVisible, ReasonNoGrant, Manage},
		{"Q-AUTHZ-172 a metadata coordinate that also asks for a write is refused", direct, Coordinate{Repository: qr("npm-internal"), Kind: KindMetadata, Actions: []Action{Read, Deploy}}, DenyVisible, ReasonNoGrant, Deploy},
		{"R-AUTHZ-009 the auditor passes the gate of a virtual repository for metadata", direct, metadata("npm-all"), Allow, "", ""},
		{"R-AUTHZ-009 and reads a member's document through it", direct, Coordinate{Repository: qr("npm-internal"), Addressed: qr("npm-all"), Package: "left-pad", Kind: KindMetadata, Actions: []Action{Read}}, Allow, ReasonAllowRole, ""},
		{"Q-AUTHZ-172 an artifact through a virtual repository stops at its gate", direct, read("npm-all", "left-pad", "left-pad/-/left-pad-1.0.0.tgz"), DenyHidden, ReasonVirtualGate, Read},
		{"Q-AUTHZ-172 a publish through a virtual repository is refused", member, write("npm-all", "left-pad", "", Deploy), DenyVisible, ReasonVirtualGate, Read},
		{"R-AUTHZ-001 a user without the role reads no metadata", carolSub, metadata("npm-internal"), DenyHidden, ReasonNoGrant, Read},
		{"R-AUTHZ-011 the security administrator holds no metadata read", userSub("user-secadm", "secadm"), metadata("npm-internal"), DenyHidden, ReasonNoGrant, Read},
		{"R-AUTHZ-010 the administrator reads artifacts through content:bypass", adminSub, read("npm-internal", "left-pad", "x.tgz"), Allow, ReasonAllowRole, ""},
	}, &n)

	t.Run("R-AUTHZ-013 a token carries the capability only through its scope", func(t *testing.T) {
		withScope := func(sc scope.Scope) Subject { return tokenScop(direct, sc) }
		npmOnly := scope.Scope{
			System:       []string{string(ContentMetadataRead)},
			Repositories: []scope.Grant{{Repositories: []string{"it/npm-internal"}, Actions: []string{"read"}}},
		}
		runAuthzCases(t, e, []authzCase{
			{"Q-AUTHZ-172 the default scope exercises no role", withScope(scope.Scope{Inherit: true}), metadata("npm-internal"), DenyHidden, ReasonNoGrant, Read},
			{"Q-AUTHZ-172 the system item admits the capability", withScope(scope.Scope{Inherit: true, System: []string{string(ContentMetadataRead)}}), metadata("nuget-local"), Allow, ReasonAllowRole, ""},
			{"Q-AUTHZ-172 the system item never admits an artifact", withScope(scope.Scope{Inherit: true, System: []string{string(ContentMetadataRead)}}), read("nuget-local", "", "a.nupkg"), DenyHidden, ReasonNoGrant, Read},
			{"R-AUTHZ-013 the repository scope narrows the capability like a grant", withScope(npmOnly), metadata("npm-internal"), Allow, ReasonAllowRole, ""},
			{"R-AUTHZ-013 a repository outside the scope stays hidden", withScope(npmOnly), metadata("nuget-local"), DenyHidden, ReasonNoGrant, Read},
			{"R-AUTHZ-013 a scope with the item and no repository reads nothing", withScope(scope.Scope{System: []string{string(ContentMetadataRead)}}), metadata("npm-internal"), DenyHidden, ReasonNoGrant, Read},
			{"R-AUTHZ-013 a user without the role gains nothing from the item", tokenScop(carolSub, scope.Scope{Inherit: true, System: []string{string(ContentMetadataRead)}}), metadata("npm-internal"), DenyHidden, ReasonNoGrant, Read},
		}, &n)
	})

	t.Run("R-AUTHZ-010 the capability belongs to admin and auditor", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			sub  Subject
			ok   bool
		}{
			{"auditor", direct, true},
			{"auditor through a group", member, true},
			{"admin", adminSub, true},
			{"security-admin", userSub("user-secadm", "secadm"), false},
			{"no role", carolSub, false},
		} {
			n++
			if err := e.Require(t.Context(), tc.sub, string(ContentMetadataRead)); (err == nil) != tc.ok {
				t.Errorf("%s: Require(content:metadata-read) = %v, want ok=%v", tc.name, err, tc.ok)
			}
		}
		me, err := e.Effective(t.Context(), member)
		if err != nil || !slices.Contains(me.Capabilities, ContentMetadataRead) || slices.Contains(me.Capabilities, ContentBypass) {
			t.Fatalf("Effective = %+v, %v", me, err)
		}
	})

	t.Run("R-AUTHZ-016 the auditor lists every repository", func(t *testing.T) {
		n++
		keys := []string{"generic-local", "npm-all", "npm-internal", "nuget-local"}
		got, err := e.Readable(t.Context(), direct, qrs(keys))
		if err != nil || !slices.Equal(qkeys(got), keys) {
			t.Fatalf("Readable = %v, %v", got, err)
		}
		if got, _ := e.Readable(t.Context(), carolSub, qrs(keys)); len(got) != 0 {
			t.Fatalf("a user without the role lists %v", got)
		}
	})

	t.Run("R-EP-013 an endpoint's exposure binds the auditor", func(t *testing.T) {
		f := metadataFixture()
		f.exposure(ruleDef{endpoint: "customers", name: "releases", repo: "generic-local", dims: paths("releases/**")})
		ce := newTestEngine(t, f, func(o *Options) { o.Ceiling = exposureCeiling() })
		meta := func(path string) Coordinate {
			return Coordinate{Repository: qr("generic-local"), Path: path, Kind: KindMetadata, Actions: []Action{Read}}
		}
		runAuthzCases(t, ce, []authzCase{
			{"R-EP-013 a folder the rule exposes is listed", onEP(direct, "customers"), meta("releases/"), Allow, ReasonAllowRole, ""},
			{"R-EP-013 a folder outside the rule is hidden", onEP(direct, "customers"), meta("internal/"), DenyHidden, ReasonExposure, Read},
			{"R-EP-013 a repository without a rule is hidden", onEP(direct, "customers"), metadata("npm-internal"), DenyHidden, ReasonExposure, Read},
			{"R-EP-013 the default endpoint shows everything", onEP(direct, "default"), metadata("npm-internal"), Allow, ReasonAllowRole, ""},
		}, &n)
	})
	if n == 0 {
		t.Fatal("no case ran")
	}
}

// TestR_AUTHZ_010_ConfigurationKnowsTheSystemRoles keeps the roles authz.role_bindings
// accepts equal to the roles of this package: config is a leaf and cannot import them.
func TestR_AUTHZ_010_ConfigurationKnowsTheSystemRoles(t *testing.T) {
	var roles []string
	for _, r := range Roles() {
		roles = append(roles, string(r))
	}
	if !slices.Equal(config.SystemRoles(), roles) {
		t.Fatalf("config.SystemRoles() = %v, authz.Roles() = %v", config.SystemRoles(), roles)
	}
}

// TestQ_AUTHZ_172_CapabilityIsASystemScopeItem keeps the capability in the list the
// scope validation reads its items from (R-AUTH-025).
func TestQ_AUTHZ_172_CapabilityIsASystemScopeItem(t *testing.T) {
	if !slices.Contains(Capabilities(), ContentMetadataRead) || !ContentMetadataRead.Valid() {
		t.Fatalf("content:metadata-read is not a capability: %v", Capabilities())
	}
	if !roleCaps(RoleAuditor).has(ContentMetadataRead) || !roleCaps(RoleAdmin).has(ContentMetadataRead) {
		t.Fatal("the auditor and the administrator must hold content:metadata-read")
	}
	for _, r := range []Role{RoleSecurityAdmin, RoleRepositoryAdmin, RoleWebhookAdmin} {
		if roleCaps(r).has(ContentMetadataRead) {
			t.Errorf("%s holds content:metadata-read", r)
		}
	}
}
