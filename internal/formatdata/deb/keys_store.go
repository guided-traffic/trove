package deb

import (
	"context"
	"crypto/sha256"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/openpgp"
	"github.com/guided-traffic/trove/internal/secrets"
)

// keysStore is the group of the data port that holds the signing keys of the local and virtual repositories (R-DEB-015 to R-DEB-018,
// R-DEB-032). It is the only part of the module that opens a private key: the index build gets it through SigningKeys for one signing
// call, and no other method returns, logs or records key material. A key is sealed with the stored-secret key ring under its row's id
// (R-ARCH-021); the statements are queries/deb_keys.sql.
//
// Every change of a repository's keys locks the repository's key rows first, so two requests that change them one after the other
// cannot trip over the partial unique indexes (one primary and one next key per repository).
type keysStore struct{ *group }

// CreateFirstKey generates the primary key of a new repository in the transaction that creates it (R-DEB-015, Q-REPO-273). The key's
// creation time is the transaction's time minus one hour (Q-DEB-30), its user ID "trove <projectKey>/<repoKey>".
func (s keysStore) CreateFirstKey(ctx context.Context, tx meta.Tx, repoID int64, repoKey string) error {
	qs, err := queries(tx)
	if err != nil {
		return err
	}
	repo, err := lookupRepo(ctx, qs, repoID)
	if err != nil {
		return err
	}
	if repoKey != "" {
		repo.Ref = repoKey
	}
	key, err := s.generate(ctx, qs, repo.Ref)
	if err != nil {
		return err
	}
	if _, err := s.insertKey(ctx, qs, repoID, newKeyRecord{key: key, state: rows.KeyPrimary, origin: rows.KeyGenerated, by: s.actorID(ctx)}); err != nil {
		return err
	}
	return s.record(ctx, tx, repo, audit.EventDebSigningKeyCreated, s.actorID(ctx),
		map[string]any{"fingerprint": key.Fingerprint, "state": rows.KeyPrimary, "origin": rows.KeyGenerated}, nil)
}

// actorID is the id of the request's actor, "" outside a request.
func (s keysStore) actorID(ctx context.Context) string {
	if s.o.Actor == nil {
		return ""
	}
	return s.o.Actor(ctx).ID
}

// generate makes a key for the repository with the database's clock: created an hour before now.
func (s keysStore) generate(ctx context.Context, qs *q.Queries, ref string) (*openpgp.Key, error) {
	now, err := dbNow(ctx, qs)
	if err != nil {
		return nil, err
	}
	secret, _, _, err := openpgp.Generate("trove "+ref, now.Add(-keyLead))
	if err != nil {
		return nil, err
	}
	defer clear(secret)
	return openpgp.ParseSecret(secret, nil, now, openpgp.Limits{})
}

// PublicCertificates returns what signing-key.asc serves: the primary and, while one exists, the next key, as one armored block with
// the SHA-256 that is its ETag (R-DEB-018). A repository without a primary or next key answers an empty block.
func (s keysStore) PublicCertificates(ctx context.Context, repoID int64) ([]byte, [32]byte, error) {
	var armored []byte
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		armored = nil
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		list, err := keysOf(ctx, qs, repoID, false)
		if err != nil {
			return err
		}
		var certs []openpgp.Certificate
		for _, state := range []string{rows.KeyPrimary, rows.KeyNext} {
			k, ok := list.inState(state)
			if !ok {
				continue
			}
			parsed, err := openpgp.ParseCertificates([]byte(k.PublicKey), time.Now(), openpgp.Limits{})
			if err != nil {
				return errs.E(errs.Internal, errs.CodeInternal, "A stored signing certificate cannot be read.", errs.WithCause(err))
			}
			certs = append(certs, parsed...)
		}
		if len(certs) > 0 {
			armored = openpgp.ArmorCertificates(certs...)
		}
		return nil
	})
	if err != nil {
		return nil, [32]byte{}, err
	}
	return armored, sha256.Sum256(armored), nil
}

// Keys lists the keys of the repository in every state with their public certificates, oldest first (R-DEB-015).
func (s keysStore) Keys(ctx context.Context, repoID int64) ([]rows.Key, error) {
	var out []rows.Key
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		out = nil
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		if _, err := lookupRepo(ctx, qs, repoID); err != nil {
			return err
		}
		list, err := keysOf(ctx, qs, repoID, false)
		if err != nil {
			return err
		}
		out = make([]rows.Key, 0, len(list))
		for _, k := range list {
			out = append(out, keyRow(k))
		}
		return nil
	})
	return out, err
}

// SigningKeys returns the keys that sign a build, the primary first and the next key after it, with the secret opened. The caller
// uses the secret for one signing call and drops it. A repository without a primary key returns none (the build's failure
// no_signing_key, R-DEB-016); a next key without a primary does not sign alone.
func (s keysStore) SigningKeys(ctx context.Context, repoID int64) ([]rows.SigningKey, error) {
	ring, err := s.ring()
	if err != nil {
		return nil, err
	}
	var out []rows.SigningKey
	err = s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		out = nil
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		list, err := keysOf(ctx, qs, repoID, false)
		if err != nil {
			return err
		}
		if _, ok := list.inState(rows.KeyPrimary); !ok {
			return nil
		}
		sealed, err := qs.DebSealedKeys(ctx, repoID)
		if err != nil {
			return err
		}
		for _, sk := range sealed {
			k, ok := list.byFingerprint(sk.Fingerprint)
			if !ok {
				continue
			}
			secret, err := ring.Open(SigningKeys, secrets.Row(sk.ID), sk.Sealed)
			if err != nil {
				// The error names the key ring problem, not the key.
				return errs.E(errs.Internal, errs.CodeInternal, "A signing key cannot be opened with the key ring.", errs.WithCause(err))
			}
			out = append(out, rows.SigningKey{Key: keyRow(k), Secret: secret})
		}
		return nil
	})
	return out, err
}

// CreateNext adds the next key, generated or imported (R-DEB-017). A repository has at most one: a second answers 409 deb_key_state.
// An imported key that does not meet the rules of R-DEB-015 answers 422 deb_key_unsupported; a fingerprint any repository holds in
// any state answers 409 deb_key_in_use, so a retired or withheld key cannot come back by import (Q-DEB-31).
func (s keysStore) CreateNext(ctx context.Context, in rows.NewKey) (rows.Key, error) {
	var out rows.Key
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		out = rows.Key{}
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		repo, err := lockRepo(ctx, qs, in.RepoID)
		if err != nil {
			return err
		}
		list, err := keysOf(ctx, qs, in.RepoID, true)
		if err != nil {
			return err
		}
		if _, exists := list.inState(rows.KeyNext); exists {
			return errs.E(errs.Conflict, errs.CodeDebKeyState, "The repository has a next key already: promote it or retire it first (R-DEB-017).")
		}
		origin := rows.KeyImported
		var key *openpgp.Key
		if in.Generate {
			origin = rows.KeyGenerated
			key, err = s.generate(ctx, qs, repo.Ref)
		} else {
			var now time.Time
			if now, err = dbNow(ctx, qs); err == nil {
				key, err = openpgp.ParseSecret(in.Armored, in.Passphrase, now, openpgp.Limits{})
				if err != nil && isImportRefusal(err) {
					return keyUnsupported(err)
				}
			}
		}
		if err != nil {
			return err
		}
		known, err := qs.DebFingerprintKnown(ctx, key.Fingerprint)
		if err != nil {
			return err
		}
		if known {
			return errs.E(errs.Conflict, errs.CodeDebKeyInUse, "This key is known to trove already, in this or another repository.")
		}
		if _, err := s.insertKey(ctx, qs, in.RepoID, newKeyRecord{key: key, state: rows.KeyNext, origin: origin, by: in.Actor}); err != nil {
			if uniqueViolation(err) {
				return errs.E(errs.Conflict, errs.CodeDebKeyState, "The key is known already, or the repository's keys changed meanwhile: try again (R-DEB-017).")
			}
			return err
		}
		if err := s.keysChanged(ctx, tx, qs, in.RepoID); err != nil {
			return err
		}
		if err := s.record(ctx, tx, repo, audit.EventDebSigningKeyCreated, in.Actor,
			map[string]any{"fingerprint": key.Fingerprint, "state": rows.KeyNext, "origin": origin}, nil); err != nil {
			return err
		}
		created, err := keysOf(ctx, qs, in.RepoID, false)
		if err != nil {
			return err
		}
		k, _ := created.byFingerprint(key.Fingerprint)
		out = keyRow(k)
		return nil
	})
	return out, err
}

// Promote makes the next key primary and retires the old primary: it stops signing, leaves signing-key.asc and its private key is
// erased. It writes deb.signing_key.promoted with the journal entry a restore's replay applies (R-DEB-017, R-DEB-032). A repository
// without a primary key (after a replay) promotes its next key and retires nobody. Without a next key it answers 409 deb_no_next_key.
func (s keysStore) Promote(ctx context.Context, repoID int64, actor string) (rows.Promotion, error) {
	var out rows.Promotion
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		out = rows.Promotion{}
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		repo, err := lockRepo(ctx, qs, repoID)
		if err != nil {
			return err
		}
		list, err := keysOf(ctx, qs, repoID, true)
		if err != nil {
			return err
		}
		next, ok := list.inState(rows.KeyNext)
		if !ok {
			return errs.E(errs.Conflict, errs.CodeDebNoNextKey, "The repository has no next key to promote (R-DEB-017).")
		}
		oldFingerprint := ""
		old, hadPrimary := list.inState(rows.KeyPrimary)
		if hadPrimary {
			oldFingerprint = old.Fingerprint
			if _, err := qs.DebRetireKey(ctx, q.DebRetireKeyParams{ID: old.ID, RetiredBy: uuidOrEmpty(actor)}); err != nil {
				return err
			}
		}
		if _, err := qs.DebSetKeyState(ctx, q.DebSetKeyStateParams{ID: next.ID, State: rows.KeyPrimary}); err != nil {
			return err
		}
		if err := s.keysChanged(ctx, tx, qs, repoID); err != nil {
			return err
		}
		details := map[string]any{"newFingerprint": next.Fingerprint}
		entry := map[string]any{"newFingerprint": next.Fingerprint}
		if hadPrimary {
			details["oldFingerprint"], entry["oldFingerprint"] = oldFingerprint, oldFingerprint
		}
		if err := s.record(ctx, tx, repo, audit.EventDebSigningKeyPromoted, actor, details, entry); err != nil {
			return err
		}
		after, err := keysOf(ctx, qs, repoID, false)
		if err != nil {
			return err
		}
		if k, ok := after.byFingerprint(next.Fingerprint); ok {
			out.New = keyRow(k)
		}
		if k, ok := after.byFingerprint(oldFingerprint); ok && hadPrimary {
			out.Old = keyRow(k)
		}
		return nil
	})
	return out, err
}

// RetireNext removes the next key before a promotion: its private key is erased, and deb.signing_key.retired is written with its
// journal entry (R-DEB-017). The primary key answers 409 deb_key_state; so does any key that is not the next key.
func (s keysStore) RetireNext(ctx context.Context, repoID int64, fingerprint, actor string) error {
	return s.retire(ctx, repoID, fingerprint, actor, rows.KeyNext)
}

// ConfirmWithheld confirms the retirement of a key the replay withheld: its private key is erased (R-DEB-032).
func (s keysStore) ConfirmWithheld(ctx context.Context, repoID int64, fingerprint, actor string) error {
	return s.retire(ctx, repoID, fingerprint, actor, rows.KeyWithheld)
}

func (s keysStore) retire(ctx context.Context, repoID int64, fingerprint, actor, want string) error {
	return s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		repo, err := lockRepo(ctx, qs, repoID)
		if err != nil {
			return err
		}
		list, err := keysOf(ctx, qs, repoID, true)
		if err != nil {
			return err
		}
		k, ok := list.byFingerprint(fingerprint)
		if !ok {
			return errs.E(errs.NotFound, errs.CodeNotFound, "The repository has no signing key with this fingerprint.")
		}
		if k.State != want {
			return errs.E(errs.Conflict, errs.CodeDebKeyState, "The key is "+k.State+"; only a "+want+" key can be retired this way (R-DEB-017, R-DEB-032).")
		}
		if _, err := qs.DebRetireKey(ctx, q.DebRetireKeyParams{ID: k.ID, RetiredBy: uuidOrEmpty(actor)}); err != nil {
			return err
		}
		// A withheld key did not sign, so confirming its retirement changes no build; the next key did.
		if want == rows.KeyNext {
			if err := s.keysChanged(ctx, tx, qs, repoID); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, repo, audit.EventDebSigningKeyRetired, actor,
			map[string]any{"fingerprint": fingerprint}, map[string]any{"fingerprint": fingerprint})
	})
}

// ReinstateKey returns a withheld key: to primary when the repository has none, else to next when it has none, else it answers 409
// deb_key_state (R-DEB-015). It writes deb.signing_key.reinstated.
func (s keysStore) ReinstateKey(ctx context.Context, repoID int64, fingerprint, actor string) (rows.Reinstated, error) {
	var out rows.Reinstated
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		out = rows.Reinstated{}
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		repo, err := lockRepo(ctx, qs, repoID)
		if err != nil {
			return err
		}
		list, err := keysOf(ctx, qs, repoID, true)
		if err != nil {
			return err
		}
		k, ok := list.byFingerprint(fingerprint)
		if !ok {
			return errs.E(errs.NotFound, errs.CodeNotFound, "The repository has no signing key with this fingerprint.")
		}
		if k.State != rows.KeyWithheld {
			return errs.E(errs.Conflict, errs.CodeDebKeyState, "The key is "+k.State+"; only a withheld key can be reinstated (R-DEB-032).")
		}
		to := rows.KeyPrimary
		if _, has := list.inState(rows.KeyPrimary); has {
			to = rows.KeyNext
			if _, has := list.inState(rows.KeyNext); has {
				return errs.E(errs.Conflict, errs.CodeDebKeyState, "The repository has a primary and a next key already; retire one first (R-DEB-015).")
			}
		}
		if _, err := qs.DebSetKeyState(ctx, q.DebSetKeyStateParams{ID: k.ID, State: to}); err != nil {
			return err
		}
		if err := s.keysChanged(ctx, tx, qs, repoID); err != nil {
			return err
		}
		if err := s.record(ctx, tx, repo, audit.EventDebSigningKeyReinstated, actor, map[string]any{"fingerprint": fingerprint, "state": to}, nil); err != nil {
			return err
		}
		after, err := keysOf(ctx, qs, repoID, false)
		if err != nil {
			return err
		}
		if r, ok := after.byFingerprint(fingerprint); ok {
			out.Key = keyRow(r)
		}
		return nil
	})
	return out, err
}

// WithholdKey is the replay of deb.signing_key.retired (keys_journal.go).
func (s keysStore) WithholdKey(ctx context.Context, tx meta.Tx, in rows.KeyRetiredEntry) (rows.ReplayResult, error) {
	return replayRetired(ctx, tx, in)
}

// PromoteByJournal is the replay of deb.signing_key.promoted (keys_journal.go).
func (s keysStore) PromoteByJournal(ctx context.Context, tx meta.Tx, in rows.KeyPromotedEntry) (rows.ReplayResult, error) {
	return replayPromoted(ctx, tx, in)
}
