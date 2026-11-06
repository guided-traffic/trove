package claims_test

import (
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/ci/claims"
)

// R-CI-007, Q-AUTHZ-216: a project mapping writes bare keys; the token it contributes to carries instance
// selectors, so the engine and the scope items of the exchange read one grammar.
func TestR_CI_007_AProjectMappingFreezesInstanceSelectors(t *testing.T) {
	r := claims.Rendered{Inline: []claims.InlineGrant{inline([]string{"npm-local", "npm-*", "npm-*:npm", "@any", "@any:nuget", "@any-local"}, claims.Read)}}
	m := claims.Freeze("ci:gitlab-com/web/publish", "m-2", "project:web", &claims.FrozenProject{Key: "web", ID: "p-7"}, r, nil)
	want := []string{"web/npm-local", "web/npm-*", "web/npm-*:npm", "web/*", "web/*:nuget", "@any-local"}
	if got := m.Inline[0].Repositories; !slices.Equal(got, want) {
		t.Fatalf("frozen selectors %v, want %v", got, want)
	}
	if r.Inline[0].Repositories[0] != "npm-local" {
		t.Fatal("the rendered grants must stay unchanged")
	}
	sys := claims.Freeze("ci:gitlab-com/acme", "m-1", "system", nil, claims.Rendered{Inline: []claims.InlineGrant{inline([]string{"web/npm-local"}, claims.Read)}}, nil)
	if got := sys.Inline[0].Repositories[0]; got != "web/npm-local" {
		t.Fatalf("a system mapping's selector stays as written: %q", got)
	}
}

// R-CI-007: an item of the scope parameter is covered by a pattern of project and key, and a key of another
// project is disjoint from a key pattern that does not match it.
func TestR_CI_007_NarrowingReadsProjectAndKey(t *testing.T) {
	r := claims.Rendered{Inline: []claims.InlineGrant{inline([]string{"web/npm-*"}, claims.Read, claims.Deploy)}}
	items, err := claims.ParseScope("read:web/npm-x deploy:shop/npm-x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := claims.Narrow(r, items[:1], nil); err != nil {
		t.Fatalf("a key under the pattern is held: %v", err)
	}
	if _, err := claims.Narrow(r, items[1:], nil); err == nil {
		t.Fatal("a key of another project is not held by the pattern of web")
	}
}
