package app

import (
	"context"

	"github.com/guided-traffic/trove/internal/buildinfo"
	nugetdata "github.com/guided-traffic/trove/internal/formatdata/nuget"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// BackgroundMigrations are the background data migrations of this binary (R-HA-022, Q-HA-114), declared by the packages
// that run them: `trove migrate` hands them to the contract guard, which refuses a contract step that depends on one that
// has not succeeded (Q-HA-115), and GET /api/v1/system/migrations and `trove migrate status` list their states. The
// composition root alone imports a format's data package, so the list is here.
func BackgroundMigrations() []postgres.BackgroundMigration {
	return []postgres.BackgroundMigration{nugetdata.Semver2Background()}
}

// migrationReport is the source of GET /api/v1/system/migrations: postgres.Report, the code of `trove migrate status`, on
// the replica's pool, for the replica's binary (R-HA-018, R-HA-020, R-HA-021). It reads through Read, so it rides out a
// failover like every other read of the API.
type migrationReport struct{ db *postgres.DB }

func (m migrationReport) MigrationReport(ctx context.Context) (postgres.StatusReport, error) {
	var rep postgres.StatusReport
	err := m.db.Read(ctx, func(c q.DBTX) (err error) {
		rep, err = postgres.Report(ctx, c, postgres.MigrateOptions{Version: buildinfo.Get().Version, Background: BackgroundMigrations()})
		return err
	})
	return rep, err
}
