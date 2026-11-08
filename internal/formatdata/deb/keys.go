package deb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/openpgp"
	"github.com/guided-traffic/trove/internal/secrets"
)

// This file holds what the key store (keys_store.go) and the replay of the key events (keys_journal.go) share: the statements' types
// in the words of the port, the repository an operation names, the clock of the database, and the audit events of the key changes.

// keyLead is how far before the creating transaction a generated key's creation time lies, so that every signature trove makes with
// it is later than the key on every client whose clock trails the database by less than that (R-DEB-015, Q-DEB-30).
const keyLead = time.Hour

func queries(tx meta.Tx) (*q.Queries, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errForeignTx
	}
	return q.New(ptx), nil
}

// stamp renders a time for a statement that casts it to timestamptz.
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// keyRec is a row of deb_signing_keys without its private part (queries/deb_keys.sql).
type keyRec = q.DebKeysOfRepoRow

// parseTime reads a time of the statements' text form; the empty string is no time.
func parseTime(text string) time.Time {
	if text == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func optTime(text string) *time.Time {
	t := parseTime(text)
	if t.IsZero() {
		return nil
	}
	return &t
}

// keyRow turns a row into the key of the port, which has no private part.
func keyRow(r keyRec) rows.Key {
	return rows.Key{
		ID: r.ID, RepoID: r.RepoID, Fingerprint: r.Fingerprint, Algorithm: int16(r.Algorithm), Bits: int(r.Bits), //nolint:gosec // 1 or 22
		KeyCreated: parseTime(r.KeyCreated), BoundSince: parseTime(r.BoundSince), State: r.State, Origin: r.Origin,
		PublicKey: r.PublicKey, CreatedAt: parseTime(r.CreatedAt), CreatedBy: r.CreatedBy,
		PromotedAt: optTime(r.PromotedAt), WithheldAt: optTime(r.WithheldAt), RetiredAt: optTime(r.RetiredAt), RetiredBy: r.RetiredBy,
	}
}

// keysOf reads the keys of a repository; locked takes the row locks of DebKeysOfRepoLocked.
func keysOf(ctx context.Context, qs *q.Queries, repoID int64, locked bool) (keySet, error) {
	if !locked {
		list, err := qs.DebKeysOfRepo(ctx, repoID)
		return keySet(list), err
	}
	list, err := qs.DebKeysOfRepoLocked(ctx, repoID)
	out := make(keySet, 0, len(list))
	for _, r := range list {
		out = append(out, keyRec(r))
	}
	return out, err
}

// keySet is the keys of one repository, locked or not, found by state and fingerprint.
type keySet []keyRec

func (ks keySet) inState(state string) (keyRec, bool) {
	for _, k := range ks {
		if k.State == state {
			return k, true
		}
	}
	return keyRec{}, false
}

func (ks keySet) byFingerprint(fp string) (keyRec, bool) {
	for _, k := range ks {
		if k.Fingerprint == fp {
			return k, true
		}
	}
	return keyRec{}, false
}

// repoRef is the repository an operation names.
type repoRef struct {
	ID      int64
	Project string
	// Ref is "<project>/<key>".
	Ref    string
	Format string
	Class  string
}

// lookupRepo reads the repository of an id. A repository that does not exist answers 404; one that is not a local or virtual
// Debian repository answers 400 not_applicable: only those have signing keys (R-DEB-015).
func lookupRepo(ctx context.Context, qs *q.Queries, repoID int64) (repoRef, error) {
	list, err := qs.DebRepoRef(ctx, repoID)
	if err != nil {
		return repoRef{}, err
	}
	if len(list) == 0 {
		return repoRef{}, errs.E(errs.NotFound, errs.CodeNotFound, "The repository does not exist.")
	}
	r := list[0]
	return checkKeyed(repoRef{ID: r.ID, Project: r.Project, Ref: r.Ref, Format: r.Format, Class: r.Class})
}

// lockRepo is lookupRepo for a write: the repository row is locked until the transaction ends, so that the changes of one
// repository's keys run one after the other and each reads what the one before committed (READ COMMITTED, one statement later).
func lockRepo(ctx context.Context, qs *q.Queries, repoID int64) (repoRef, error) {
	list, err := qs.DebRepoRefLocked(ctx, repoID)
	if err != nil {
		return repoRef{}, err
	}
	if len(list) == 0 {
		return repoRef{}, errs.E(errs.NotFound, errs.CodeNotFound, "The repository does not exist.")
	}
	r := list[0]
	return checkKeyed(repoRef{ID: r.ID, Project: r.Project, Ref: r.Ref, Format: r.Format, Class: r.Class})
}

func checkKeyed(r repoRef) (repoRef, error) {
	if !r.hasKeys() {
		return repoRef{}, errs.E(errs.Invalid, errs.CodeNotApplicable, "Only a local or virtual Debian repository has signing keys (R-DEB-015).")
	}
	return r, nil
}

func (r repoRef) hasKeys() bool {
	return r.Format == "deb" && (r.Class == "local" || r.Class == "virtual")
}

// dbNow is the database's time of the transaction, to the second.
func dbNow(ctx context.Context, qs *q.Queries) (time.Time, error) {
	text, err := qs.DebNowText(ctx)
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, text)
}

var fingerprintText = regexp.MustCompile(`^[0-9A-F]{40}$`)

// validFingerprint reports whether text is a v4 fingerprint as the port names it.
func validFingerprint(text string) bool { return fingerprintText.MatchString(text) }

// keyUnsupported turns a refusal of the OpenPGP library into the 422 of the import rules (R-DEB-015). The text of the library's
// error names the reason and never holds key material.
func keyUnsupported(err error) error {
	return errs.E(errs.Unprocessable, errs.CodeDebKeyUnsupported, "The signing key is refused: "+err.Error(), errs.WithCause(err))
}

// isImportRefusal reports whether err is the library's refusal of a key.
func isImportRefusal(err error) bool {
	return errors.Is(err, openpgp.ErrInvalid) || errors.Is(err, openpgp.ErrUnsupportedKey)
}

// uniqueViolation reports whether err is the violation of a unique index of the keys: two requests imported one key at once, or
// changed one repository's keys at once, which the row lock of lockRepo should have prevented. Neither loser is an internal error.
func uniqueViolation(err error) bool { return postgres.IsState(err, "23505") }

func (s keysStore) ring() (*secrets.KeyRing, error) {
	if s.o.Ring == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "deb: the signing keys need the stored-secret key ring")
	}
	return s.o.Ring, nil
}

// newKeyRecord is a key about to be stored.
type newKeyRecord struct {
	key    *openpgp.Key
	state  string
	origin string
	by     string
}

// insertKey seals the secret key under the row it will occupy and stores the key. The row's id is made here because the sealed
// value is bound to it (R-ARCH-021).
func (s keysStore) insertKey(ctx context.Context, qs *q.Queries, repoID int64, rec newKeyRecord) (string, error) {
	ring, err := s.ring()
	if err != nil {
		return "", err
	}
	secret, err := rec.key.SecretArmored()
	if err != nil {
		return "", fmt.Errorf("deb: serialize the secret key: %w", err)
	}
	id := uuid.NewString()
	sealed, err := ring.Seal(SigningKeys, secrets.Row(id), secret)
	if err != nil {
		return "", fmt.Errorf("deb: seal the secret key: %w", err)
	}
	clear(secret)
	cert := rec.key.Certificate()
	err = qs.DebInsertKey(ctx, q.DebInsertKeyParams{
		ID: id, RepoID: repoID, Fingerprint: rec.key.Fingerprint, Algorithm: int16(rec.key.Algorithm), Bits: int32(rec.key.Bits), //nolint:gosec // 1 or 22; at most 16384
		KeyCreated: stamp(rec.key.Created), BoundSince: stamp(rec.key.BoundSince), State: rec.state, Origin: rec.origin,
		PublicKey: string(openpgp.ArmorCertificates(cert)), PrivateKey: sealed, CreatedBy: uuidOrEmpty(rec.by),
	})
	return id, err
}

// uuidOrEmpty keeps an actor id for a uuid column: the empty string is no actor, anything that is no uuid is none as well.
func uuidOrEmpty(id string) string {
	if _, err := uuid.Parse(id); err != nil {
		return ""
	}
	return id
}

// The audit events of the key changes (R-DEB-033). Details follow the registry's schemas; the two journaled events carry the entry
// the replay applies (keys_journal.go).

func (s keysStore) actor(ctx context.Context, id string) audit.Actor {
	if s.o.Actor != nil {
		if a := s.o.Actor(ctx); a.Kind != "" && (id == "" || a.ID == id) {
			return a
		}
	}
	if id == "" {
		return audit.Actor{Kind: "anonymous"}
	}
	return audit.Actor{Kind: "user", ID: id}
}

func (s keysStore) record(ctx context.Context, tx meta.Tx, repo repoRef, typ, actorID string, details map[string]any, journalEntry map[string]any) error {
	raw, err := json.Marshal(details)
	if err != nil {
		return err
	}
	ev := audit.Event{
		Type: typ, Outcome: audit.Success, Actor: s.actor(ctx, actorID),
		Subject: audit.Subject{Kind: "repository", ID: fmt.Sprint(repo.ID), Name: repo.Ref}, Repository: repo.Ref, Project: repo.Project, Details: raw,
	}
	if s.o.Source != nil {
		ev.Source = s.o.Source(ctx)
	}
	if journalEntry != nil {
		journalEntry["repositoryId"] = repo.ID
		journalEntry["actorId"] = uuidOrEmpty(actorID)
		if ev.Journal, err = json.Marshal(journalEntry); err != nil {
			return err
		}
	}
	return s.o.Recorder.RecordTx(ctx, tx, ev)
}

// keysChanged tells the index build that the keys of the repository changed: change_seq of every suite is raised, so that the next
// build re-signs (R-DEB-017), and the configured hook enqueues that build.
func (s keysStore) keysChanged(ctx context.Context, tx meta.Tx, qs *q.Queries, repoID int64) error {
	if _, err := qs.DebRaiseChangeSeq(ctx, repoID); err != nil {
		return err
	}
	if s.o.KeysChanged != nil {
		return s.o.KeysChanged(ctx, tx, repoID)
	}
	return nil
}
