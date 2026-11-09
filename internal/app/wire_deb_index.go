package app

import (
	"context"
	"encoding/json"

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

// startDebIndexer binds the builder of the job deb-index (R-DEB-010) on a replica that claims jobs. The publication port is the item
// store, which the files area builds later, so the builder asks for it when a build runs.
func (a *App) startDebIndexer(ports map[formats.ID]any) {
	st, ok := ports[debformat.ID].(*debdata.Store)
	if !ok {
		return
	}
	debdata.BindIndexer(&debdata.Indexer{
		Store: st,
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
