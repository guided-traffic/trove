package ci

import (
	"context"

	"github.com/guided-traffic/trove/internal/ci/subject"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The key source of the exchange (R-CI-003, Q-CI-12): the in-process copy of the key rows, the
// unknown-kid protocol and the fail-closed age check. A restarted replica validates tokens without
// contacting the issuer, because the set is in PostgreSQL; nothing here reads a key from Valkey
// (R-ARCH-030 rule 2).

// keySource implements subject.KeySource for one issuer.
type keySource struct {
	s  *Service
	is Issuer
}

// KeySource returns the key lookup of an issuer for the subject-token validation.
func (s *Service) KeySource(is Issuer) subject.KeySource { return &keySource{s: s, is: is} }

// fetchOutcome is the result of the on-demand fetch of one flight.
type fetchOutcome struct {
	// attempted is false when the throttle refused the fetch.
	attempted bool
	err       error
}

// readKeyRow reads the key row of an issuer from PostgreSQL.
func (s *Service) readKeyRow(ctx context.Context, issuerID string) (*keyRow, error) {
	var row *keyRow
	err := s.o.DB.Read(ctx, func(db q.DBTX) error {
		rows, err := q.New(db).CiKeysGet(ctx, issuerID)
		if err != nil {
			return err
		}
		row = &keyRow{}
		if len(rows) > 0 {
			row = rowOf(rows[0])
		}
		return nil
	})
	return row, err
}

// keyRow returns the key row from the in-process copy, reading and filling it under the fill rule of
// R-ARCH-018 on a miss.
func (s *Service) keyRow(ctx context.Context, issuerID string) (*keyRow, error) {
	return s.keys.Load(ctx, issuerID, func(ctx context.Context) (*keyRow, error) { return s.readKeyRow(ctx, issuerID) })
}

// stale reports whether a stored set is older than ci.signing_keys.max_stale.
func (s *Service) stale(row *keyRow) bool {
	return row.hasSet() && s.since(row.FetchedAt) > s.o.Config.SigningKeys.MaxStale
}

// reread re-reads the key row from PostgreSQL, bypassing the copy, at most once per issuer and replica per
// cluster.epoch_poll_interval (R-CI-003 step 1). Concurrent callers share one read, and a caller that
// finds a read younger than the interval gets the copy, which that read filled. A key that any replica or
// the task has stored is therefore accepted on every replica within that interval, whether or not the
// change message has arrived.
func (s *Service) reread(ctx context.Context, issuerID string) (*keyRow, error) {
	row, _, err := s.rereads.do(issuerID, func() (*keyRow, error) {
		dctx := context.WithoutCancel(ctx)
		st := s.state(issuerID)
		s.mu.Lock()
		due := s.since(st.lastReread) >= s.o.EpochPollInterval
		if due {
			st.lastReread = s.now()
		}
		s.mu.Unlock()
		if due {
			s.keys.Invalidate(issuerID)
		}
		return s.keyRow(dctx, issuerID)
	})
	return row, err
}

// fetchOnDemand fetches the issuer's keys for an unknown kid, at most once per
// ci.signing_keys.unknown_kid_refresh_interval per issuer and replica, the fetch shared by every request
// that asks while it runs (R-CI-003 step 2). The fetch runs on a context of its own, so one request that
// gives up does not cancel it for the others; it ends within the fetch timeouts.
func (s *Service) fetchOnDemand(ctx context.Context, is Issuer) fetchOutcome {
	out, _, _ := s.fetches.do(is.ID, func() (fetchOutcome, error) {
		st := s.state(is.ID)
		s.mu.Lock()
		due := s.since(st.lastFetch) >= s.o.Config.SigningKeys.UnknownKIDRefreshInterval
		if due {
			st.lastFetch = s.now()
		}
		s.mu.Unlock()
		if !due {
			return fetchOutcome{}, nil
		}
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*s.o.Config.SigningKeys.FetchTimeout)
		defer cancel()
		err := s.refresh(fctx, is, TriggerUnknownKID, systemActor("ci-exchange"))
		// A store of an unchanged set sends no change message, so nothing has dropped the copy although the
		// row's fetched_at moved: drop it here, so the lookup that follows reads the row.
		s.keys.Invalidate(is.ID)
		return fetchOutcome{attempted: true, err: err}, nil
	})
	return out
}

// lookup finds the keys for a token in a row. ok is false for an unknown kid; without a kid it is false
// while no set is stored: such a token counts as an unknown kid (R-CI-003).
func lookup(row *keyRow, kid, alg string) (keys []subject.Key, ok bool) {
	if kid != "" {
		keys = row.Set.find(kid, alg)
		return keys, len(keys) > 0
	}
	return row.Set.find("", alg), row.hasSet()
}

// Keys implements subject.KeySource.
func (k *keySource) Keys(ctx context.Context, kid, alg string) ([]subject.Key, error) {
	s, id := k.s, k.is.ID
	row, err := s.keyRow(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.stale(row) {
		// Fail closed (R-CI-003 "Fetch failures"): a refresh that stored an unchanged set sends no change
		// message, so the copy can be older than the row. Re-read before answering 503, and try to recover
		// with the throttled fetch of an unknown kid before giving up.
		if row, err = s.reread(ctx, id); err != nil {
			return nil, err
		}
		if s.stale(row) {
			s.fetchOnDemand(ctx, k.is)
			if row, err = s.keyRow(ctx, id); err != nil {
				return nil, err
			}
			if s.stale(row) {
				return nil, subject.ErrKeysUnavailable
			}
		}
	}
	if keys, ok := lookup(row, kid, alg); ok {
		return keys, nil
	}
	return k.unknown(ctx, kid, alg)
}

// unknown is the protocol of R-CI-003 for a kid the copy does not hold: re-read the row (throttled), then
// fetch (throttled), then reject. A forged token picks its kid freely and is refused only by the signature
// check, so neither step runs once per request: random kids cost at most one row read per issuer, replica
// and epoch_poll_interval and one fetch per issuer, replica and unknown_kid_refresh_interval.
func (k *keySource) unknown(ctx context.Context, kid, alg string) ([]subject.Key, error) {
	s, id := k.s, k.is.ID
	row, err := s.reread(ctx, id)
	if err != nil {
		return nil, err
	}
	if keys, ok := lookup(row, kid, alg); ok {
		return keys, nil
	}
	s.fetchOnDemand(ctx, k.is)
	if row, err = s.keyRow(ctx, id); err != nil {
		return nil, err
	}
	if keys, ok := lookup(row, kid, alg); ok {
		return keys, nil
	}
	if !row.hasSet() {
		// Nothing stored and nothing could be fetched.
		return nil, subject.ErrKeysUnavailable
	}
	return nil, subject.ErrUnknownKey
}
