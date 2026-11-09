package deb

import (
	"context"
	"crypto/sha256"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/formats/deb/release"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/mirror/discover"
	"github.com/guided-traffic/trove/internal/openpgp"
	"github.com/guided-traffic/trove/internal/qname"
)

// systemHousekeeping is the actor of the removals the housekeeping makes: a removal that no request asked for.
var systemHousekeeping = audit.Actor{Kind: "system", Name: "removal"}

// Remover removes files in the transaction of the caller; *items.Store implements it.
type Remover interface {
	RemoveIn(ctx context.Context, tx meta.Tx, spec items.RemoveSpec) error
}

// removalBatch is the number of by-hash files one transaction removes.
const removalBatch = 200

// RunIndex is the handler of the job deb-index for a remote repository (R-DEB-021, R-DEB-020, Q-DEB-33, Q-DEB-42), under its key
// index:<repository id>: it verifies again, from the stored bytes, the releases a change of the keys left unverified, and it removes
// what no release names any more. It deletes nothing the mirror keeps: package files are never touched, only the by-hash files of
// index generations that neither the stored release nor a release stored within deb.by_hash_keep names, and the rows that remember
// those releases.
func (j *RemoteJobs) RunIndex(ctx context.Context, run *jobs.Run) error {
	id, err := strconv.ParseInt(run.TargetKey, 10, 64)
	if err != nil {
		return jobs.Permanent(err)
	}
	info, ok, err := j.repo(ctx, id)
	if err != nil || !ok {
		return err
	}
	if info.Class != "remote" || info.Format != "deb" {
		return nil
	}
	if info.Keys != "" {
		up, err := j.Background.Upstream(ctx, id, run.ID)
		if err != nil {
			return err
		}
		if err := j.reverify(ctx, up, info, id); err != nil {
			return err
		}
	}
	return j.housekeep(ctx, info, id)
}

// reverify verifies the releases that no key verifies now against the keys the remote has now (R-DEB-020, Q-DEB-42). The stored
// bytes are read through the engine: while they are fresh no request is sent. A release no key verifies stays unverified; its next
// revalidation is sent without validators and the fill verifies the upstream's body.
func (j *RemoteJobs) reverify(ctx context.Context, up discover.Upstream, info repoInfo, repoID int64) error {
	certs, err := info.certs()
	if err != nil {
		j.log().Warn("the upstream keys of a remote do not read", "repository", info.Project+"/"+info.Key, "error", err)
		return nil
	}
	todo, err := j.Store.Unverified(ctx, repoID)
	if err != nil {
		return err
	}
	for _, d := range todo {
		dir, doc := d[0], d[1]
		body, found, err := storedDocument(ctx, up, join(dir, doc))
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		if doc == rows.DocRelease {
			if err := j.verifyPair(ctx, up, info, repoID, dir, body); err != nil {
				return err
			}
			continue
		}
		now := j.now()
		rel, signer, err := release.Signed(body, certs, now)
		if err == nil {
			err = rel.CheckSuite(strings.TrimPrefix(dir, "dists/"), now)
		}
		if err != nil {
			j.log().Info("a stored release is not verified by the keys of the remote", "repository", info.Project+"/"+info.Key, "dir", dir,
				"reason", openpgp.Reason(err))
			continue
		}
		if _, err := j.Store.RecordVerified(ctx, rel.Record(repoID, dir, rows.DocInRelease, sha256.Sum256(body), signer, true)); err != nil {
			return err
		}
	}
	return nil
}

// housekeep forgets the files of releases replaced longer ago than deb.by_hash_keep and removes the by-hash files no remembered
// release names (R-DEB-021). A by-hash file younger than the keep period is left whatever the rows say: it may belong to a release
// that a replica is about to record.
func (j *RemoteJobs) housekeep(ctx context.Context, info repoInfo, repoID int64) error {
	before := j.now().Add(-j.ByHashKeep)
	if err := j.Store.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		_, err = qs.DebRemotePruneFiles(ctx, q.DebRemotePruneFilesParams{RepoID: repoID, Before: stamp(before)})
		return err
	}); err != nil {
		return err
	}
	var rm Remover
	if j.Items != nil {
		rm = j.Items()
	}
	if rm == nil {
		return nil
	}
	ref := items.RepoRef{ID: repoID, Name: qname.Repo{Project: info.Project, Key: info.Key}, Class: "remote", Format: "deb"}
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var paths []string
		if err := j.Store.o.DB.InTx(ctx, func(tx meta.Tx) error {
			qs, err := queries(tx)
			if err != nil {
				return err
			}
			paths, err = qs.DebRemoteOrphanByHash(ctx, q.DebRemoteOrphanByHashParams{
				RepoID: repoID, After: after, Before: stamp(before), Batch: removalBatch,
			})
			return err
		}); err != nil {
			return err
		}
		if len(paths) == 0 {
			return nil
		}
		if err := j.Store.o.DB.InTx(ctx, func(tx meta.Tx) error {
			return rm.RemoveIn(ctx, tx, items.RemoveSpec{Repo: ref, Paths: paths, Actor: systemHousekeeping})
		}); err != nil {
			return err
		}
		after = paths[len(paths)-1]
	}
}

// StoredReader is the optional capability of the upstream of a background run to read a metadata document as the cache holds it, with no
// request to the upstream (formathost's background upstream).
type StoredReader interface {
	StoredDocument(ctx context.Context, path, variant string) (doc []byte, found bool, err error)
}

// storedDocument reads a document from the cache when the upstream can, and through the engine otherwise.
func storedDocument(ctx context.Context, up discover.Upstream, path string) ([]byte, bool, error) {
	if r, ok := up.(StoredReader); ok {
		return r.StoredDocument(ctx, path, "")
	}
	return up.Document(ctx, path, "")
}
