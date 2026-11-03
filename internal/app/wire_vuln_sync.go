package app

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/match"
	vmetrics "github.com/guided-traffic/trove/internal/vuln/metrics"
	vsync "github.com/guided-traffic/trove/internal/vuln/sync"
)

// The wiring of the advisory sync (work package A, docs/developer/vulnerabilities.md): the service over the database, the
// change log, the cache registry, the outbound client of purpose vuln_source and the worker's spool; its kinds and tasks; and
// the replica's copy of the state that vuln.States answers from. The marked blocks of wire_vuln.go call into this file.

// vulnAdapters returns the vulnerability adapter of every registered format that has one, by OSV ecosystem (R-VULN-003: the
// ecosystems are derived from the formats of the running release). It is read at run time, when the registry is complete.
func vulnAdapters() map[string]match.Adapter {
	out := map[string]match.Adapter{}
	for _, e := range catalog.Default().List() {
		if e.Ecosystem == "" {
			continue
		}
		if ad := formats.CapabilitiesFor(e.ID).Vulnerability; ad != nil {
			out[e.Ecosystem] = ad
		}
	}
	return out
}

// checkVulnEcosystems refuses a configuration that names an ecosystem no format of this release serves: the list may only
// narrow the set (R-VULN-003, R-VULN-025). The configuration validates against the fixed list of the release it knows
// (config.VulnerabilityEcosystems); this is the check against the format registry itself, which is the source of the list.
func checkVulnEcosystems(want []string, have map[string]match.Adapter) error {
	var bad []string
	for _, e := range want {
		if _, ok := have[e]; !ok {
			bad = append(bad, e)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	known := make([]string, 0, len(have))
	for e := range have {
		known = append(known, e)
	}
	slices.Sort(known)
	return fmt.Errorf("vulnerabilities.sources.osv.ecosystems names %s, which no registered format has a vulnerability adapter for (the release serves %s)",
		strings.Join(bad, ", "), strings.Join(known, ", "))
}

// newVulnSync builds the sync service and its state cache.
func (a *App) newVulnSync(in infra, m *vmetrics.Metrics) (*vsync.Service, error) {
	if err := requireInputs(AreaVuln, given("outbound factory", in.out), given("blob store", in.blobs)); err != nil {
		return nil, err
	}
	adapters := vulnAdapters
	if a.o.VulnAdapters != nil {
		adapters = a.o.VulnAdapters
	}
	if have := adapters(); len(have) > 0 {
		if err := checkVulnEcosystems(a.cfg.Vulnerabilities.Sources.OSV.Ecosystems, have); err != nil {
			return nil, err
		}
	}
	o := vsync.Options{
		DB: in.db, Changes: in.changes, Caches: in.caches, Spool: in.blobs, Recorder: in.rec, Metrics: m,
		// Redirects stay https, at most 3, each target checked by the egress rules; the timeouts are the run's own per request
		// and per archive. The outbound proxy is the factory's environment setting (Q-VULN-32).
		HTTP: in.out.Client(vsync.Purpose, outbound.WithMaxRedirects(3), outbound.WithResponseHeaderTimeout(30*time.Second)),
		Mirror: vsync.RepositoryMirror{
			Repos: func() repository.Reader { return part[repositoryArea](a.parts).reader },
			Items: func() *items.Store { s, _ := a.itemStore.get(); return s },
		},
		Config: a.cfg.Vulnerabilities, Adapters: adapters, Log: a.log,
	}
	if in.queue != nil {
		o.Jobs = in.queue
	}
	return vsync.New(o)
}

// VulnSync returns the advisory sync; nil in the shell profile. The integration tests of the sync call it the way the jobs do.
func (a *App) VulnSync() *vsync.Service { return part[vulnArea](a.parts).sync }

// registerVulnSyncJobs registers the three kinds and tasks of the sync (R-VULN-004, R-VULN-008): queue maintenance, exclusive,
// MaxAttempts 1, so one OSV sync runs at a time (both OSV kinds share the dedup key of their tasks) and a failed run is not
// retried within its slot.
func (a *App) registerVulnSyncJobs(r *jobs.Registry) {
	osvHandler := func(full bool) jobs.Handler {
		return func(ctx context.Context, run *jobs.Run) error {
			svc := part[vulnArea](a.parts).sync
			if svc == nil {
				return fmt.Errorf("vulnerability sync: this replica has not wired it yet")
			}
			var args vuln.SyncArgs
			if len(run.Args) > 0 {
				_ = json.Unmarshal(run.Args, &args) // a body that is not the parameters is a plain run
			}
			return svc.RunOSV(ctx, run, vsync.Params{AcceptShrink: args.AcceptShrink, Principal: run.CreatedBy}, full)
		}
	}
	kev := func(ctx context.Context, run *jobs.Run) error {
		svc := part[vulnArea](a.parts).sync
		if svc == nil {
			return fmt.Errorf("vulnerability sync: this replica has not wired it yet")
		}
		return svc.RunKEV(ctx, run, vsync.Params{Principal: run.CreatedBy})
	}
	for _, k := range []struct {
		kind    string
		handler jobs.Handler
	}{{vuln.KindSyncOSV, osvHandler(false)}, {vuln.KindSyncOSVFull, osvHandler(true)}, {vuln.KindSyncKEV, kev}} {
		r.RegisterKind(k.kind, jobs.KindSpec{Queue: vuln.QueueMaintenance, DedupMode: jobs.Exclusive, MaxAttempts: 1, Handler: k.handler})
	}
	for _, td := range vuln.TaskDefaults() {
		switch td.Name {
		case vuln.TaskSyncOSV, vuln.TaskSyncOSVFull, vuln.TaskSyncKEV:
			r.RegisterTask(td.Name, a.vulnTaskSpec(td))
		}
	}
}
