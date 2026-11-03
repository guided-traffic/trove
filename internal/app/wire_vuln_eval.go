package app

import (
	"context"
	"errors"
	"sort"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/evaluate"
)

// The wiring of the evaluation of the vulnerability scanning (docs/developer/vulnerabilities.md): the service
// behind the Evaluator port, and the kinds vuln-evaluate, vuln-rescan and vuln-findings-retention.

// vulnEcosystems lists the ecosystems of the registered formats that have the vulnerability adapter, each with the IDs of the
// formats that store its packages. The adapter is the format's own, taken by structural typing (match.Adapter).
func vulnEcosystems() []evaluate.Ecosystem {
	byName := map[string]*evaluate.Ecosystem{}
	for _, f := range formats.List() {
		adapter := formats.CapabilitiesOf(f).Vulnerability
		if adapter == nil {
			continue
		}
		e := byName[adapter.Ecosystem()]
		if e == nil {
			e = &evaluate.Ecosystem{Name: adapter.Ecosystem(), Adapter: adapter}
			byName[e.Name] = e
		}
		e.Formats = append(e.Formats, string(f.Descriptor().ID))
	}
	out := make([]evaluate.Ecosystem, 0, len(byName))
	for _, e := range byName {
		sort.Strings(e.Formats)
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// wireVulnEvaluation builds the evaluation over the replica's repository snapshot and claim index and puts it in the area as
// the Evaluator. The data state is the replica's cache (vuln.States); without it a commit only adds findings.
func (a *App) wireVulnEvaluation(_ context.Context, in infra, rp repositoryArea, vr virtualArea, out *vulnArea) error {
	if rp.reader == nil || vr.routing == nil {
		return errors.New("vuln: the evaluation needs the repository snapshot and the claim index")
	}
	cfg := a.cfg.Vulnerabilities
	svc, err := evaluate.New(evaluate.Options{
		Runner: in.db, Repos: rp.reader, Claims: vr.routing, States: out.states, Recorder: in.rec, Metrics: out.metrics,
		Ecosystems: vulnEcosystems(), Enabled: cfg.Enabled, Enforcement: vuln.Enforcement(cfg.Enforcement),
		PublicUpstreams: cfg.Matching.PublicUpstreams, Retention: cfg.Findings.ResolvedRetention,
		Log: a.log.With("component", "vuln-evaluate"),
	})
	if err != nil {
		return err
	}
	out.evaluator = svc
	return nil
}

// evaluation returns the evaluation service of the replica's area; false until the area is wired.
func (v vulnArea) evaluation() (*evaluate.Service, bool) {
	svc, ok := v.evaluator.(*evaluate.Service)
	return svc, ok && svc != nil
}

// registerVulnEvaluationJobs registers the kinds of the evaluation and the task of the retention. The handlers find the
// service at run time through the area, as the CI tasks do: the registry is built before the area is wired, and a job claimed
// before then is retried like any failure.
func (a *App) registerVulnEvaluationJobs(r *jobs.Registry) {
	handler := func(run func(ctx context.Context, svc *evaluate.Service, j *jobs.Run) error) jobs.Handler {
		return func(ctx context.Context, j *jobs.Run) error {
			svc, ok := part[vulnArea](a.parts).evaluation()
			if !ok {
				return errors.New("vulnerability evaluation: this replica has not wired it yet")
			}
			return run(ctx, svc, j)
		}
	}
	r.RegisterKind(vuln.KindEvaluate, jobs.KindSpec{
		Queue: vuln.QueueDefault, DedupMode: jobs.Coalesce,
		Handler: handler(func(ctx context.Context, svc *evaluate.Service, j *jobs.Run) error { return svc.RunEvaluate(ctx, j) }),
	})
	r.RegisterKind(vuln.KindRescan, jobs.KindSpec{
		Queue: vuln.QueueDefault, DedupMode: jobs.Exclusive,
		Handler: handler(func(ctx context.Context, svc *evaluate.Service, j *jobs.Run) error {
			return svc.RunRescan(ctx, j, j.Args)
		}),
	})
	r.RegisterKind(vuln.KindFindingsRetention, jobs.KindSpec{
		Queue: vuln.QueueMaintenance, DedupMode: jobs.Exclusive, MaxAttempts: 3,
		Handler: handler(func(ctx context.Context, svc *evaluate.Service, j *jobs.Run) error { return svc.RunRetention(ctx, j) }),
	})
	for _, td := range vuln.TaskDefaults() {
		if td.Name == vuln.TaskFindingsRetention {
			r.RegisterTask(td.Name, a.vulnTaskSpec(td))
		}
	}
}
