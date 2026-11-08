package deb

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/meta"
)

// keysStore is the group of the data port that holds the signing keys of the local and virtual repositories (R-DEB-015 to R-DEB-018, R-DEB-032).
// It is a stub: the work package of the signing keys replaces this file and no other (apt-work-packages.md, 2.2).
type keysStore struct{ *group }

func (s keysStore) CreateFirstKey(context.Context, meta.Tx, int64, string) error {
	return errNotBuilt("the creation of the first signing key")
}

func (s keysStore) PublicCertificates(context.Context, int64) ([]byte, [32]byte, error) {
	return nil, [32]byte{}, errNotBuilt("the public certificates")
}

func (s keysStore) Keys(context.Context, int64) ([]rows.Key, error) {
	return nil, errNotBuilt("the key list")
}

func (s keysStore) SigningKeys(context.Context, int64) ([]rows.SigningKey, error) {
	return nil, errNotBuilt("the signing keys")
}

func (s keysStore) CreateNext(context.Context, rows.NewKey) (rows.Key, error) {
	return rows.Key{}, errNotBuilt("the creation of the next key")
}

func (s keysStore) Promote(context.Context, int64, string) (rows.Promotion, error) {
	return rows.Promotion{}, errNotBuilt("the promotion of a key")
}

func (s keysStore) RetireNext(context.Context, int64, string, string) error {
	return errNotBuilt("the retirement of the next key")
}

func (s keysStore) ConfirmWithheld(context.Context, int64, string, string) error {
	return errNotBuilt("the confirmation of a withheld key")
}

func (s keysStore) ReinstateKey(context.Context, int64, string, string) (rows.Reinstated, error) {
	return rows.Reinstated{}, errNotBuilt("the reinstatement of a key")
}

func (s keysStore) WithholdKey(context.Context, meta.Tx, rows.KeyRetiredEntry) (rows.ReplayResult, error) {
	return "", errNotBuilt("the replay of a key retirement")
}

func (s keysStore) PromoteByJournal(context.Context, meta.Tx, rows.KeyPromotedEntry) (rows.ReplayResult, error) {
	return "", errNotBuilt("the replay of a key promotion")
}
