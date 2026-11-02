package ci

import (
	"context"
	"errors"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The write rule of R-CI-003. A fetch runs outside any transaction and holds no connection while it waits
// for the issuer; its result is then written in one short transaction on the issuer's ci_issuer_keys row
// alone: the row is locked FOR UPDATE (or inserted), a set stored with a later fetched_at is kept, the
// ci_issuers row is never locked (every exchange holds it FOR SHARE, R-CI-007, so a key store neither
// waits for an exchange nor blocks one), next_refresh_at follows the response's Cache-Control and the
// change area ci_issuer_keys is notified only when the content of the set changed, so several replicas
// that fetch the same set at once cause no extra reload anywhere.

// sqlstateForeignKey is the SQLSTATE of the key row of an issuer that was deleted while a fetch ran.
const sqlstateForeignKey = "23503"

// keyRow is the in-process copy of a key row and the status read from it.
type keyRow struct {
	// Exists is false for an issuer without a row.
	Exists bool
	Set    keySet
	// JWKSURI is where the set came from.
	JWKSURI string
	// FetchedAt is the time of the fetch that produced the stored set; the zero time marks the
	// placeholder row of an issuer whose first fetch failed (no set).
	FetchedAt     time.Time
	NextRefreshAt time.Time
	LastError     string
	LastErrorAt   time.Time
}

// hasSet reports whether a fetched set is stored.
func (r *keyRow) hasSet() bool { return r != nil && r.Exists && !r.FetchedAt.IsZero() }

func microTime(us int64) time.Time {
	if us <= 0 {
		return time.Time{}
	}
	return time.UnixMicro(us).UTC()
}

func optTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// rowOf maps a CiKeysGet row; a stored set that does not parse counts as no set.
func rowOf(r q.CiKeysGetRow) *keyRow {
	row := &keyRow{
		Exists: true, JWKSURI: r.JwksUri, FetchedAt: microTime(r.FetchedUs), NextRefreshAt: microTime(r.NextRefreshUs),
		LastError: r.LastError, LastErrorAt: microTime(r.LastErrorUs),
	}
	if set, err := parseStored(r.Jwks); err == nil {
		row.Set = set
	}
	return row
}

// summary is the short form on the issuer.
func (r *keyRow) summary() *KeySummary {
	sum := &KeySummary{LastError: r.LastError, LastErrorAt: optTime(r.LastErrorAt), FetchedAt: optTime(r.FetchedAt)}
	sum.Usable = len(r.Set.Usable)
	sum.Ignored = len(r.Set.Infos) - len(r.Set.Usable)
	return sum
}

// status is the answer of the key status operations.
func (r *keyRow) status(issuer string) KeyStatus {
	st := KeyStatus{Issuer: issuer, Keys: []KeyInfo{}}
	if r == nil || !r.Exists {
		return st
	}
	st.JWKSURI = r.JWKSURI
	st.Keys = append(st.Keys, r.Set.Infos...)
	st.FetchedAt, st.NextRefreshAt = optTime(r.FetchedAt), optTime(r.NextRefreshAt)
	st.LastError, st.LastErrorAt = r.LastError, optTime(r.LastErrorAt)
	return st
}

// systemActor is the actor of the events a fetch that no administrator started writes.
func systemActor(name string) audit.Actor { return audit.Actor{Kind: "system", Name: name} }

func (s *Service) count(issuer string, trigger RefreshTrigger, outcome string) {
	if s.o.Metrics != nil {
		s.o.Metrics.KeysRefresh.WithLabelValues(issuer, string(trigger), outcome).Inc()
	}
}

func keysEvent(name string, actor audit.Actor, is Issuer, details map[string]any) audit.Event {
	details["issuer"] = is.Name
	return audit.Event{
		Type: name, Actor: actor, Outcome: audit.Success,
		Subject: audit.Subject{Kind: "ci_issuer", ID: is.ID, Name: is.Name}, Details: mustJSON(details),
	}
}

// storeKeysTx writes a fetched set in the caller's transaction under the write rule and records
// ci.keys.refreshed. It reports whether the content changed; the change area ci_issuer_keys is notified
// only then. The transaction touches the key row alone (the caller of a create has just inserted the
// issuer row itself).
func (s *Service) storeKeysTx(ctx context.Context, tx meta.Tx, qs *q.Queries, is Issuer, res *fetched, trigger RefreshTrigger, actor audit.Actor) (bool, error) {
	lock, err := qs.CiKeysLock(ctx, q.CiKeysLockParams{IssuerID: is.ID, Jwks: string(res.Stored)})
	if err != nil {
		return false, err
	}
	changed, kept := true, false
	if len(lock) > 0 {
		// A fetch that finished later stored its set already: keep it.
		kept = lock[0].FetchedUs >= res.FetchedAt.UnixMicro()
		changed = !lock[0].Same || lock[0].FetchedUs <= 0
	}
	if kept {
		changed = false
	} else {
		if err := qs.CiKeysStore(ctx, q.CiKeysStoreParams{
			IssuerID: is.ID, Jwks: string(res.Stored), JwksUri: res.JWKSURI, FetchedUs: res.FetchedAt.UnixMicro(), Etag: res.ETag,
			NextRefreshSecs: int64(nextRefresh(s.o.Config.SigningKeys, res.MaxAge) / time.Second),
		}); err != nil {
			return false, err
		}
		if changed {
			if err := s.o.Changes.Notify(tx, cluster.AreaCIIssuerKeys, changePayloadVersion, issuerChange{Issuer: is.ID}); err != nil {
				return false, err
			}
		}
	}
	ev := keysEvent(audit.EventCIKeysRefreshed, actor, is, map[string]any{"trigger": string(trigger), "kids": res.Set.kids(), "changed": changed})
	return changed, s.o.Recorder.RecordTx(ctx, tx, ev)
}

// refresh fetches the discovery document and the key set of an issuer and stores the result, or records
// the failure (R-CI-003): the sole path of every fetch, whatever triggered it. The error of a failed fetch
// is a *FetchError. ctx bounds the whole call; the fetch itself runs outside any transaction.
func (s *Service) refresh(ctx context.Context, is Issuer, trigger RefreshTrigger, actor audit.Actor) error {
	res, err := s.fetcher.fetch(ctx, is)
	if err != nil {
		s.recordFailure(ctx, is, err, trigger, actor)
		return err
	}
	err = s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		_, err = s.storeKeysTx(ctx, tx, qs, is, res, trigger, actor)
		return err
	})
	if err != nil {
		// The issuer was deleted while the fetch ran: nothing to store.
		if postgres.IsState(err, sqlstateForeignKey) {
			return nil
		}
		s.count(is.Name, trigger, OutcomeError)
		return err
	}
	s.count(is.Name, trigger, OutcomeSuccess)
	return nil
}

// recordFailure writes the failure of a fetch to the key row and records ci.keys.refresh_failed: the class,
// the HTTP status and the host, never a byte of a response body. A fetch that was cancelled (the replica
// drains) leaves the stored set as it was and records nothing.
func (s *Service) recordFailure(ctx context.Context, is Issuer, err error, trigger RefreshTrigger, actor audit.Actor) {
	var fe *FetchError
	if !errors.As(err, &fe) {
		fe = &FetchError{Class: classConnection}
	}
	s.count(is.Name, trigger, OutcomeError)
	if fe.Class == classCanceled {
		return
	}
	uri := derefStr(is.JWKSURI)
	if uri == "" {
		uri = derefStr(is.DiscoveryURL)
	}
	if uri == "" {
		uri = is.Issuer
	}
	werr := s.o.DB.InTx(context.WithoutCancel(ctx), func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		if err := qs.CiKeysRecordError(ctx, q.CiKeysRecordErrorParams{
			IssuerID: is.ID, JwksUri: uri, LastError: fe.Error(),
			RetrySecs: int64(s.o.Config.SigningKeys.MinRefreshInterval / time.Second),
		}); err != nil {
			return err
		}
		return s.o.Recorder.RecordTx(ctx, tx, keysEvent(audit.EventCIKeysRefreshFailed, actor, is, map[string]any{
			"trigger": string(trigger), "errorClass": fe.Class, "httpStatus": fe.Status, "host": fe.Host,
		}))
	})
	if werr != nil && !postgres.IsState(werr, sqlstateForeignKey) {
		s.o.Log.Warn("a CI key fetch failure could not be recorded", "issuer", is.Name, "error", werr)
	}
}
