package files

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
)

func TestRepositoryUploadLimit(t *testing.T) {
	for cfg, want := range map[string]int64{
		`{}`:                                 0,
		`{"storage":{"maxUploadSize":1024}}`: 1024,
		`{"storage":{"quota":"1TiB"}}`:       0,
		``:                                   0,
	} {
		if got, err := maxUpload(repository.Repository{Config: []byte(cfg)}); err != nil || got != want {
			t.Errorf("maxUpload(%s) = %d, %v, want %d", cfg, got, err, want)
		}
	}
	// R-STOR-004: a limit that cannot be decoded refuses the upload, it does not lift it.
	for _, cfg := range []string{
		`{"storage":{"maxUploadSize":-5}}`, `{"storage":{"maxUploadSize":1e9}}`, `{"storage":{"maxUploadSize":"10"}}`,
		`{"storage":{"maxUploadSize":null}}`, `{"storage":{"maxUploadSize":1.5}}`, `not json`,
	} {
		if got, err := maxUpload(repository.Repository{Config: []byte(cfg)}); err == nil {
			t.Errorf("R-STOR-004 maxUpload(%s) = %d, want an error", cfg, got)
		}
	}
}

// access is a decision holder that records every question and answers from its
// fields. Every decision of one request must arrive here: the generic layer has no
// other way to decide.
type access struct {
	allow     bool
	overwrite bool
	decides   []authz.Coordinate
	paths     []string
	recorded  []authz.Coordinate
}

func (a *access) Decide(_ context.Context, c authz.Coordinate) authz.Decision {
	a.decides = append(a.decides, c)
	if !a.allow {
		return authz.Decision{Outcome: authz.DenyHidden}
	}
	return authz.Decision{Outcome: authz.Allow}
}

func (a *access) PathsAllowed(_ context.Context, base authz.Coordinate, act authz.Action, paths []string) ([]bool, error) {
	a.paths = append(a.paths, string(act)+":"+base.Repository)
	out := make([]bool, len(paths))
	for i := range out {
		out[i] = act != authz.Overwrite || a.overwrite
	}
	return out, nil
}

func (a *access) RecordAllowed(_ context.Context, _ meta.Tx, c authz.Coordinate, _ authz.Decision) error {
	a.recorded = append(a.recorded, c)
	return nil
}

// store is an item store that holds nothing: it records the commit and runs the
// callbacks the generic layer handed it, as the real store does in its transaction.
type store struct {
	live   items.Item
	found  bool
	commit *items.CommitSpec
	denied bool
	folder *items.FolderSpec
	del    *items.DeleteSpec
}

func (s *store) Spool(context.Context, io.Reader, items.IngestOptions) (*items.Spooled, error) {
	sp := &blob.Spooled{}
	sp.SHA256[0] = 1
	return sp, nil
}

func (s *store) Commit(ctx context.Context, spec items.CommitSpec) (items.CommitResult, error) {
	s.commit = &spec
	if s.denied && !spec.MayOverwrite {
		return items.CommitResult{}, items.ErrOverwriteDenied
	}
	if spec.Extra != nil {
		if err := spec.Extra(ctx, nil, items.Ref{ID: 9, Path: spec.Path.Path}); err != nil {
			return items.CommitResult{}, err
		}
	}
	return items.CommitResult{Created: true, Item: items.Item{Repository: spec.Repo.Key, Path: spec.Path.Path, Kind: items.KindFile}}, nil
}

func (s *store) CreateFolder(ctx context.Context, spec items.FolderSpec) (items.FolderResult, error) {
	s.folder = &spec
	if spec.Extra != nil {
		if err := spec.Extra(ctx, nil); err != nil {
			return items.FolderResult{}, err
		}
	}
	return items.FolderResult{Created: true}, nil
}

func (s *store) Live(context.Context, items.RepoRef, string) (items.Item, bool, error) {
	return s.live, s.found, nil
}

func (s *store) Open(context.Context, items.Item, *items.Range) (io.ReadCloser, error) {
	return nil, errors.New("not used")
}

func (s *store) Children(ctx context.Context, _ items.RepoRef, _ items.ChildrenQuery, visible items.Visibility) (items.ChildrenPage, error) {
	rows := []items.Child{{Abs: "a.bin", Kind: items.KindFile}, {Abs: "secret/b.bin", Kind: items.KindFile}}
	ok, err := visible(ctx, rows)
	if err != nil {
		return items.ChildrenPage{}, err
	}
	var page items.ChildrenPage
	for i, r := range rows {
		if ok[i] {
			page.Items = append(page.Items, r)
		}
	}
	return page, nil
}

func (s *store) DownloadStats(context.Context, int64) (items.DownloadStats, error) {
	return items.DownloadStats{}, nil
}

func (s *store) Delete(ctx context.Context, spec items.DeleteSpec) (items.DeleteResult, error) {
	s.del = &spec
	if spec.Check != nil {
		if err := spec.Check(ctx, []string{spec.Path + "/x", spec.Path + "/y"}); err != nil {
			return items.DeleteResult{}, err
		}
	}
	if spec.Extra != nil {
		if err := spec.Extra(ctx, nil); err != nil {
			return items.DeleteResult{}, err
		}
	}
	return items.DeleteResult{Done: true}, nil
}

func (s *store) Served(context.Context, items.ServedEvent) {}

type repos map[string]repository.Repository

func (r repos) ByKey(_ context.Context, key string) (repository.Repository, error) {
	if repo, ok := r[key]; ok {
		return repo, nil
	}
	return repository.Repository{}, errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "no repository")
}

func (r repos) List(context.Context) ([]repository.Repository, error) { return nil, nil }

func service(t *testing.T, st *store) *Service {
	t.Helper()
	svc, err := New(Options{Items: st, Repos: repos{
		"generic-local":  {ID: "5", Key: "generic-local", Class: repository.Local, Format: "generic"},
		"generic-remote": {ID: "6", Key: "generic-remote", Class: repository.Remote, Format: "generic"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func caller(a *access) Caller {
	return Caller{Access: a, Subject: authz.Subject{Endpoint: "customers"}, Actor: audit.Actor{Name: "alice"}}
}

func upload(path string) DeployRequest {
	return DeployRequest{Repo: "generic-local", Path: repository.Cleaned{Path: path}, Body: strings.NewReader("x"), ContentLength: 1}
}

func TestGenericLayerDecidesOnTheRequestsAccess(t *testing.T) {
	t.Run("R-AUTHZ-015 a deploy asks the coarse check, the commit-time re-check and the overwrite question of one holder", func(t *testing.T) {
		acc, st := &access{allow: true}, &store{}
		if _, err := service(t, st).Deploy(context.Background(), caller(acc), upload("a/b.bin")); err != nil {
			t.Fatal(err)
		}
		if len(acc.decides) != 2 || acc.decides[0].Actions[0] != authz.Deploy || acc.decides[1].Actions[0] != authz.Deploy {
			t.Errorf("decisions = %+v", acc.decides)
		}
		if len(acc.paths) != 1 || acc.paths[0] != "overwrite:generic-local" {
			t.Errorf("the overwrite question = %v", acc.paths)
		}
	})
	t.Run("R-AUTHZ-015 the decisions that follow the body are made on a new holder, the one before it on the request's", func(t *testing.T) {
		first, second, st := &access{allow: true}, &access{allow: true}, &store{}
		c := caller(first)
		c.Recheck = func(context.Context, string) (Access, error) { return second, nil }
		if _, err := service(t, st).Deploy(context.Background(), c, upload("a/b.bin")); err != nil {
			t.Fatal(err)
		}
		if len(first.decides) != 1 || len(second.decides) != 1 {
			t.Errorf("coarse check on the first holder: %d, commit-time check on the second: %d", len(first.decides), len(second.decides))
		}
		if len(second.paths) != 1 || len(second.recorded) != 1 || len(first.recorded) != 0 {
			t.Errorf("the overwrite question and the recorded write belong to the second holder: %v, %d, %d", second.paths, len(second.recorded), len(first.recorded))
		}
	})
	t.Run("R-AUTHZ-029 the allowed write is recorded in the transaction of the commit, through the holder", func(t *testing.T) {
		acc, st := &access{allow: true}, &store{}
		if _, err := service(t, st).Deploy(context.Background(), caller(acc), upload("a/b.bin")); err != nil {
			t.Fatal(err)
		}
		if len(acc.recorded) != 1 || acc.recorded[0].Path != "a/b.bin" || acc.recorded[0].Kind != authz.KindWrite {
			t.Errorf("recorded = %+v", acc.recorded)
		}
	})
	t.Run("R-STOR-007 the commit is an upload that names the endpoint of the subject and may overwrite only what was decided", func(t *testing.T) {
		acc, st := &access{allow: true, overwrite: true}, &store{}
		if _, err := service(t, st).Deploy(context.Background(), caller(acc), upload("a/b.bin")); err != nil {
			t.Fatal(err)
		}
		c := st.commit
		if c.Origin != items.OriginUpload || c.Endpoint != "customers" || c.Actor.Name != "alice" || !c.MayOverwrite {
			t.Errorf("commit = %+v", c)
		}
		acc2, st2 := &access{allow: true}, &store{}
		if _, err := service(t, st2).Deploy(context.Background(), caller(acc2), upload("a/b.bin")); err != nil {
			t.Fatal(err)
		}
		if st2.commit.MayOverwrite {
			t.Error("a caller without overwrite was allowed to replace content")
		}
	})
	t.Run("R-STOR-007 a path that appeared meanwhile is refused as a decision when the caller holds deploy only", func(t *testing.T) {
		acc, st := &access{allow: true}, &store{denied: true}
		_, err := service(t, st).Deploy(context.Background(), caller(acc), upload("a/b.bin"))
		if e, ok := errs.As(err); !ok || e.Kind != errs.Forbidden {
			t.Fatalf("err = %v", err)
		}
		last := acc.decides[len(acc.decides)-1]
		if len(last.Actions) != 2 || last.Actions[1] != authz.Overwrite {
			t.Errorf("the refusal was not decided as an overwrite: %+v", last)
		}
	})
	t.Run("R-AUTHZ-016 a hidden repository is refused before it is looked up", func(t *testing.T) {
		acc, st := &access{allow: false}, &store{}
		_, err := service(t, st).Deploy(context.Background(), caller(acc), upload("a/b.bin"))
		if e, ok := errs.As(err); !ok || e.Kind != errs.NotFound {
			t.Fatalf("err = %v", err)
		}
		if st.commit != nil {
			t.Error("the store was reached")
		}
	})
	t.Run("R-REPO-005 a remote repository does not accept a deploy", func(t *testing.T) {
		acc, st := &access{allow: true}, &store{}
		req := upload("a/b.bin")
		req.Repo = "generic-remote"
		_, err := service(t, st).Deploy(context.Background(), caller(acc), req)
		if e, ok := errs.As(err); !ok || e.Kind != errs.MethodNotAllowed {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-STOR-017 a listing keeps the entries the holder allows to read", func(t *testing.T) {
		acc, st := &access{allow: true}, &store{}
		svc := service(t, st)
		acc2 := &readAccess{access: acc, hide: "secret/"}
		page, err := svc.Children(context.Background(), caller2(acc2), "generic-local", ChildrenQuery{Folders: true, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || page.Items[0].Abs != "a.bin" {
			t.Errorf("page = %+v", page.Items)
		}
	})
	t.Run("R-STOR-018 a delete is checked on every path of the subtree before the write is recorded, and one refusal stops it", func(t *testing.T) {
		acc, st := &access{allow: true, overwrite: true}, &store{found: true, live: items.Item{Kind: items.KindFolder}}
		res, err := service(t, st).Delete(context.Background(), caller(acc), DeleteRequest{Repo: "generic-local", Path: "d"})
		if err != nil || !res.Done {
			t.Fatalf("%+v, %v", res, err)
		}
		if len(acc.paths) != 1 || acc.paths[0] != "delete:generic-local" || len(acc.recorded) != 1 {
			t.Errorf("paths %v, recorded %v", acc.paths, acc.recorded)
		}
		denied := &denyDelete{access: &access{allow: true}}
		st2 := &store{found: true, live: items.Item{Kind: items.KindFolder}}
		_, err = service(t, st2).Delete(context.Background(), caller2(denied), DeleteRequest{Repo: "generic-local", Path: "d"})
		if e, ok := errs.As(err); !ok || e.Kind != errs.Forbidden {
			t.Fatalf("err = %v", err)
		}
		if len(denied.recorded) != 0 {
			t.Error("a refused delete recorded an allowed write")
		}
	})
}

func caller2(a Access) Caller {
	return Caller{Access: a, Subject: authz.Subject{Endpoint: "customers"}}
}

// readAccess hides the paths below a prefix from PathsAllowed.
type readAccess struct {
	*access
	hide string
}

func (r *readAccess) PathsAllowed(_ context.Context, _ authz.Coordinate, _ authz.Action, paths []string) ([]bool, error) {
	out := make([]bool, len(paths))
	for i, p := range paths {
		out[i] = !strings.HasPrefix(p, r.hide)
	}
	return out, nil
}

// denyDelete refuses delete on every path.
type denyDelete struct{ *access }

func (d *denyDelete) PathsAllowed(_ context.Context, _ authz.Coordinate, _ authz.Action, paths []string) ([]bool, error) {
	return make([]bool, len(paths)), nil
}
