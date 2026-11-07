package nuget

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"regexp"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The replay of package.published and package.deleted entries of local NuGet repositories after a restore
// (R-NUGET-033, Q-NUGET-10). The restore returned the repository to the backup's snapshot. A version published after it is
// gone, and its number would be free for other bytes; the replay reserves the number for the journaled bytes. A version
// deleted after it is back, and the replay leaves it (nothing is deleted: the trash can is P1, so a replayed deletion
// could not be undone) and reports it `revived`; where the version is gone for good the replay keeps its number refused.
//
// The table of R-NUGET-033, row by row, is replayEntry below. The entry is a claim: it acts only on a local NuGet
// repository, only with a complete SHA-512, and never grants anything.

func init() {
	journal.Register(journal.Handler{
		Domain: "nuget",
		Events: []string{audit.EventPackagePublished, audit.EventPackageDeleted},
		Apply:  replayPackage,
	})
}

// packageEntry is the entry of both events (journalEntry): the repository, the lower-cased ID, the URL form of the
// version and the hex SHA-512 of the .nupkg.
type packageEntry struct {
	Repository int64  `json:"repository"`
	ID         string `json:"id"`
	Version    string `json:"version"`
	SHA512     string `json:"sha512"`
}

var sha512Hex = regexp.MustCompile(`^[0-9a-f]{128}$`)

// maxEntryText bounds the ID and the version of an entry before they reach a statement.
const maxEntryText = 512

// decodePackage parses an entry and its digest; the last result says why it is no entry the replay can act on, "" for a usable one.
func decodePackage(raw json.RawMessage) (packageEntry, []byte, string) {
	var e packageEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		return e, nil, "the entry is not a package entry"
	}
	if e.Repository <= 0 || e.ID == "" || len(e.ID) > maxEntryText || e.Version == "" || len(e.Version) > maxEntryText || !sha512Hex.MatchString(e.SHA512) {
		return e, nil, "the entry is incomplete"
	}
	sum := make([]byte, hex.DecodedLen(len(e.SHA512)))
	hex.Decode(sum, []byte(e.SHA512)) //nolint:errcheck // sha512Hex admits only hexadecimal digits
	return e, sum, ""
}

// replayPackage applies one entry.
func replayPackage(ctx context.Context, tx meta.Tx, env journal.Env, o journal.Object) (journal.Result, error) {
	res := journal.Result{ID: o.ID, Event: o.Event}
	e, sum, why := decodePackage(o.Entry)
	if why != "" {
		return skipped(res, why), nil
	}
	qs, err := queries(tx)
	if err != nil {
		return res, err
	}
	res.Subject = e.ID + " " + e.Version
	repos, err := qs.BackupNuGetReplayRepository(ctx, e.Repository)
	if err != nil {
		return res, err
	}
	if len(repos) == 0 {
		res.Outcome = journal.SubjectUnknown
		return res, nil
	}
	res.Subject = repos[0].Ref + " " + res.Subject
	r := replayer{ctx: ctx, qs: qs, env: env, e: e, sum: sum, res: res, repoRef: repos[0].Ref}
	if o.Event == audit.EventPackagePublished {
		return r.published()
	}
	return r.deleted()
}

type replayer struct {
	ctx     context.Context
	qs      *q.Queries
	env     journal.Env
	e       packageEntry
	sum     []byte
	res     journal.Result
	repoRef string
}

func (r replayer) done(o journal.Outcome, detail map[string]string) (journal.Result, error) {
	r.res.Outcome = o
	if detail != nil {
		raw, err := json.Marshal(detail)
		if err != nil {
			return r.res, err
		}
		r.res.Detail = raw
	}
	return r.res, nil
}

// published is the first half of the table of R-NUGET-033.
//
//	A version entered the repository | absent, without a tombstone | a `reserved` tombstone with the journaled SHA-512 (applied)
//	A version entered the repository | present, or tombstoned      | nothing (already in effect)
func (r replayer) published() (journal.Result, error) {
	present, err := r.present()
	if err != nil {
		return r.res, err
	}
	if present {
		return r.done(journal.AlreadyInEffect, nil)
	}
	tomb, err := r.tombstone()
	if err != nil {
		return r.res, err
	}
	if tomb != nil {
		return r.done(journal.AlreadyInEffect, nil)
	}
	n, err := r.insertTombstone("reserved")
	if err != nil {
		return r.res, err
	}
	if n == 0 {
		return r.done(journal.AlreadyInEffect, nil)
	}
	return r.done(journal.Applied, map[string]string{"reserved": r.e.ID + " " + r.e.Version})
}

// deleted is the second half of the table of R-NUGET-033.
//
//	A version was removed | present with the journaled SHA-512 | nothing is deleted; reported `revived`
//	A version was removed | present with other bytes           | nothing (subject unknown)
//	A version was removed | absent, `reserved` tombstone       | the tombstone becomes `removed` (applied)
//	A version was removed | absent, no tombstone               | a `removed` tombstone with the journaled SHA-512 (applied)
//	A version was removed | absent, `removed` tombstone        | nothing (already in effect)
func (r replayer) deleted() (journal.Result, error) {
	present, err := r.present()
	if err != nil {
		return r.res, err
	}
	if present {
		facts, err := r.qs.NuGetVersionFacts(r.ctx, q.NuGetVersionFactsParams{
			RepositoryID: r.e.Repository, LowerID: r.e.ID, LowerVersion: r.e.Version,
		})
		if err != nil {
			return r.res, err
		}
		if len(facts) == 0 || !bytes.Equal(facts[0].Sha512, r.sum) {
			return r.done(journal.SubjectUnknown, nil)
		}
		return r.done(journal.Revived, map[string]string{"revived": r.e.ID + " " + r.e.Version})
	}
	tomb, err := r.tombstone()
	if err != nil {
		return r.res, err
	}
	switch {
	case tomb == nil:
		n, err := r.insertTombstone("removed")
		if err != nil {
			return r.res, err
		}
		if n == 0 {
			return r.done(journal.AlreadyInEffect, nil)
		}
		return r.done(journal.Applied, nil)
	case tomb.Kind == "reserved":
		n, err := r.qs.BackupNuGetPromoteReserved(r.ctx, q.BackupNuGetPromoteReservedParams{
			RepositoryID: r.e.Repository, LowerID: r.e.ID, LowerVersion: r.e.Version, At: stamp(r.env.Now), By: noActor,
		})
		if err != nil {
			return r.res, err
		}
		if n == 0 {
			return r.done(journal.AlreadyInEffect, nil)
		}
		return r.done(journal.Applied, nil)
	}
	return r.done(journal.AlreadyInEffect, nil)
}

func (r replayer) present() (bool, error) {
	vs, err := r.qs.NuGetFindVersion(r.ctx, q.NuGetFindVersionParams{RepositoryID: r.e.Repository, LowerID: r.e.ID, LowerVersion: r.e.Version})
	return len(vs) > 0, err
}

func (r replayer) tombstone() (*q.NuGetTombstoneRow, error) {
	ts, err := r.qs.NuGetTombstone(r.ctx, q.NuGetTombstoneParams{RepositoryID: r.e.Repository, LowerID: r.e.ID, LowerVersion: r.e.Version})
	if err != nil || len(ts) == 0 {
		return nil, err
	}
	return &ts[0], nil
}

// insertTombstone writes the tombstone of the entry, with the time of the replay and the system principal of the
// restore (the nil UUID).
func (r replayer) insertTombstone(kind string) (int64, error) {
	return r.qs.BackupNuGetInsertTombstone(r.ctx, q.BackupNuGetInsertTombstoneParams{
		RepositoryID: r.e.Repository, LowerID: r.e.ID, LowerVersion: r.e.Version, Sha512: r.sum,
		At: stamp(r.env.Now), By: noActor, Kind: kind,
	})
}

func skipped(res journal.Result, why string) journal.Result {
	res.Outcome = journal.Skipped
	res.Detail, _ = json.Marshal(map[string]string{"reason": why}) //nolint:errcheck // a map of strings marshals
	return res
}
