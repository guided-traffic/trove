package authz

import (
	"context"
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

type fakeStored struct {
	sp    StoredPackage
	limit int
}

func (f *fakeStored) Stored(_ context.Context, _ StoredRepo, _ string, limit int) (StoredPackage, error) {
	f.limit = limit
	return f.sp, nil
}

func TestR_EP_016_StoredOfShowsOnlyWhatTheCallerMayRead(t *testing.T) {
	src := &fakeStored{sp: StoredPackage{Listed: true, Versions: []string{"3.0.0", "2.0.0", "1.0.0"}, Tags: map[string]string{"latest": "3.0.0", "lts": "1.0.0"}}}
	s := &ExposureService{}
	s.SetStored(src)
	repo := q.EndpointsRepositoryByKeyRow{ID: 1, Key: "npm-local", Format: "npm", Class: "local"}
	ctx := context.Background()

	t.Run("R-EP-016 Q-EP-62 a package the caller may not read answers as one the repository does not hold", func(t *testing.T) {
		got, err := s.storedOf(ctx, repo, "@finance/ledger", nil, func(pkg, v string) bool { return false })
		if err != nil || !got.Listed || len(got.Versions) != 0 || len(got.Tags) != 0 {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("R-EP-016 Q-EP-62 a version the caller may not read is dropped with the tags that point to it", func(t *testing.T) {
		got, err := s.storedOf(ctx, repo, "p", nil, func(pkg, v string) bool { return v != "3.0.0" })
		if err != nil || !slices.Equal(got.Versions, []string{"2.0.0", "1.0.0"}) || len(got.Tags) != 1 || got.Tags["lts"] != "1.0.0" {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("R-EP-016 the source is asked for one more than the check judges, so a cut is noticed", func(t *testing.T) {
		if _, err := s.storedOf(ctx, repo, "p", nil, nil); err != nil || src.limit != MaxCheckStored+1 {
			t.Fatalf("limit %d, err %v", src.limit, err)
		}
	})
}
