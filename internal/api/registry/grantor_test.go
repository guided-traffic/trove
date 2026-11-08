package registry

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/registrytoken"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/oci"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// fakeAsker allows the actions listed per repository and package, and records the coordinates it was asked.
type fakeAsker struct {
	allow map[string]bool // "<repo> <package> <action>"
	err   error
	asked []string
}

func (f *fakeAsker) PathsAllowed(_ context.Context, base authz.Coordinate, act authz.Action, paths []string, classify authz.Classifier) ([]bool, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]bool, len(paths))
	for i, p := range paths {
		pkg, _ := classify(p)
		key := base.Repository.String() + " " + pkg + " " + string(act)
		f.asked = append(f.asked, key)
		out[i] = f.allow[key] && p == pkg+"/_taglist"
	}
	return out, nil
}

// fakeRepos is a repository reader over a table.
type fakeRepos struct {
	repos  map[qname.Repo]repository.Repository
	err    error
	lookup int
}

func (f *fakeRepos) ByName(_ context.Context, n qname.Repo) (repository.Repository, error) {
	f.lookup++
	if f.err != nil {
		return repository.Repository{}, f.err
	}
	r, ok := f.repos[n]
	if !ok {
		return repository.Repository{}, errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "no")
	}
	return r, nil
}

func (f *fakeRepos) ByID(context.Context, int64) (repository.Repository, error) {
	return repository.Repository{}, errors.New("not used")
}
func (f *fakeRepos) List(context.Context) ([]repository.Repository, error) { return nil, nil }
func (f *fakeRepos) ListProject(context.Context, string) ([]repository.Repository, error) {
	return nil, nil
}

// R-OCI-006 (the grant of the token service): named in the test name by WP3 once /v2/ is mounted and the grant check runs.
func TestGrantIsTheCoarseCheck(t *testing.T) {
	npm := qname.Repo{Project: "web", Key: "npm-local"}
	gone := qname.Repo{Project: "web", Key: "gone"}
	off := qname.Repo{Project: "web", Key: "off"}
	repos := &fakeRepos{repos: map[qname.Repo]repository.Repository{
		local: {Format: "oci", Lifecycle: repository.Active},
		npm:   {Format: "npm", Lifecycle: repository.Active},
		gone:  {Format: "oci", Lifecycle: repository.Deleting},
		off:   {Format: "oci", Lifecycle: repository.Active, Disabled: true},
	}}
	g := Grantor{Repos: repos, Format: "oci", Documents: NewDocuments(oci.New())}
	ctx := context.Background()

	t.Run("pull asks read, push asks deploy, delete asks delete, each with the image name as the package", func(t *testing.T) {
		ask := &fakeAsker{allow: map[string]bool{"web/docker-local app read": true, "web/docker-local app deploy": true}}
		got, err := g.Grant(ctx, ask, []registrytoken.Scope{{Repo: local, Name: "app", Actions: registrytoken.All}})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Entries) != 1 || got.Entries[0].Actions != registrytoken.Pull|registrytoken.Push {
			t.Fatalf("grant %+v", got)
		}
		want := []string{"web/docker-local app read", "web/docker-local app deploy", "web/docker-local app delete"}
		if len(ask.asked) != 3 || ask.asked[0] != want[0] || ask.asked[1] != want[1] || ask.asked[2] != want[2] {
			t.Fatalf("asked %v", ask.asked)
		}
	})
	t.Run("a path set decides a name on its package document: team/** grants team/app and not other/app", func(t *testing.T) {
		ask := pathAsker{include: "team/"}
		got, err := g.Grant(ctx, ask, []registrytoken.Scope{
			{Repo: local, Name: "team/app", Actions: registrytoken.Pull},
			{Repo: local, Name: "other/app", Actions: registrytoken.Pull},
		})
		if err != nil || len(got.Entries) != 1 || got.Entries[0].Name != "team/app" {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("a caller with read only is granted pull, never push", func(t *testing.T) {
		ask := &fakeAsker{allow: map[string]bool{"web/docker-local app read": true}}
		got, _ := g.Grant(ctx, ask, []registrytoken.Scope{{Repo: local, Name: "app", Actions: registrytoken.Pull | registrytoken.Push}})
		if !got.Allows(local, "app", registrytoken.Pull) || got.Allows(local, "app", registrytoken.Push) {
			t.Fatalf("grant %+v", got)
		}
	})
	t.Run("a package-scoped grant covers the names it matches and no other", func(t *testing.T) {
		ask := &fakeAsker{allow: map[string]bool{"web/docker-local team/app read": true}}
		got, _ := g.Grant(ctx, ask, []registrytoken.Scope{{Repo: local, Name: "team/app", Actions: registrytoken.Pull}, {Repo: local, Name: "other/x", Actions: registrytoken.Pull}})
		if !got.Allows(local, "team/app", registrytoken.Pull) || got.Allows(local, "other/x", registrytoken.Pull) {
			t.Fatalf("grant %+v", got)
		}
	})
	t.Run("a hidden or missing repository grants nothing and is never looked up", func(t *testing.T) {
		repos.lookup = 0
		ask := &fakeAsker{}
		got, err := g.Grant(ctx, ask, []registrytoken.Scope{{Repo: qname.Repo{Project: "web", Key: "secret"}, Name: "app", Actions: registrytoken.Pull}})
		if err != nil || len(got.Entries) != 0 || repos.lookup != 0 {
			t.Fatalf("grant %+v lookups %d %v", got, repos.lookup, err)
		}
	})
	t.Run("a repository of another format, being deleted, disabled or absent grants nothing", func(t *testing.T) {
		ask := &fakeAsker{allow: map[string]bool{
			"web/npm-local app read": true, "web/gone app read": true, "web/off app read": true, "web/absent app read": true,
		}}
		got, err := g.Grant(ctx, ask, []registrytoken.Scope{
			{Repo: npm, Name: "app", Actions: registrytoken.Pull},
			{Repo: gone, Name: "app", Actions: registrytoken.Pull},
			{Repo: off, Name: "app", Actions: registrytoken.Pull},
			{Repo: qname.Repo{Project: "web", Key: "absent"}, Name: "app", Actions: registrytoken.Pull},
		})
		if err != nil || len(got.Entries) != 0 {
			t.Fatalf("grant %+v %v", got, err)
		}
	})
	t.Run("the catalog is granted to whoever reached the token service", func(t *testing.T) {
		got, err := g.Grant(ctx, &fakeAsker{}, []registrytoken.Scope{{Catalog: true}})
		if err != nil || !got.Catalog {
			t.Fatalf("grant %+v %v", got, err)
		}
	})
	t.Run("an anonymous caller the decision point will not answer for gets the empty grant; a stale index is the error", func(t *testing.T) {
		got, err := g.Grant(ctx, &fakeAsker{err: authz.Unauthenticated()}, []registrytoken.Scope{{Repo: local, Name: "app", Actions: registrytoken.Pull}})
		if err != nil || len(got.Entries) != 0 {
			t.Fatalf("grant %+v %v", got, err)
		}
		stale := errs.E(errs.Unavailable, "authz_stale", "stale")
		if _, err := g.Grant(ctx, &fakeAsker{err: stale}, []registrytoken.Scope{{Repo: local, Name: "app", Actions: registrytoken.Pull}}); !errors.Is(err, stale) {
			t.Fatalf("err = %v", err)
		}
		repos.err = errs.E(errs.Unavailable, errs.CodeDatabaseUnavailable, "down")
		defer func() { repos.err = nil }()
		ask := &fakeAsker{allow: map[string]bool{"web/docker-local app read": true}}
		if _, err := g.Grant(ctx, ask, []registrytoken.Scope{{Repo: local, Name: "app", Actions: registrytoken.Pull}}); err == nil {
			t.Fatal("a failed lookup granted")
		}
	})
}

// pathAsker is a decision point with one include pattern on paths: a coordinate without a path is refused, as the engine refuses it.
type pathAsker struct{ include string }

func (a pathAsker) PathsAllowed(_ context.Context, _ authz.Coordinate, _ authz.Action, paths []string, classify authz.Classifier) ([]bool, error) {
	out := make([]bool, len(paths))
	for i, p := range paths {
		pkg, _ := classify(p)
		out[i] = pkg != "" && strings.HasPrefix(p, a.include)
	}
	return out, nil
}
