package app

import (
	"context"
	"slices"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	debformat "github.com/guided-traffic/trove/internal/formats/deb"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/packages"
)

// debAPI is the services of the Debian operations of the management API (wire_deb_api.go). Without a database or without the Debian
// data port the member stays nil and its operation answers 503.
func (a *App) debAPI() v1.DebServices {
	db := a.database()
	if db == nil {
		return v1.DebServices{}
	}
	b := part[formatPortsArea](a.parts).bindings
	if b == nil {
		return v1.DebServices{}
	}
	st, ok := b.Get(debformat.ID).(debformat.Store)
	if !ok {
		return v1.DebServices{}
	}
	return v1.DebServices{Coordinates: debCoordinates{st: st, run: db}}
}

// debCoordinates replaces the coordinates of a stored package file (R-DEB-009).
type debCoordinates struct {
	st  debformat.Store
	run meta.Runner
}

var _ v1.DebCoordinateService = debCoordinates{}

// Replace validates the list, decides deploy for what is added and delete for what is removed on the file's package, and writes the
// change, its properties and its events in one transaction. The sides are decided before the transaction from the file as it was
// read; if the file changed meanwhile so that the change differs, the request is refused instead of being applied unchecked.
func (d debCoordinates) Replace(ctx context.Context, in v1.DebCoordinateChange) (any, error) {
	pairs := make([][2]string, 0, len(in.Coordinates))
	for _, c := range in.Coordinates {
		pairs = append(pairs, [2]string{c.Distribution, c.Component})
	}
	want, err := debformat.CoordinateList(pairs)
	if err != nil {
		return nil, err
	}
	b, found, err := d.st.Binary(ctx, in.RepoID, in.Path)
	if err != nil {
		return nil, err
	}
	if !found || b.Trashed {
		return nil, errs.E(errs.NotFound, errs.CodeNotFound, "The repository holds no package file at "+in.Path+".")
	}
	added, removed := diff(b.Coordinates, want)
	if len(added) > 0 {
		if err := in.Decide(b.Package, authz.Deploy); err != nil {
			return nil, err
		}
	}
	if len(removed) > 0 {
		if err := in.Decide(b.Package, authz.Delete); err != nil {
			return nil, err
		}
	}
	if len(added) == 0 && len(removed) == 0 {
		// Nothing to change still needs the right to change it.
		if err := in.Decide(b.Package, authz.Deploy); err != nil {
			return nil, err
		}
	}
	if t, tomb, err := d.st.Tombstone(ctx, in.RepoID, in.Path); err != nil {
		return nil, err
	} else if tomb && t.SHA256 == b.SHA256 {
		return nil, errs.E(errs.Conflict, errs.CodeDebWithheld,
			"This file was deleted before a restore and is withheld; an administrator reinstates it with trove admin deb reinstate.")
	}
	var res rows.CoordinatesChanged
	err = d.run.InTx(ctx, func(tx meta.Tx) error {
		if err := in.Own(ctx, tx, b.Package); err != nil {
			return err
		}
		res, err = d.st.SetCoordinates(ctx, tx, rows.CoordinatesChange{RepoID: in.RepoID, Path: in.Path, Coordinates: want, Actor: in.Actor})
		if err != nil {
			return err
		}
		if !subset(res.Added, added) || !subset(res.Removed, removed) {
			return errs.E(errs.Conflict, errs.CodeConflict, "The coordinates of the file changed while the request was decided; read them and retry.")
		}
		if len(res.Added) == 0 && len(res.Removed) == 0 {
			return nil
		}
		w, err := packages.NewWriter(tx, "deb.")
		if err != nil {
			return err
		}
		if err := w.SetProperties(ctx, b.ItemID, debformat.FileProperties(b, want)); err != nil {
			return err
		}
		ev := rows.CoordinatesChangedEvent{RepoID: in.RepoID, RepoKey: in.Repo.String(), Path: in.Path, Added: res.Added, Removed: res.Removed, Actor: in.Actor}
		if err := d.st.RecordCoordinatesChanged(ctx, tx, ev); err != nil {
			return err
		}
		if len(res.Removed) == 0 {
			return nil
		}
		return d.st.RecordCoordinatesRemoved(ctx, tx, rows.CoordinatesRemoved{
			RepoID: in.RepoID, RepoKey: in.Repo.String(), Path: in.Path, SHA256: b.SHA256, Removed: res.Removed, Actor: in.Actor,
		})
	})
	if err != nil {
		return nil, err
	}
	return debformat.AnswerOf(in.Path, b.Package, b.Version, b.Architecture, b.SHA256[:], rows.UploadResult{Coordinates: want, Suites: res.Suites}), nil
}

// diff is what a replacement adds to and removes from the present coordinates.
func diff(have, want []rows.Coordinate) (added, removed []rows.Coordinate) {
	for _, c := range want {
		if !slices.Contains(have, c) {
			added = append(added, c)
		}
	}
	for _, c := range have {
		if !slices.Contains(want, c) {
			removed = append(removed, c)
		}
	}
	return added, removed
}

func subset(got, decided []rows.Coordinate) bool {
	for _, c := range got {
		if !slices.Contains(decided, c) {
			return false
		}
	}
	return true
}
