package authz

import (
	"context"
	"errors"
	"slices"
	"strings"
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
		got, err := e.PathsAllowed(ctx, alice, Coordinate{Repository: "generic-local"}, Read, paths, nil)
		if err != nil {
			t.Fatal(err)
		}
		want := []bool{true, true, false, false, true} // the root is repository level: any grant covers it
		if !slices.Equal(got, want) {
			t.Fatalf("PathsAllowed = %v, want %v", got, want)
		}
	})

	t.Run("R-STOR-018 delete is decided for the action it names", func(t *testing.T) {
		got, err := e.PathsAllowed(ctx, alice, Coordinate{Repository: "generic-local"}, Delete, []string{"releases/1.0/x.bin"}, nil)
		if err != nil || !got[0] {
			t.Fatalf("delete = %v, %v", got, err)
		}
		got, _ = e.PathsAllowed(ctx, alice, Coordinate{Repository: "generic-local"}, Deploy, []string{"releases/1.0/x.bin"}, nil)
		if got[0] {
			t.Fatal("deploy was allowed without a grant")
		}
	})

	t.Run("R-AUTHZ-016 a repository the subject has no grant on, or that does not exist, allows nothing", func(t *testing.T) {
		for _, repo := range []string{"other", "missing"} {
			got, err := e.PathsAllowed(ctx, alice, Coordinate{Repository: repo}, Read, []string{"releases/x"}, nil)
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
		_, err := e.PathsAllowed(ctx, Subject{Anonymous: true}, Coordinate{Repository: "generic-local"}, Read, paths, nil)
		var ee *errs.Error
		if !errors.As(err, &ee) || ee.Kind != errs.Unauthenticated {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestR_EP_014_PathsAllowedClassifies: a listing and a subtree of a format with
// package names are decided per path on the package and the version the format
// gives the path, as Decide decides one of them (R-EP-014, R-AUTHZ-005); without a
// classifier such a path names no package and fails a name set with any pattern
// (Q-AUTHZ-160).
func TestR_EP_014_PathsAllowedClassifies(t *testing.T) {
	f := newFixture().repo("npm-local", "local", "npm", "")
	f.policy(policyDef{name: "no-secrets", selectors: []string{"npm-local"}, pkgExc: []string{"@secret/*"}, entries: map[string][]string{idAlice: {"read", "delete"}}})
	f.policy(policyDef{name: "everything", selectors: []string{"npm-local"}, entries: map[string][]string{idBob: {"read"}}})
	e := newTestEngine(t, f, nil)
	ctx := context.Background()
	classify := func(p string) (string, string) {
		switch {
		case strings.HasPrefix(p, "@secret/x"):
			return "@secret/x", "1.0.0"
		case strings.HasPrefix(p, "@acme/ui"):
			return "@acme/ui", "1.0.0"
		}
		return "", ""
	}
	paths := []string{"@secret/x/-/x-1.0.0.tgz", "@acme/ui/-/ui-1.0.0.tgz", "@acme"}
	for _, tc := range []struct {
		name     string
		sub      Subject
		a        Action
		classify Classifier
		want     []bool
	}{
		{"R-AUTHZ-005 an exclude-only grant reads the packages outside its excludes and not the excluded one", userSub(idAlice, "alice"), Read, classify, []bool{false, true, false}},
		{"R-STOR-018 an exclude-only delete grant cannot delete an excluded package's file", userSub(idAlice, "alice"), Delete, classify, []bool{false, true, false}},
		{"Q-AUTHZ-160 without the format's classification every path of the exclude-only grant is refused", userSub(idAlice, "alice"), Read, nil, []bool{false, false, false}},
		{"R-AUTHZ-005 a grant without patterns reads every path, classified or not", userSub(idBob, "bob"), Read, classify, []bool{true, true, true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.PathsAllowed(ctx, tc.sub, Coordinate{Repository: "npm-local"}, tc.a, paths, tc.classify)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("PathsAllowed = %v, want %v", got, tc.want)
			}
			for i, p := range paths {
				c := Coordinate{Repository: "npm-local", Path: p, Kind: KindMetadata, Actions: []Action{tc.a}}
				if tc.a != Read {
					c.Kind = KindWrite
				}
				if tc.classify != nil {
					c.Package, c.Version = tc.classify(p)
				}
				if d := e.Decide(ctx, tc.sub, c); (d.Outcome == Allow) != got[i] {
					t.Errorf("%s: PathsAllowed %v, Decide %+v", p, got[i], d)
				}
			}
		})
	}
}
