package deb

import (
	"context"
	"encoding/json"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The replay of the key events after a restore (R-DEB-032, Q-DEB-22, Q-DEB-31). The restore returned the repository to the backup's
// snapshot, so a key that was retired or replaced afterwards is live again. The replay only restricts: it sets such a key withheld
// (it neither signs nor is served), keeps its sealed private key and deletes nothing, because the entries come from the journal in the
// object store, which a holder of the runtime S3 credential could write (R-HA-054). A forged entry costs availability until an
// administrator reinstates the key, never a key. The entry is a claim to be checked against the restored database, not a fact: it
// acts on the key the restored repository holds with that fingerprint, in the state the entry assumes, and on a local or virtual
// Debian repository only.

func init() {
	journal.Register(journal.Handler{
		Domain: "deb",
		Events: []string{audit.EventDebSigningKeyPromoted, audit.EventDebSigningKeyRetired},
		Apply:  applyKeyEntry,
	})
}

// keyEntry is the entry of deb.signing_key.promoted and deb.signing_key.retired (keysStore.record): the repository, the fingerprints
// and the actor, and no key material. A promotion of a repository that had no primary key names no old fingerprint.
type keyEntry struct {
	RepositoryID   int64  `json:"repositoryId"`
	Fingerprint    string `json:"fingerprint"`
	OldFingerprint string `json:"oldFingerprint"`
	NewFingerprint string `json:"newFingerprint"`
	ActorID        string `json:"actorId"`
}

// replayRepo finds the repository an entry names when it is a local or virtual Debian repository; an entry about any other (a
// forged one included) finds nothing and is "subject unknown".
func replayRepo(ctx context.Context, qs *q.Queries, id int64) (repoRef, bool, error) {
	list, err := qs.DebRepoRefLocked(ctx, id)
	if err != nil || len(list) == 0 {
		return repoRef{}, false, err
	}
	r := list[0]
	repo := repoRef{ID: r.ID, Project: r.Project, Ref: r.Ref, Format: r.Format, Class: r.Class}
	return repo, repo.hasKeys(), nil
}

// replayRetired withholds the key of a deb.signing_key.retired entry that the restored repository still holds as primary or next.
// A key withheld or retired already is in effect; a key the repository does not hold is unknown.
func replayRetired(ctx context.Context, tx meta.Tx, in rows.KeyRetiredEntry) (rows.ReplayResult, error) {
	qs, err := queries(tx)
	if err != nil {
		return "", err
	}
	if _, ok, err := replayRepo(ctx, qs, in.RepoID); err != nil || !ok {
		return rows.SubjectUnknown, err
	}
	list, err := keysOf(ctx, qs, in.RepoID, true)
	if err != nil {
		return "", err
	}
	k, ok := list.byFingerprint(in.Fingerprint)
	if !ok {
		return rows.SubjectUnknown, nil
	}
	if k.State == rows.KeyWithheld || k.State == rows.KeyRetired {
		return rows.AlreadyInEffect, nil
	}
	if err := withhold(ctx, qs, in.RepoID, k); err != nil {
		return "", err
	}
	return rows.Applied, nil
}

// withhold sets a key withheld and raises change_seq, so that the next build signs with what is left.
func withhold(ctx context.Context, qs *q.Queries, repoID int64, k keyRec) error {
	if _, err := qs.DebSetKeyState(ctx, q.DebSetKeyStateParams{ID: k.ID, State: rows.KeyWithheld}); err != nil {
		return err
	}
	_, err := qs.DebRaiseChangeSeq(ctx, repoID)
	return err
}

// replayPromoted applies a deb.signing_key.promoted entry (R-DEB-032):
//   - the restored repository holds the new key as next (a key created before the snapshot and promoted after it): the new key
//     becomes primary and the old primary is withheld;
//   - it does not hold the new key (created after the snapshot): the old primary is withheld, so the repository has no primary and
//     its builds fail with no_signing_key until an administrator acts, while the generation published before the restore stays served;
//   - the new key is primary already: in effect.
//
// A repository whose primary key is neither the entry's old key nor its new key is not the repository the entry describes: subject
// unknown, nothing changes.
func replayPromoted(ctx context.Context, tx meta.Tx, in rows.KeyPromotedEntry) (rows.ReplayResult, error) {
	qs, err := queries(tx)
	if err != nil {
		return "", err
	}
	if _, ok, err := replayRepo(ctx, qs, in.RepoID); err != nil || !ok {
		return rows.SubjectUnknown, err
	}
	list, err := keysOf(ctx, qs, in.RepoID, true)
	if err != nil {
		return "", err
	}
	ks := list
	primary, hasPrimary := ks.inState(rows.KeyPrimary)
	newKey, hasNew := ks.byFingerprint(in.NewFingerprint)
	switch {
	case hasNew && newKey.State == rows.KeyPrimary:
		return rows.AlreadyInEffect, nil
	case hasNew && newKey.State != rows.KeyNext:
		// Withheld or retired: the replay never brings a key back to signing; it restricts only.
		return rows.SubjectUnknown, nil
	case hasPrimary && primary.Fingerprint != in.OldFingerprint:
		return rows.SubjectUnknown, nil
	}
	if !hasPrimary && !hasNew {
		if old, ok := ks.byFingerprint(in.OldFingerprint); ok && in.OldFingerprint != "" && old.State != rows.KeyNext {
			return rows.AlreadyInEffect, nil
		}
		return rows.SubjectUnknown, nil
	}
	if hasPrimary {
		if err := withhold(ctx, qs, in.RepoID, primary); err != nil {
			return "", err
		}
	}
	if hasNew {
		if _, err := qs.DebSetKeyState(ctx, q.DebSetKeyStateParams{ID: newKey.ID, State: rows.KeyPrimary}); err != nil {
			return "", err
		}
		if _, err := qs.DebRaiseChangeSeq(ctx, in.RepoID); err != nil {
			return "", err
		}
	}
	return rows.Applied, nil
}

// decodeKeyEntry parses an entry; false for one that is no object of the entry's shape.
func decodeKeyEntry(raw json.RawMessage) (keyEntry, bool) {
	var e keyEntry
	return e, json.Unmarshal(raw, &e) == nil
}

// applyKeyEntry is the handler of the journal replay for both events.
func applyKeyEntry(ctx context.Context, tx meta.Tx, _ journal.Env, o journal.Object) (journal.Result, error) {
	res := journal.Result{ID: o.ID, Event: o.Event}
	e, ok := decodeKeyEntry(o.Entry)
	if !ok {
		return skipped(res, "the entry is not a signing key entry"), nil
	}
	if e.RepositoryID <= 0 {
		return skipped(res, "the entry names no repository"), nil
	}
	var (
		out rows.ReplayResult
		err error
	)
	switch o.Event {
	case audit.EventDebSigningKeyRetired:
		if !validFingerprint(e.Fingerprint) {
			return skipped(res, "the entry names no fingerprint"), nil
		}
		res.Subject = e.Fingerprint
		out, err = replayRetired(ctx, tx, rows.KeyRetiredEntry{RepoID: e.RepositoryID, Fingerprint: e.Fingerprint})
	default:
		if !validFingerprint(e.NewFingerprint) || (e.OldFingerprint != "" && !validFingerprint(e.OldFingerprint)) {
			return skipped(res, "the entry names no fingerprint"), nil
		}
		res.Subject = e.NewFingerprint
		out, err = replayPromoted(ctx, tx, rows.KeyPromotedEntry{RepoID: e.RepositoryID, OldFingerprint: e.OldFingerprint, NewFingerprint: e.NewFingerprint})
	}
	if err != nil {
		return res, err
	}
	res.Outcome = journal.Outcome(out)
	qs, err := queries(tx)
	if err != nil {
		return res, err
	}
	if repo, ok, err := replayRepo(ctx, qs, e.RepositoryID); err != nil {
		return res, err
	} else if ok {
		res.Subject = repo.Ref + " " + res.Subject
		if out == rows.Applied {
			if note, err := primaryNote(ctx, qs, repo); err != nil {
				return res, err
			} else if note != "" {
				res.Note = note
			}
		}
	}
	return res, nil
}

// primaryNote names a repository the replay left without a primary key (R-DEB-032 "The replay output lists every repository with a
// withheld key or without a primary").
func primaryNote(ctx context.Context, qs *q.Queries, repo repoRef) (string, error) {
	list, err := keysOf(ctx, qs, repo.ID, false)
	if err != nil {
		return "", err
	}
	if _, ok := list.inState(rows.KeyPrimary); ok {
		return "", nil
	}
	return repo.Ref + " has no primary signing key: its builds fail with no_signing_key until an administrator reinstates a withheld key or promotes a new one", nil
}

func skipped(res journal.Result, why string) journal.Result {
	res.Outcome = journal.Skipped
	res.Detail, _ = json.Marshal(map[string]string{"reason": why}) //nolint:errcheck // a map of strings marshals
	return res
}
