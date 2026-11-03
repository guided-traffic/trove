package ci

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

func notFound(name string) error {
	return errs.E(errs.NotFound, errs.CodeNotFound, fmt.Sprintf("The CI issuer %q does not exist.", name))
}

// List returns every issuer with its mapping count and key summary, ordered by name.
func (s *Service) List(ctx context.Context) ([]Issuer, error) {
	var rows []q.CiIssuerListRow
	err := s.o.DB.Read(ctx, func(db q.DBTX) (err error) {
		rows, err = q.New(db).CiIssuerList(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]Issuer, 0, len(rows))
	for _, r := range rows {
		out = append(out, toIssuerFromList(r))
	}
	return out, nil
}

// Get returns one issuer with its mapping count and key summary.
func (s *Service) Get(ctx context.Context, name string) (Issuer, error) {
	var is Issuer
	found := false
	err := s.o.DB.Read(ctx, func(db q.DBTX) error {
		qs := q.New(db)
		rows, err := qs.CiIssuerByName(ctx, name)
		if err != nil || len(rows) == 0 {
			return err
		}
		found = true
		is = toIssuer(q.CiIssuerByIDRow(rows[0]))
		n, err := qs.CiIssuerMappingCount(ctx, is.ID)
		if err != nil {
			return err
		}
		if len(n) > 0 {
			is.MappingCount = int(n[0])
		}
		krows, err := qs.CiKeysGet(ctx, is.ID)
		if err != nil {
			return err
		}
		is.Keys = &KeySummary{}
		if len(krows) > 0 {
			row := rowOf(krows[0])
			is.Keys = row.summary()
		}
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

// loadSet reads every issuer for the in-process copy. The read counts as the latest re-read of the issuers
// (rereadIssuers).
func (s *Service) loadSet(ctx context.Context) (*issuerSet, error) {
	s.mu.Lock()
	s.issuersReadAt = s.now()
	s.mu.Unlock()
	list, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	set := &issuerSet{byIssuer: make(map[string]Issuer, len(list)), byID: make(map[string]Issuer, len(list)), list: list}
	for _, is := range list {
		set.byIssuer[is.Issuer] = is
		set.byID[is.ID] = is
	}
	return set, nil
}

func (s *Service) set(ctx context.Context) (*issuerSet, error) {
	return s.issuers.Load(ctx, struct{}{}, s.loadSet)
}

// Choices lists the issuers read-only for a project administrator (Q-CI-26): the name, the kind, the
// issuer URL and whether it is public, from the in-process copy, ordered by name.
func (s *Service) Choices(ctx context.Context) ([]IssuerChoice, error) {
	set, err := s.set(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]IssuerChoice, 0, len(set.list))
	for _, is := range set.list {
		out = append(out, IssuerChoice{Name: is.Name, Kind: is.Kind, Issuer: is.Issuer, PublicIssuer: is.PublicIssuer})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Resolve returns the issuer whose `issuer` equals iss exactly (R-CI-004 step 5), from the in-process
// copy; an enabled hit makes no query while the copy is current. The comparison is on the stored
// string, byte for byte: no trailing-slash or case normalization (R-CI-001).
//
// A miss and a disabled hit re-read the issuers (rereadIssuers, Q-CI-51), at most once per replica and
// cluster.epoch_poll_interval: a replica whose copy lags (its change message lost, its change-log poll stalled)
// serves a just-created or re-enabled issuer within that interval instead of after the copy's SafetyTTL, and a
// flood of made-up iss values costs one read of the issuers per interval and replica, never one per request.
func (s *Service) Resolve(ctx context.Context, iss string) (Issuer, bool, error) {
	set, err := s.set(ctx)
	if err != nil {
		return Issuer{}, false, err
	}
	if is, ok := set.byIssuer[iss]; ok && is.Enabled {
		return is, true, nil
	}
	if set, err = s.rereadIssuers(ctx); err != nil {
		return Issuer{}, false, err
	}
	is, ok := set.byIssuer[iss]
	return is, ok, nil
}

// rereadIssuers reads the issuers again, bypassing the copy, when the last read is at least
// cluster.epoch_poll_interval old; otherwise, and for the callers that arrive while one runs, it answers the copy
// that read filled. The throttle is per-replica state that changes an answer for at most one interval (R-ARCH-032),
// the same bound as the unknown-kid re-read of R-CI-003.
func (s *Service) rereadIssuers(ctx context.Context) (*issuerSet, error) {
	set, _, err := s.issuerRereads.do("issuers", func() (*issuerSet, error) {
		s.mu.Lock()
		due := s.since(s.issuersReadAt) >= s.o.EpochPollInterval
		s.mu.Unlock()
		if due {
			s.issuers.Invalidate(struct{}{})
		}
		return s.set(context.WithoutCancel(ctx))
	})
	return set, err
}

// LockShareTx re-reads the issuer row FOR SHARE in the exchange's transaction, bypassing the copy
// (R-CI-007): a concurrent write of the issuer waits for the exchange, and the exchange compares the
// versions with what it evaluated.
func (s *Service) LockShareTx(ctx context.Context, tx meta.Tx, issuerID string) (Issuer, error) {
	qs, err := queriesOf(tx)
	if err != nil {
		return Issuer{}, err
	}
	rows, err := qs.CiIssuerByIDShare(ctx, issuerID)
	if err != nil {
		return Issuer{}, err
	}
	if len(rows) == 0 {
		return Issuer{}, notFound(issuerID)
	}
	return toIssuer(q.CiIssuerByIDRow(rows[0])), nil
}

// Reload drops the replica's copy of the issuers after the exchange found it behind the row (R-CI-007);
// the next lookup fills it again under the fill rule of R-ARCH-018, which skips a fill that raced a
// change message.
func (s *Service) Reload(_ context.Context, _ Issuer) { s.issuers.Invalidate(struct{}{}) }

// since is the age of a time on this service's clock.
func (s *Service) since(t time.Time) time.Duration { return s.now().Sub(t) }
