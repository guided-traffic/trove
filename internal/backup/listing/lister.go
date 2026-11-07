package listing

import (
	"context"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/target"
)

// Lister implements [backup.Lister] over a target. It writes nothing (R-BAK-018).
type Lister struct {
	Store  target.Store
	Layout format.Layout
}

var _ backup.Lister = (*Lister)(nil)

// List returns the complete backups, newest first, and with opts.All the incomplete directories as well.
func (l *Lister) List(ctx context.Context, opts backup.ListOptions) ([]backup.Listing, error) {
	all, err := Scan(ctx, l.Store, l.Layout)
	if err != nil {
		return nil, err
	}
	out := make([]backup.Listing, 0, len(all))
	for _, b := range all {
		switch {
		case b.Complete():
			out = append(out, Describe(b.Manifest))
		case opts.All:
			out = append(out, backup.Listing{BackupID: b.ID})
		}
	}
	return out, nil
}

// Describe turns a manifest into the line of `trove backup list`.
func Describe(m *format.Manifest) backup.Listing {
	opts, enc := m.Options, m.Encryption
	return backup.Listing{
		BackupID:      m.BackupID,
		Complete:      true,
		SnapshotAt:    m.SnapshotAt,
		TroveVersion:  m.Trove.Version,
		SchemaStep:    m.Schema.Step,
		DatabaseBytes: DatabaseBytes(m),
		BlobCount:     m.Blobs.Referenced,
		BlobBytes:     m.Blobs.ReferencedBytes,
		BlobsCopied:   m.Blobs.Copied,
		Options:       &opts,
		KeyIDs:        append([]string(nil), m.Secrets.KeyIDs...),
		Incarnation:   m.Incarnation,
		Damaged:       len(m.Blobs.Damaged) > 0,
		Encryption:    &enc,
	}
}
