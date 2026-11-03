package app

import (
	"context"
	"fmt"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/report"
	"github.com/guided-traffic/trove/internal/vuln/store"
	"github.com/guided-traffic/trove/internal/vuln/triage"
)

// AreaVulnAPI is the services of the vulnerability scanning's operations of the management API (R-VULN-013, R-VULN-019 to
// R-VULN-021): the report over the findings, the triage, and what the caller can read.
const AreaVulnAPI Area = "vuln-api"

// vulnAPIArea is the vulnerability operations' services of a replica with a client listener.
type vulnAPIArea struct{ services v1.VulnServices }

// wireVulnAPI builds the report and the triage. The report reads what the caller can read through the decision point
// (authz.ReadScoper) and the repositories' policy from the replica's snapshot; the triage writes the history row and the audit
// event in the transaction of the change. The advisories port is optional: without it every name of a local
// repository counts as public in the evaluation state of a version.
func (a *App) wireVulnAPI(_ context.Context, in infra, az authzArea, repo repositoryArea, vz vulnArea) (vulnAPIArea, error) {
	if err := requireInputs(AreaVulnAPI, given("database", in.db), given("decision point", az.engine), given("repository reader", repo.reader)); err != nil {
		return vulnAPIArea{}, err
	}
	cfg := a.cfg.Vulnerabilities
	enforcement := vuln.Enforcement(cfg.Enforcement)
	st := store.NewAPI(in.db)
	rep, err := report.New(report.Options{
		Store: st, Repositories: repo.reader, Advisories: vz.advisories,
		Config: report.Config{
			Enabled: cfg.Enabled, Enforcement: enforcement, StaleAfter: cfg.StaleAfter, ExportMaxRows: cfg.Export.MaxRows,
			PublicUpstreams: cfg.Matching.PublicUpstreams, OSVMode: cfg.Sources.OSV.Mode, KEVMode: cfg.Sources.KEV.Mode,
			OSVRepository: cfg.Sources.OSV.Repository, KEVRepository: cfg.Sources.KEV.Repository,
			Ecosystems: cfg.Sources.OSV.EffectiveEcosystems(),
		},
	})
	if err != nil {
		return vulnAPIArea{}, fmt.Errorf("%s: the report: %w", AreaVulnAPI, err)
	}
	tri, err := triage.New(triage.Options{Store: st, Repositories: repo.reader, Recorder: in.rec, Enabled: cfg.Enabled, Enforcement: enforcement})
	if err != nil {
		return vulnAPIArea{}, fmt.Errorf("%s: the triage: %w", AreaVulnAPI, err)
	}
	return vulnAPIArea{services: v1.VulnServices{Report: rep, Triage: tri, Decider: az.engine, Scoper: authz.ReadScoper(az.engine)}}, nil
}
