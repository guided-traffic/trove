package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/observability"
	"github.com/guided-traffic/trove/internal/secrets"
)

// The stored secret fields of a repository (R-REPO-033). A field is a JSON pointer
// into the configuration document; its row in repository_secrets holds the value
// sealed under the key ring, or the reference to a file or a variable. The
// document itself holds only the marker or the reference, so neither the
// configuration, nor an audit event, nor a pg_dump of the table carries a
// plaintext.

// SealSecret seals the plaintext of a secret field of a repository. The row key,
// the repository id and the field, is part of the associated data: a sealed value
// copied into another repository's row, or another field's, does not open.
func SealSecret(ring *secrets.KeyRing, repoID int64, field string, v observability.Secret) (string, error) {
	if ring == nil {
		return "", errors.New("repository: no key ring to seal a secret")
	}
	return ring.Seal(RepositorySecrets, secretRow(repoID, field), []byte(v.Reveal()))
}

// OpenSecret opens a sealed secret field. A value that was moved, sealed under a
// key the ring lacks or damaged fails; the caller reports it as
// upstream_secret_unavailable without the cause.
func OpenSecret(ring *secrets.KeyRing, repoID int64, field, sealed string) (observability.Secret, error) {
	if ring == nil {
		return "", errors.New("repository: no key ring to open a secret")
	}
	b, err := ring.Open(RepositorySecrets, secretRow(repoID, field), sealed)
	if err != nil {
		return "", err
	}
	return observability.Secret(b), nil
}

func secretRow(repoID int64, field string) secrets.RowKey {
	return secrets.Row(strconv.FormatInt(repoID, 10), field)
}

// StoredSecret is one row of repository_secrets: a sealed value or a reference.
type StoredSecret struct {
	Field  string
	Sealed string     // empty for a reference
	Ref    *SecretRef // nil for a sealed value
}

// SecretStore reads the secret fields of repositories, for the upstream clients
// of the proxy: they unseal the values themselves, on the replica that uses them.
type SecretStore struct{ run meta.Runner }

// NewSecretStore returns the reader over the runner.
func NewSecretStore(run meta.Runner) *SecretStore { return &SecretStore{run: run} }

// Secrets returns the stored secret fields of a repository, sorted by field.
func (s *SecretStore) Secrets(ctx context.Context, repoID int64) ([]StoredSecret, error) {
	var rows []q.ListRepositorySecretsRow
	err := s.run.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rows, err = qs.ListRepositorySecrets(ctx, repoID)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]StoredSecret, 0, len(rows))
	for _, r := range rows {
		st := StoredSecret{Field: r.Field, Sealed: r.Sealed}
		if r.SecretRef != "" {
			var ref SecretRef
			if err := json.Unmarshal([]byte(r.SecretRef), &ref); err != nil {
				return nil, fmt.Errorf("repository: the reference of %s of repository %d: %w", r.Field, repoID, err)
			}
			st.Ref = &ref
		}
		out = append(out, st)
	}
	return out, nil
}

// RemoteDeps are what the service needs to store and probe the secrets and the
// upstream of remote repositories. The composition root binds them once the proxy
// components exist (Service.Bind); a service without them refuses a document that
// carries an inline secret and an upstream test.
type RemoteDeps struct {
	// Ring seals the inline values of secret fields (R-ARCH-021).
	Ring *secrets.KeyRing
	// Prober performs the upstream test (R-REPO-024).
	Prober UpstreamProber
	// OnUpstreamChanged runs in the transaction of an update that changed what the
	// repository's cached answers are evidence of: url, extraHosts, authMode,
	// username, password, clientCertificate or headers (R-REPO-011). The cache
	// invalidation of R-REPO-045 binds it to record an invalidation of scope all
	// and an empty prefix. Nil does nothing.
	OnUpstreamChanged func(ctx context.Context, tx meta.Tx, repo Repository) error
}

// Bind sets the dependencies of the remote settings. It is called once, before
// the service takes requests.
func (s *Service) Bind(d RemoteDeps) { s.remote.Store(&d) }

func (s *Service) remoteDeps() RemoteDeps {
	if d := s.remote.Load(); d != nil {
		return *d
	}
	return RemoteDeps{}
}

// writeSecrets stores the secret fields of a validated document in the
// transaction that writes the repository row: new values are sealed under the row
// key, references stored as they are, a kept marker is checked against the stored
// sealed value, and every field the document no longer carries is removed.
func (s *Service) writeSecrets(ctx context.Context, tx meta.Tx, repoID int64, pending []pendingSecret) error {
	qs, err := queries(tx)
	if err != nil {
		return err
	}
	keep := make([]string, 0, len(pending))
	var existing map[string]q.ListRepositorySecretsRow
	for _, p := range pending {
		keep = append(keep, p.field)
		switch p.op {
		case secretSeal:
			ring := s.remoteDeps().Ring
			if ring == nil {
				return errs.E(errs.Unavailable, errs.CodeUnavailable, "The secret store is not available on this replica; retry on another one.",
					errs.WithRetryAfter(time.Second), errs.WithOp("repository.writeSecrets"))
			}
			sealed, err := SealSecret(ring, repoID, p.field, p.value)
			if err != nil {
				return fmt.Errorf("repository: seal %s of repository %d: %w", p.field, repoID, err)
			}
			if err := qs.UpsertSealedRepositorySecret(ctx, q.UpsertSealedRepositorySecretParams{RepoID: repoID, Field: p.field, Sealed: sealed}); err != nil {
				return err
			}
		case secretReference:
			ref, err := json.Marshal(p.ref)
			if err != nil {
				return err
			}
			if err := qs.UpsertReferenceRepositorySecret(ctx, q.UpsertReferenceRepositorySecretParams{RepoID: repoID, Field: p.field, SecretRef: string(ref)}); err != nil {
				return err
			}
		case secretKeep:
			if existing == nil {
				rows, err := qs.ListRepositorySecrets(ctx, repoID)
				if err != nil {
					return err
				}
				existing = make(map[string]q.ListRepositorySecretsRow, len(rows))
				for _, r := range rows {
					existing[r.Field] = r
				}
			}
			if r, ok := existing[p.field]; !ok || r.Sealed == "" {
				return errs.E(errs.Invalid, errs.CodeValidation, "1 field is invalid.", errs.WithFields(errs.FieldError{
					Pointer: p.field, Code: codeInvalidSecret,
					Detail: `{"set": true} keeps a stored value, and this repository has none; send {"value": …} or {"secretRef": …}`,
				}))
			}
		}
	}
	slices.Sort(keep)
	_, err = qs.DeleteRepositorySecretsExcept(ctx, q.DeleteRepositorySecretsExceptParams{RepoID: repoID, Keep: keep})
	return err
}

// hasSecretWrites reports whether the document carries a secret that must be
// written even when the stored configuration is unchanged: a new value, whose
// marker reads the same as the old one, or a reference.
func hasSecretWrites(pending []pendingSecret) bool {
	for _, p := range pending {
		if p.op != secretKeep {
			return true
		}
	}
	return false
}

// ResealResult counts a run of ResealSecrets.
type ResealResult struct {
	// Resealed values were sealed under another key and now are under the active one.
	Resealed int64
	// Current values were under the active key already.
	Current int64
	// Failed values could not be opened (a key the ring lacks, a moved or damaged
	// value); they are left as they are.
	Failed int64
}

// ResealSecrets seals every stored secret value of repositories under the active
// key of the ring (R-ARCH-021 rotation step 3): the walk of the task
// secrets-reencrypt over the column repository_secrets.sealed. It is idempotent
// and works in batches of one transaction each, so a value is never unreadable
// for a moment: the old key stays in the ring until the status shows no value
// under it.
func ResealSecrets(ctx context.Context, run meta.Runner, ring *secrets.KeyRing, batch int) (ResealResult, error) {
	if batch <= 0 {
		batch = 200
	}
	var res ResealResult
	var afterRepo int64
	var afterField string
	for {
		var n int
		err := run.InTx(ctx, func(tx meta.Tx) error {
			qs, err := queries(tx)
			if err != nil {
				return err
			}
			rows, err := qs.ListSealedRepositorySecretsAfter(ctx, q.ListSealedRepositorySecretsAfterParams{
				AfterRepo: afterRepo, AfterField: afterField, Batch: int64(batch),
			})
			if err != nil {
				return err
			}
			n = len(rows)
			for _, r := range rows {
				afterRepo, afterField = r.RepoID, r.Field
				sealed, changed, err := ring.Reseal(RepositorySecrets, secretRow(r.RepoID, r.Field), r.Sealed)
				switch {
				case err != nil:
					res.Failed++
				case !changed:
					res.Current++
				default:
					if err := qs.UpdateSealedRepositorySecret(ctx, q.UpdateSealedRepositorySecretParams{RepoID: r.RepoID, Field: r.Field, Sealed: sealed}); err != nil {
						return err
					}
					res.Resealed++
				}
			}
			return nil
		})
		if err != nil {
			return res, err
		}
		if n < batch {
			return res, nil
		}
	}
}
