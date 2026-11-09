package deb

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// remoteStore is the group of the data port that holds the release documents, the files they list and the parsed index rows of the
// remote repositories (R-DEB-019 to R-DEB-023). The rows are cache rows: a restore without remote caches deletes them (R-BAK-008,
// purge.go). The statements are queries/deb_remote.sql.
type remoteStore struct{ *group }

// ParseArgs are the arguments of a job of kind [RemoteParseJobKind]. A Path names the index to parse (relative to Dir, without the
// compression suffix); an empty Path asks the job to fetch the release documents of Dir first and then parse the indexes of the scope,
// which is what the shadowing check enqueues for a suite no release is stored for (R-DEB-023, Q-DEB-41).
type ParseArgs struct {
	RepoID int64  `json:"repoId"`
	Dir    string `json:"dir"`
	Path   string `json:"path,omitempty"`
	// Components and Architectures are the scope of a job with an empty Path: the indexes it parses.
	Components    []string `json:"components,omitempty"`
	Architectures []string `json:"architectures,omitempty"`
}

// parseDedupKey is the coalescing key of the parse of one index path of one release directory of one repository (Q-DEB-36).
func parseDedupKey(repoID int64, dir, path string) string {
	return "deb-remote-parse:" + strconv.FormatInt(repoID, 10) + ":" + dir + ":" + path
}

func parseSpec(a ParseArgs) jobs.EnqueueSpec {
	id := strconv.FormatInt(a.RepoID, 10)
	return jobs.EnqueueSpec{
		Kind: RemoteParseJobKind, Args: a, DedupKey: parseDedupKey(a.RepoID, a.Dir, a.Path), TargetKind: "repository", TargetKey: id,
	}
}

func parseStamp(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func sum32(b []byte) (out [32]byte) {
	copy(out[:], b)
	return out
}

func releaseOf(repoID int64, dir string, r q.DebRemoteReleasesRow) rows.Release {
	out := rows.Release{
		RepoID: repoID, Dir: dir, Document: r.Document, SHA256: sum32(r.Sha256), ByHash: r.ByHash, VerifiedBy: r.VerifiedBy,
		StoredAt: parseStamp(r.StoredAt),
	}
	if r.HasDate {
		t := parseStamp(r.ReleaseDate)
		out.ReleaseDate = &t
	}
	if r.HasFloor {
		t := parseStamp(r.FloorDate)
		out.FloorDate = &t
	}
	return out
}

// RecordRelease records a release document in the fill transaction and decides the anti-rollback rule (R-DEB-019, Q-DEB-35, Q-DEB-42).
//
// The directory's rows are written under an advisory lock of the directory, so two fills of its documents and a re-verification
// decide one after the other. While the remote has keys, a document whose Date is older than the floor (the newest Date of any release
// verified for the directory) is not stored: the decision says RolledBack and the stored copy stays. Otherwise the document replaces the
// stored one of its name; a verified document raises the floor, which never falls and stays when verified_by is cleared. The files the
// document lists replace those of the directory when the document is usable (verified, or the remote has no keys); the replaced ones
// stay for deb.by_hash_keep. A fill that replaces a release enqueues the parse of every index that has rows and whose hash it changes
// (Q-DEB-36). A successful fill deletes the 'absent' record of the directory (Q-DEB-41).
func (s remoteStore) RecordRelease(ctx context.Context, tx meta.Tx, in rows.ReleaseRecord) (rows.ReleaseDecision, error) {
	qs, err := queries(tx)
	if err != nil {
		return rows.ReleaseDecision{}, err
	}
	return s.record(ctx, tx, qs, in)
}

func (s remoteStore) record(ctx context.Context, tx meta.Tx, qs *q.Queries, in rows.ReleaseRecord) (rows.ReleaseDecision, error) {
	if err := qs.DebRemoteLockDir(ctx, q.DebRemoteLockDirParams{RepoID: in.RepoID, Dir: in.Dir}); err != nil {
		return rows.ReleaseDecision{}, err
	}
	if in.Absent {
		return rows.ReleaseDecision{Stored: true}, qs.DebRemoteUpsertAbsent(ctx, q.DebRemoteUpsertAbsentParams{RepoID: in.RepoID, Dir: in.Dir})
	}
	floor, err := qs.DebRemoteFloor(ctx, q.DebRemoteFloorParams{RepoID: in.RepoID, Dir: in.Dir})
	if err != nil {
		return rows.ReleaseDecision{}, err
	}
	floorAt := parseStamp(floor.FloorDate)
	if in.KeysSet && floor.HasFloor && in.ReleaseDate.Before(floorAt) {
		return rows.ReleaseDecision{RolledBack: true}, nil
	}
	hasFloor, newFloor := floor.HasFloor, floorAt
	if in.VerifiedBy != "" && (!hasFloor || in.ReleaseDate.After(newFloor)) {
		hasFloor, newFloor = true, in.ReleaseDate
	}
	existing, err := qs.DebRemoteReleases(ctx, q.DebRemoteReleasesParams{RepoID: in.RepoID, Dir: in.Dir})
	if err != nil {
		return rows.ReleaseDecision{}, err
	}
	hasInRelease := slices.ContainsFunc(existing, func(r q.DebRemoteReleasesRow) bool { return r.Document == rows.DocInRelease })
	if err := qs.DebRemoteUpsertRelease(ctx, q.DebRemoteUpsertReleaseParams{
		RepoID: in.RepoID, Dir: in.Dir, Document: in.Document, Sha256: in.SHA256[:], ReleaseDate: stamp(in.ReleaseDate), ByHash: in.ByHash,
		VerifiedBy: in.VerifiedBy, HasFloor: hasFloor, FloorDate: stamp(newFloor),
	}); err != nil {
		return rows.ReleaseDecision{}, err
	}
	if in.VerifiedBy != "" {
		if err := qs.DebRemoteRaiseFloor(ctx, q.DebRemoteRaiseFloorParams{RepoID: in.RepoID, Dir: in.Dir, FloorDate: stamp(newFloor)}); err != nil {
			return rows.ReleaseDecision{}, err
		}
	}
	if err := qs.DebRemoteDeleteAbsent(ctx, q.DebRemoteDeleteAbsentParams{RepoID: in.RepoID, Dir: in.Dir}); err != nil {
		return rows.ReleaseDecision{}, err
	}
	dec := rows.ReleaseDecision{Stored: true}
	// The InRelease names the files; a Release does only where the upstream has no InRelease.
	if !in.Usable() || (in.Document == rows.DocRelease && hasInRelease) {
		return dec, nil
	}
	if err := s.replaceFiles(ctx, qs, in); err != nil {
		return rows.ReleaseDecision{}, err
	}
	dec.Parses, err = s.enqueueChanged(ctx, tx, qs, in)
	return dec, err
}

// replaceFiles makes the files of the document the current ones of the directory; the files of the release it replaces stay, marked
// superseded.
func (s remoteStore) replaceFiles(ctx context.Context, qs *q.Queries, in rows.ReleaseRecord) error {
	if err := qs.DebRemoteSupersedeFiles(ctx, q.DebRemoteSupersedeFilesParams{RepoID: in.RepoID, Dir: in.Dir}); err != nil {
		return err
	}
	if len(in.Files) == 0 {
		return nil
	}
	arg := q.DebRemoteInsertFilesParams{RepoID: in.RepoID, Dir: in.Dir}
	for _, f := range in.Files {
		arg.Paths = append(arg.Paths, f.Path)
		arg.Sha256s = append(arg.Sha256s, slices.Clone(f.SHA256[:]))
		arg.Sizes = append(arg.Sizes, f.Size)
	}
	return qs.DebRemoteInsertFiles(ctx, arg)
}

// enqueueChanged enqueues the parse of every index of the directory that has rows and that the release lists with another hash than
// the one parsed (Q-DEB-36): the rows follow the release, not the clients, because APT applies pdiffs and may never fetch a full
// Packages again.
func (s remoteStore) enqueueChanged(ctx context.Context, tx meta.Tx, qs *q.Queries, in rows.ReleaseRecord) (int, error) {
	if s.o.Jobs == nil {
		return 0, nil
	}
	parsed, err := qs.DebRemoteIndexRows(ctx, q.DebRemoteIndexRowsParams{RepoID: in.RepoID, Dir: in.Dir})
	if err != nil || len(parsed) == 0 {
		return 0, err
	}
	n := 0
	for _, ref := range rows.PreferredIndexes(in.Files, true) {
		i := slices.IndexFunc(parsed, func(p q.DebRemoteIndexRowsRow) bool { return p.Path == ref.Path })
		if i < 0 || sum32(parsed[i].Sha256) == ref.SHA256 {
			continue
		}
		if _, err := s.o.Jobs.Enqueue(ctx, tx, parseSpec(ParseArgs{RepoID: in.RepoID, Dir: in.Dir, Path: ref.Path})); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// RecordAbsent records that the upstream answered 404 or 410 for the release documents of a suite (Q-DEB-41), in a transaction of its
// own. A later successful fill deletes the record.
func (s remoteStore) RecordAbsent(ctx context.Context, repoID int64, dir string) error {
	return s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		_, err = s.record(ctx, tx, qs, rows.ReleaseRecord{RepoID: repoID, Dir: dir, Absent: true})
		return err
	})
}

// RecordVerified records a release a signature verified after it was stored: the Release of a Release and Release.gpg pair, which is
// verified when it is served (R-DEB-019), and a release a key change left unverified, which the remote's job verifies again from
// its stored bytes (R-DEB-020, Q-DEB-42). It applies the rules of RecordRelease in a transaction of its own.
func (s remoteStore) RecordVerified(ctx context.Context, in rows.ReleaseRecord) (rows.ReleaseDecision, error) {
	var dec rows.ReleaseDecision
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		dec, err = s.record(ctx, tx, qs, in)
		return err
	})
	return dec, err
}

// Release reads the stored release document that names a directory's index files: the InRelease when the upstream has one, else the
// Release. found is false when the directory has none (an 'absent' record is none).
func (s remoteStore) Release(ctx context.Context, repoID int64, dir string) (rows.Release, bool, error) {
	var list []q.DebRemoteReleasesRow
	err := s.g().read(ctx, nil, func(qs *q.Queries) (err error) {
		list, err = qs.DebRemoteReleases(ctx, q.DebRemoteReleasesParams{RepoID: repoID, Dir: dir})
		return err
	})
	if err != nil {
		return rows.Release{}, false, err
	}
	for _, doc := range []string{rows.DocInRelease, rows.DocRelease} {
		for _, r := range list {
			if r.Document == doc {
				return releaseOf(repoID, dir, r), true, nil
			}
		}
	}
	return rows.Release{}, false, nil
}

// Releases reads every stored document, 'absent' records included, of the directories (the candidates of an index path, the suites of a
// scope), in the order of the directories and the documents.
func (s remoteStore) Releases(ctx context.Context, repoID int64, dirs []string) ([]rows.Release, error) {
	var list []q.DebRemoteReleasesOfDirsRow
	err := s.g().read(ctx, nil, func(qs *q.Queries) (err error) {
		list, err = qs.DebRemoteReleasesOfDirs(ctx, q.DebRemoteReleasesOfDirsParams{RepoID: repoID, Dirs: dirs})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]rows.Release, 0, len(list))
	for _, r := range list {
		out = append(out, releaseOf(repoID, r.Dir, q.DebRemoteReleasesRow{
			Document: r.Document, Sha256: r.Sha256, HasDate: r.HasDate, ReleaseDate: r.ReleaseDate, ByHash: r.ByHash, VerifiedBy: r.VerifiedBy,
			StoredAt: r.StoredAt,
		}))
	}
	return out, nil
}

func (g *group) g() *group { return g }

// ListedFile reads what the current release of the directory lists for a path relative to it; found is false when it lists none. A
// transaction of the caller is used when given (the fill's), a short one otherwise.
func (s remoteStore) ListedFile(ctx context.Context, tx meta.Tx, repoID int64, dir, path string) (rows.ReleaseFile, bool, error) {
	var out rows.ReleaseFile
	found := false
	err := s.g().read(ctx, tx, func(qs *q.Queries) error {
		r, err := qs.DebRemoteFileOf(ctx, q.DebRemoteFileOfParams{RepoID: repoID, Dir: dir, Path: path})
		if postgres.IsNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		out, found = rows.ReleaseFile{Path: path, SHA256: sum32(r.Sha256), Size: r.Size}, true
		return nil
	})
	return out, found, err
}

// Files reads the files the current release of the directory lists.
func (s remoteStore) Files(ctx context.Context, repoID int64, dir string) ([]rows.ReleaseFile, error) {
	var list []q.DebRemoteFilesOfDirRow
	err := s.g().read(ctx, nil, func(qs *q.Queries) (err error) {
		list, err = qs.DebRemoteFilesOfDir(ctx, q.DebRemoteFilesOfDirParams{RepoID: repoID, Dir: dir})
		return err
	})
	out := make([]rows.ReleaseFile, 0, len(list))
	for _, f := range list {
		out = append(out, rows.ReleaseFile{Path: f.Path, SHA256: sum32(f.Sha256), Size: f.Size})
	}
	return out, err
}

// EntryDigest reads the digest and size the newest parsed index lists for a path (R-DEB-022).
func (s remoteStore) EntryDigest(ctx context.Context, repoID int64, path string) (rows.Entry, bool, error) {
	var list []q.DebRemoteEntryDigestRow
	err := s.g().read(ctx, nil, func(qs *q.Queries) (err error) {
		list, err = qs.DebRemoteEntryDigest(ctx, q.DebRemoteEntryDigestParams{RepoID: repoID, Path: path})
		return err
	})
	if err != nil || len(list) == 0 {
		return rows.Entry{}, false, err
	}
	r := list[0]
	return rows.Entry{
		Dir: r.Dir, IndexPath: r.IndexPath, Package: r.Package, Version: r.Version, Architecture: r.Architecture, Path: path,
		SHA256: sum32(r.Sha256), Size: r.Size,
	}, true, nil
}

// EnqueueParse enqueues the parse of an index path of a release directory in the transaction tx, coalesced per repository, directory
// and index path (Q-DEB-36).
func (s remoteStore) EnqueueParse(ctx context.Context, tx meta.Tx, repoID int64, dir, indexPath string) error {
	if s.o.Jobs == nil {
		return nil
	}
	_, err := s.o.Jobs.Enqueue(ctx, tx, parseSpec(ParseArgs{RepoID: repoID, Dir: dir, Path: indexPath}))
	return err
}

// ClearVerifiedBy clears verified_by of the releases that the fingerprints verified (R-DEB-020, Q-DEB-42) and returns how many; floor_date
// stays.
func (s remoteStore) ClearVerifiedBy(ctx context.Context, tx meta.Tx, repoID int64, fingerprints []string) (int, error) {
	if len(fingerprints) == 0 {
		return 0, nil
	}
	n := 0
	err := s.g().read(ctx, tx, func(qs *q.Queries) error {
		list, err := qs.DebRemoteClearVerifiedBy(ctx, q.DebRemoteClearVerifiedByParams{RepoID: repoID, Fingerprints: fingerprints})
		n = len(list)
		return err
	})
	return n, err
}

// Unverified lists the stored documents of the repository that no key verifies now: (dir, document) pairs.
func (s remoteStore) Unverified(ctx context.Context, repoID int64) ([][2]string, error) {
	var list []q.DebRemoteUnverifiedRow
	err := s.g().read(ctx, nil, func(qs *q.Queries) (err error) {
		list, err = qs.DebRemoteUnverified(ctx, repoID)
		return err
	})
	out := make([][2]string, 0, len(list))
	for _, r := range list {
		out = append(out, [2]string{r.Dir, r.Document})
	}
	return out, err
}

// ServesName answers the shadowing check in a scope (R-DEB-023, Q-DEB-37, Q-DEB-41): the remote serves the name when a parsed row of
// an index in the scope lists it. known is false while a suite of the scope is neither settled by a stored usable release whose
// indexes of the scope are all parsed nor recorded absent (the record counts for the remote's not-found period).
func (s remoteStore) ServesName(ctx context.Context, sc rows.NameScope) (served, known bool, err error) {
	if sc, err = s.withRemote(ctx, sc); err != nil {
		return false, false, err
	}
	dirs := scopeDirs(sc)
	var locations []q.DebRemoteNameLocationsRow
	err = s.g().read(ctx, nil, func(qs *q.Queries) (err error) {
		locations, err = qs.DebRemoteNameLocations(ctx, q.DebRemoteNameLocationsParams{RepoID: sc.RepoID, Name: sc.Name})
		return err
	})
	if err != nil {
		return false, false, err
	}
	for _, l := range locations {
		if slices.Contains(dirs, l.Dir) && rows.InScope(l.IndexPath, sc.Components, sc.Architectures) {
			return true, true, nil
		}
	}
	unsettled, err := s.unsettled(ctx, sc, dirs)
	return false, len(unsettled) == 0, err
}

// withRemote completes a scope with what the remote's document says: whether it has keys and its not-found period.
func (s remoteStore) withRemote(ctx context.Context, sc rows.NameScope) (rows.NameScope, error) {
	var row q.DebRemoteRepoMetaRow
	if err := s.g().read(ctx, nil, func(qs *q.Queries) (err error) {
		row, err = qs.DebRemoteRepoMeta(ctx, sc.RepoID)
		return err
	}); err != nil {
		return sc, err
	}
	var doc struct {
		Deb struct {
			UpstreamKeys *string `json:"upstreamKeys"`
		} `json:"deb"`
		Remote struct {
			Cache struct {
				NotFoundTTL *int `json:"notFoundTtlSeconds"`
			} `json:"cache"`
		} `json:"remote"`
	}
	_ = json.Unmarshal(row.Config, &doc) //nolint:errcheck // a document written around the service yields the defaults
	sc.KeysSet = doc.Deb.UpstreamKeys != nil && strings.TrimSpace(*doc.Deb.UpstreamKeys) != ""
	sc.NotFoundTTL = 300 * time.Second
	if doc.Remote.Cache.NotFoundTTL != nil {
		sc.NotFoundTTL = time.Duration(*doc.Remote.Cache.NotFoundTTL) * time.Second
	}
	return sc, nil
}

func scopeDirs(sc rows.NameScope) []string {
	out := make([]string, 0, len(sc.Suites))
	for _, suite := range sc.Suites {
		out = append(out, "dists/"+suite)
	}
	return out
}

// unsettled lists the directories of a scope that the shadowing check cannot decide from rows: no stored usable release (and no
// fresh 'absent' record), or an index of the scope that the release lists and no rows of that hash exist for.
func (s remoteStore) unsettled(ctx context.Context, sc rows.NameScope, dirs []string) ([]string, error) {
	rels, err := s.Releases(ctx, sc.RepoID, dirs)
	if err != nil {
		return nil, err
	}
	var files []q.DebRemoteFilesOfDirsRow
	var parsed []q.DebRemoteIndexesOfDirsRow
	err = s.g().read(ctx, nil, func(qs *q.Queries) error {
		var err error
		if files, err = qs.DebRemoteFilesOfDirs(ctx, q.DebRemoteFilesOfDirsParams{RepoID: sc.RepoID, Dirs: dirs}); err != nil {
			return err
		}
		parsed, err = qs.DebRemoteIndexesOfDirs(ctx, q.DebRemoteIndexesOfDirsParams{RepoID: sc.RepoID, Dirs: dirs})
		return err
	})
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var out []string
	for _, dir := range dirs {
		settled := false
		for _, r := range rels {
			if r.Dir != dir {
				continue
			}
			switch {
			case r.Document == rows.DocAbsent:
				if sc.NotFoundTTL <= 0 || now.Sub(r.StoredAt) < sc.NotFoundTTL {
					settled = true
				}
			case !sc.KeysSet || r.VerifiedBy != "":
				settled = true
			}
		}
		if settled && !slices.ContainsFunc(rels, func(r rows.Release) bool { return r.Dir == dir && r.Document == rows.DocAbsent }) {
			var list []rows.ReleaseFile
			for _, f := range files {
				if f.Dir == dir {
					list = append(list, rows.ReleaseFile{Path: f.Path, SHA256: sum32(f.Sha256)})
				}
			}
			for _, ref := range rows.PreferredIndexes(list, false) {
				if !rows.InScope(ref.Path, sc.Components, sc.Architectures) {
					continue
				}
				if !slices.ContainsFunc(parsed, func(p q.DebRemoteIndexesOfDirsRow) bool {
					return p.Dir == dir && p.Path == ref.Path && sum32(p.Sha256) == ref.SHA256
				}) {
					settled = false
					break
				}
			}
		}
		if !settled {
			out = append(out, dir)
		}
	}
	return out, nil
}

// EnsureParsed enqueues, through the background path, the release fetch and the parse of every suite of the scope that is not
// settled (R-DEB-023, Q-DEB-41), and reports whether it enqueued any. The job of a suite without a usable release fetches the release
// and then parses the indexes of the scope; the job of a suite with a release parses only the indexes that lack rows.
func (s remoteStore) EnsureParsed(ctx context.Context, sc rows.NameScope) (bool, error) {
	if s.o.Jobs == nil {
		return false, errNotBuilt("the queue of the remote's background jobs")
	}
	sc, err := s.withRemote(ctx, sc)
	if err != nil {
		return false, err
	}
	dirs := scopeDirs(sc)
	todo, err := s.unsettled(ctx, sc, dirs)
	if err != nil || len(todo) == 0 {
		return false, err
	}
	err = s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		for _, dir := range todo {
			a := ParseArgs{RepoID: sc.RepoID, Dir: dir, Components: sc.Components, Architectures: sc.Architectures}
			if _, err := s.o.Jobs.Enqueue(ctx, tx, parseSpec(a)); err != nil {
				return err
			}
		}
		return nil
	})
	return err == nil, err
}
