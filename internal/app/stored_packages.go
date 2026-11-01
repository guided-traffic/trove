package app

import (
	"context"
	"fmt"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/formats"
)

// storedPackages answers the check of an endpoint's exposure rules when it names
// no versions (R-EP-016, Q-EP-61): the view adapter of the repository's format
// says what the repository stores of the package, through the format's own data
// port. A format without that ability lists nothing, and the check says so.
type storedPackages struct {
	byFormat map[string]formats.StoredLister
	ports    formats.DataPorts
}

var _ authz.StoredPackages = (*storedPackages)(nil)

// newStoredPackages collects the listers of the given formats.
func newStoredPackages(fs []formats.Format, ports formats.DataPorts) *storedPackages {
	s := &storedPackages{byFormat: map[string]formats.StoredLister{}, ports: ports}
	for _, f := range fs {
		if v := f.View(); v != nil {
			if l, ok := v.(formats.StoredLister); ok {
				s.byFormat[string(f.Descriptor().ID)] = l
			}
		}
	}
	return s
}

// Stored implements [authz.StoredPackages].
func (s *storedPackages) Stored(ctx context.Context, repo authz.StoredRepo, pkg string, limit int) (authz.StoredPackage, error) {
	l, ok := s.byFormat[repo.Format]
	if !ok {
		return authz.StoredPackage{}, nil
	}
	st, err := l.Stored(ctx, s.ports, formats.RepoRef{ID: repo.ID, Key: repo.Key, Class: formats.Class(repo.Class), Format: formats.ID(repo.Format)}, pkg, limit)
	if err != nil {
		return authz.StoredPackage{}, fmt.Errorf("stored versions of %s in %s: %w", pkg, repo.Key, err)
	}
	return authz.StoredPackage{Listed: st.Listed, Versions: st.Versions, Tags: st.Tags}, nil
}
