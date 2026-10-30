package packages

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// bumpsKey is the slot of meta.Tx.Value that holds the revisions this
// transaction raised, by package id. The slot belongs to one attempt of the
// transaction: a retry starts empty and raises again.
type bumpsKey struct{}

type bumps struct{ rev map[int64]int64 }

func bumpsOf(tx meta.Tx) *bumps {
	if b, ok := tx.Value(bumpsKey{}).(*bumps); ok {
		return b
	}
	b := &bumps{rev: map[int64]int64{}}
	tx.SetValue(bumpsKey{}, b)
	return b
}

// Writer writes the package rows inside one transaction. Writers made for the same
// transaction share the record of which revisions were raised, so a revision is
// raised once per package per transaction however many writers (a hook of the
// item store, a format's own write) touch it.
type Writer struct {
	tx     meta.Tx
	qs     *q.Queries
	prefix string
}

// NewWriter binds a writer to tx. propertyPrefix is the namespace SetProperties
// may write below ("npm."); "" refuses every property write.
func NewWriter(tx meta.Tx, propertyPrefix string) (*Writer, error) {
	qs, err := queries(tx)
	if err != nil {
		return nil, err
	}
	return &Writer{tx: tx, qs: qs, prefix: propertyPrefix}, nil
}

// bump raises the revision of a package once per transaction and returns the
// revision this transaction owns.
func (w *Writer) bump(ctx context.Context, pkg int64) (rev int64, at time.Time, fresh bool, err error) {
	b := bumpsOf(w.tx)
	if rev, ok := b.rev[pkg]; ok {
		r, err := w.qs.ReadPackageRevision(ctx, pkg)
		if err != nil {
			return 0, time.Time{}, false, err
		}
		return rev, r.At.Time, false, nil
	}
	r, err := w.qs.BumpPackageRevision(ctx, pkg)
	if err != nil {
		return 0, time.Time{}, false, err
	}
	b.rev[pkg] = r.RevSeq
	return r.RevSeq, r.At.Time, true, nil
}

func (w *Writer) touch(ctx context.Context, pkg int64) error {
	_, _, _, err := w.bump(ctx, pkg)
	return err
}

// EnsureLocked locks the package row of (repository, normalized name) FOR UPDATE
// and inserts it when it is missing. A package this call creates starts at
// revision 1, which counts as this transaction's raise; one that exists is not
// raised by being locked.
func (w *Writer) EnsureLocked(ctx context.Context, repoID int64, name, displayName string) (Ref, bool, error) {
	if displayName == "" {
		displayName = name
	}
	n, err := w.qs.InsertPackage(ctx, q.InsertPackageParams{RepoID: repoID, Name: name, DisplayName: displayName})
	if err != nil {
		return Ref{}, false, err
	}
	rows, err := w.qs.LockPackage(ctx, q.LockPackageParams{RepoID: repoID, Name: name})
	if err != nil {
		return Ref{}, false, err
	}
	if len(rows) != 1 {
		return Ref{}, false, errs.E(errs.Internal, errs.CodeInternal, "packages: the package row vanished under its lock")
	}
	r := rows[0]
	created := n == 1
	if created {
		bumpsOf(w.tx).rev[r.ID] = r.RevSeq
	}
	return Ref{ID: r.ID, RepoID: r.RepoID, Name: r.Name, RevSeq: r.RevSeq}, created, nil
}

// Versions lists the live versions of a package.
func (w *Writer) Versions(ctx context.Context, pkg Ref) ([]Version, error) {
	rows, err := w.qs.ListLiveVersions(ctx, pkg.ID)
	if err != nil {
		return nil, err
	}
	out := make([]Version, len(rows))
	for i, r := range rows {
		out[i] = Version{
			ID: r.ID, Version: r.Version, DisplayVersion: r.DisplayVersion, LeadItem: r.LeadItemID,
			Attributes: r.Attributes, CreatedAt: r.CreatedAt.Time, CreatedBy: r.CreatedBy,
		}
	}
	return out, nil
}

// InsertVersion adds a version of a package and links its lead item. A live
// version of the same name is a conflict.
func (w *Writer) InsertVersion(ctx context.Context, pkg Ref, v NewVersion) (int64, error) {
	attrs, err := attributes(v.Attributes)
	if err != nil {
		return 0, err
	}
	if v.DisplayVersion == "" {
		v.DisplayVersion = v.Version
	}
	ids, err := w.qs.InsertPackageVersion(ctx, q.InsertPackageVersionParams{
		PackageID: pkg.ID, Version: v.Version, DisplayVersion: v.DisplayVersion, LeadItemID: v.LeadItem,
		Attributes: attrs, CreatedBy: v.CreatedBy,
	})
	if err != nil {
		return 0, err
	}
	if len(ids) != 1 {
		return 0, errs.E(errs.Conflict, errs.CodeAlreadyExists, "The version already exists.")
	}
	if v.LeadItem != 0 {
		if _, err := w.qs.LinkItemToVersion(ctx, q.LinkItemToVersionParams{ItemID: v.LeadItem, VersionID: ids[0]}); err != nil {
			return 0, err
		}
	}
	return ids[0], w.touch(ctx, pkg.ID)
}

// TrashOrDeleteVersion trashes a version, or deletes its row when purge is set.
func (w *Writer) TrashOrDeleteVersion(ctx context.Context, version int64, purge bool) error {
	var (
		pkgs []int64
		err  error
	)
	if purge {
		pkgs, err = w.qs.DeletePackageVersion(ctx, version)
	} else {
		pkgs, err = w.qs.TrashPackageVersion(ctx, version)
	}
	if err != nil {
		return err
	}
	if len(pkgs) != 1 {
		return errs.E(errs.NotFound, errs.CodeNotFound, "The version does not exist.")
	}
	return w.touch(ctx, pkgs[0])
}

// SetPackageAttributes replaces the attributes of a package.
func (w *Writer) SetPackageAttributes(ctx context.Context, pkg Ref, attrs json.RawMessage) error {
	a, err := attributes(attrs)
	if err != nil {
		return err
	}
	if _, err := w.qs.SetPackageAttributes(ctx, q.SetPackageAttributesParams{ID: pkg.ID, Attributes: a}); err != nil {
		return err
	}
	return w.touch(ctx, pkg.ID)
}

// SetVersionAttributes replaces the attributes of a version.
func (w *Writer) SetVersionAttributes(ctx context.Context, version int64, attrs json.RawMessage) error {
	a, err := attributes(attrs)
	if err != nil {
		return err
	}
	pkgs, err := w.qs.SetPackageVersionAttributes(ctx, q.SetPackageVersionAttributesParams{ID: version, Attributes: a})
	if err != nil {
		return err
	}
	if len(pkgs) != 1 {
		return errs.E(errs.NotFound, errs.CodeNotFound, "The version does not exist.")
	}
	return w.touch(ctx, pkgs[0])
}

// LinkItem attaches a live item to a version of its repository; lead makes it the
// version's lead item.
func (w *Writer) LinkItem(ctx context.Context, item, version int64, lead bool) error {
	pkgs, err := w.qs.LinkItemToVersion(ctx, q.LinkItemToVersionParams{ItemID: item, VersionID: version})
	if err != nil {
		return err
	}
	if len(pkgs) != 1 {
		return errs.E(errs.NotFound, errs.CodeNotFound, "The item or the version does not exist in the repository.")
	}
	if lead {
		if _, err := w.qs.SetLeadItem(ctx, q.SetLeadItemParams{ItemID: item, VersionID: version}); err != nil {
			return err
		}
	}
	return w.touch(ctx, pkgs[0])
}

// SetProperties replaces the properties of an item below the writer's namespace
// prefix; a key outside it is refused. The package the item belongs to, if any,
// changes with them.
func (w *Writer) SetProperties(ctx context.Context, item int64, props map[string]string) error {
	if w.prefix == "" {
		return errs.E(errs.Internal, errs.CodeInternal, "packages: this writer has no property namespace")
	}
	for k := range props {
		if !strings.HasPrefix(k, w.prefix) {
			return errs.E(errs.Internal, errs.CodeInternal, "packages: the property "+k+" is outside the namespace "+w.prefix)
		}
	}
	if err := w.qs.DeleteItemProperties(ctx, q.DeleteItemPropertiesParams{ItemID: item, Prefix: w.prefix}); err != nil {
		return err
	}
	if len(props) > 0 {
		raw, err := json.Marshal(props)
		if err != nil {
			return err
		}
		if err := w.qs.InsertItemProperties(ctx, q.InsertItemPropertiesParams{ItemID: item, Props: string(raw)}); err != nil {
			return err
		}
	}
	pkgs, err := w.qs.GetItemPackage(ctx, item)
	if err != nil {
		return err
	}
	for _, p := range pkgs {
		if err := w.touch(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

// BumpRevision raises the revision of the package (once per transaction) and
// returns it with the database clock read after the row lock. It is for a change
// that writes only the format's own side rows; every call above already raised it.
func (w *Writer) BumpRevision(ctx context.Context, pkg Ref) (rev int64, at time.Time, err error) {
	rev, at, _, err = w.bump(ctx, pkg.ID)
	return rev, at, err
}

// attributes validates a JSON object and returns it as text; nothing is an empty
// object.
func attributes(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "{}", nil
	}
	if raw[0] != '{' || !json.Valid(raw) {
		return "", errs.E(errs.Internal, errs.CodeInternal, "packages: attributes are a JSON object")
	}
	return string(raw), nil
}
