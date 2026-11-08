package ci

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The reads of the mappings: the lists in evaluation order, one mapping, the project's forms and the mapping
// set a replica evaluates (R-CI-009, R-CI-019, Q-CI-26).

func mappingNotFound(issuer, name string) error {
	return errs.E(errs.NotFound, errs.CodeNotFound, fmt.Sprintf("The CI mapping %q of the issuer %q does not exist.", name, issuer))
}

func projectNotFound(key string) error {
	return errs.E(errs.NotFound, errs.CodeNotFound, fmt.Sprintf("The project %q does not exist or is not visible to the caller.", key))
}

// readMappings converts rows and fills each mapping's policies from the principal lists of the policies
// (R-CI-013), in the order of the rows.
func readMappings(ctx context.Context, qs *q.Queries, rows []q.CiMappingListRow) ([]Mapping, error) {
	out := make([]Mapping, 0, len(rows))
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		m, err := toMapping(r)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
		ids = append(ids, r.PrincipalID)
	}
	if len(out) == 0 {
		return out, nil
	}
	binds, err := qs.CiMappingPolicyBindings(ctx, ids)
	if err != nil {
		return nil, err
	}
	byPrincipal := map[string][]q.CiMappingPolicyBindingsRow{}
	for _, b := range binds {
		byPrincipal[b.PrincipalID] = append(byPrincipal[b.PrincipalID], b)
	}
	for i := range out {
		var names []string
		for _, b := range byPrincipal[out[i].PrincipalID] {
			// A project's mapping shows the project's own policies only: it manages no other (Q-CI-26).
			if out[i].Project == "" {
				names = append(names, b.Policy)
			} else if b.Project == out[i].Project {
				names = append(names, strings.TrimPrefix(b.Policy, out[i].Project+"/"))
			}
		}
		sort.Strings(names)
		out[i].Grants.Policies = names
	}
	return out, nil
}

func listRows[R ~struct {
	ID, IssuerID, Issuer, PrincipalID, Tier, ProjectID, DelegationID, Name string
	Priority                                                               int32
	Enabled                                                                bool
	SubjectKinds                                                           []string
	Match, Grants                                                          json.RawMessage
	MaxTtlSecs                                                             int64
	Options                                                                json.RawMessage
	Description                                                            string
	Version                                                                int64
	CreatedBy                                                              string
	CreatedUs, UpdatedUs                                                   int64
},
](rows []R) []q.CiMappingListRow {
	out := make([]q.CiMappingListRow, len(rows))
	for i, r := range rows {
		out[i] = q.CiMappingListRow(r)
	}
	return out
}

// issuerID reads the ID of an issuer by name inside a read; ok is false when there is none.
func issuerIDByName(ctx context.Context, qs *q.Queries, name string) (string, bool, error) {
	rows, err := qs.CiIssuerAudiences(ctx, name)
	if err != nil || len(rows) == 0 {
		return "", false, err
	}
	return rows[0].ID, true, nil
}

// List returns the issuer's mappings in evaluation order (tier, priority).
func (s *MappingService) List(ctx context.Context, issuer string) ([]Mapping, error) {
	var out []Mapping
	err := s.o.DB.Read(ctx, func(db q.DBTX) error {
		qs := q.New(db)
		if _, ok, err := issuerIDByName(ctx, qs, issuer); err != nil || !ok {
			if err == nil {
				err = notFound(issuer)
			}
			return err
		}
		rows, err := qs.CiMappingList(ctx, issuer)
		if err != nil {
			return err
		}
		out, err = readMappings(ctx, qs, listRows(rows))
		return err
	})
	return out, err
}

// Get returns one mapping of the issuer's system tier.
func (s *MappingService) Get(ctx context.Context, issuer, name string) (Mapping, error) {
	return s.get(ctx, issuer, TierSystem, name)
}

// get reads the mapping name of the tier of the issuer.
func (s *MappingService) get(ctx context.Context, issuer, tier, name string) (Mapping, error) {
	var out Mapping
	err := s.o.DB.Read(ctx, func(db q.DBTX) error {
		qs := q.New(db)
		rows, err := qs.CiMappingByName(ctx, q.CiMappingByNameParams{Issuer: issuer, Tier: tier, Name: name})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			if _, ok, err := issuerIDByName(ctx, qs, issuer); err != nil {
				return err
			} else if !ok {
				return notFound(issuer)
			}
			return mappingNotFound(issuer, name)
		}
		ms, err := readMappings(ctx, qs, listRows(rows[:1]))
		if err != nil {
			return err
		}
		out = ms[0]
		return nil
	})
	return out, err
}

// ListIn returns the project's mappings of every issuer, ordered by issuer, priority (Q-CI-26).
func (s *MappingService) ListIn(ctx context.Context, project string) ([]Mapping, error) {
	var out []Mapping
	err := s.o.DB.Read(ctx, func(db q.DBTX) error {
		qs := q.New(db)
		if ids, err := qs.CiProjectID(ctx, project); err != nil || len(ids) == 0 {
			if err == nil {
				err = projectNotFound(project)
			}
			return err
		}
		rows, err := qs.CiMappingListOfProject(ctx, project)
		if err != nil {
			return err
		}
		out, err = readMappings(ctx, qs, listRows(rows))
		return err
	})
	return out, err
}

// GetIn returns one mapping of the project's tier; a mapping of another tier is 404, as one that does not exist.
func (s *MappingService) GetIn(ctx context.Context, project, issuer, name string) (Mapping, error) {
	return s.get(ctx, issuer, ProjectTier(project), name)
}

// CountIn returns the number of CI mappings of a project; the project service refuses to delete a
// project with any (409 project_not_empty).
func (s *MappingService) CountIn(ctx context.Context, project string) (int, error) {
	n := 0
	err := s.o.DB.Read(ctx, func(db q.DBTX) error {
		qs := q.New(db)
		ids, err := qs.CiProjectID(ctx, project)
		if err != nil || len(ids) == 0 {
			if err == nil {
				err = projectNotFound(project)
			}
			return err
		}
		c, err := qs.CiMappingCountOfProject(ctx, ids[0])
		n = int(c)
		return err
	})
	return n, err
}

// loadSet reads the mapping set of an issuer: the issuer's versions first, then the mappings, so a write
// that lands between the two statements leaves the set newer than its label, never older (Q-CI-15).
func (s *MappingService) loadSet(ctx context.Context, issuerID string) (MappingSet, error) {
	var set MappingSet
	err := s.o.DB.Read(ctx, func(db q.DBTX) error {
		qs := q.New(db)
		vs, err := qs.CiMappingVersions(ctx, issuerID)
		if err != nil {
			return err
		}
		if len(vs) == 0 {
			return notFound(issuerID)
		}
		rows, err := qs.CiMappingsOfIssuerID(ctx, issuerID)
		if err != nil {
			return err
		}
		ms, err := readMappings(ctx, qs, listRows(rows))
		if err != nil {
			return err
		}
		set = newMappingSet(issuerID, vs[0].Version, vs[0].MappingsVersion, ms)
		return nil
	})
	return set, err
}

// Set returns the issuer's mapping set from the in-process copy with the versions it was read at, in evaluation
// order and with every condition compiled (Q-CI-49). The slice is shared: a caller never changes it.
func (s *MappingService) Set(ctx context.Context, issuerID string) (MappingSet, error) {
	return s.sets.Load(ctx, issuerID, func(ctx context.Context) (MappingSet, error) { return s.loadSet(ctx, issuerID) })
}

// SetTx reads the mapping set from PostgreSQL in the exchange's transaction, every mapping row FOR
// SHARE, and refreshes the copy (R-CI-007). The caller holds the issuer row FOR SHARE (LockShareTx), so no
// mapping write commits between the versions and the rows.
func (s *MappingService) SetTx(ctx context.Context, tx meta.Tx, issuerID string) (MappingSet, error) {
	qs, err := queriesOf(tx)
	if err != nil {
		return MappingSet{}, err
	}
	vs, err := qs.CiMappingVersions(ctx, issuerID)
	if err != nil {
		return MappingSet{}, err
	}
	if len(vs) == 0 {
		return MappingSet{}, notFound(issuerID)
	}
	rows, err := qs.CiMappingsOfIssuerIDShare(ctx, issuerID)
	if err != nil {
		return MappingSet{}, err
	}
	ms, err := readMappings(ctx, qs, listRows(rows))
	if err != nil {
		return MappingSet{}, err
	}
	set := newMappingSet(issuerID, vs[0].Version, vs[0].MappingsVersion, ms)
	s.sets.Set(issuerID, set)
	return set, nil
}
