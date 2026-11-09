// Package restoreversions is the logic of `trove admin storage restore-versions --before <t>` (R-HA-025 step 5): after a
// point-in-time restore of the database, the objects that the old installation's collector deleted after t exist in the
// versioned blob bucket under a delete marker. The package finds the blobs whose object is missing (the check of
// `trove admin storage verify`, over every live blob row) and removes the delete markers younger than t from exactly those
// keys, so that the object of the version before it is current again.
//
// It removes nothing else: a delete marker of a key that no blob row references stays (the orphan sweep or the grace period
// of the collector deals with those objects after the deletions resume), a delete marker older than t stays (the object was
// already gone at t), and no object version is ever deleted.
package restoreversions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/blob/verify"
)

// Outcomes of one blob.
const (
	// OutcomeRestored: the delete markers younger than t are removed and the object is there again.
	OutcomeRestored = "restored"
	// OutcomeWouldRestore: a dry run found a delete marker younger than t above an object version.
	OutcomeWouldRestore = "would_restore"
	// OutcomeNoDeleteMarker: the key has no delete marker younger than t, so the object is not missing because of a deletion
	// after t.
	OutcomeNoDeleteMarker = "no_delete_marker"
	// OutcomeNoVersion: the key has no object version below its delete markers, so nothing can be made current again.
	OutcomeNoVersion = "no_version"
	// OutcomeStillMissing: the markers are removed and the object is still not there.
	OutcomeStillMissing = "still_missing"
)

// Store is the object store side of a run: the existence check of the verification, the listing of the versions of a key and
// the removal of one version. *s3.Client satisfies it; the credential must be one that may list and delete object versions.
type Store interface {
	verify.Objects
	ListVersions(ctx context.Context, key string) ([]s3.ObjectVersion, error)
	DeleteVersion(ctx context.Context, key, versionID string) error
}

// Options configure a run.
type Options struct {
	DB    verify.Source
	Store Store
	// Prefix is storage.s3.prefix.
	Prefix string
	// Before is t: delete markers created after it are removed.
	Before time.Time
	// DryRun lists what a run would do and removes nothing.
	DryRun bool
	// Concurrency and PageSize are those of the verification.
	Concurrency, PageSize int
	Log                   *slog.Logger
}

// Entry is the result for one blob whose object was missing.
type Entry struct {
	SHA256  string `json:"sha256"`
	Key     string `json:"key"`
	Outcome string `json:"outcome"`
	// MarkersRemoved is the number of delete markers this run removed (with DryRun: would remove) from the key.
	MarkersRemoved int `json:"markersRemoved"`
	// ItemCount is the number of items that reference the blob.
	ItemCount int64 `json:"itemCount"`
}

// Report is the result of a run.
type Report struct {
	Before time.Time `json:"before"`
	DryRun bool      `json:"dryRun,omitempty"`
	// Checked is the number of blobs whose object was asked for; Missing those without an object when the run started.
	Checked int64 `json:"checked"`
	Missing int64 `json:"missing"`
	// Restored is the number of blobs whose object is current again (with DryRun: would be); MarkersRemoved the number of
	// delete markers removed.
	Restored       int64   `json:"restored"`
	MarkersRemoved int64   `json:"markersRemoved"`
	Entries        []Entry `json:"entries"`
	// Remaining is the number of blobs that are still without an object after the run; SizeMismatch those of another size, which
	// this command does not repair.
	Remaining    int64 `json:"remaining"`
	SizeMismatch int64 `json:"sizeMismatch"`
	// Unchecked is the number of blobs the store did not answer for.
	Unchecked int `json:"unchecked"`
	// ItemsAffected is the number of items that reference a blob that is damaged after the run.
	ItemsAffected int64 `json:"itemsAffected"`
}

// OK reports whether no blob is damaged after the run and the store answered for every blob.
func (r *Report) OK() bool { return r.Remaining == 0 && r.SizeMismatch == 0 && r.Unchecked == 0 }

// Run finds the blobs without an object and removes the delete markers younger than o.Before from their keys. It returns the
// report so far with the error when it ends early; a permission error of the store (AccessDenied on the listing of versions or
// on the removal) ends it at once, because every further call would fail the same way.
func Run(ctx context.Context, o Options) (*Report, error) {
	if o.Store == nil || o.DB == nil {
		return nil, errors.New("restore-versions: a database source and an object store are required")
	}
	if o.Before.IsZero() {
		return nil, errors.New("restore-versions: the time before which the versions are restored is required")
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	// One item per blob keeps the memory of the verification small; the report counts the items instead of listing them.
	vr, err := verify.Run(ctx, verify.Options{
		DB: o.DB, Objects: o.Store, Prefix: o.Prefix, Concurrency: o.Concurrency, PageSize: o.PageSize, MaxItems: 1, Log: o.Log,
	})
	if vr == nil {
		return nil, err
	}
	rep := &Report{
		Before: o.Before, DryRun: o.DryRun, Checked: vr.Checked, Missing: vr.Missing, Entries: []Entry{},
		SizeMismatch: vr.SizeMismatch, Unchecked: len(vr.Unchecked),
	}
	if err != nil {
		return rep, err
	}
	for _, f := range vr.Findings {
		if f.Kind != verify.KindMissing {
			rep.ItemsAffected += f.ItemCount
			continue
		}
		e, err := repair(ctx, o, f)
		if !o.DryRun {
			rep.MarkersRemoved += int64(e.MarkersRemoved)
		}
		if err != nil {
			if e.Outcome != "" {
				rep.Entries = append(rep.Entries, e)
			}
			return rep, fmt.Errorf("blob %s: %w", f.SHA256, err)
		}
		rep.Entries = append(rep.Entries, e)
		if e.Outcome == OutcomeRestored || e.Outcome == OutcomeWouldRestore {
			rep.Restored++
		} else {
			rep.Remaining++
			rep.ItemsAffected += f.ItemCount
		}
	}
	return rep, nil
}

// repair removes the delete markers of one key that are younger than t.
func repair(ctx context.Context, o Options, f verify.Finding) (Entry, error) {
	e := Entry{SHA256: f.SHA256, Key: f.Key, ItemCount: f.ItemCount}
	versions, err := o.Store.ListVersions(ctx, f.Key)
	if err != nil {
		return e, fmt.Errorf("listing the versions of %s: %w", f.Key, err)
	}
	var markers []s3.ObjectVersion
	objects := 0
	for _, v := range versions {
		switch {
		case !v.Marker:
			objects++
		case v.Modified.After(o.Before):
			markers = append(markers, v)
		}
	}
	switch {
	case len(markers) == 0:
		e.Outcome = OutcomeNoDeleteMarker
		return e, nil
	case objects == 0:
		e.Outcome = OutcomeNoVersion
		return e, nil
	case o.DryRun:
		e.Outcome, e.MarkersRemoved = OutcomeWouldRestore, len(markers)
		return e, nil
	}
	for _, m := range markers {
		if err := o.Store.DeleteVersion(ctx, f.Key, m.ID); err != nil {
			return e, fmt.Errorf("removing the delete marker %s of %s: %w", m.ID, f.Key, err)
		}
		e.MarkersRemoved++
		o.Log.Info("delete marker removed", "key", f.Key, "versionId", m.ID, "modified", m.Modified.UTC().Format(time.RFC3339))
	}
	if _, err := o.Store.Head(ctx, f.Key); err != nil {
		if errors.Is(err, blob.ErrNotFound) {
			e.Outcome = OutcomeStillMissing
			return e, nil
		}
		return e, fmt.Errorf("checking %s after the repair: %w", f.Key, err)
	}
	e.Outcome = OutcomeRestored
	return e, nil
}
