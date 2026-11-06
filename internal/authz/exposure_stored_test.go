package authz

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

type fakeStored struct {
	sp       StoredPackage
	limit    int
	withTags bool
	calls    int
}

func (f *fakeStored) Stored(_ context.Context, _ StoredRepo, _ string, limit int, withTags bool) (StoredPackage, error) {
	f.limit, f.withTags = limit, withTags
	f.calls++
	return f.sp, nil
}

func allowAll(string, string) bool { return true }

func serviceOver(src StoredPackages) *ExposureService {
	return &ExposureService{o: ExposureServiceOptions{Stored: src}}
}

var storedRepo = q.EndpointsRepositoryByNameRow{ID: 1, ProjectKey: "it", Key: "npm-local", Format: "npm", Class: "local"}

func TestR_EP_016_StoredOfShowsOnlyWhatTheCallerMayRead(t *testing.T) {
	src := &fakeStored{sp: StoredPackage{Listed: true, Versions: []string{"3.0.0", "2.0.0", "1.0.0"}, Tags: map[string]string{"latest": "3.0.0", "lts": "1.0.0"}}}
	s := serviceOver(src)
	repo := storedRepo
	ctx := context.Background()
	want := storedWant{versions: true, tags: true}

	t.Run("R-EP-016 Q-EP-62 a package the caller may not read answers as one the repository does not hold", func(t *testing.T) {
		got, _, err := s.storedOf(ctx, repo, "@finance/ledger", nil, want, func(pkg, v string) bool { return false })
		if err != nil || !got.Listed || len(got.Versions) != 0 || len(got.Tags) != 0 {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("R-EP-016 Q-EP-62 a version the caller may not read is dropped with the tags that point to it", func(t *testing.T) {
		got, _, err := s.storedOf(ctx, repo, "p", nil, want, func(pkg, v string) bool { return v != "3.0.0" })
		if err != nil || !slices.Equal(got.Versions, []string{"2.0.0", "1.0.0"}) || len(got.Tags) != 1 || got.Tags["lts"] != "1.0.0" {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("R-EP-016 the source is asked for one more than the check judges, so a cut is noticed", func(t *testing.T) {
		if _, _, err := s.storedOf(ctx, repo, "p", nil, want, allowAll); err != nil || src.limit != MaxCheckStored+1 {
			t.Fatalf("limit %d, err %v", src.limit, err)
		}
	})
	t.Run("R-EP-016 Q-EP-66 a nil MayRead lists nothing and keeps Listed", func(t *testing.T) {
		got, cut, err := s.storedOf(ctx, repo, "p", nil, want, nil)
		if err != nil || !got.Listed || len(got.Versions) != 0 || len(got.Tags) != 0 || cut {
			t.Fatalf("%+v cut %v %v", got, cut, err)
		}
	})
	t.Run("R-EP-016 Q-EP-66 a check that names the versions decides none for the read scope", func(t *testing.T) {
		var asked []string
		got, _, err := s.storedOf(ctx, repo, "p", nil, storedWant{tags: true}, func(pkg, v string) bool { asked = append(asked, v); return true })
		if err != nil || len(got.Versions) != 0 || len(got.Tags) != 2 {
			t.Fatalf("%+v %v", got, err)
		}
		// The package, and the version each of the two tags points to: never the whole list.
		if len(asked) != 3 || asked[0] != "" {
			t.Fatalf("the read scope was asked %q", asked)
		}
	})
	t.Run("R-EP-016 Q-EP-67 the tags are asked for only when the format has tags", func(t *testing.T) {
		if _, _, err := s.storedOf(ctx, repo, "p", nil, storedWant{versions: true}, allowAll); err != nil || src.withTags {
			t.Fatalf("withTags %v, err %v", src.withTags, err)
		}
		got, _, _ := s.storedOf(ctx, repo, "p", nil, storedWant{versions: true}, allowAll)
		if len(got.Tags) != 0 {
			t.Fatalf("tags of a format without tags: %v", got.Tags)
		}
		if _, _, err := s.storedOf(ctx, repo, "p", nil, want, allowAll); err != nil || !src.withTags {
			t.Fatalf("withTags %v, err %v", src.withTags, err)
		}
	})
	t.Run("R-EP-016 a service without a source lists nothing", func(t *testing.T) {
		got, cut, err := serviceOver(nil).storedOf(ctx, repo, "p", nil, want, allowAll)
		if err != nil || got.Listed || cut {
			t.Fatalf("%+v %v", got, err)
		}
	})
}

// TestR_EP_016_ATruncationIsJudgedBeforeTheReadFilter: the source returns one row more than the
// check judges; a filter that drops the unreadable ones must not make a cut list look complete.
func TestR_EP_016_ATruncationIsJudgedBeforeTheReadFilter(t *testing.T) {
	var all []string
	for i := range MaxCheckStored + 1 {
		all = append(all, fmt.Sprintf("1.0.%d", i))
	}
	s := serviceOver(&fakeStored{sp: StoredPackage{Listed: true, Versions: all}})
	ctx := context.Background()
	want := storedWant{versions: true}

	t.Run("Q-EP-66 rows the caller may not read are dropped and the cut is still reported", func(t *testing.T) {
		got, cut, err := s.storedOf(ctx, storedRepo, "p", nil, want, func(_, v string) bool { return v == "" || v != "1.0.0" })
		if err != nil || !cut || len(got.Versions) != MaxCheckStored {
			t.Fatalf("cut %v, %d versions, %v", cut, len(got.Versions), err)
		}
	})
	t.Run("Q-EP-66 a list the filter made short is a cut one when the source held more than it judges", func(t *testing.T) {
		only := map[string]bool{"1.0.1": true, "1.0.2": true}
		got, cut, err := s.storedOf(ctx, storedRepo, "p", nil, want, func(_, v string) bool { return v == "" || only[v] })
		if err != nil || !cut || len(got.Versions) != 2 {
			t.Fatalf("cut %v, %d versions, %v", cut, len(got.Versions), err)
		}
	})
	t.Run("Q-EP-66 a list that fits is not cut", func(t *testing.T) {
		s := serviceOver(&fakeStored{sp: StoredPackage{Listed: true, Versions: all[:MaxCheckStored]}})
		got, cut, err := s.storedOf(ctx, storedRepo, "p", nil, want, allowAll)
		if err != nil || cut || len(got.Versions) != MaxCheckStored {
			t.Fatalf("cut %v, %d versions, %v", cut, len(got.Versions), err)
		}
	})
	t.Run("Q-EP-66 versions the request named are not a cut", func(t *testing.T) {
		_, cut, err := s.storedOf(ctx, storedRepo, "p", nil, storedWant{}, allowAll)
		if err != nil || cut {
			t.Fatalf("cut %v %v", cut, err)
		}
	})
	t.Run("Q-EP-66 more tags than the check judges are a cut", func(t *testing.T) {
		tags := map[string]string{}
		for i := range MaxCheckStored + 1 {
			tags[fmt.Sprintf("t%04d", i)] = "1.0.0"
		}
		s := serviceOver(&fakeStored{sp: StoredPackage{Listed: true, Tags: tags}})
		got, cut, err := s.storedOf(ctx, storedRepo, "p", nil, storedWant{tags: true}, allowAll)
		if err != nil || !cut || len(got.Tags) != MaxCheckStored {
			t.Fatalf("cut %v, %d tags, %v", cut, len(got.Tags), err)
		}
	})
}
