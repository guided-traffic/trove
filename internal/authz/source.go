package authz

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Source reads the inputs of the grant index from the system of record.
type Source interface {
	// Snapshot reads every input in one consistent snapshot and labels it with the
	// epochs of that moment (R-AUTHZ-028 "Consistent build").
	Snapshot(ctx context.Context) (*Snapshot, error)
	// Epochs reads the two epochs with one indexed query. It is the read behind
	// the coalesced epoch read of R-AUTHZ-028 and R-AUTHZ-032.
	Epochs(ctx context.Context) (Label, error)
}

// pgSource is the PostgreSQL implementation of Source.
type pgSource struct {
	db      meta.Runner
	timeout time.Duration
}

// NewPostgresSource returns the Source over the request pool. timeout is
// authz.cache.max_staleness: the deadline of a build and its statement timeout.
func NewPostgresSource(db meta.Runner, timeout time.Duration) Source {
	return &pgSource{db: db, timeout: timeout}
}

var errNoEpochRow = errors.New("authz: cluster_epoch has no row for the area authz or repo_config; the migrations are not applied")

func labelOf(rows []q.ClusterEpoch) (Label, error) {
	var l Label
	var haveAuthz, haveRepo bool
	for _, r := range rows {
		switch r.Area {
		case AreaAuthz:
			l.AuthzEpoch, l.AuthzAt, haveAuthz = r.Epoch, r.ChangedAt.Time, true
		case AreaRepoConfig:
			l.RepoEpoch, l.RepoAt, haveRepo = r.Epoch, r.ChangedAt.Time, true
		}
	}
	if !haveAuthz || !haveRepo {
		return Label{}, errNoEpochRow
	}
	return l, nil
}

func (s *pgSource) Epochs(ctx context.Context) (Label, error) {
	var l Label
	err := s.db.InTx(ctx, func(tx meta.Tx) error {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return errors.New("authz: the transaction does not come from the PostgreSQL store")
		}
		rows, err := q.New(ptx).ListClusterEpochs(ctx, []string{AreaAuthz, AreaRepoConfig})
		if err != nil {
			return err
		}
		l, err = labelOf(rows)
		return err
	})
	return l, err
}

// Snapshot is the build of R-AUTHZ-028: one REPEATABLE READ READ ONLY
// transaction on the request pool whose first query reads the epochs, so the
// index is the state of one moment labelled with that moment's epochs.
func (s *pgSource) Snapshot(ctx context.Context) (*Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var out *Snapshot
	err := s.db.InTx(ctx, func(tx meta.Tx) error {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return errors.New("authz: the transaction does not come from the PostgreSQL store")
		}
		// The isolation level is chosen before the first query of the
		// transaction; the statement timeout is transaction-scoped, so it works
		// through a transaction-mode pooler.
		if _, err := ptx.Exec(ctx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY"); err != nil {
			return fmt.Errorf("setting the isolation level: %w", err)
		}
		if _, err := ptx.Exec(ctx, "SELECT set_config('statement_timeout', $1, true)", strconv.FormatInt(s.timeout.Milliseconds(), 10)); err != nil {
			return fmt.Errorf("setting the statement timeout: %w", err)
		}
		qs := q.New(ptx)
		started := time.Now()
		rows, err := qs.ListClusterEpochs(ctx, []string{AreaAuthz, AreaRepoConfig})
		if err != nil {
			return err
		}
		label, err := labelOf(rows)
		if err != nil {
			return err
		}
		snap := &Snapshot{Label: label, Started: started}
		if snap.Policies, err = qs.AuthzBuildPolicies(ctx); err != nil {
			return err
		}
		if snap.Selectors, err = qs.AuthzBuildSelectors(ctx); err != nil {
			return err
		}
		if snap.Patterns, err = qs.AuthzBuildPatterns(ctx); err != nil {
			return err
		}
		if snap.Entries, err = qs.AuthzBuildEntries(ctx); err != nil {
			return err
		}
		if snap.Bindings, err = qs.AuthzBuildBindings(ctx); err != nil {
			return err
		}
		if snap.Repositories, err = qs.AuthzBuildRepositories(ctx); err != nil {
			return err
		}
		if snap.Exposure, err = qs.EndpointsExposureBuild(ctx); err != nil {
			return err
		}
		// >>> M2 virtual
		if snap.VirtualMembers, err = qs.AuthzBuildVirtualMembers(ctx); err != nil {
			return err
		}
		if snap.DeployTargets, err = qs.AuthzBuildDeployTargets(ctx); err != nil {
			return err
		}
		// <<< M2 virtual
		out = snap
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// The change areas the index depends on (R-AUTHZ-028).
const (
	// AreaAuthz is the change area of policies, role bindings, exposure rules and
	// authorization settings.
	AreaAuthz = cluster.AreaAuthz
	// AreaRepoConfig is the change area of repository rows and virtual membership.
	AreaRepoConfig = "repo_config"
)
