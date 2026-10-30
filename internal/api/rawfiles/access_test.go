package rawfiles

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/files"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
)

// decider hands out a new index at every Begin and records which index each
// question was asked on. A request that took its index once asks every question
// of that one; a request that began again would show two.
type decider struct {
	authz.Decider // the questions a /files/ request does not ask

	mu      sync.Mutex
	begins  int
	issued  []*authz.Index
	asked   []*authz.Index
	keys    [][]string
	recordN int
}

func (d *decider) Begin(_ context.Context, _ authz.Subject, keys ...string) (*authz.Index, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.begins++
	idx := new(authz.Index)
	d.issued = append(d.issued, idx)
	d.keys = append(d.keys, keys)
	return idx, nil
}

func (d *decider) ask(idx *authz.Index) {
	d.mu.Lock()
	d.asked = append(d.asked, idx)
	d.mu.Unlock()
}

func (d *decider) DecideOn(_ context.Context, idx *authz.Index, _ authz.Subject, _ authz.Coordinate) authz.Decision {
	d.ask(idx)
	return authz.Decision{Outcome: authz.Allow}
}

func (d *decider) PathsAllowedOn(_ context.Context, idx *authz.Index, _ authz.Subject, _ authz.Coordinate, _ authz.Action, paths []string, _ authz.Classifier) ([]bool, error) {
	d.ask(idx)
	return make([]bool, len(paths)), nil
}

func (d *decider) RecordAllowed(context.Context, meta.Tx, authz.Subject, authz.Coordinate, authz.Decision) error {
	d.mu.Lock()
	d.recordN++
	d.mu.Unlock()
	return nil
}

// store serves one stored file and accepts every commit.
type store struct {
	file items.Item
	body []byte
}

func (s *store) Spool(context.Context, io.Reader, items.IngestOptions) (*items.Spooled, error) {
	return &blob.Spooled{}, nil
}

func (s *store) Commit(ctx context.Context, spec items.CommitSpec) (items.CommitResult, error) {
	if spec.Extra != nil {
		if err := spec.Extra(ctx, nil, items.Ref{ID: 1, Path: spec.Path.Path}); err != nil {
			return items.CommitResult{}, err
		}
	}
	return items.CommitResult{Created: true, Item: items.Item{Repository: spec.Repo.Key, Path: spec.Path.Path, Kind: items.KindFile}}, nil
}

func (s *store) CreateFolder(context.Context, items.FolderSpec) (items.FolderResult, error) {
	return items.FolderResult{}, nil
}

func (s *store) Live(context.Context, items.RepoRef, string) (items.Item, bool, error) {
	return s.file, s.file.Path != "", nil
}

func (s *store) Open(context.Context, items.Item, *items.Range) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.body)), nil
}

func (s *store) Children(context.Context, items.RepoRef, items.ChildrenQuery, items.Visibility) (items.ChildrenPage, error) {
	return items.ChildrenPage{}, nil
}

func (s *store) DownloadStats(context.Context, int64) (items.DownloadStats, error) {
	return items.DownloadStats{}, nil
}

func (s *store) Delete(context.Context, items.DeleteSpec) (items.DeleteResult, error) {
	return items.DeleteResult{Done: true}, nil
}

func (s *store) Served(context.Context, items.ServedEvent) {}

type repos struct{}

func (repos) ByKey(_ context.Context, key string) (repository.Repository, error) {
	if key != "generic-local" {
		return repository.Repository{}, errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "no repository")
	}
	return repository.Repository{ID: "5", Key: key, Class: repository.Local, Format: "generic"}, nil
}

func (repos) List(context.Context) ([]repository.Repository, error) { return nil, nil }

func handler(t *testing.T, d *decider, st *store) *Handler {
	t.Helper()
	svc, err := files.New(files.Options{Items: st, Repos: repos{}})
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{Service: svc, Decider: d})
}

func TestOneIndexPerRequest(t *testing.T) {
	var sum blob.Digest
	sum[0] = 1
	file := items.Item{ID: 3, RepoID: 5, Repository: "generic-local", Path: "a.bin", Kind: items.KindFile, Size: 3, Digest: sum, SHA512: make([]byte, 64)}

	t.Run("R-AUTHZ-015 a download begins one index, with the repository key, and decides on it", func(t *testing.T) {
		d := &decider{}
		rec := httptest.NewRecorder()
		handler(t, d, &store{file: file, body: []byte("abc")}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/files/generic-local/a.bin", nil))
		if rec.Code != http.StatusOK || rec.Body.String() != "abc" {
			t.Fatalf("status %d, body %q", rec.Code, rec.Body.String())
		}
		if d.begins != 1 || len(d.keys[0]) != 1 || d.keys[0][0] != "generic-local" {
			t.Errorf("begins %d, keys %v", d.begins, d.keys)
		}
		if len(d.asked) == 0 {
			t.Fatal("no decision was asked")
		}
		for _, idx := range d.asked {
			if idx != d.issued[0] {
				t.Error("a decision was asked on another index than the request began with")
			}
		}
	})

	t.Run("R-AUTHZ-015 an upload decides before its body on the request's index and after it on a new one", func(t *testing.T) {
		d := &decider{}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/files/generic-local/new/b.bin", strings.NewReader("xyz"))
		handler(t, d, &store{}).ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		if d.begins != 2 || d.keys[1][0] != "generic-local" {
			t.Fatalf("begins %d, keys %v: the request begins once and its commit-time re-check once, with the key", d.begins, d.keys)
		}
		// The coarse check is the first question; the re-check, the overwrite question and
		// the recorded write follow the body.
		if len(d.asked) != 3 || d.asked[0] != d.issued[0] || d.asked[1] != d.issued[1] || d.asked[2] != d.issued[1] {
			t.Errorf("the questions were not asked as: coarse on the first index, re-check and overwrite on the second (%d asked)", len(d.asked))
		}
		if d.recordN != 1 {
			t.Errorf("the allowed write was recorded %d times", d.recordN)
		}
	})

	t.Run("R-AUTHZ-015 a second request begins an index of its own", func(t *testing.T) {
		d := &decider{}
		h := handler(t, d, &store{file: file, body: []byte("abc")})
		for range 2 {
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/files/generic-local/a.bin", nil))
		}
		if d.begins != 2 || d.issued[0] == d.issued[1] {
			t.Errorf("begins %d", d.begins)
		}
	})

	t.Run("R-ARCH-024 a replica without the authz area answers 503 instead of deciding without a decision point", func(t *testing.T) {
		svc, _ := files.New(files.Options{Items: &store{}, Repos: repos{}})
		rec := httptest.NewRecorder()
		New(Options{Service: svc}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/files/generic-local/a.bin", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status %d", rec.Code)
		}
	})
}
