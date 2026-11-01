package repository

import (
	"log/slog"
	"testing"
)

// keeps asks keepsAway on the fixture's claims, with the remote refusal on or off.
func (f *routeFixture) keeps(via *Repository, member Repository, term string, refusal bool) bool {
	f.t.Helper()
	rt := &Routing{cat: f.cat, refusal: func() bool { return refusal }}
	return keepsAway(rt, buildClaimIndex(f.rows, slog.New(slog.DiscardHandler)), via, member, term)
}

func TestR_REPO_023_KeepsAwayAWithheldTerm(t *testing.T) {
	f := newRouteFixture(t)
	local := f.repo("local", Local, "npmlike")
	f.repo("other", Local, "npmlike")
	remote := f.repo("remote", Remote, "npmlike", exclude("acme-*", "internal"))
	f.repo("pinned", Remote, "npmlike")
	f.claim("local", "partner-*", ScopeMembers)
	f.claim("other", "shared", ScopeGlobal)
	f.claim("local", "tools.*", ScopeMembers)
	rules := []PinRule{{Packages: []string{"vendor-*"}, Members: []string{"local"}}}
	v := f.virtual("all", "npmlike", []string{"local", "remote", "pinned"}, rules)
	pinned := f.repos["pinned"]

	t.Run("an exclude pattern of the member keeps the names it matches and its stem away", func(t *testing.T) {
		for _, term := range []string{"acme-sdk", "internal", "acme"} {
			if !f.keeps(nil, remote, term, true) {
				t.Errorf("%q is not kept away from the remote directly", term)
			}
			if !f.keeps(&v, remote, term, true) {
				t.Errorf("%q is not kept away from the remote through the virtual repository", term)
			}
		}
		if f.keeps(nil, remote, "left-pad", true) {
			t.Error("a name nothing protects is kept away")
		}
	})
	t.Run("a claim in effect in the virtual repository keeps its names away from a member that does not claim", func(t *testing.T) {
		for _, term := range []string{"partner-sdk", "partner", "tools.core", "tools"} {
			if !f.keeps(&v, remote, term, true) {
				t.Errorf("%q is not kept away from the remote", term)
			}
			if f.keeps(&v, local, term, true) {
				t.Errorf("%q is kept away from the claiming member", term)
			}
		}
	})
	t.Run("a claim of a repository outside the virtual repository, scope members, is not in effect in it", func(t *testing.T) {
		f.claim("other", "elsewhere-*", ScopeMembers)
		if f.keeps(&v, remote, "elsewhere-x", true) || f.keeps(&v, remote, "elsewhere", true) {
			t.Error("a members claim outside the virtual repository keeps a term away")
		}
	})
	t.Run("a global claim keeps a name away from a remote directly only while the remote refusal is on", func(t *testing.T) {
		if !f.keeps(nil, remote, "shared", true) {
			t.Error("a global claim does not keep the name away from the remote")
		}
		if f.keeps(nil, remote, "shared", false) {
			t.Error("a global claim keeps the name away with the remote refusal off")
		}
		if f.keeps(nil, local, "shared", true) {
			t.Error("a claim keeps a name away from a local repository addressed directly")
		}
	})
	t.Run("a pin rule that does not list the member keeps its names and their stem away from it", func(t *testing.T) {
		for _, term := range []string{"vendor-sdk", "vendor"} {
			if !f.keeps(&v, pinned, term, true) || !f.keeps(&v, remote, term, true) {
				t.Errorf("%q is not kept away from the members the rule does not list", term)
			}
			if f.keeps(&v, local, term, true) {
				t.Errorf("%q is kept away from the member the rule lists", term)
			}
		}
	})
	t.Run("an include pattern does not protect a term", func(t *testing.T) {
		g := newRouteFixture(t)
		r := g.repo("r", Remote, "npmlike", include("only-*"))
		if g.keeps(nil, r, "only", true) || g.keeps(nil, r, "only-this", true) {
			t.Error("a name the include pattern admits is kept away")
		}
		if g.keeps(nil, r, "else", true) {
			t.Error("a name the include pattern does not admit is kept away: include patterns restrict, they do not protect")
		}
	})
	t.Run("a term that is no name of the format keeps nothing away", func(t *testing.T) {
		if f.keeps(&v, remote, "", true) {
			t.Error("the empty term is kept away")
		}
	})
}
