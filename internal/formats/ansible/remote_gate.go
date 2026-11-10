package ansible

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
)

// BeforeArtifact is the road every archive of a remote repository takes (R-ANS-018, Q-ANS-13): it returns the SHA-256 the version
// document announced for the version, which the engine verifies the bytes against whatever integrity.checksumPolicy says. The digest
// is the one recorded first for the version while its archive is cached (RecordAnnounced). A version whose document was never stored
// is revalidated first, in one flight whichever replicas ask, because the announced digest decides the artifact and a lockfile
// install asks for archives without their documents; a version the upstream does not have is 404. The cache view never contacts the
// upstream. A role archive announces no digest (R-ANS-020, H-840): nothing is returned, and the checksum policy decides.
func (remoteAdapter) BeforeArtifact(ctx context.Context, h formats.Host, rq *formats.Request, p formats.Path) (formats.Digests, error) {
	t, err := parseTarget(p)
	if err != nil {
		return formats.Digests{}, err
	}
	switch t.kind {
	case kindRoleArchive:
		return formats.Digests{}, nil
	case kindArchive:
	default:
		return formats.Digests{}, missing()
	}
	st, err := storeOf(h.FormatData(ID))
	if err != nil {
		return formats.Digests{}, err
	}
	repoID := rq.RepoRef().ID
	name := t.pkg()
	a, found, err := st.Announced(ctx, repoID, name, t.version)
	if err != nil {
		return formats.Digests{}, err
	}
	if !found && !rq.CacheView {
		if _, _, err := h.Remote().EnsureCurrent(ctx, rq, formats.Path{Path: VersionDocumentPath(t.ns, t.name, t.version)}, documentVariant); err != nil {
			return formats.Digests{}, err
		}
		if a, found, err = st.Announced(ctx, repoID, name, t.version); err != nil {
			return formats.Digests{}, err
		}
	}
	if !found {
		return formats.Digests{}, missing()
	}
	return announcedDigests(a), nil
}

// announcedDigests is what R-ANS-018 hands to the integrity check.
func announcedDigests(a rows.Announced) formats.Digests {
	return formats.Digests{SHA256: append([]byte(nil), a.SHA256[:]...)}
}
