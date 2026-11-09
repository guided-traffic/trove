package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"github.com/guided-traffic/trove/internal/errs"
	debdata "github.com/guided-traffic/trove/internal/formatdata/deb"
	debformat "github.com/guided-traffic/trove/internal/formats/deb"
	"github.com/guided-traffic/trove/internal/formats/deb/index"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
)

// debVirtualRepos reads a virtual Debian repository from the repository model for the build of its indexes (R-DEB-025). The members and
// the packages they may serve are those of stored configuration, never of a caller (Q-DEB-19).
type debVirtualRepos struct {
	a  *App
	st *debdata.Store
}

var _ debdata.VirtualRepos = debVirtualRepos{}

func (r debVirtualRepos) repo(ctx context.Context, id int64) (repository.Repository, bool, error) {
	rd := part[repositoryArea](r.a.parts).reader
	if rd == nil {
		return repository.Repository{}, false, nil
	}
	repo, err := rd.ByID(ctx, id)
	if err != nil {
		if e, ok := errs.As(err); ok && e.Kind == errs.NotFound {
			return repository.Repository{}, false, nil
		}
		return repository.Repository{}, false, err
	}
	if repo.Format != string(debformat.ID) || repo.Class != repository.Virtual {
		return repository.Repository{}, false, nil
	}
	return repo, true, nil
}

func debSettings(config []byte) debformat.Settings {
	var doc struct {
		Deb json.RawMessage `json:"deb"`
	}
	_ = json.Unmarshal(config, &doc) // a document the service did not write yields the defaults
	return debformat.SettingsOf(doc.Deb)
}

// Virtual implements [debdata.VirtualRepos].
func (r debVirtualRepos) Virtual(ctx context.Context, id int64) (debdata.VirtualRepo, bool, error) {
	repo, ok, err := r.repo(ctx, id)
	if err != nil || !ok {
		return debdata.VirtualRepo{}, false, err
	}
	routing := part[virtualArea](r.a.parts).routing
	if routing == nil {
		return debdata.VirtualRepo{}, false, fmt.Errorf("deb: this replica has no routing for virtual repositories")
	}
	route, err := routing.Route(ctx, repo, repository.RouteRequest{})
	if err != nil {
		return debdata.VirtualRepo{}, false, err
	}
	set := debSettings(repo.Config)
	label := set.Label
	if label == "" {
		label = repo.Name.Key
	}
	archs := append([]string(nil), set.Architectures...)
	out := debdata.VirtualRepo{
		Ref:     items.RepoRef{ID: repo.NumericID(), Name: repo.Name, Class: string(repo.Class), Format: repo.Format},
		Project: repo.Name.Project, Key: repo.Name.Key,
		Settings: index.Settings{Origin: set.Origin, Label: label, Architectures: archs, XZ: containsString(set.Compression, "xz")},
		Suites:   set.Suites, Components: set.Components, Architectures: archs,
	}
	claims, err := r.st.ClaimsFingerprint(ctx)
	if err != nil {
		return debdata.VirtualRepo{}, false, err
	}
	fp := "v" + strconv.FormatInt(repo.Version, 10) + "|" + claims
	for _, m := range route.Members {
		vm := debdata.VirtualMember{
			ID: m.NumericID(), Key: m.Name.Key, Class: string(m.Class),
			Ref: items.RepoRef{ID: m.NumericID(), Name: m.Name, Class: string(m.Class), Format: m.Format},
		}
		if rs, ok := m.Remote(); ok {
			vm.URL = rs.URL
		}
		fp += "|" + m.Name.Key + ":" + strconv.FormatInt(m.Version, 10)
		out.Members = append(out.Members, vm)
	}
	out.Fingerprint = fp
	return out, true, nil
}

// Allowed implements [debdata.VirtualRepos]: the keys of the members that serve a package name.
func (r debVirtualRepos) Allowed(ctx context.Context, id int64, name string) (map[string]bool, error) {
	repo, ok, err := r.repo(ctx, id)
	if err != nil || !ok {
		return nil, err
	}
	route, err := part[virtualArea](r.a.parts).routing.Route(ctx, repo, repository.RouteRequest{Name: name})
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	if !route.Admitted {
		return out, nil
	}
	for _, m := range route.Members {
		out[m.Name.Key] = true
	}
	return out, nil
}

// debMemberIndexes reads the generated index files of a local member through the item store.
type debMemberIndexes struct{ a *App }

var _ debdata.MemberIndexes = debMemberIndexes{}

// Open implements [debdata.MemberIndexes].
func (m debMemberIndexes) Open(ctx context.Context, repo items.RepoRef, path string) (io.ReadCloser, bool, error) {
	s, ok := m.a.itemStore.get()
	if !ok {
		return nil, false, fmt.Errorf("deb: this replica has no item store")
	}
	it, found, err := s.Live(ctx, repo, path)
	if err != nil || !found {
		return nil, false, err
	}
	rc, err := s.Open(ctx, it, nil)
	if err != nil {
		return nil, false, err
	}
	return rc, true, nil
}

// debRepositoryHook enqueues the builds a change of a repository calls for, in the transaction of the change and without delay
// (R-DEB-026): the change of a virtual repository (its members, pin rules or settings), and the change of a member, which can change what
// the virtual repositories that list it may merge (its patterns, its upstream keys).
type debRepositoryHook struct{}

var _ repository.Hooks = debRepositoryHook{}

// OnUpdated implements [repository.Hooks].
func (debRepositoryHook) OnUpdated(ctx context.Context, tx meta.Tx, _ repository.Caller, _, after repository.Repository) error {
	st := debHook.store.Load()
	if after.Format != string(debformat.ID) || st == nil {
		return nil
	}
	ids := []int64{after.NumericID()}
	if after.Class != repository.Virtual {
		var err error
		if ids, err = st.VirtualsOfMember(ctx, after.NumericID()); err != nil {
			return err
		}
	}
	for _, id := range ids {
		if err := st.EnqueueBuild(ctx, tx, id, 0); err != nil {
			return err
		}
	}
	return nil
}

// OnDelete implements [repository.Hooks]: the rows of the repository go with it.
func (debRepositoryHook) OnDelete(context.Context, meta.Tx, repository.Caller, repository.Repository) error {
	return nil
}
