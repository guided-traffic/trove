package v1

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// MigrationSource reads the migration report of GET /api/v1/system/migrations: postgres.Report on the replica's pool, the
// code `trove migrate status` runs on its session connection, for the replica's binary and the background data migrations
// it declares (R-HA-018, R-HA-022, Q-HA-114).
type MigrationSource interface {
	MigrationReport(ctx context.Context) (postgres.StatusReport, error)
}

// GetMigrationStatus implements GET /api/v1/system/migrations (R-HA-018, R-HA-020's upgrade verification, step 4 of
// R-HA-021's preparation); settings:read is enforced before it runs. The report is the answering replica's: during a rolling
// upgrade each release answers for its own binary.
func (s *Server) GetMigrationStatus(w http.ResponseWriter, r *http.Request) {
	if s.o.API.Migrations == nil {
		httpserver.Problem(w, r, unavailable("The migration report"))
		return
	}
	rep, err := s.o.API.Migrations.MigrationReport(r.Context())
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.migrationStatus"))
		return
	}
	writeJSON(w, r, http.StatusOK, migrationStatusOf(rep))
}

// migrationStatusOf is the document's shape of the report: the members of R-HA-018, without the gate's verdict, which is
// true for every replica that answers (a replica the gate refuses does not serve).
func migrationStatusOf(rep postgres.StatusReport) gen.MigrationStatus {
	out := gen.MigrationStatus{
		Pending:    make([]gen.PendingMigrationStep, 0, len(rep.Pending)),
		Applied:    make([]gen.AppliedMigrationStep, 0, len(rep.Applied)),
		Background: make([]gen.BackgroundMigration, 0, len(rep.Background)),
	}
	out.Schema.Applied, out.Schema.CompatFloor = rep.Schema.Applied, rep.Schema.CompatFloor
	out.Binary.Version, out.Binary.RequiredSchema = rep.Binary.Version, rep.Binary.RequiredSchema
	for _, p := range rep.Pending {
		step := gen.PendingMigrationStep{
			Version: p.Version, Name: p.Name, Class: gen.PendingMigrationStepClass(p.Class),
			RequiresBackground: append([]string{}, p.RequiresBackground...),
		}
		if p.RequiresApp != "" {
			v := p.RequiresApp
			step.RequiresApp = &v
		}
		out.Pending = append(out.Pending, step)
	}
	for _, a := range rep.Applied {
		out.Applied = append(out.Applied, gen.AppliedMigrationStep{
			Version: a.Version, Name: a.Name, Class: gen.AppliedMigrationStepClass(a.Class), Release: a.Release,
			AppliedAt: a.AppliedAt.UTC(), DurationMs: int(a.DurationMs),
		})
	}
	for _, b := range rep.Background {
		out.Background = append(out.Background, gen.BackgroundMigration{
			Name: b.Name, Release: b.Release, State: gen.BackgroundMigrationState(b.State),
			Processed: b.Processed, EstimatedTotal: b.EstimatedTotal, StartedAt: b.StartedAt,
		})
	}
	return out
}
