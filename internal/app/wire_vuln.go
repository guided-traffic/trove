package app

import (
	"context"
	"fmt"
	"time"

	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/enforce"
	"github.com/guided-traffic/trove/internal/vuln/evaluate"
	vmetrics "github.com/guided-traffic/trove/internal/vuln/metrics"
	"github.com/guided-traffic/trove/internal/vuln/store"
	vsync "github.com/guided-traffic/trove/internal/vuln/sync"
)

// The wiring of the vulnerability scanning (docs/developer/vulnerabilities.md, section "Composition root"). This file is the
// area: its ports and the order they are built in. The parts live in their own files:
//
//	wire_vuln_sync.go    the advisory sync, the replica's view of the data state, the kinds vuln-sync-osv, vuln-sync-osv-full, vuln-sync-kev
//	wire_vuln_eval.go    the Evaluator, the commit hook, the kinds vuln-evaluate, vuln-rescan, vuln-findings-retention
//	wire_vuln_audits.go  the Advisories port, the cache vuln_nuget_page and the ports the Host hands to the formats
//	wire_vuln_api.go     the services behind api/v1's vulnerability routes
//
// A port nobody builds is nil, which every consumer reads as "the scanning is not wired": the Host answers
// formats.NoVulnerabilityData, the commit hook skips the evaluation, the enforcer resolves every policy as disabled.

// AreaVuln is the scanning's services. Every role builds it: a web replica reads the state, the advisories and the
// evaluation, a worker replica runs the jobs.
const AreaVuln Area = "vuln"

// vulnArea is the set of the scanning's ports and metrics of the replica. A nil port is a part that is not wired.
type vulnArea struct {
	// states answers the state of an ecosystem's advisory data from the replica's cache. Every role builds it.
	states vuln.States
	// sync is the advisory sync: the jobs' service. Only a role that runs jobs builds it; a web replica has the states alone.
	sync *vsync.Service
	// evaluator evaluates stored versions: the commit hook, the removal hook and the remote miss.
	evaluator vuln.Evaluator
	// enabled is vulnerabilities.enabled; the Host hands it to the formats' audits (R-VULN-017).
	enabled bool
	// advisories reads advisory data for the client audits; nil while vulnerabilities.enabled is false.
	advisories vuln.Advisories
	// rule is the internal-names rule of R-VULN-010 behind the advisories; the report's views apply it too.
	rule evaluate.ClaimRule
	// pages is the cache vuln_nuget_page of the NuGet vulnerability page.
	pages *store.Pages
	// localAudits is whether the client-audit modes that a repository document leaves out default to local: the scanning is
	// enabled and the OSV source is not off (Q-VULN-44).
	localAudits bool
	// metrics are the collectors of R-VULN-024.
	metrics *vmetrics.Metrics
	// enforcer applies the download policy: the policy of a repository for the item lookup, the verdict, the remote miss gate
	// and the refusal. Nil is a replica without the area.
	enforcer *enforce.Enforcer
}

// VulnOverride replaces ports of the scanning, for tests (Options.Vuln): the state of an ecosystem's advisory data and the
// verdict of a remote miss. A nil member keeps the port the composition root built. The override applies before any part
// takes the port, so the evaluation and the enforcer see the replacement.
type VulnOverride struct {
	States    vuln.States
	Evaluator vuln.Evaluator
}

// wireVuln builds the area in dependency order: the metrics, the data state (and on a role that runs jobs the sync service), the
// test override of the state, the evaluation over the state, the client audits, the database gauges and the enforcer over
// the ports above.
func (a *App) wireVuln(ctx context.Context, in infra, repo repositoryArea, vir virtualArea) (vulnArea, error) {
	if err := requireInputs(AreaVuln, given("database", in.db), given("change log", in.changes), given("cache registry", in.caches),
		given("audit recorder", in.rec), given("repository reader", repo.reader), given("claim routing", vir.routing)); err != nil {
		return vulnArea{}, err
	}
	m, err := vmetrics.New(a.tele.metrics.Registry())
	if err != nil {
		return vulnArea{}, fmt.Errorf("vulnerability metrics: %w", err)
	}
	vcfg := a.cfg.Vulnerabilities
	out := vulnArea{metrics: m, enabled: vcfg.Enabled}

	// The data state: the sync service owns it on a role that runs jobs, a web replica builds the reader alone.
	if a.sub.JobLoops {
		sv, err := a.newVulnSync(in, m)
		if err != nil {
			return vulnArea{}, fmt.Errorf("vulnerability sync: %w", err)
		}
		out.sync, out.states = sv, sv.States()
	} else {
		out.states = vsync.NewStates(in.db, in.caches, vcfg.StaleAfter, time.Now, a.log.With("component", "vulnerabilities"))
	}
	if o := a.o.Vuln; o != nil && o.States != nil {
		out.states = o.States
	}

	if err := a.wireVulnEvaluation(ctx, in, repo, vir, &out); err != nil {
		return vulnArea{}, err
	}
	if o := a.o.Vuln; o != nil && o.Evaluator != nil {
		out.evaluator = o.Evaluator
	}

	if err := a.wireAudits(in, repo, vir, &out); err != nil {
		return vulnArea{}, err
	}

	// The database gauges of R-VULN-024, evaluated in the background once per metrics.db_gauge_interval (Q-OPS-18).
	a.tele.metrics.DBGauges().Add(store.GaugeSource(in.db,
		func() []store.ExpectedSource { return store.ExpectedSources(vcfg) },
		func() []string { return vcfg.Sources.OSV.EffectiveEcosystems() }))

	// The enforcer takes the ports above, so it is built last: a port that is still nil is a part that is not wired, and the
	// enforcer then resolves every policy as disabled.
	public := vcfg.Matching.PublicUpstreams
	out.enforcer = enforce.New(enforce.Options{
		Covered: func(r repository.Repository) bool { return evaluate.RepositoryCovered(public, r) }, Enabled: vcfg.Enabled, Enforcement: vuln.Enforcement(vcfg.Enforcement),
		States: out.states, Evaluator: out.evaluator, Names: store.NewEnforce(in.db), Metrics: m, Audit: in.rec,
		BaseURL: a.cfg.Server.BaseURL, Log: a.log.With("component", "vuln-enforce"),
	})
	return out, nil
}

// countAudit counts one client audit (trove_vuln_audit_requests_total); the Host hands it to the formats' audit routes.
func (v vulnArea) countAudit(format, mode, outcome string) {
	if v.metrics != nil {
		v.metrics.AuditRequests.WithLabelValues(format, mode, outcome).Inc()
	}
}

// hooks returns the storage hooks the item store runs: inner, which calls the formats' hooks, wrapped by the evaluation when
// it is wired (R-VULN-011: the version of a committed item is evaluated in the commit's transaction after the format's
// OnCommit linked the item to it, and a removal resolves the version's findings in the removing transaction).
func (v vulnArea) hooks(inner items.Hooks) items.Hooks {
	if v.evaluator != nil {
		return evaluate.Hooks(inner, v.evaluator)
	}
	return inner
}

// vulnTaskSpec is the scheduled task of a default of R-VULN-025: the specification's schedule, replaced by
// maintenance.tasks.<name> when the configuration sets one (its enabled switch included).
func (a *App) vulnTaskSpec(d vuln.TaskDefault) jobs.TaskSpec {
	spec := jobs.TaskSpec{Schedule: d.Schedule, DedupKey: d.DedupKey}
	if c, ok := a.cfg.Maintenance.Tasks[d.Name]; ok && c.Schedule != "" {
		spec.Schedule, spec.Disabled = c.Schedule, !c.Enabled
	}
	return spec
}

// registerVulnJobs registers the kinds and tasks of the scanning on the job registry, which every role builds and freezes
// before the replica serves (a web replica must know a kind to enqueue it). The handlers find their services at run time
// through part[vulnArea](a.parts), as the CI tasks do.
func (a *App) registerVulnJobs(r *jobs.Registry) {
	a.registerVulnSyncJobs(r)
	a.registerVulnEvaluationJobs(r)
}
