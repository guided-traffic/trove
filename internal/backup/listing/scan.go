// Package listing reads the backups of a target (R-BAK-018): the scan that tells complete backups from incomplete
// directories, and the Lister behind `trove backup list`. Retention, the pool sweep and verify use the same scan, so every
// operation agrees on what a backup is: a directory whose manifest exists (R-BAK-002).
package listing

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/target"
)

// Backup is one directory below backups/. Manifest is nil for an incomplete directory.
type Backup struct {
	ID       format.ID
	Manifest *format.Manifest
}

// Complete reports whether the directory has a manifest.
func (b Backup) Complete() bool { return b.Manifest != nil }

// Scan lists backups/ once and reads the manifest of every directory that has one. The result is newest first. A key
// below backups/ that is not <id>/<name> is ignored: only trove writes there, and a stray key must not stop an operation
// that never touches it.
//
// A manifest that cannot be read, or that names another backup ID than its directory, fails the scan: retention could not
// tell what to keep, and the sweep could not build the keep set. A manifest of a higher format yields a
// [*format.UnknownFormatError] (exit 6).
func Scan(ctx context.Context, store target.Store, l format.Layout) ([]Backup, error) {
	return ScanTicking(ctx, store, l, nil)
}

// PageSize is how many keys a listing page holds; a listing reports progress once per page (R-BAK-013).
const PageSize = 1000

// ScanTicking is [Scan] for a run that must report progress: tick is called once per page of keys and after every manifest.
func ScanTicking(ctx context.Context, store target.Store, l format.Layout, tick func()) ([]Backup, error) {
	dirs, err := DirsTicking(ctx, store, l, tick)
	if err != nil {
		return nil, err
	}
	out := make([]Backup, 0, len(dirs))
	for _, d := range dirs {
		b := Backup{ID: d.ID}
		if d.HasManifest {
			m, err := ReadManifest(ctx, store, l, d.ID)
			if err != nil {
				return nil, err
			}
			b.Manifest = m
			if tick != nil {
				tick()
			}
		}
		out = append(out, b)
	}
	return out, nil
}

// Dir is a directory below backups/ and whether it holds a manifest.
type Dir struct {
	ID          format.ID
	HasManifest bool
}

// Dirs lists backups/ once without reading any manifest, newest first.
func Dirs(ctx context.Context, store target.Store, l format.Layout) ([]Dir, error) {
	return DirsTicking(ctx, store, l, nil)
}

// DirsTicking is [Dirs] for a run that must report progress: tick is called once per [PageSize] keys.
func DirsTicking(ctx context.Context, store target.Store, l format.Layout, tick func()) ([]Dir, error) {
	hasManifest := map[format.ID]bool{}
	n := 0
	err := store.List(ctx, l.BackupsPrefix(), func(o target.Object) error {
		if n++; tick != nil && n%PageSize == 0 {
			tick()
		}
		id, inner, ok := l.ParseBackupKey(o.Key)
		if !ok {
			return nil
		}
		hasManifest[id] = hasManifest[id] || inner == format.ManifestName
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing the backups of the target: %w", err)
	}
	out := make([]Dir, 0, len(hasManifest))
	for id, has := range hasManifest {
		out = append(out, Dir{ID: id, HasManifest: has})
	}
	slices.SortFunc(out, func(a, b Dir) int { return strings.Compare(string(b.ID), string(a.ID)) })
	return out, nil
}

// ReadManifest reads and validates the manifest of a backup. The error of a missing manifest wraps target.ErrNotFound.
func ReadManifest(ctx context.Context, store target.Store, l format.Layout, id format.ID) (*format.Manifest, error) {
	rc, _, err := store.Get(ctx, l.Manifest(id))
	if err != nil {
		return nil, fmt.Errorf("manifest of backup %s: %w", id, err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("manifest of backup %s: %w", id, err)
	}
	m, err := format.DecodeManifest(raw)
	if err != nil {
		return nil, fmt.Errorf("backup %s: %w", id, err)
	}
	if m.BackupID != id {
		return nil, fmt.Errorf("manifest of backup %s names backup %s", id, m.BackupID)
	}
	return m, nil
}

// Complete returns the complete backups of a scan, in the order of the scan.
func Complete(all []Backup) []Backup {
	out := make([]Backup, 0, len(all))
	for _, b := range all {
		if b.Complete() {
			out = append(out, b)
		}
	}
	return out
}

// DatabaseBytes is the size of the database part of a backup on the target: the compressed table files. The result of a
// create run and the listing use this one definition.
func DatabaseBytes(m *format.Manifest) int64 {
	var n int64
	for _, t := range m.Tables {
		n += t.CompressedBytes
	}
	return n
}
