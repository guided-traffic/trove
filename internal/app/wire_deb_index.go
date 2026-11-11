package app

import (
	"context"
	"encoding/json"

	"github.com/prometheus/client_golang/prometheus"

	debdata "github.com/guided-traffic/trove/internal/formatdata/deb"
	"github.com/guided-traffic/trove/internal/formats"
	debformat "github.com/guided-traffic/trove/internal/formats/deb"
	"github.com/guided-traffic/trove/internal/formats/deb/index"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// startDebIndexer builds the indexer of the job deb-index (R-DEB-010) on a replica that claims jobs and keeps it in the App, where the
// registry's handlers (debHandlers) and [App.DebIndexer] find it; a second replica of the process has its own. The publication port is the item
// store, which the files area builds later, so the builder asks for it when a build runs.
func (a *App) startDebIndexer(ports map[formats.ID]any) {
	st, ok := ports[debformat.ID].(*debdata.Store)
	if !ok {
		return
	}
	m := a.tele.metrics
	obs := debObserver{
		builds:  m.NewCounterVec("trove_deb_index_builds_total", "Builds of the indexes of a Debian suite by outcome (R-DEB-033).", "class", "outcome"),
		seconds: m.NewHistogramVec("trove_deb_index_build_seconds", "Duration of the build of a Debian suite's indexes (R-DEB-033).", debBuildBuckets(), "class"),
		delay:   m.NewHistogramVec("trove_deb_index_delay_seconds", "From the first change a published Debian generation covers to its commit (R-DEB-010, R-DEB-033).", debDelayBuckets(), "class"),
	}
	remote, _ := a.debRemote.get()
	a.debIndexer.set(&debdata.Indexer{
		Observe: obs, Remote: remote, Virtual: debVirtualRepos{a: a, st: st}, Local: debMemberIndexes{a: a},
		VirtualInterval: a.cfg.Deb.VirtualIndexInterval,
		Store:           st,
		Publisher: func() items.Publisher {
			if s, ok := a.itemStore.get(); ok {
				return s
			}
			return nil
		},
		Repos:      debRepos{st: st, db: a.database()},
		ByHashKeep: a.cfg.Deb.ByHashKeep,
		Log:        a.log.With("component", "deb-index"),
	})
}

// DebIndexer is this replica's builder of the Debian indexes, or nil on a replica that claims no jobs. The integration tests set its
// fault injection and enqueue the due builds through it, each on the replica it started.
func (a *App) DebIndexer() *debdata.Indexer {
	x, _ := a.debIndexer.get()
	return x
}

// debRepos reads the repository a build is for: its names, its class and the settings of its indexes (R-DEB-008).
type debRepos struct {
	st *debdata.Store
	db meta.Runner
}

var _ debdata.Repos = debRepos{}

// IndexRepo finds a local Debian repository by id; any other repository is not found, which ends its job.
func (r debRepos) IndexRepo(ctx context.Context, id int64) (debdata.IndexRepo, bool, error) {
	var rows []q.DebRepositoryOfRow
	if err := r.db.InTx(ctx, func(tx meta.Tx) (err error) {
		qs, err := r.st.Queries(tx)
		if err != nil {
			return err
		}
		rows, err = qs.DebRepositoryOf(ctx, id)
		return err
	}); err != nil || len(rows) == 0 {
		return debdata.IndexRepo{}, false, err
	}
	row := rows[0]
	if row.Format != string(debformat.ID) || row.Class != string(repository.Local) {
		return debdata.IndexRepo{}, false, nil
	}
	var doc struct {
		Deb json.RawMessage `json:"deb"`
	}
	_ = json.Unmarshal(row.Config, &doc) // a document the service did not write yields the defaults
	set := debformat.SettingsOf(doc.Deb)
	label := set.Label
	if label == "" {
		label = row.Key
	}
	name := qname.Repo{Project: row.Project, Key: row.Key}
	return debdata.IndexRepo{
		Ref:     items.RepoRef{ID: row.ID, Name: name, Class: row.Class, Format: row.Format},
		Project: row.Project, Key: row.Key,
		Settings: index.Settings{Origin: set.Origin, Label: label, Architectures: set.Architectures, XZ: containsString(set.Compression, "xz")},
	}, true, nil
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// The buckets of the build metrics: a build takes from milliseconds to a minute, and the delay is the index delay (1 s to 60 s) plus a build.
func debBuildBuckets() []float64 { return []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60} }

func debDelayBuckets() []float64 { return []float64{0.5, 1, 2, 3, 4, 5, 7, 10, 15, 30, 60, 120} }

// debObserver records the measurements of the index builds (R-DEB-033), labelled with the class of the repository.
type debObserver struct {
	builds         *prometheus.CounterVec
	seconds, delay *prometheus.HistogramVec
}

func (o debObserver) Build(class, outcome string, seconds float64) {
	o.builds.WithLabelValues(class, outcome).Inc()
	if outcome != debdata.OutcomeUnchanged {
		o.seconds.WithLabelValues(class).Observe(seconds)
	}
}

func (o debObserver) Delay(class string, seconds float64) {
	o.delay.WithLabelValues(class).Observe(seconds)
}
