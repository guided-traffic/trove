package invalidation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/repository"
)

// Notifier attaches a change of a change area to the writing transaction
// (R-ARCH-018 step 1). *cluster.Changes implements it.
type Notifier interface {
	Notify(tx meta.Tx, area string, version int, payload any) error
}

// ServiceOptions configure [NewService].
type ServiceOptions struct {
	Runner   meta.Runner
	Notifier Notifier
	// Recorder records repository.cache_invalidated in the transaction of the call;
	// nil discards the events.
	Recorder audit.Recorder
}

// Service is the write side of the invalidations and the reader of the cache-entry
// list.
type Service struct{ o ServiceOptions }

// NewService creates a Service.
func NewService(o ServiceOptions) (*Service, error) {
	if o.Runner == nil || o.Notifier == nil {
		return nil, errors.New("invalidation: the service needs a runner and a notifier")
	}
	if o.Recorder == nil {
		o.Recorder = audit.Nop{}
	}
	return &Service{o: o}, nil
}

// eventDetails are the data of repository.cache_invalidated.
type eventDetails struct {
	Path  string `json:"path"`
	Scope string `json:"scope"`
	Count int64  `json:"count"`
}

// Invalidate makes the cached metadata (scope metadata), the cached misses (negative)
// or both (all) of repository repo under prefix count as expired, and returns the
// number of covered metadata entries (R-REPO-045, R-OPS-021). An empty scope is
// metadata and an empty prefix is the whole repository.
//
// On a remote repository one transaction inserts the row, counts the covered
// entries, records the change in the area cache_invalidation and the audit event,
// so the row is in force on every replica within cluster.epoch_poll_interval. On a
// virtual repository no row exists: the change names the virtual repository and the
// prefix, and the merged entries under it are dropped on every replica. A local
// repository answers 400 not_applicable. The call touches no cache entry and
// deletes nothing, so its cost does not depend on the size of the cache.
func (s *Service) Invalidate(ctx context.Context, c repository.Caller, repo repository.Repository, prefix, scope string) (int64, error) {
	if scope == "" {
		scope = ScopeMetadata
	}
	if !ValidScope(scope) {
		return 0, errs.E(errs.Invalid, errs.CodeValidation, `The scope must be "metadata", "negative" or "all".`,
			errs.WithFields(errs.FieldError{Pointer: "/scope", Code: "invalid_value", Detail: "must be metadata, negative or all"}))
	}
	if err := CheckPrefix(prefix); err != nil {
		return 0, err
	}
	if repo.Lifecycle != repository.Active {
		return 0, errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "no repository has the key "+strconv.Quote(repo.Key))
	}
	id, err := strconv.ParseInt(repo.ID, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalidation: repository id %q: %w", repo.ID, err)
	}
	switch repo.Class {
	case repository.Remote:
	case repository.Virtual:
		return 0, s.virtual(ctx, c, repo, id, prefix, scope)
	default:
		return 0, errs.E(errs.Invalid, errs.CodeNotApplicable,
			"A cache invalidation applies to remote and virtual repositories; "+strconv.Quote(repo.Key)+" is a "+string(repo.Class)+" repository.")
	}
	var count int64
	err = s.o.Runner.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		ins, err := qs.InvalidationInsert(ctx, q.InvalidationInsertParams{
			RepoID: id, Prefix: prefix, Scope: scope, CreatedBy: principal.IDOrEmpty(c.Actor.ID),
		})
		if err != nil {
			return err
		}
		// Negative entries live in Valkey and are not counted (R-REPO-045).
		count = 0
		if scope != ScopeNegative {
			if count, err = qs.InvalidationCountCovered(ctx, q.InvalidationCountCoveredParams{RepoID: id, Prefix: prefix}); err != nil {
				return err
			}
		}
		if err := s.o.Notifier.Notify(tx, cluster.AreaCacheInvalidation, PayloadVersion, Payload{
			Kind: KindAdd, Repo: id, ID: ins.ID, Prefix: EncodePrefix(prefix), Scope: scope, CreatedUs: ins.CreatedUs,
		}); err != nil {
			return err
		}
		return s.record(ctx, tx, c, repo, prefix, scope, count)
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Service) virtual(ctx context.Context, c repository.Caller, repo repository.Repository, id int64, prefix, scope string) error {
	return s.o.Runner.InTx(ctx, func(tx meta.Tx) error {
		if err := s.o.Notifier.Notify(tx, cluster.AreaCacheInvalidation, PayloadVersion, Payload{
			Kind: KindVirtual, Repo: id, Prefix: EncodePrefix(prefix), Scope: scope,
		}); err != nil {
			return err
		}
		return s.record(ctx, tx, c, repo, prefix, scope, 0)
	})
}

func (s *Service) record(ctx context.Context, tx meta.Tx, c repository.Caller, repo repository.Repository, prefix, scope string, count int64) error {
	raw, err := json.Marshal(eventDetails{Path: prefix, Scope: scope, Count: count})
	if err != nil {
		return err
	}
	return s.o.Recorder.RecordTx(ctx, tx, audit.Event{
		Type: audit.EventRepositoryCacheInvalidated, Actor: c.Actor, Outcome: audit.Success, Source: c.Source,
		Subject:    audit.Subject{Kind: "repository", ID: repo.ID, Name: repo.Key},
		Repository: repo.Key, Path: prefix, Endpoint: c.Endpoint, Details: raw,
	})
}

// EntryKinds are the values of the kind filter of the entry list.
const (
	EntryKindMetadata = "metadata"
	EntryKindArtifact = "artifact"
)

// EntryFilter selects a page of cache entries.
type EntryFilter struct {
	// Prefix limits the list to the entries a prefix of an invalidation would cover.
	Prefix string
	// Kinds are the entry kinds; empty means metadata.
	Kinds []string
	// After is the key of the last entry of the previous page.
	After *EntryKey
	// Limit is the number of rows to fetch; the caller asks for one more than the
	// page to learn whether a next page exists.
	Limit int
}

// EntryKey is the sort key of an entry: its primary key within the repository.
type EntryKey struct{ Path, Variant string }

// Entry is one cache entry as the list shows it (R-REPO-032).
type Entry struct {
	Path           string
	Variant        string
	Kind           string
	UpstreamURL    string
	FetchedAt      time.Time
	ValidatedAt    time.Time
	ExpiresAt      *time.Time // nil for an artifact
	UpstreamGoneAt *time.Time
	LastUsedAt     time.Time
}

// Key returns the sort key of the entry.
func (e Entry) Key() EntryKey { return EntryKey{Path: e.Path, Variant: e.Variant} }

// Entries returns a page of the cache entries of a remote repository, in the order
// of (path, variant). The list is for diagnosis; it reads PostgreSQL.
func (s *Service) Entries(ctx context.Context, repoID int64, f EntryFilter) ([]Entry, error) {
	if err := CheckPrefix(f.Prefix); err != nil {
		return nil, err
	}
	kinds := f.Kinds
	if len(kinds) == 0 {
		kinds = []string{EntryKindMetadata}
	}
	p := q.InvalidationListEntriesParams{RepoID: repoID, Kinds: kinds, Prefix: f.Prefix, MaxRows: int32(f.Limit)} //nolint:gosec // G115: a page size
	if f.After != nil {
		p.HasAfter, p.AfterPath, p.AfterVariant = true, f.After.Path, f.After.Variant
	}
	var rows []q.InvalidationListEntriesRow
	err := s.o.Runner.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rows, err = qs.InvalidationListEntries(ctx, p)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]Entry, len(rows))
	for i, r := range rows {
		out[i] = Entry{
			Path: r.Path, Variant: r.Variant, Kind: r.Kind, UpstreamURL: r.UpstreamUrl,
			FetchedAt: time.UnixMicro(r.FetchedUs).UTC(), ValidatedAt: time.UnixMicro(r.ValidatedUs).UTC(),
			ExpiresAt: optionalTime(r.ExpiresUs), UpstreamGoneAt: optionalTime(r.GoneUs), LastUsedAt: time.UnixMicro(r.LastUsedUs).UTC(),
		}
	}
	return out, nil
}

// optionalTime reads an instant that the list query returns as 0 when it is absent.
func optionalTime(us int64) *time.Time {
	if us == 0 {
		return nil
	}
	t := time.UnixMicro(us).UTC()
	return &t
}
