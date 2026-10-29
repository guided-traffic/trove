package authz

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

func TestPathsAllowed(t *testing.T) {
	f := newFixture().repo("generic-local", "local", "generic", "").repo("other", "local", "generic", "")
	f.policy(policyDef{
		name: "releases", selectors: []string{"generic-local"}, pathInc: []string{"releases/**"}, pathExc: []string{"releases/secret/**"},
		entries: map[string][]string{idAlice: {"read", "delete"}},
	})
	rec := &recorder{}
	e := newTestEngine(t, f, func(o *Options) { o.Recorder = rec })
	ctx := context.Background()
	alice := userSub(idAlice, "alice")
	paths := []string{"releases", "releases/1.0/x.bin", "releases/secret/k", "tmp/x.bin", ""}

	t.Run("R-AUTHZ-016 R-STOR-017 a listing keeps the paths the subject may read, by the patterns of its grant", func(t *testing.T) {
		got, err := e.PathsAllowed(ctx, alice, Coordinate{Repository: "generic-local"}, Read, paths)
		if err != nil {
			t.Fatal(err)
		}
		want := []bool{true, true, false, false, true} // the root is repository level: any grant covers it
		if !slices.Equal(got, want) {
			t.Fatalf("PathsAllowed = %v, want %v", got, want)
		}
	})

	t.Run("R-STOR-018 delete is decided for the action it names", func(t *testing.T) {
		got, err := e.PathsAllowed(ctx, alice, Coordinate{Repository: "generic-local"}, Delete, []string{"releases/1.0/x.bin"})
		if err != nil || !got[0] {
			t.Fatalf("delete = %v, %v", got, err)
		}
		got, _ = e.PathsAllowed(ctx, alice, Coordinate{Repository: "generic-local"}, Deploy, []string{"releases/1.0/x.bin"})
		if got[0] {
			t.Fatal("deploy was allowed without a grant")
		}
	})

	t.Run("R-AUTHZ-016 a repository the subject has no grant on, or that does not exist, allows nothing", func(t *testing.T) {
		for _, repo := range []string{"other", "missing"} {
			got, err := e.PathsAllowed(ctx, alice, Coordinate{Repository: repo}, Read, []string{"releases/x"})
			if err != nil || got[0] {
				t.Fatalf("%s = %v, %v", repo, got, err)
			}
		}
	})

	t.Run("R-AUTHZ-029 a listing writes no audit event for the entries it omits", func(t *testing.T) {
		if n := len(rec.list()); n != 0 {
			t.Fatalf("%d audit events", n)
		}
	})

	t.Run("R-AUTHZ-016 an anonymous subject is answered 401 while anonymous access is off", func(t *testing.T) {
		_, err := e.PathsAllowed(ctx, Subject{Anonymous: true}, Coordinate{Repository: "generic-local"}, Read, paths)
		var ee *errs.Error
		if !errors.As(err, &ee) || ee.Kind != errs.Unauthenticated {
			t.Fatalf("err = %v", err)
		}
	})
}
