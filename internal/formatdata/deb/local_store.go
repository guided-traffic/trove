package deb

import (
	"context"
	"slices"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// localStore is the group of the data port that the local class writes and reads: uploads, coordinates, removal and the stored files
// (R-DEB-006, R-DEB-008, R-DEB-009, R-DEB-013).
type localStore struct{ *group }

func txQueries(tx meta.Tx) (*q.Queries, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errForeignTx
	}
	return q.New(ptx), nil
}

// read runs fn in the transaction when there is one and in a short one of its own otherwise.
func (g *group) read(ctx context.Context, tx meta.Tx, fn func(qs *q.Queries) error) error {
	run := func(tx meta.Tx) error {
		qs, err := txQueries(tx)
		if err != nil {
			return err
		}
		return fn(qs)
	}
	if tx != nil {
		return run(tx)
	}
	return g.o.DB.InTx(ctx, run)
}

func toCoords(in []q.DebCoordinatesOfRow) []rows.Coordinate {
	out := make([]rows.Coordinate, 0, len(in))
	for _, c := range in {
		out = append(out, rows.Coordinate{Suite: c.Suite, Component: c.Component})
	}
	return out
}

func suitesOf(cs []rows.Coordinate) []string {
	var out []string
	for _, c := range cs {
		if !slices.Contains(out, c.Suite) {
			out = append(out, c.Suite)
		}
	}
	slices.Sort(out)
	return out
}

func seqsOf(ctx context.Context, qs *q.Queries, repoID int64, suites []string) ([]rows.SuiteSeq, error) {
	list, err := qs.DebSuiteSeqs(ctx, q.DebSuiteSeqsParams{RepoID: repoID, Suites: suites})
	if err != nil {
		return nil, err
	}
	out := make([]rows.SuiteSeq, 0, len(list))
	for _, s := range list {
		out = append(out, rows.SuiteSeq{Suite: s.Suite, ChangeSeq: s.ChangeSeq, PublishedSeq: s.BuiltSeq})
	}
	return out, nil
}

// raise raises change_seq of the suites; a suite that does not exist is no change.
func raise(ctx context.Context, qs *q.Queries, repoID int64, suites []string) error {
	if len(suites) == 0 {
		return nil
	}
	_, err := qs.DebRaiseSuites(ctx, q.DebRaiseSuitesParams{RepoID: repoID, Suites: suites})
	return err
}

func binaryOf(r q.DebBinaryByItemRow) rows.Binary {
	b := rows.Binary{
		ItemID: r.ItemID, RepoID: r.RepoID, Path: r.Path, Package: r.Package, Version: r.Version, Architecture: r.Architecture,
		Source: r.Source, Control: r.Control, Size: r.Size, Trashed: r.Trashed,
	}
	copy(b.SHA256[:], r.Sha256)
	return b
}

// RecordUpload records a package file in the transaction that writes its item: the row, its coordinates (a suite exists from the first
// coordinate that names it) and the raise of change_seq of every suite that gained the file, so the next build reflects it. The file of
// an identical upload keeps its row and gains the coordinates of the request. A package, version and architecture stored with other
// bytes meets the unique key, which is the refusal 409 deb_version_exists (Q-DEB-4): the handler asks first, this is the race.
func (s localStore) RecordUpload(ctx context.Context, tx meta.Tx, in rows.UploadInput) (rows.UploadResult, error) {
	var res rows.UploadResult
	qs, err := txQueries(tx)
	if err != nil {
		return res, err
	}
	n, err := qs.DebInsertBinary(ctx, q.DebInsertBinaryParams{
		ItemID: in.ItemID, RepoID: in.RepoID, Package: in.Package, Version: in.Version, Architecture: in.Architecture, Source: in.Source, Control: in.Control,
	})
	if err != nil {
		if postgres.IsState(err, "23505") {
			return res, errs.E(errs.Conflict, errs.CodeDebVersionExists,
				in.Package+" "+in.Version+" "+in.Architecture+" is stored already with other bytes; delete the file first, or upload the changed package under a new version.")
		}
		return res, err
	}
	res.Created = n == 1
	touched := map[string]bool{}
	fresh := map[string]bool{}
	for _, c := range in.Coordinates {
		made, err := qs.DebEnsureSuite(ctx, q.DebEnsureSuiteParams{RepoID: in.RepoID, Suite: c.Suite})
		if err != nil {
			return res, err
		}
		if made == 1 {
			fresh[c.Suite] = true
		}
		added, err := qs.DebInsertCoordinate(ctx, q.DebInsertCoordinateParams{ItemID: in.ItemID, RepoID: in.RepoID, Suite: c.Suite, Component: c.Component})
		if err != nil {
			return res, err
		}
		if added == 1 {
			res.Added = append(res.Added, c)
			touched[c.Suite] = !fresh[c.Suite]
		}
	}
	if err := raise(ctx, qs, in.RepoID, mapKeys(touched)); err != nil {
		return res, err
	}
	all, err := qs.DebCoordinatesOf(ctx, in.ItemID)
	if err != nil {
		return res, err
	}
	if err := checkBounds(ctx, qs, in.RepoID, len(all), suitesOf(res.Added)); err != nil {
		return res, err
	}
	res.Coordinates = toCoords(all)
	if res.Suites, err = seqsOf(ctx, qs, in.RepoID, suitesOf(res.Coordinates)); err != nil {
		return res, err
	}
	return res, nil
}

func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k, on := range m {
		if on {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// SetCoordinates replaces the coordinates of a stored file (R-DEB-009) and raises change_seq of every suite that gained or lost it. The
// caller has authorized both sides. A file would be left without coordinates only by an empty list, which the caller refuses.
func (s localStore) SetCoordinates(ctx context.Context, tx meta.Tx, in rows.CoordinatesChange) (rows.CoordinatesChanged, error) {
	var out rows.CoordinatesChanged
	qs, err := txQueries(tx)
	if err != nil {
		return out, err
	}
	rowsB, err := qs.DebBinaryByPath(ctx, q.DebBinaryByPathParams{RepoID: in.RepoID, Path: in.Path})
	if err != nil {
		return out, err
	}
	if len(rowsB) == 0 {
		return out, errs.E(errs.NotFound, errs.CodeNotFound, "The repository holds no package file at "+in.Path+".")
	}
	b := rowsB[0]
	out.ItemID, out.Package, out.Version = b.ItemID, b.Package, b.Version
	copy(out.SHA256[:], b.Sha256)
	current, err := qs.DebCoordinatesOf(ctx, b.ItemID)
	if err != nil {
		return out, err
	}
	have := toCoords(current)
	touched := map[string]bool{}
	fresh := map[string]bool{}
	for _, c := range have {
		if !slices.Contains(in.Coordinates, c) {
			if _, err := qs.DebDeleteCoordinate(ctx, q.DebDeleteCoordinateParams{ItemID: b.ItemID, Suite: c.Suite, Component: c.Component}); err != nil {
				return out, err
			}
			out.Removed = append(out.Removed, c)
			touched[c.Suite] = true
		}
	}
	for _, c := range in.Coordinates {
		if slices.Contains(have, c) {
			continue
		}
		made, err := qs.DebEnsureSuite(ctx, q.DebEnsureSuiteParams{RepoID: in.RepoID, Suite: c.Suite})
		if err != nil {
			return out, err
		}
		if made == 1 {
			fresh[c.Suite] = true
		}
		if _, err := qs.DebInsertCoordinate(ctx, q.DebInsertCoordinateParams{ItemID: b.ItemID, RepoID: in.RepoID, Suite: c.Suite, Component: c.Component}); err != nil {
			return out, err
		}
		out.Added = append(out.Added, c)
		touched[c.Suite] = !fresh[c.Suite]
	}
	if err := raise(ctx, qs, in.RepoID, mapKeys(touched)); err != nil {
		return out, err
	}
	if err := checkBounds(ctx, qs, in.RepoID, len(in.Coordinates), suitesOf(out.Added)); err != nil {
		return out, err
	}
	out.Suites, err = seqsOf(ctx, qs, in.RepoID, suitesOf(in.Coordinates))
	return out, err
}

// The bounds of R-DEB-009: 32 coordinates per file, 64 suites per repository, 32 components per suite. They are read after the rows
// are written, in the transaction, so a crossing rolls the change back.
const (
	maxFileCoordinates = 32
	maxRepoSuites      = 64
	maxSuiteComponents = 32
)

func outOfRange(pointer, detail string) error {
	return errs.E(errs.Invalid, errs.CodeValidation, detail, errs.WithFields(errs.FieldError{Pointer: pointer, Code: "out_of_range", Detail: detail}))
}

func checkBounds(ctx context.Context, qs *q.Queries, repoID int64, fileCoordinates int, touched []string) error {
	if fileCoordinates > maxFileCoordinates {
		return outOfRange("/coordinates", "A file has at most 32 coordinates.")
	}
	n, err := qs.DebSuiteCount(ctx, repoID)
	if err != nil {
		return err
	}
	if n > maxRepoSuites {
		return outOfRange("/coordinates", "A repository has at most 64 suites.")
	}
	for _, suite := range touched {
		c, err := qs.DebComponentCount(ctx, q.DebComponentCountParams{RepoID: repoID, Suite: suite})
		if err != nil {
			return err
		}
		if c > maxSuiteComponents {
			return outOfRange("/coordinates", "A suite has at most 32 components.")
		}
	}
	return nil
}

// Removed runs for the removal of an item of a local repository: change_seq of the file's suites rises, and with purge the rows go
// (the coordinates with them, by the foreign key). The rows of a file in the trash can stay, so a restore needs no rebuild of them. The
// item store deleted the item of a permanent removal before the hook ran, so the path and the digest come from the event.
func (s localStore) Removed(ctx context.Context, tx meta.Tx, repoID, itemID int64, path string, sha256 [32]byte, purge bool) (rows.RemovedFile, bool, error) {
	var out rows.RemovedFile
	qs, err := txQueries(tx)
	if err != nil {
		return out, false, err
	}
	list, err := qs.DebBinaryRow(ctx, q.DebBinaryRowParams{ItemID: itemID, RepoID: repoID})
	if err != nil || len(list) == 0 {
		return out, false, err
	}
	b := list[0]
	return s.leaveRow(ctx, qs, repoID, itemID, purge, rows.RemovedFile{
		ItemID: itemID, Path: path, SHA256: sha256, Package: b.Package, Version: b.Version, Architecture: b.Architecture,
	})
}

// Restored runs for an item restored from the trash can: change_seq of the file's suites is raised, so the next build lists it again.
func (s localStore) Restored(ctx context.Context, tx meta.Tx, repoID, itemID int64) (rows.RemovedFile, bool, error) {
	var out rows.RemovedFile
	qs, err := txQueries(tx)
	if err != nil {
		return out, false, err
	}
	list, err := qs.DebBinaryByItem(ctx, q.DebBinaryByItemParams{ItemID: itemID, RepoID: repoID})
	if err != nil || len(list) == 0 {
		return out, false, err
	}
	b := binaryOf(list[0])
	return s.leaveRow(ctx, qs, repoID, itemID, false, rows.RemovedFile{
		ItemID: itemID, Path: b.Path, SHA256: b.SHA256, Package: b.Package, Version: b.Version, Architecture: b.Architecture,
	})
}

// leaveRow raises the suites of a file that leaves or returns, deletes its rows for a permanent removal and counts the files of its
// version that remain.
func (s localStore) leaveRow(ctx context.Context, qs *q.Queries, repoID, itemID int64, purge bool, out rows.RemovedFile) (rows.RemovedFile, bool, error) {
	cs, err := qs.DebCoordinatesOf(ctx, itemID)
	if err != nil {
		return out, false, err
	}
	out.Suites = suitesOf(toCoords(cs))
	if err := raise(ctx, qs, repoID, out.Suites); err != nil {
		return out, false, err
	}
	if purge {
		if _, err := qs.DebDeleteBinary(ctx, itemID); err != nil {
			return out, false, err
		}
	}
	left, err := qs.DebVersionFiles(ctx, q.DebVersionFilesParams{RepoID: repoID, Package: out.Package, Version: out.Version, ExceptItem: itemID})
	if err != nil {
		return out, false, err
	}
	out.VersionFiles = int(left)
	return out, true, nil
}

// Binary reads the row of the live package file at a canonical path; found is false for a path that holds none.
func (s localStore) Binary(ctx context.Context, repoID int64, path string) (rows.Binary, bool, error) {
	var out rows.Binary
	found := false
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		list, err := qs.DebBinaryByPath(ctx, q.DebBinaryByPathParams{RepoID: repoID, Path: path})
		if err != nil || len(list) == 0 {
			return err
		}
		out, found = binaryOf(q.DebBinaryByItemRow(list[0])), true
		cs, err := qs.DebCoordinatesOf(ctx, out.ItemID)
		out.Coordinates = toCoords(cs)
		return err
	})
	return out, found, err
}

// BinaryOf reads the row stored for a package, version and architecture, a file in the trash can included; found is false when there is
// none. It reads in tx when there is one.
func (s localStore) BinaryOf(ctx context.Context, tx meta.Tx, repoID int64, pkg, version, architecture string) (rows.Binary, bool, error) {
	var out rows.Binary
	found := false
	err := s.read(ctx, tx, func(qs *q.Queries) error {
		list, err := qs.DebBinaryOf(ctx, q.DebBinaryOfParams{RepoID: repoID, Package: pkg, Version: version, Architecture: architecture})
		if err != nil || len(list) == 0 {
			return err
		}
		out, found = binaryOf(q.DebBinaryByItemRow(list[0])), true
		cs, err := qs.DebCoordinatesOf(ctx, out.ItemID)
		out.Coordinates = toCoords(cs)
		return err
	})
	return out, found, err
}
