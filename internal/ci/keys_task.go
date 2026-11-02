package ci

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Keys returns the stored key set status without contacting the issuer (R-CI-002): the key IDs, types and
// algorithms with the keys that were ignored and why, the fetch time, the next refresh and the last error.
func (s *Service) Keys(ctx context.Context, name string) (KeyStatus, error) {
	var st KeyStatus
	found := false
	err := s.o.DB.Read(ctx, func(db q.DBTX) error {
		qs := q.New(db)
		rows, err := qs.CiIssuerByName(ctx, name)
		if err != nil || len(rows) == 0 {
			return err
		}
		found = true
		krows, err := qs.CiKeysGet(ctx, rows[0].ID)
		if err != nil {
			return err
		}
		row := &keyRow{}
		if len(krows) > 0 {
			row = rowOf(krows[0])
		}
		st = row.status(name)
		return nil
	})
	if err != nil {
		return KeyStatus{}, err
	}
	if !found {
		return KeyStatus{}, notFound(name)
	}
	return st, nil
}

// RefreshKeys fetches the discovery document and the key set of an issuer now, on this replica and outside
// any transaction, stores the result under the write rule of R-CI-003 and records ci.keys.refreshed or
// ci.keys.refresh_failed. An issuer that cannot be fetched answers 502; the failure is in the status.
func (s *Service) RefreshKeys(ctx context.Context, caller Actor, name string, trigger RefreshTrigger) (KeyStatus, error) {
	is, err := s.byName(ctx, name)
	if err != nil {
		return KeyStatus{}, err
	}
	fctx, cancel := context.WithTimeout(ctx, 3*s.o.Config.SigningKeys.FetchTimeout)
	defer cancel()
	if err := s.refresh(fctx, is, trigger, actorOf(caller)); err != nil {
		var fe *FetchError
		if errors.As(err, &fe) {
			return KeyStatus{}, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed,
				"The issuer's discovery document or key set could not be fetched: "+fe.Error()+".", errs.WithCause(err))
		}
		return KeyStatus{}, err
	}
	return s.Keys(ctx, name)
}

// byName reads one issuer row without its summary.
func (s *Service) byName(ctx context.Context, name string) (Issuer, error) {
	var is Issuer
	found := false
	err := s.o.DB.Read(ctx, func(db q.DBTX) error {
		rows, err := q.New(db).CiIssuerByName(ctx, name)
		if err != nil || len(rows) == 0 {
			return err
		}
		found, is = true, toIssuer(q.CiIssuerByIDRow(rows[0]))
		return nil
	})
	if err != nil {
		return Issuer{}, err
	}
	if !found {
		return Issuer{}, notFound(name)
	}
	return is, nil
}

// dueConcurrency bounds the fetches of one run of the task in flight at a time.
const dueConcurrency = 4

// RefreshDue is the body of the task ci-keys-refresh (R-CI-030): it fetches every enabled issuer whose
// next_refresh_at has passed or that has no row, each outside a transaction and within the fetch timeout,
// and stores each under the write rule. A failure is recorded in that issuer's last_error and does not stop
// the run for the others. The task is idempotent: an issuer another replica fetched meanwhile is fetched
// again with no other effect (R-CI-003). It returns the number of issuers whose set was stored.
func (s *Service) RefreshDue(ctx context.Context) (int, error) {
	var due []q.CiKeysDueRow
	err := s.o.DB.Read(ctx, func(db q.DBTX) (err error) {
		due, err = q.New(db).CiKeysDue(ctx)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("ci-keys-refresh: %w", err)
	}
	var refreshed atomic.Int64
	var wg sync.WaitGroup
	sem := make(chan struct{}, dueConcurrency)
	for _, d := range due {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			is, err := s.byName(ctx, d.Name)
			if err != nil || !is.Enabled {
				return
			}
			fctx, cancel := context.WithTimeout(ctx, 3*s.o.Config.SigningKeys.FetchTimeout)
			defer cancel()
			if err := s.refresh(fctx, is, TriggerScheduled, systemActor("ci-keys-refresh")); err != nil {
				s.o.Log.Warn("a CI signing key refresh failed", "issuer", is.Name, "error", err)
				return
			}
			refreshed.Add(1)
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return int(refreshed.Load()), err
	}
	return int(refreshed.Load()), nil
}
