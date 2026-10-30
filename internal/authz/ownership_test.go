package authz

import (
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

// ownershipFixture is the publishing side of the team example: npm-internal
// enforces ownership, npm-loose does not, npm-all writes into npm-internal. The
// group team-web deploys @web/*, carol (the team's lead) holds deploy and manage
// on everything, the CI mapping deploys @acme/tools, and the platform
// administrators hold the admin role (content:bypass).
func ownershipFixture() *fixture {
	f := newFixture().
		repo("npm-internal", "local", "npm", `{"local":{"packageOwnership":"enforce"}}`).
		repo("npm-loose", "local", "npm", `{"local":{"packageOwnership":"off"}}`).
		repo("npm-unset", "local", "npm", "").
		repo("npm-broken", "local", "npm", `{"local":`).
		repo("files-internal", "local", "generic", "").
		repo("npmjs", "remote", "npm", "").
		repo("npm-all", "virtual", "npm", `{"virtual":{"members":["npm-internal","npmjs"],"deployTarget":"npm-internal"}}`)
	f.policy(policyDef{name: "readers", selectors: []string{"npm-internal", "npm-all"}, entries: map[string][]string{idDevs: {"read"}}})
	f.policy(policyDef{name: "team-web-publish", selectors: []string{"npm-internal"}, pkgInc: []string{"@web/*"}, entries: map[string][]string{idWeb: {"read", "deploy"}}})
	f.policy(policyDef{name: "lead", selectors: []string{"npm-internal"}, entries: map[string][]string{idCarol: {"read", "deploy", "manage"}}})
	f.policy(policyDef{name: "ci-tools", selectors: []string{"npm-internal"}, pkgInc: []string{"@acme/tools"}, entries: map[string][]string{idMapAcm: {"read", "deploy"}}})
	f.bind(RoleAdmin, idOps)
	return f
}

func newOwnershipEngine(t *testing.T) (*Ownership, *Index) {
	t.Helper()
	e := newTestEngine(t, ownershipFixture(), nil)
	o, err := NewOwnership(OwnershipOptions{Engine: e})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := e.Begin(t.Context(), aliceSub)
	if err != nil {
		t.Fatal(err)
	}
	return o, idx
}

func publish(repo, pkg string) Coordinate { return write(repo, pkg, "", Deploy) }

// TestR_AUTHZ_018_WhenOwnershipIsChecked: only a local repository whose format has
// package names and whose packageOwnership is enforce, and only a coordinate with
// a package name; a write through a virtual repository is checked on its deploy
// target; a document the index cannot read fails closed.
func TestR_AUTHZ_018_WhenOwnershipIsChecked(t *testing.T) {
	o, idx := newOwnershipEngine(t)
	cases := []struct {
		name    string
		c       Coordinate
		applies bool
		target  string
	}{
		{"an enforcing local repository", publish("npm-internal", "@web/new-lib"), true, "npm-internal"},
		{"a write through the virtual repository is checked on its deploy target", publish("npm-all", "@web/new-lib"), true, "npm-internal"},
		{"packageOwnership off", publish("npm-loose", "@web/new-lib"), false, ""},
		{"no packageOwnership in the document", publish("npm-unset", "@web/new-lib"), false, ""},
		{"an unreadable document enforces", publish("npm-broken", "@web/new-lib"), true, "npm-broken"},
		{"a format without package names", write("files-internal", "", "a/b.bin", Deploy), false, ""},
		{"a coordinate without a package name", write("npm-internal", "", "x/y", Deploy), false, ""},
		{"a remote repository", publish("npmjs", "left-pad"), false, ""},
	}
	for _, tc := range cases {
		t.Run("R-AUTHZ-018 "+tc.name, func(t *testing.T) {
			k, applies, err := o.keyOf(idx, tc.c)
			if err != nil || applies != tc.applies {
				t.Fatalf("applies %v, %v; want %v", applies, err, tc.applies)
			}
			if applies && (k.repo.Key != tc.target || k.domain != domainOf(k.repo) || k.format != "npm" || k.name != tc.c.Package) {
				t.Fatalf("key %+v", k)
			}
		})
	}
	t.Run("R-AUTHZ-018 a repository the index does not hold is an error, not a pass", func(t *testing.T) {
		if _, _, err := o.keyOf(idx, publish("ghost", "x")); err == nil {
			t.Fatal("no error")
		}
	})
}

// TestR_AUTHZ_018_ClaimOwners is rule 2: the owners of a claim are the principals
// of every grant that allowed deploy on the coordinate (a group grant makes the
// group the owner, a CI mapping's entry the mapping), or the subject itself when
// content:bypass allowed it.
func TestR_AUTHZ_018_ClaimOwners(t *testing.T) {
	o, idx := newOwnershipEngine(t)
	refs := func(sub Subject, c Coordinate) []string {
		k, _, err := o.keyOf(idx, c)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, cl := range o.factsOf(idx, sub, k, c).claimers {
			out = append(out, cl.ref.String())
		}
		return out
	}
	ciSub := Subject{Kind: ciSub.Kind, Name: "ci", Mappings: []string{"ci:gitlab-internal/acme"}}
	for _, tc := range []struct {
		name string
		sub  Subject
		c    Coordinate
		want []string
	}{
		{"a group grant makes the group the owner", aliceSub, publish("npm-internal", "@web/new-lib"), []string{"group:team-web"}},
		{"a user grant makes the user the owner", carolSub, publish("npm-internal", "@web/new-lib"), []string{"user:carol"}},
		{"through the virtual repository the target's grants decide", aliceSub, publish("npm-all", "@web/new-lib"), []string{"group:team-web"}},
		{"a CI mapping's entry makes the mapping the owner", ciSub, publish("npm-internal", "@acme/tools"), []string{"ci:gitlab-internal/acme"}},
		{"content:bypass makes the subject itself the owner", adminSub, publish("npm-internal", "@web/new-lib"), []string{"user:ops-admin"}},
		{"a grant outside its patterns designates nobody", aliceSub, publish("npm-internal", "@pay/core"), nil},
	} {
		t.Run("R-AUTHZ-018 "+tc.name, func(t *testing.T) {
			if got := refs(tc.sub, tc.c); !slices.Equal(got, tc.want) {
				t.Fatalf("owners %v, want %v", got, tc.want)
			}
		})
	}
}

// TestR_AUTHZ_018_OwnershipRules is the rule table of R-AUTHZ-018 (rules 1, 2, 3
// and 6, and the closed names of R-AUTHZ-031) on the state a commit transaction
// reads.
func TestR_AUTHZ_018_OwnershipRules(t *testing.T) {
	o, idx := newOwnershipEngine(t)
	k, _, _ := o.keyOf(idx, publish("npm-internal", "@web/new-lib"))
	webOwner := []Owner{{Owner: "group:team-web", principalID: idWeb}}
	otherOwner := []Owner{{Owner: "group:team-mobile", principalID: "group-team-mobile"}}
	first := OwnershipWrite{Publish: true, FirstVersion: true}
	next := OwnershipWrite{Publish: true}
	tag := OwnershipWrite{}
	cases := []struct {
		name          string
		sub           Subject
		state         string
		owners        []Owner
		w             OwnershipWrite
		allowed, clm  bool
		reason, sourc string
	}{
		{"rule 1: an unclaimed name without versions is free to the holder of deploy and claimed", aliceSub, StateUnclaimed, nil, first, true, true, ReasonAllowGrant, ""},
		{"rule 1: an operation that is no publish claims nothing", aliceSub, StateUnclaimed, nil, OwnershipWrite{FirstVersion: true}, true, false, ReasonAllowGrant, ""},
		{"rule 1: an owner identity (a group) passes", aliceSub, StateClaimed, webOwner, next, true, false, ReasonAllowGrant, SourceOwnership},
		{"rule 1: a principal that owns nothing is refused", aliceSub, StateClaimed, otherOwner, next, false, false, ReasonOwnership, ""},
		{"rule 1: a dist-tag of an owned name is refused to a non-owner too", aliceSub, StateClaimed, otherOwner, tag, false, false, ReasonOwnership, ""},
		{"rule 1: manage passes and claims nothing", carolSub, StateClaimed, otherOwner, next, true, false, ReasonAllowGrant, "lead"},
		{"rule 1: content:bypass passes", adminSub, StateClaimed, otherOwner, next, true, false, ReasonAllowRole, "role:admin"},
		{"rule 3: an unclaimed name with versions needs manage", aliceSub, StateUnclaimed, nil, next, false, false, ReasonOwnership, ""},
		{"rule 3: a publish by a manage holder claims it", carolSub, StateUnclaimed, nil, next, true, true, ReasonAllowGrant, "lead"},
		{"rule 3: another change by a manage holder claims nothing", carolSub, StateUnclaimed, nil, tag, true, false, ReasonAllowGrant, "lead"},
		{"rule 3: content:bypass publishing a name with versions claims it", adminSub, StateUnclaimed, nil, next, true, true, ReasonAllowRole, "role:admin"},
		{"R-AUTHZ-019: a released name without versions is free again", aliceSub, StateReleased, nil, first, true, true, ReasonAllowGrant, ""},
		{"R-AUTHZ-019: a released name with versions needs manage", aliceSub, StateReleased, nil, next, false, false, ReasonOwnership, ""},
		{"rule 6, R-AUTHZ-031: a claimed name without owners needs manage, versions or not", aliceSub, StateClaimed, nil, first, false, false, ReasonOwnership, ""},
		{"rule 6: a manage holder's publish claims it again", carolSub, StateClaimed, nil, first, true, true, ReasonAllowGrant, "lead"},
	}
	for _, tc := range cases {
		t.Run("R-AUTHZ-018 "+tc.name, func(t *testing.T) {
			c := publish("npm-internal", "@web/new-lib")
			chk := o.decide(OwnershipCheck{
				Applies: true, State: tc.state, Owners: tc.owners, key: k, write: tc.w,
				facts: o.factsOf(idx, tc.sub, k, c), sub: tc.sub, c: c, epoch: idx.Epoch(),
			})
			if (chk.Decision.Outcome == Allow) != tc.allowed || chk.Claim != tc.clm || chk.Decision.Reason != tc.reason {
				t.Fatalf("decision %+v claim %v; want allowed %v claim %v reason %s", chk.Decision, chk.Claim, tc.allowed, tc.clm, tc.reason)
			}
			if tc.sourc != "" && !slices.Contains(chk.Decision.Sources, tc.sourc) {
				t.Fatalf("sources %v, want %s", chk.Decision.Sources, tc.sourc)
			}
		})
	}
	t.Run("R-AUTHZ-018 rule 2: a claim nobody's grant would own is refused, not recorded empty", func(t *testing.T) {
		c := publish("npm-internal", "@pay/core")
		pk, _, _ := o.keyOf(idx, c)
		chk := o.decide(OwnershipCheck{Applies: true, State: StateUnclaimed, key: pk, write: first, facts: o.factsOf(idx, aliceSub, pk, c), sub: aliceSub, c: c})
		if chk.Decision.Outcome == Allow || chk.Claim {
			t.Fatalf("check %+v", chk)
		}
	})
	t.Run("R-AUTHZ-016 the refusal is 403 package_owned_by_other_owner and names only the coordinate", func(t *testing.T) {
		d := Decision{Outcome: DenyVisible, Reason: ReasonOwnership, Missing: Deploy, Target: "npm-internal/@web/new-lib"}
		ee, ok := errs.As(d.Err())
		if !ok || ee.Status() != 403 || ee.Code != errs.CodePackageOwnedByOtherOwner || ee.Detail != "npm-internal/@web/new-lib is owned by another owner; an owner or a holder of manage can add you." {
			t.Fatalf("err = %#v", d.Err())
		}
	})
}

// TestR_AUTHZ_019_OwnIdentity: the caller may list the names of its own identity
// and of its groups without policies:read.
func TestR_AUTHZ_019_OwnIdentity(t *testing.T) {
	o, _ := newOwnershipEngine(t)
	for _, tc := range []struct {
		sub  Subject
		ref  string
		want bool
	}{
		{aliceSub, "user:alice", true},
		{aliceSub, "group:team-web", true},
		{aliceSub, "group:platform-admins", false},
		{aliceSub, "user:bob", false},
		{botSub, "sa:ci-bot", true},
		{botSub, "user:ci-bot", false},
		{anonSub, "anonymous", false},
	} {
		if got := o.ownIdentity(tc.sub, tc.ref); got != tc.want {
			t.Errorf("R-AUTHZ-019 %s as %s: %v, want %v", tc.ref, tc.sub.Name, got, tc.want)
		}
	}
}
