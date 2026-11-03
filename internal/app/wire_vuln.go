package app

import (
	"context"
	"fmt"

	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/enforce"
	vmetrics "github.com/guided-traffic/trove/internal/vuln/metrics"
	"github.com/guided-traffic/trove/internal/vuln/store"
	vsync "github.com/guided-traffic/trove/internal/vuln/sync"
)

// The wiring of the vulnerability scanning (docs/developer/vulnerabilities.md). The contract wave builds the area, the
// metrics and the places where each work package registers its parts; it registers no kind, task or port itself: a kind
// without a handler is a start-up error, and a port nobody implements is nil, which every consumer reads as "the scanning is
// not wired" (the Host answers formats.NoVulnerabilityData, the commit hook skips the evaluation).
//
// Each work package edits its own marked blocks, and only those:
//
//	>>> WP-A  advisory data: the source-state cache and States, the sync service, the kinds vuln-sync-osv, vuln-sync-osv-full and vuln-sync-kev
//	>>> WP-B  matching and findings: the Evaluator, the commit hooks, the kinds vuln-evaluate, vuln-rescan and vuln-findings-retention, the repository hook of a settings change
//	>>> WP-C  enforcement: the enforcer over the ports above (it follows the blocks of the ports); wireFiles, wireProxyEngine and wireFormatHost take it from the area
//	>>> WP-D  client audits: the Advisories port and the cache vuln_nuget_page
//	>>> WP-E  API and triage: the services behind api/v1's vuln_api.go
//	>>> WP-G  operations: the database gauges of R-VULN-024 and the vulnerabilityData facts
//
// A block names the registrations it adds; the other blocks stay empty until their package is built.

// AreaVuln is the scanning's services. Every role builds it: a web replica reads the state, the advisories and the
// evaluation, a worker replica runs the jobs.
const AreaVuln Area = "vuln"

// vulnArea is the set of the scanning's ports and metrics of the replica. A nil port is a part that is not wired.
type vulnArea struct {
	// states answers the state of an ecosystem's advisory data from the replica's cache (work package A).
	states vuln.States
	// evaluator evaluates stored versions: the commit hook, the removal hook and the remote miss (work package B).
	evaluator vuln.Evaluator
	// advisories reads advisory data for the client audits (work package D); nil while vulnerabilities.enabled is false.
	advisories vuln.Advisories
	// pages is the cache vuln_nuget_page of the NuGet vulnerability page (work package D).
	pages *store.Pages
	// localAudits is whether the client-audit modes that a repository document leaves out default to local: the scanning is
	// enabled and the OSV source is not off (work package D, Q-VULN-44).
	localAudits bool
	// metrics are the collectors of R-VULN-024.
	metrics *vmetrics.Metrics
	// enforcer applies the download policy: the policy of a repository for the item lookup, the verdict, the remote miss gate
	// and the refusal (work package C). Nil is a replica without the area.
	enforcer *enforce.Enforcer
	// >>> WP-A
	// sync is the advisory sync: the jobs' service and the owner of the state cache (wire_vuln_sync.go).
	sync *vsync.Service
	// <<< WP-A
}

// wireVuln builds the area: the metrics today, and the ports as the work packages fill their blocks.
func (a *App) wireVuln(_ context.Context, in infra, repo repositoryArea, vir virtualArea) (vulnArea, error) {
	if err := requireInputs(AreaVuln, given("database", in.db), given("change log", in.changes), given("cache registry", in.caches),
		given("audit recorder", in.rec), given("repository reader", repo.reader), given("claim routing", vir.routing)); err != nil {
		return vulnArea{}, err
	}
	m, err := vmetrics.New(a.tele.metrics.Registry())
	if err != nil {
		return vulnArea{}, fmt.Errorf("vulnerability metrics: %w", err)
	}
	out := vulnArea{metrics: m}

	// >>> WP-A
	// The source-state cache (vuln.CacheSourceState, area vuln_data) and the vuln.States over it: out.states.
	sv, err := a.newVulnSync(in, m)
	if err != nil {
		return vulnArea{}, fmt.Errorf("vulnerability sync: %w", err)
	}
	out.sync, out.states = sv, sv.States()
	// <<< WP-A

	// >>> WP-B
	// The evaluator over the store and the repository snapshot: out.evaluator.
	// <<< WP-B

	// >>> WP-D
	// The advisories over the store, the claim index and the cache vuln_nuget_page: out.advisories, out.pages (wire_vuln_audits.go).
	if err := a.wireAudits(in, repo, vir, &out); err != nil {
		return vulnArea{}, err
	}
	// <<< WP-D

	// >>> WP-G
	// The database gauges of R-VULN-024, evaluated in the background once per metrics.db_gauge_interval (Q-OPS-18).
	vcfg := a.cfg.Vulnerabilities
	a.tele.metrics.DBGauges().Add(store.GaugeSource(in.db,
		func() []store.ExpectedSource { return store.ExpectedSources(vcfg) },
		func() []string { return vcfg.Sources.OSV.EffectiveEcosystems() }))
	// <<< WP-G
	// >>> WP-C
	if o := a.o.Vuln; o != nil {
		if o.States != nil {
			out.states = o.States
		}
		if o.Evaluator != nil {
			out.evaluator = o.Evaluator
		}
	}
	// The enforcer takes the ports of the blocks above, so it is built after them: a port that is still nil is a part that is
	// not wired, and the enforcer then resolves every policy as disabled.
	out.enforcer = enforce.New(enforce.Options{
		Enabled: a.cfg.Vulnerabilities.Enabled, Enforcement: vuln.Enforcement(a.cfg.Vulnerabilities.Enforcement),
		States: out.states, Evaluator: out.evaluator, Names: store.NewEnforce(in.db), Metrics: m, Audit: in.rec,
		BaseURL: a.cfg.Server.BaseURL, Log: a.log.With("component", "vuln-enforce"),
	})
	// <<< WP-C

	return out, nil
}

// VulnOverride replaces ports of the scanning, for tests (Options.Vuln): the state of an ecosystem's advisory data and the
// verdict of a remote miss. A nil member keeps the port the work package built.
type VulnOverride struct {
	States    vuln.States
	Evaluator vuln.Evaluator
}

// countAudit counts one client audit (trove_vuln_audit_requests_total); the Host hands it to the formats' audit routes.
func (v vulnArea) countAudit(format, mode, outcome string) {
	if v.metrics != nil {
		v.metrics.AuditRequests.WithLabelValues(format, mode, outcome).Inc()
	}
}

// hooks returns the storage hooks the item store runs: inner, which calls the formats' hooks, wrapped by the evaluation
// when work package B wired it (R-VULN-011: the version of a committed item is evaluated in the commit's transaction after
// the format's OnCommit linked the item to it, and a removal resolves the version's findings in the removing transaction).
func (v vulnArea) hooks(inner items.Hooks) items.Hooks {
	// >>> WP-B
	// if v.evaluator != nil { return evaluate.Hooks(inner, v.evaluator) }
	// <<< WP-B
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
// before the replica serves (a web replica must know a kind to enqueue it). A kind is registered with its handler by the work
// package that owns it, with the queue, dedup mode and attempts of vuln/jobs.go; the handlers find their services at run time
// through part[vulnArea](a.parts), as the CI tasks do. Nothing is registered until a package fills its block.
func (a *App) registerVulnJobs(r *jobs.Registry) {
	// >>> WP-A
	// vuln-sync-osv and vuln-sync-osv-full (queue maintenance, key vuln.DedupSyncOSV, Exclusive, MaxAttempts 1) with their
	// tasks, vuln-sync-kev (maintenance, Exclusive, MaxAttempts 1) with its task: a.vulnTaskSpec(td) per vuln.TaskDefaults().
	a.registerVulnSyncJobs(r)
	// <<< WP-A

	// >>> WP-B
	// vuln-evaluate (queue default, key vuln.DedupEvaluate, Coalesce), vuln-rescan (default, key vuln.RescanKey(id),
	// Exclusive) and vuln-findings-retention (maintenance) with its task.
	// <<< WP-B
}
