package deb

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
)

// KeyStore is the part of the data port that holds the signing keys of the local and virtual repositories (R-DEB-015 to R-DEB-018,
// R-DEB-032). Built by the work package of the signing keys; the only implementation that imports the OpenPGP library. The format
// plug-in never sees a private key: SigningKeys is read by the index build, not by a handler, and returns the opened secret for one
// signing call.
type KeyStore interface {
	// CreateFirstKey generates the primary key of a new local or virtual repository in the transaction that creates it (the
	// creation hook of Q-REPO-273, bound in the composition root). repoKey is "<projectKey>/<repoKey>", the user ID's suffix.
	CreateFirstKey(ctx context.Context, tx formats.Tx, repoID int64, repoKey string) error
	// PublicCertificates returns the certificates signing-key.asc serves: the primary and, while one exists, the next key, as
	// one armored block, with the SHA-256 that is its ETag (R-DEB-018).
	PublicCertificates(ctx context.Context, repoID int64) (armored []byte, sha256 [32]byte, err error)
	// Keys lists the keys of the repository with their public certificates, in every state (R-DEB-015).
	Keys(ctx context.Context, repoID int64) ([]rows.Key, error)
	// SigningKeys returns the keys that sign a build: the primary and, while one exists, the next key, with the secret opened.
	// An empty primary is the build's failure no_signing_key (R-DEB-016).
	SigningKeys(ctx context.Context, repoID int64) ([]rows.SigningKey, error)
	// CreateNext adds the next key, generated or imported, and raises change_seq of every suite so that a build re-signs at once
	// (R-DEB-017). A repository has at most one next key: a second is refused with 409.
	CreateNext(ctx context.Context, in rows.NewKey) (rows.Key, error)
	// Promote makes the next key primary and retires the old one (its private key is erased) and writes
	// deb.signing_key.promoted with its journal entry (R-DEB-017).
	Promote(ctx context.Context, repoID int64, actor string) (rows.Promotion, error)
	// RetireNext removes the next key before a promotion and writes deb.signing_key.retired with its journal entry.
	RetireNext(ctx context.Context, repoID int64, fingerprint, actor string) error
	// ConfirmWithheld confirms the retirement of a withheld key: its private key is erased (R-DEB-032).
	ConfirmWithheld(ctx context.Context, repoID int64, fingerprint, actor string) error
	// ReinstateKey returns a withheld key: to primary when the repository has none, else to next when it has none, else it is
	// refused with deb_key_state (R-DEB-015).
	ReinstateKey(ctx context.Context, repoID int64, fingerprint, actor string) (rows.Reinstated, error)

	// WithholdKey is the replay of deb.signing_key.retired: the key is set withheld and keeps its sealed private key (Q-DEB-31).
	WithholdKey(ctx context.Context, tx formats.Tx, in rows.KeyRetiredEntry) (rows.ReplayResult, error)
	// PromoteByJournal is the replay of deb.signing_key.promoted: with the new key present the replay makes it primary and withholds
	// the old one, without it the old primary is withheld (R-DEB-032).
	PromoteByJournal(ctx context.Context, tx formats.Tx, in rows.KeyPromotedEntry) (rows.ReplayResult, error)
}
