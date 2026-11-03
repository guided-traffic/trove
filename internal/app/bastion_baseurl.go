package app

import (
	"context"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// bastionBaseURLs reads the base URL a Bastion registration reported at its most recent
// handshake, for client-setup with bastion=<name> (R-BAS-007). It runs two statements of the
// Bastion files, BastionGet (does the registration exist) and BastionLastBaseURL, in one read.
type bastionBaseURLs struct{ db *postgres.DB }

var _ v1.BastionBaseURLs = bastionBaseURLs{}

// ReportedBaseURL implements v1.BastionBaseURLs.
func (b bastionBaseURLs) ReportedBaseURL(ctx context.Context, name string) (string, bool, error) {
	var (
		base       string
		registered bool
	)
	err := b.db.Read(ctx, func(d q.DBTX) error {
		base, registered = "", false
		qs := q.New(d)
		rows, err := qs.BastionGet(ctx, name)
		if err != nil || len(rows) == 0 {
			return err
		}
		registered = true
		urls, err := qs.BastionLastBaseURL(ctx, name)
		if err != nil {
			return err
		}
		if len(urls) > 0 {
			base = urls[0]
		}
		return nil
	})
	return base, registered, err
}
