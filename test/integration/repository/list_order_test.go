//go:build integration

package repository

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/repository"
)

// TestR_REPO_012_ListPagesEveryKeyOnceInByteOrder: the list sorts in the order its
// cursor compares in, whatever collation the database was created with. The cursor
// is the last key of a page compared byte by byte; a database collation such as
// en_US.UTF-8 ignores the hyphen at the first level and sorts weba before web-b, so
// an ORDER BY in that collation skipped web-b on the next page.
func TestR_REPO_012_ListPagesEveryKeyOnceInByteOrder(t *testing.T) {
	e := newStackEnv(t)
	rep := e.newReplica("order", stackOpts{})
	ctx := context.Background()
	keys := []string{"web-a", "weba", "web-b", "webc", "web-c", "web.d"}
	for _, k := range keys {
		if _, err := rep.svc.Create(ctx, caller, "it", []byte(fmt.Sprintf(genericBody, k))); err != nil {
			t.Fatalf("create %s: %v", k, err)
		}
	}
	want := slices.Clone(keys)
	slices.Sort(want) // byte order: web-a web-b web-c web.d weba webc

	t.Run("R-REPO-012 one page lists the keys in byte order", func(t *testing.T) {
		res, err := rep.svc.List(ctx, repository.ListFilter{})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, st := range res.Items {
			got = append(got, st.Document.Key)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("order = %v, want %v", got, want)
		}
	})
	t.Run("R-REPO-012 pages of two, each after the last key of the one before, list every key once", func(t *testing.T) {
		var got []string
		after := ""
		for range len(keys) {
			res, err := rep.svc.List(ctx, repository.ListFilter{After: qr(after), Limit: 2})
			if err != nil {
				t.Fatal(err)
			}
			for _, st := range res.Items {
				got = append(got, st.Document.Key)
			}
			if !res.HasMore || len(res.Items) == 0 {
				break
			}
			after = res.Items[len(res.Items)-1].Document.Key
		}
		if !slices.Equal(got, want) {
			t.Fatalf("paged = %v, want %v", got, want)
		}
	})
}
