package npm

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/formats/npm/semver"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The replay of npm.unpublish entries after a restore (R-NPM-047, Q-NPM-12). The restore returned the repository to the
// backup's snapshot, so a version that was unpublished afterwards is live again. The replay withholds it: it writes
// the tombstone with source `journal`, applies the dist-tag rule of R-NPM-016 and raises the package's rev_seq, in
// the one transaction the journal gives a handler. It deletes nothing, neither the version row nor its items, because
// the trash can is P1 and a deletion could not be undone (R-HA-054): a forged or mistaken entry costs availability
// until an administrator reinstates the version, never data, integrity or access.
//
// The entry is a claim to be checked against the restored database, not a fact. It acts only on a version that the
// repository stores with exactly the integrity the entry names, and only on a local npm repository.

func init() {
	journal.Register(journal.Handler{
		Domain: "npm",
		Events: []string{audit.EventNPMUnpublish},
		Apply:  replayUnpublish,
	})
}

// unpublishEntry is the entry of npm.unpublish (Store.Tombstone): the repository, the package, the version, its
// integrity and the actor, and no secret.
type unpublishEntry struct {
	RepositoryID int64  `json:"repositoryId"`
	Package      string `json:"package"`
	Version      string `json:"version"`
	Integrity    string `json:"integrity"`
	ActorID      string `json:"actorId"`
}

var uuidText = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// maxEntryText bounds each string of an entry before it reaches a statement: an npm name is at most 214 characters,
// a version a few hundred, an SRI string of a SHA-512 about 100.
const maxEntryText = 512

// decodeUnpublish parses an entry; the second result says why it is no entry the replay can act on, "" for a usable one.
func decodeUnpublish(raw json.RawMessage) (unpublishEntry, string) {
	var e unpublishEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		return e, "the entry is not an npm.unpublish entry"
	}
	if why := e.invalid(); why != "" {
		return e, "the entry is incomplete: " + why
	}
	return e, ""
}

func (e unpublishEntry) invalid() string {
	switch {
	case e.RepositoryID <= 0:
		return "no repository"
	case e.Package == "" || len(e.Package) > maxEntryText:
		return "no package name"
	case e.Version == "" || len(e.Version) > maxEntryText:
		return "no version"
	case !strings.HasPrefix(e.Integrity, "sha512-") || len(e.Integrity) > maxEntryText:
		return "no integrity"
	}
	return ""
}

// replayUnpublish applies one npm.unpublish entry.
func replayUnpublish(ctx context.Context, tx meta.Tx, env journal.Env, o journal.Object) (journal.Result, error) {
	res := journal.Result{ID: o.ID, Event: o.Event}
	e, why := decodeUnpublish(o.Entry)
	if why != "" {
		return skipped(res, why), nil
	}
	res.Subject = e.Package + "@" + e.Version
	qs, err := queries(tx)
	if err != nil {
		return res, err
	}
	repos, err := qs.BackupNPMReplayRepository(ctx, e.RepositoryID)
	if err != nil {
		return res, err
	}
	if len(repos) == 0 {
		res.Outcome = journal.SubjectUnknown
		return res, nil
	}
	repoRef := repos[0].Ref
	res.Subject = repoRef + " " + res.Subject

	tombstoned, err := qs.NPMTombstoned(ctx, q.NPMTombstonedParams{RepoID: e.RepositoryID, Name: e.Package, Version: e.Version})
	if err != nil {
		return res, err
	}
	if tombstoned {
		res.Outcome = journal.AlreadyInEffect
		return res, nil
	}
	pkgs, err := qs.LockPackage(ctx, q.LockPackageParams{RepoID: e.RepositoryID, Name: e.Package})
	if err != nil {
		return res, err
	}
	if len(pkgs) == 0 {
		res.Outcome = journal.SubjectUnknown
		return res, nil
	}
	pkg := pkgs[0]
	stored, err := qs.BackupNPMStoredVersion(ctx, q.BackupNPMStoredVersionParams{PackageID: pkg.ID, Version: e.Version})
	if err != nil {
		return res, err
	}
	if len(stored) == 0 || stored[0].Integrity != e.Integrity {
		res.Outcome = journal.SubjectUnknown
		return res, nil
	}

	// The dist-tag rule of R-NPM-016, on the versions the document holds.
	live, err := qs.NPMLiveVersions(ctx, pkg.ID)
	if err != nil {
		return res, err
	}
	remaining := make([]string, 0, len(live))
	for _, v := range live {
		if v.Version != e.Version {
			remaining = append(remaining, v.Version)
		}
	}
	raw, err := qs.NPMReadTags(ctx, pkg.ID)
	if err != nil {
		return res, err
	}
	tags := map[string]string{}
	if len(raw) > 0 {
		if err := json.Unmarshal([]byte(raw[0]), &tags); err != nil {
			return res, fmt.Errorf("npm journal replay: dist-tags of %s: %w", e.Package, err)
		}
	}
	tags = semver.TagsAfterRemoval(tags, e.Version, remaining)

	by := e.ActorID
	if !uuidText.MatchString(by) {
		by = noActor
	}
	n, err := qs.BackupNPMInsertJournalTombstone(ctx, q.BackupNPMInsertJournalTombstoneParams{
		RepoID: e.RepositoryID, Name: e.Package, Version: e.Version, Integrity: e.Integrity,
		At: stamp(o.At), By: by, JournalEntry: o.ID,
	})
	if err != nil {
		return res, err
	}
	if n == 0 { // a concurrent writer tombstoned it between the check and the insert
		res.Outcome = journal.AlreadyInEffect
		return res, nil
	}
	if err := setTags(ctx, qs, pkg.ID, tags, env.Now); err != nil {
		return res, err
	}
	if _, err := qs.BumpPackageRevision(ctx, pkg.ID); err != nil {
		return res, err
	}
	res.Outcome = journal.Applied
	detail, err := json.Marshal(map[string]string{
		"withheld": "true", "repository": repoRef, "package": e.Package, "version": e.Version, "integrity": e.Integrity,
	})
	if err != nil {
		return res, err
	}
	res.Detail = detail
	return res, nil
}

func skipped(res journal.Result, why string) journal.Result {
	res.Outcome = journal.Skipped
	res.Detail, _ = json.Marshal(map[string]string{"reason": why}) //nolint:errcheck // a map of strings marshals
	return res
}

// noActor is the principal ID a tombstone records when no principal acted: the column is a not-null uuid, and the nil
// UUID is the one value that names nobody.
const noActor = "00000000-0000-0000-0000-000000000000"
