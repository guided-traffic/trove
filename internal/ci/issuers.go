package ci

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/ci/subject"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/outbound"
)

// The issuer and signing-key service of work package W1 (R-CI-001, R-CI-002, R-CI-003, R-CI-005, Q-CI-12,
// Q-CI-16). The files of the service:
//
//	issuers.go           the service, its options and the two in-process copies
//	issuers_validate.go  the defaults and rules of an issuer (pure)
//	issuers_store.go     the rows, the audit record
//	issuers_read.go      List, Get, Choices, Resolve, LockShareTx, Reload
//	issuers_write.go     Create, Replace, Delete and their events
//	issuers_declared.go  ApplyDeclared, the step of `trove migrate`
//	keys_jwks.go         the key rules and the stored form of a key set (pure)
//	keys_fetch.go        discovery and key fetch through the outbound factory
//	keys_store.go        the write rule, the failure record, the events and metrics
//	keys_source.go       the in-process copy and the unknown-kid protocol of the exchange
//	keys_task.go         the status, the forced refresh and the task ci-keys-refresh

// Database is what the service needs of the PostgreSQL store: a transaction runner and a retrying read.
// *postgres.DB and *postgres.MigrationSession implement it.
type Database interface {
	meta.Runner
	Read(ctx context.Context, fn func(q.DBTX) error) error
}

// Changes notifies a change area in the transaction of a write. *cluster.Changes implements it.
type Changes interface {
	Notify(tx meta.Tx, area string, version int, payload any) error
}

// Options configure [New].
type Options struct {
	// DB is the PostgreSQL store.
	DB Database
	// Changes writes the change areas ci_mappings and ci_issuer_keys.
	Changes Changes
	// Recorder records the events of the service; nil discards them.
	Recorder audit.Recorder
	// Caches is the registry the in-process copies register with; nil gives the service a registry of its
	// own, which no change log feeds (`trove migrate`).
	Caches *cache.Registry
	// Outbound is the outbound client factory (R-ARCH-022); nil makes every fetch fail with the class
	// trust_store_unavailable (`trove migrate` fetches nothing).
	Outbound *outbound.Factory
	// Config is the ci section; OutboundConfig is proxy.outbound, whose trust the issuer's own bundle adds to.
	Config         config.CI
	OutboundConfig config.Outbound
	// EpochPollInterval is cluster.epoch_poll_interval: the bound of the row re-read on an unknown kid.
	EpochPollInterval time.Duration
	// BaseURL is server.base_url; the default audiences of an issuer are made from it.
	BaseURL string
	// Revoker ends the tokens of an issuer that is disabled or deleted, in the transaction of the write
	// (R-CI-017). Mappings deletes the mappings of an issuer that is deleted with its cascade.
	Revoker  Revoker
	Mappings Mappings
	// Metrics counts the key fetches; nil counts nothing.
	Metrics *Metrics
	Log     *slog.Logger
	// Now overrides the clock, for tests.
	Now func() time.Time
}

// Service implements [Issuers].
type Service struct {
	o       Options
	fetcher *fetcher
	now     func() time.Time

	// issuers is the in-process copy of the issuers, one entry holding all of them: the exchange resolves
	// the issuer of every token from it, and a token of an unknown issuer is answered from it too, so an
	// anonymous request with a made-up `iss` costs no query. Invalidated by the change area ci_mappings.
	issuers *cache.Cache[struct{}, *issuerSet]
	// keys is the in-process copy of the key rows by issuer ID (R-CI-003). Invalidated by the change area
	// ci_issuer_keys.
	keys *cache.Cache[string, *keyRow]

	mu     sync.Mutex
	states map[string]*issuerState

	rereads flight[*keyRow]
	fetches flight[fetchOutcome]
}

var _ Issuers = (*Service)(nil)

// issuerState is the throttle state of one issuer on this replica (R-CI-003, R-ARCH-032): per-replica
// state that can change an answer for at most one interval.
type issuerState struct {
	lastReread time.Time
	lastFetch  time.Time
}

// issuerSet is every issuer of the installation, indexed for the two lookups.
type issuerSet struct {
	byIssuer map[string]Issuer
	byID     map[string]Issuer
	list     []Issuer
}

// CacheIssuers and CacheKeys are the names of the in-process copies (R-ARCH-018).
const (
	CacheIssuers = "ci_issuers"
	CacheKeys    = "ci_issuer_keys"
)

// changePayloadVersion is the payload version of the change areas this package writes.
const changePayloadVersion = 1

// issuerChange is the payload of a change of ci_mappings and ci_issuer_keys: the issuer ID, never a key
// or a setting (R-ARCH-018).
type issuerChange struct {
	Issuer string `json:"issuer"`
}

// New creates the service and registers its two copies.
func New(o Options) (*Service, error) {
	if o.DB == nil || o.Changes == nil {
		return nil, errors.New("ci: the issuer service needs a database and a change log")
	}
	if o.Recorder == nil {
		o.Recorder = audit.Nop{}
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Caches == nil {
		reg, err := cache.NewRegistry(nil, nil)
		if err != nil {
			return nil, err
		}
		o.Caches = reg
	}
	if o.EpochPollInterval <= 0 {
		o.EpochPollInterval = time.Second
	}
	s := &Service{o: o, now: o.Now, states: map[string]*issuerState{}}
	s.fetcher = newFetcher(o.Outbound, o.OutboundConfig, o.Config.SigningKeys, o.Now)
	s.issuers = cache.Register(o.Caches, CacheIssuers, cache.Options[struct{}, *issuerSet]{
		MaxEntries: 1, SafetyTTL: o.Config.Mappings.CacheSafetyTTL, Areas: []string{cluster.AreaCIMappings},
	})
	s.keys = cache.Register(o.Caches, CacheKeys, cache.Options[string, *keyRow]{
		MaxEntries: 4096, SafetyTTL: o.Config.SigningKeys.CacheSafetyTTL, Areas: []string{cluster.AreaCIIssuerKeys},
		OnChange: func(c *cache.Cache[string, *keyRow], _ string, _ int, payload json.RawMessage) {
			var p issuerChange
			if json.Unmarshal(payload, &p) != nil || p.Issuer == "" {
				c.Flush(cache.ReasonChange)
				return
			}
			c.Invalidate(p.Issuer)
		},
	})
	return s, nil
}

func (s *Service) limits() issuerLimits {
	return issuerLimits{BaseURL: s.o.BaseURL, TokenMaxTTL: s.o.Config.Token.MaxTTL}
}

// state returns the throttle state of an issuer.
func (s *Service) state(id string) *issuerState {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.states[id]
	if st == nil {
		st = &issuerState{}
		s.states[id] = st
	}
	return st
}

// notifyMappings notifies the change area ci_mappings of a write of an issuer (R-CI-002).
func (s *Service) notifyMappings(tx meta.Tx, issuerID string) error {
	return s.o.Changes.Notify(tx, cluster.AreaCIMappings, changePayloadVersion, issuerChange{Issuer: issuerID})
}

// actorOf converts the caller of a write to the actor of its audit event.
func actorOf(sub authz.Subject) audit.Actor {
	if sub.Anonymous {
		return audit.Actor{Kind: string(principal.KindAnonymous)}
	}
	kind := string(sub.Kind)
	if kind == "" {
		kind = string(principal.KindUser)
	}
	return audit.Actor{Kind: kind, ID: sub.ID, Name: sub.Name, TokenID: sub.TokenID, AuthMethod: sub.AuthMethod}
}

// Keys of the subject package are used through the interface only.
var _ subject.KeySource = (*keySource)(nil)
