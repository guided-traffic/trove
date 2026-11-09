package deb

import (
	"context"
	"encoding/hex"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

// checksumVerifyIfAvailable is the value of remote.integrity.checksumPolicy that compares the digests an upstream announces and stores
// a file nothing announces a digest for (R-REPO-041).
const checksumVerifyIfAvailable = "verify-if-available"

// BeforeArtifact is the road every file of a remote repository takes before the engine looks at or fetches it (R-DEB-022): a by-hash
// file is announced with the digest its path names, a package file with the digest and size the newest parsed index lists for its
// path. It is the gate of a remote member of a virtual repository as well (R-DEB-027).
func (a remoteAdapter) BeforeArtifact(ctx context.Context, h formats.Host, rq *formats.Request, p formats.Path) (formats.Digests, error) {
	d, _, err := a.BeforeArtifactWith(ctx, h, rq, p, formats.ServeFacts{})
	return d, err
}

// BeforeArtifactWith is [remoteAdapter.BeforeArtifact] for a caller that holds the rows the item lookup of the cached file read
// (have): they are the facts of the gate when they were read for this path's key, and the data port is asked otherwise. The facts
// returned are the ones the serve check is to use (R-DEB-022, R-ARCH-027).
//
// A path no parsed index lists announces no digest: the engine stores the file unverified under the policy verify-if-available (and
// the format counts it, H-625) and refuses it under require with 502 upstream_digest_missing. A path below dists/ that is neither a
// by-hash file nor a patch is no artifact: the release names it.
func (a remoteAdapter) BeforeArtifactWith(ctx context.Context, h formats.Host, rq *formats.Request, p formats.Path, have formats.ServeFacts) (formats.Digests, formats.ServeFacts, error) {
	rp := analyzeRemote(p.Path)
	switch rp.kind {
	case rkByHash:
		sum, err := hex.DecodeString(rp.digest)
		if err != nil {
			return formats.Digests{}, have, errs.E(errs.NotFound, errs.CodeNotFound, "The resource does not exist.")
		}
		if rp.algo == "SHA512" {
			return formats.Digests{SHA512: sum}, have, nil
		}
		return formats.Digests{SHA256: sum}, have, nil
	case rkNoHash:
		return formats.Digests{}, have, errs.E(errs.NotFound, errs.CodeDebNotInRelease, "Only the SHA-256 and SHA-512 by-hash files of an archive are served.")
	case rkContent:
	default:
		return formats.Digests{}, have, nil
	}
	repo := rq.RepoRef()
	key := a.f.ServeFactsKey(repo, p)
	facts := have.DebRemote
	if !have.For(key) {
		st, err := storeOf(h.FormatData(ID))
		if err != nil {
			return formats.Digests{}, have, err
		}
		e, found, err := st.EntryDigest(ctx, repo.ID, p.Path)
		if err != nil {
			return formats.Digests{}, have, err
		}
		facts = formats.ServeDebRemoteFacts{Known: found, SHA256: e.SHA256, Size: e.Size}
	}
	current := formats.ServeFacts{Key: key, DebRemote: facts}
	if facts.Known {
		return formats.Digests{SHA256: facts.SHA256[:]}, current, nil
	}
	if st, ok := rq.Repo.Remote(); ok && st.Integrity.ChecksumPolicy == checksumVerifyIfAvailable && !rq.CacheView {
		a.f.replicaOf(h).met.unverifiedFill(repo.Name.String())
	}
	return formats.Digests{}, current, nil
}
