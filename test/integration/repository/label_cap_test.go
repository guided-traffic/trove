//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/observability"
	"github.com/guided-traffic/trove/internal/repository"
)

// R-OPS-007, Q-OPS-10: the snapshot a replica rebuilds is what the metrics label cap follows. After a change of the
// repository configuration, a repository that matches metrics.repository_label_include takes a place of its own series
// before the older ones, and one that does not and arrives past the cap folds into _other.
func TestR_OPS_007_TheLabelCapFollowsTheRepositorySnapshot(t *testing.T) {
	e := newStackEnv(t)
	e.stack.Repository("first", "local", "generic", "")
	web := e.newReplica("cap-web", stackOpts{})
	m := observability.NewMetrics("web", observability.WithMetricsConfig(config.Metrics{
		Enabled:     true,
		LabelLimits: config.MetricsLabelLimits{Repository: 2}, RepositoryLabelInclude: []string{"it/late-*"},
	}))
	p := e.newProbe("cap-probe", 100*time.Millisecond, func(repos []repository.Repository) {
		labels := make([]observability.RepoLabel, len(repos))
		for i, r := range repos {
			labels[i] = observability.RepoLabel{Name: r.Name.String(), Project: r.Name.Project, Key: r.Name.Key, Class: string(r.Class), Format: r.Format}
		}
		m.Repos().UpdateRepositories(labels)
	})
	ctx := context.Background()
	if _, err := p.reader.List(ctx); err != nil {
		t.Fatal(err)
	}
	if got := m.Repos().Value("f", "it/first"); got != "it/first" {
		t.Fatalf("the repository of the first snapshot is labelled %q", got)
	}

	for _, key := range []string{"plain", "late-1"} {
		if _, err := web.svc.Create(ctx, caller, "it", []byte(fmt.Sprintf(genericBody, key))); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "the rebuilt snapshot reaching the label cap", 10*time.Second, func() bool {
		if _, err := p.reader.List(ctx); err != nil {
			t.Fatal(err)
		}
		return m.Repos().Value("f", "it/late-1") == "it/late-1"
	})
	// Limit 2: the selector's match comes first, the oldest repository fills the second place.
	for key, want := range map[string]string{"it/late-1": "it/late-1", "it/first": "it/first", "it/plain": observability.OtherLabel} {
		if got := m.Repos().Value("f", key); got != want {
			t.Errorf("%s -> %q, want %q", key, got, want)
		}
	}
}

// R-REPO-060: a job holds its repository by the id. A replica that has not applied a creation yet polls the change log once
// and answers, as it does for a name (R-ARCH-011 step 2); an id that exists nowhere is not found.
func TestR_REPO_060_TheReaderResolvesARepositoryByItsID(t *testing.T) {
	e := newStackEnv(t)
	e.stack.Repository("first", "local", "generic", "")
	web := e.newReplica("byid-web", stackOpts{})
	p := e.newProbe("byid-probe", time.Hour)
	ctx := context.Background()
	if _, err := p.reader.List(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := web.svc.Create(ctx, caller, "it", []byte(fmt.Sprintf(genericBody, "by-id")))
	if err != nil {
		t.Fatal(err)
	}
	id := st.Repository.NumericID()
	if id == 0 {
		t.Fatalf("the created repository has no id: %+v", st.Repository)
	}
	got, err := p.reader.ByID(ctx, id)
	if err != nil || got.Name != qr("by-id") || got.NumericID() != id {
		t.Fatalf("ByID(%d) = %+v, %v", id, got.Name, err)
	}
	if _, err := p.reader.ByID(ctx, id+1_000_000); errs.KindOf(err) != errs.NotFound {
		t.Fatalf("an id that exists nowhere: %v", err)
	}
}
