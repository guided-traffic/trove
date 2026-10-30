package v1

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"

	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/virtual"
)

// fakeExplainer records what the operation hands the engine.
type fakeExplainer struct {
	caller virtual.Caller
	repo   repository.Repository
	d      virtual.Descriptor
	out    virtual.Explanation
	err    error
}

func (f *fakeExplainer) Explain(_ context.Context, c virtual.Caller, v repository.Repository, d virtual.Descriptor) (virtual.Explanation, error) {
	f.caller, f.repo, f.d = c, v, d
	if f.out.Repository == "" {
		return virtual.Explanation{Repository: v.Key, Descriptor: virtual.ExplainedPath{Path: d.Path, Kind: string(d.Kind)}, Members: []virtual.MemberDecision{}}, f.err
	}
	return f.out, f.err
}

// fakeReader serves repositories by key.
type fakeReader struct {
	repos map[string]repository.Repository
}

func (r fakeReader) ByKey(_ context.Context, key string) (repository.Repository, error) {
	v, ok := r.repos[key]
	if !ok {
		return repository.Repository{}, errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "no repository has the key "+key)
	}
	return v, nil
}

func (r fakeReader) List(context.Context) ([]repository.Repository, error) { return nil, nil }

func resolutionSetup(t *testing.T, val *apitest.Validator, acc Access, ex *fakeExplainer, repos ...repository.Repository) setup {
	t.Helper()
	byKey := map[string]repository.Repository{}
	for _, r := range repos {
		byKey[r.Key] = r
	}
	s := newSetup(t, func(o *Options) {
		o.Access = acc
		o.API.Config = apiConfigForTest()
		o.Repositories = RepositoryServices{Reader: fakeReader{byKey}, Authz: cacheDecider{}}
		if ex != nil {
			o.Resolution = ex
		}
	}, authed)
	s.handler = val.Handler(t, s.handler)
	return s
}

func resolutionURL(key, path string) string {
	return "/api/v1/repositories/" + key + "/resolution?path=" + url.QueryEscape(path)
}

func TestResolutionAPI(t *testing.T) {
	val := apitest.New(t, EmbeddedYAML())
	virtualRepo := repository.Repository{ID: "4", Key: "all", Class: repository.Virtual, Format: "generic", Lifecycle: repository.Active}

	t.Run("R-REPO-019 the operation declares read on the virtual repository of the path", func(t *testing.T) {
		acc := &actionAccess{}
		ex := &fakeExplainer{out: virtual.Explanation{
			Repository: "all", Descriptor: virtual.ExplainedPath{Path: "a/b.txt", Kind: "artifact"},
			Members: []virtual.MemberDecision{{Key: "local", Class: "local", Decision: "hit"}},
		}}
		s := resolutionSetup(t, val, acc, ex, virtualRepo)
		w := s.do("GET", resolutionURL("all", "a/b.txt"), "", nil)
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		c := acc.coordinate
		if c.Repository != "all" || len(c.Actions) != 1 || c.Actions[0] != authz.Read {
			t.Fatalf("coordinate = %+v", c)
		}
		var got virtual.Explanation
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Repository != "all" || len(got.Members) != 1 || got.Members[0].Decision != "hit" {
			t.Fatalf("body = %s (%v)", w.Body, err)
		}
	})

	t.Run("R-REPO-019 the engine gets the descriptor of the path and the repository from the snapshot", func(t *testing.T) {
		ex := &fakeExplainer{}
		s := resolutionSetup(t, val, &actionAccess{}, ex, virtualRepo)
		if w := s.do("GET", resolutionURL("all", "docs/readme.txt"), "", nil); w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		if ex.repo.Key != "all" || ex.d.Path != "docs/readme.txt" || ex.d.Format != "generic" || ex.d.Kind == "" {
			t.Fatalf("repo %q descriptor %+v", ex.repo.Key, ex.d)
		}
	})

	t.Run("R-REPO-019 a repository that is not virtual answers 400 not_applicable", func(t *testing.T) {
		local := repository.Repository{ID: "1", Key: "files", Class: repository.Local, Format: "generic", Lifecycle: repository.Active}
		s := resolutionSetup(t, val, &actionAccess{}, &fakeExplainer{}, local)
		problem(t, s.do("GET", resolutionURL("files", "x"), "", nil), 400, "not_applicable")
	})

	t.Run("R-REPO-019 a missing or invalid path is 400", func(t *testing.T) {
		s := resolutionSetup(t, val, &actionAccess{}, &fakeExplainer{}, virtualRepo)
		if w := s.do("GET", "/api/v1/repositories/all/resolution", "", nil); w.Code != 400 {
			t.Fatalf("no path: %d %s", w.Code, w.Body)
		}
		problem(t, s.do("GET", resolutionURL("all", "a/../b"), "", nil), 400, "invalid_path")
	})

	t.Run("R-REPO-019 an unknown repository is 404 and a replica without the engine is 503", func(t *testing.T) {
		s := resolutionSetup(t, val, &actionAccess{}, &fakeExplainer{}, virtualRepo)
		problem(t, s.do("GET", resolutionURL("ghost", "x"), "", nil), 404, "repository_not_found")
		s = resolutionSetup(t, val, &actionAccess{}, nil, virtualRepo)
		problem(t, s.do("GET", resolutionURL("all", "x"), "", nil), 503, "unavailable")
	})

	t.Run("R-REPO-019 the refusal of the engine reaches the caller", func(t *testing.T) {
		s := resolutionSetup(t, val, &actionAccess{}, &fakeExplainer{err: errs.E(errs.Unavailable, errs.CodeUnavailable, "later")}, virtualRepo)
		problem(t, s.do("GET", resolutionURL("all", "x"), "", nil), 503, "unavailable")
	})

	t.Run("R-API-002 R-REPO-019 the answers were validated against the document", func(t *testing.T) {
		if missing := val.Uncovered("getRepositoryResolution"); len(missing) > 0 {
			t.Fatalf("not reached: %v", missing)
		}
	})
}
