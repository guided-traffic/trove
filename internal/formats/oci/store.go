package oci

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/oci/rows"
)

// Store is the data port of the OCI format (Q-ARCH-193, D-1): everything the format needs of its own tables
// (internal/meta/postgres/migrations/00055_oci.sql), behind an interface the format declares and internal/formatdata/oci
// implements. The format runs no SQL (R-ARCH-008); the composition root binds the implementation per format ID and refuses to
// start without it ([Format.Descriptor] names the type).
//
// The methods that take a transaction use it for every statement: the rows are written in the transaction that writes the
// items, under the lock of the image name's package row, and every change raises the package's rev_seq through the
// PackageWriter of the hook (R-OCI-007, R-STOR-003).
type Store interface {
	// PutManifest records a manifest row and the references it names. A manifest that is recorded already changes nothing. A
	// reference list that names one target twice (the empty descriptor as the config and as a layer, a layer used by two entries)
	// yields one row: the store keeps the first occurrence, the caller need not deduplicate.
	PutManifest(ctx context.Context, tx formats.Tx, in rows.ManifestInput) error
	// RemoveManifest removes the row of a manifest and, with it, the references it names (R-OCI-018). The rows of a remote
	// repository are tied to the alias item `<name>/_manifests/<digest>` its fill entered (R-OCI-022, Q-OCI-25): the eviction of
	// a metadata entry reaches no format hook, the removal of that item does, and the hook removes the rows; the rows of a local
	// repository stay while the item is in the trash, as NuGet's do, and go when the item is removed for good.
	RemoveManifest(ctx context.Context, tx formats.Tx, repoID int64, name string, digest [32]byte) error

	// Manifest reads the row of a manifest; found is false for one that is not recorded.
	Manifest(ctx context.Context, repoID int64, name string, digest [32]byte) (m rows.Manifest, found bool, err error)
	// References lists what a manifest names.
	References(ctx context.Context, repoID int64, name string, manifest [32]byte) ([]rows.Reference, error)
	// ReferencedBy lists the manifests of the name that name target: the check of a deletion that must not leave a dangling
	// reference (R-OCI-018). With a transaction it reads in it.
	ReferencedBy(ctx context.Context, tx formats.Tx, repoID int64, name string, target [32]byte) ([][32]byte, error)
	// Referrers lists the manifests whose subject is the digest, in digest order after the cursor, at most limit; a non-empty
	// artifactType keeps only the manifests of that artifact type (R-OCI-016).
	Referrers(ctx context.Context, repoID int64, name string, subject [32]byte, artifactType string, after *[32]byte, limit int) ([]rows.Manifest, error)
	// Tags lists the tags of an image name after the cursor, at most limit (R-OCI-015).
	Tags(ctx context.Context, repoID int64, name, after string, limit int) (rows.TagPage, error)
	// PutTombstone withholds a manifest: the journal replay of oci.manifest.deleted writes the mark in its transaction and
	// deletes nothing (R-OCI-033); a mark that is there stays. A live deletion writes none.
	PutTombstone(ctx context.Context, tx formats.Tx, in rows.Tombstone) error
	// RemoveTombstone reinstates a withheld manifest: the administrator's command removes the mark (R-OCI-033); it reports
	// whether a mark was there.
	RemoveTombstone(ctx context.Context, tx formats.Tx, repoID int64, name string, digest [32]byte) (bool, error)
	// Tombstone reads the withheld mark of a digest under an image name; found is false when there is none. The serve check
	// reads the same fact with the item (servefacts.OCILocal) and asks the port only when the lookup did not.
	Tombstone(ctx context.Context, repoID int64, name string, digest [32]byte) (t rows.Tombstone, found bool, err error)
	// UnreferencedBlobs lists up to limit blob items of the repository that no manifest references and that are older than the
	// grace period, for the sweep (R-OCI-019).
	UnreferencedBlobs(ctx context.Context, repoID int64, before time.Time, limit int) ([]rows.Blob, error)

	// ExistingItems returns those of the canonical item paths that are live files of the repository: the check of a push that
	// everything a manifest names is stored under the name (R-OCI-013 step 4). With a transaction it reads in it and holds the
	// rows against a concurrent removal until the transaction ends, so a blob cannot go between the check and the manifest's rows.
	ExistingItems(ctx context.Context, tx formats.Tx, repoID int64, paths []string) ([]string, error)
	// WithheldAmong returns those of the digests that carry a withheld mark under the image name (R-OCI-033). With a transaction it
	// reads in it: the commit transaction takes no second connection of the pool.
	WithheldAmong(ctx context.Context, tx formats.Tx, repoID int64, name string, digests [][32]byte) ([][32]byte, error)
	// TagViews reads, for the tags with the given version ids (of any number of image names), the facts of the manifests they point at
	// and, for an index, its children (R-OCI-031); a version that is not a stored tag of a recorded manifest is absent.
	TagViews(ctx context.Context, versionIDs []int64) ([]rows.TagView, error)
	// StoredTags lists the tags a local repository serves of an image name, newest push first, at most limit: the live versions
	// whose manifest the journal replay did not withhold.
	StoredTags(ctx context.Context, repoID int64, name string, limit int) ([]string, error)
	// WithheldVersions returns the ids of the live tags of the repositories (by "<project>/<repository>") that point at a
	// withheld manifest.
	WithheldVersions(ctx context.Context, repos []string) ([]int64, error)
	// Holds reports whether the repository holds any live item below the image name: a tag, a manifest or a blob. A tags list of
	// a name it holds nothing of answers 404 NAME_UNKNOWN (R-OCI-015).
	Holds(ctx context.Context, repoID int64, name string) (bool, error)
	// CatalogNames lists the image names of the repository that hold a manifest, in byte order after the cursor name, at most
	// limit: the page of one repository that the catalog of the registry root merges (R-OCI-017).
	CatalogNames(ctx context.Context, repoID int64, after string, limit int) ([]string, error)

	// The reads of the removal hook run in the transaction that removed the items and see its effects (R-OCI-018, Q-OCI-24).

	// TagsPointingAt lists the tags of an image name whose item holds the manifest with the digest.
	TagsPointingAt(ctx context.Context, tx formats.Tx, repoID int64, name string, digest [32]byte) ([]string, error)
	// SubjectManifests lists the manifests of the name whose subject is the digest.
	SubjectManifests(ctx context.Context, tx formats.Tx, repoID int64, name string, subject [32]byte) ([][32]byte, error)
	// Children lists the manifests that a manifest names (the entries of an index).
	Children(ctx context.Context, tx formats.Tx, repoID int64, name string, manifest [32]byte) ([][32]byte, error)
	// RecordManifestDeleted writes the audit event oci.manifest.deleted with the journal entry a restore's replay withholds the
	// manifest by (R-OCI-033).
	RecordManifestDeleted(ctx context.Context, tx formats.Tx, in rows.ManifestDeleted) error
}
