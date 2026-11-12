//go:build integration

package repository

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// TestR_REPO_012_TheQueryMatchesTheQualifiedName: q matches the key, <project>/<key> and the description (Q-API-279), so the
// results page of the search lists every repository the suggestions name; names keeps the named repositories (R-API-063).
func TestR_REPO_012_TheQueryMatchesTheQualifiedName(t *testing.T) {
	e := newStackEnv(t)
	rep := e.newReplica("query", stackOpts{})
	ctx := context.Background()
	e.stack.Project("web")
	e.stack.Project("mobile")
	for _, n := range []string{"web/npm-local", "web/docs", "mobile/npm-local", "it/web-npm"} {
		project, key, _ := strings.Cut(n, "/")
		if _, err := rep.svc.Create(ctx, caller, project, []byte(fmt.Sprintf(genericBody, key))); err != nil {
			t.Fatalf("create %s: %v", n, err)
		}
	}
	list := func(f repository.ListFilter) []string {
		t.Helper()
		res, err := rep.svc.List(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, st := range res.Items {
			out = append(out, st.Repository.Name.String())
		}
		return out
	}
	for _, c := range []struct {
		q    string
		want []string
	}{
		{"web/npm", []string{"web/npm-local"}},
		{"npm-local", []string{"mobile/npm-local", "web/npm-local"}},
		{"WEB/", []string{"web/docs", "web/npm-local"}},
		{"web-npm", []string{"it/web-npm"}},
	} {
		t.Run("R-REPO-012 q="+c.q, func(t *testing.T) {
			if got := list(repository.ListFilter{Query: c.q}); !slices.Equal(got, c.want) {
				t.Fatalf("%v, want %v", got, c.want)
			}
		})
	}
	t.Run("R-API-063 names keeps the named repositories in the list's order; an unknown name is absent", func(t *testing.T) {
		names := []qname.Repo{{Project: "web", Key: "npm-local"}, {Project: "mobile", Key: "npm-local"}, {Project: "web", Key: "nope"}}
		if got := list(repository.ListFilter{Names: names}); !slices.Equal(got, []string{"mobile/npm-local", "web/npm-local"}) {
			t.Fatalf("%v", got)
		}
	})
}
