package ci

import (
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
)

// The identity-mapping service of work package W3 (R-CI-009, R-CI-017, R-CI-019, R-CI-020, Q-CI-26). The
// files of the service:
//
//	mappings.go           the service, its options and the in-process copy of the mapping sets
//	mappings_store.go     the rows, the stored forms, the audit record and its journal entry
//	mappings_validate.go  the shape rules and the violation answer (pure)
//	mappings_read.go      List, Get, the project forms of the reads, Set, SetTx
//	mappings_write.go     Create, Replace, Delete, DeleteForIssuerTx and their transaction
//	mappings_bind.go      the policies, the groups and the project's repositories
//	mappings_evaluate.go  the dry run
//	mappings_snippet.go   the CI snippet
//	select.go             the evaluation of a mapping set, shared with the exchange (W4)

// MappingsOptions configure [NewMappings].
type MappingsOptions struct {
	// DB is the PostgreSQL store.
	DB Database
	// Changes writes the change areas ci_mappings and authz in the transaction of a write.
	Changes Changes
	// Recorder records the events of the service; nil discards them.
	Recorder audit.Recorder
	// Caches is the registry the in-process copy registers with; nil gives the service a registry of its
	// own, which no change log feeds (`trove migrate`).
	Caches *cache.Registry
	// Revoker ends the tokens of a mapping that is disabled or deleted, in the transaction of the write
	// (R-CI-017). It is asked only for a mapping that has an issuance record.
	Revoker Revoker
	// Config is the ci section: the mapping limit, the cache's safety TTL and the token lifetimes.
	Config config.CI
	// BaseURL is server.base_url, which the snippet names.
	BaseURL string
	Log     *slog.Logger
	// Now overrides the clock, for tests.
	Now func() time.Time
}

// MappingService implements [Mappings].
type MappingService struct {
	o   MappingsOptions
	now func() time.Time

	// sets is the in-process copy of the mapping sets, one entry per issuer ID, each with the issuer's
	// versions it was read at. Invalidated by the change area ci_mappings; the exchange does not rely on
	// it (R-CI-007, Q-CI-15).
	sets *cache.Cache[string, MappingSet]
}

var _ Mappings = (*MappingService)(nil)

// CacheMappings is the name of the in-process copy of the mapping sets (R-ARCH-018).
const CacheMappings = "ci_mappings"

// NewMappings creates the service and registers its copy.
func NewMappings(o MappingsOptions) (*MappingService, error) {
	if o.DB == nil || o.Changes == nil {
		return nil, errors.New("ci: the mapping service needs a database and a change log")
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
	s := &MappingService{o: o, now: o.Now}
	s.sets = cache.Register(o.Caches, CacheMappings, cache.Options[string, MappingSet]{
		MaxEntries: 1024, SafetyTTL: o.Config.Mappings.CacheSafetyTTL, Areas: []string{cluster.AreaCIMappings},
		OnChange: func(c *cache.Cache[string, MappingSet], _ string, _ int, payload json.RawMessage) {
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

// notifyMappings notifies the change area ci_mappings of a write of an issuer's mappings.
func (s *MappingService) notifyMappings(tx meta.Tx, issuerID string) error {
	return s.o.Changes.Notify(tx, cluster.AreaCIMappings, changePayloadVersion, issuerChange{Issuer: issuerID})
}

// notifyAuthz notifies the change area authz of a change of a policy's principal list, so the
// authorization index of every replica rebuilds with it (R-AUTHZ-028). The payload is the shape the
// area's listeners expect and distrust alike: a type and an ID, never a grant.
func (s *MappingService) notifyAuthz(tx meta.Tx, policyID string) error {
	return s.o.Changes.Notify(tx, cluster.AreaAuthz, 1, map[string]string{"type": "policy", "id": policyID})
}
