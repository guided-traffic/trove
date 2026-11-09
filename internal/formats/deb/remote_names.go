package deb

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
)

// ServesName answers the shadowing check of repositories-and-proxy.md R-REPO-048 for a remote member: an APT archive has no document
// per package, so the check reads the rows of the indexes the remote parsed, in the scope of the virtual repository that would serve
// the name (R-DEB-023, Q-DEB-37, Q-REPO-270). The scope is read from the virtual repository's settings: the suites, components and
// architectures it serves; the members a virtual repository merges are served in no other.
//
// served is true when a parsed row of an index in the scope lists the name. known is false while a suite of the scope is neither
// settled by a stored release whose indexes of the scope are all parsed nor recorded absent (Q-DEB-41); the check then asks the
// remote to fetch and parse what is missing through the background path, so a retry decides, and the caller gets
// shadow_check_unavailable until then.
func (remoteAdapter) ServesName(ctx context.Context, h formats.Host, q formats.NameQuery) (served, known bool, err error) {
	st, err := storeOf(h.FormatData(ID))
	if err != nil {
		return false, false, err
	}
	via := SettingsOf(q.ViaSettings)
	scope := rows.NameScope{
		RepoID: q.Remote.ID, Name: q.Name, Suites: via.Suites, Components: via.Components, Architectures: via.Architectures,
	}
	served, known, err = st.ServesName(ctx, scope)
	if err != nil || served || known {
		return served, known, err
	}
	if _, err := st.EnsureParsed(ctx, scope); err != nil {
		return false, false, err
	}
	return false, false, nil
}
