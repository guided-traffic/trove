package ansible

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// remoteStore is the group of the data port that holds the digests a remote's version documents announce (R-ANS-018): the statements
// are internal/meta/postgres/queries/ansible_remote.sql, the table is ansible_upstream_versions (migration 00070).
type remoteStore struct{ *group }

// PutCachedVersion implements ansible.RemoteStore: the row of a cached version, without the event of a publish.
func (s remoteStore) PutCachedVersion(ctx context.Context, tx meta.Tx, v rows.VersionRow) error {
	qs, err := txQueries(tx)
	if err != nil {
		return err
	}
	deps := v.Dependencies
	if deps == nil {
		deps = map[string]string{}
	}
	depsJSON, err := json.Marshal(deps)
	if err != nil {
		return err
	}
	if len(v.CollectionInfo) == 0 {
		v.CollectionInfo = json.RawMessage(`{}`)
	}
	return qs.AnsibleUpsertVersion(ctx, q.AnsibleUpsertVersionParams{
		RepoID: v.RepoID, Name: v.Name, Version: v.Version, SortKey: v.SortKey, ItemID: v.ItemID, Sha256: v.SHA256[:], Size: v.Size,
		RequiresAnsible: v.RequiresAnsible, Dependencies: depsJSON, CollectionInfo: v.CollectionInfo, PublishedBy: noActor,
		PublishedAt: stamp(v.PublishedAt),
	})
}

// RecordAnnounced records the digests of the version documents a fill stores, in the fill's transaction (the cache entry of each
// document is locked by the engine, so two fills of one document do not interleave). The first digest recorded for a version stays
// while the version's archive is cached; the rows whose offered digest was not taken are returned as recorded, and the event
// proxy.upstream_digest_changed is queued for each (R-ANS-018, Q-ANS-13). A version whose archive is not cached follows the
// document: nothing is protected, and the digest of the new document is the one a fetch is verified against.
func (s remoteStore) RecordAnnounced(ctx context.Context, tx meta.Tx, repoID int64, announced []rows.Announced) ([]rows.Announced, error) {
	if len(announced) == 0 {
		return nil, nil
	}
	qs, err := txQueries(tx)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(announced))
	versions := make([]string, len(announced))
	sums := make([][]byte, len(announced))
	sizes := make([]int64, len(announced))
	requires := make([]string, len(announced))
	for i, a := range announced {
		names[i], versions[i], sums[i], sizes[i], requires[i] = a.Name, a.Version, a.SHA256[:], a.Size, a.RequiresAnsible
	}
	if err := qs.AnsibleUpstreamRecord(ctx, q.AnsibleUpstreamRecordParams{
		RepoID: repoID, Names: names, Versions: versions, Sha256s: sums, Sizes: sizes, Requires: requires,
	}); err != nil {
		return nil, err
	}
	have, err := qs.AnsibleUpstreamRead(ctx, q.AnsibleUpstreamReadParams{RepoID: repoID, Names: names, Versions: versions})
	if err != nil {
		return nil, err
	}
	recorded := make(map[[2]string]rows.Announced, len(have))
	for _, h := range have {
		recorded[[2]string{h.Name, h.Version}] = announcedOf(repoID, h.Name, h.Version, h.Sha256, h.Size, h.RequiresAnsible, h.RecordedAt.Time)
	}
	var changed []rows.Announced
	for _, a := range announced {
		if r, ok := recorded[[2]string{a.Name, a.Version}]; ok && r.SHA256 != a.SHA256 {
			changed = append(changed, r)
			s.digestChanged(ctx, qs, repoID, r, a)
		}
	}
	return changed, nil
}

// digestChanged queues the security event of a digest that stayed: the document announced another SHA-256 for a version whose
// archive is cached. The event is of the asynchronous kind, so it never fails the fill.
func (s remoteStore) digestChanged(ctx context.Context, qs *q.Queries, repoID int64, kept, offered rows.Announced) {
	refs, err := qs.AnsibleRepositoryRef(ctx, repoID)
	if err != nil || len(refs) == 0 {
		return
	}
	ns, name, _ := strings.Cut(kept.Name, ".")
	path := "collections/" + ns + "/" + name + "/" + ns + "-" + name + "-" + kept.Version + ".tar.gz"
	details, err := json.Marshal(map[string]any{
		"path":         path,
		"cachedSha256": hex.EncodeToString(kept.SHA256[:]),
		"announced":    map[string]string{"sha256": hex.EncodeToString(offered.SHA256[:])},
	})
	if err != nil {
		return
	}
	ev := audit.Event{
		Type: audit.EventProxyUpstreamDigestChanged, Outcome: audit.Failure, Actor: audit.Actor{Kind: "system", Name: "proxy"},
		Subject: audit.Subject{Kind: "repository", ID: strconv.FormatInt(repoID, 10), Name: refs[0]}, Repository: refs[0], Path: path,
		Details: details,
	}
	if s.o.Source != nil {
		ev.Source = s.o.Source(ctx)
	}
	if s.o.Actor != nil {
		if a := s.o.Actor(ctx); a.Kind != "" {
			ev.Actor = a
		}
	}
	s.o.Recorder.Record(ctx, ev)
}

// Announced reads the digest recorded for a version.
func (s remoteStore) Announced(ctx context.Context, repoID int64, name, version string) (rows.Announced, bool, error) {
	var out rows.Announced
	found := false
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		found = false
		qs, err := txQueries(tx)
		if err != nil {
			return err
		}
		rs, err := qs.AnsibleUpstreamGet(ctx, q.AnsibleUpstreamGetParams{RepoID: repoID, Name: name, Version: version})
		if err != nil || len(rs) == 0 {
			return err
		}
		r := rs[0]
		out, found = announcedOf(repoID, r.Name, r.Version, r.Sha256, r.Size, r.RequiresAnsible, r.RecordedAt.Time), true
		return nil
	})
	return out, found, err
}

// AnnouncedNames lists the versions recorded for a collection name.
func (s remoteStore) AnnouncedNames(ctx context.Context, repoID int64, name string) ([]rows.Announced, error) {
	var out []rows.Announced
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		out = nil
		qs, err := txQueries(tx)
		if err != nil {
			return err
		}
		rs, err := qs.AnsibleUpstreamByName(ctx, q.AnsibleUpstreamByNameParams{RepoID: repoID, Name: name})
		if err != nil {
			return err
		}
		for _, r := range rs {
			out = append(out, announcedOf(repoID, r.Name, r.Version, r.Sha256, r.Size, r.RequiresAnsible, r.RecordedAt.Time))
		}
		return nil
	})
	return out, err
}

func announcedOf(repoID int64, name, version string, sum []byte, size int64, requires string, at time.Time) rows.Announced {
	a := rows.Announced{RepoID: repoID, Name: name, Version: version, Size: size, RequiresAnsible: requires, RecordedAt: at}
	copy(a.SHA256[:], sum)
	return a
}
