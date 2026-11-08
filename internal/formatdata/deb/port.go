// Package deb is the data port of the Debian (APT) format (Q-ARCH-193, D-1): the statements on the Debian tables
// (internal/meta/postgres/queries/deb_*.sql), behind the Store interface that internal/formats/deb declares. The format runs no SQL;
// this package holds it, and the composition root binds one value per replica.
//
// It is a domain package: it imports neither the format nor the edge, so the values that cross the port are those of
// internal/formats/deb/rows, a leaf both import. It owns the tables of the migrations 00060_deb, 00061_deb_signing_keys and
// 00062_deb_remote and registers them with the backup (tables_deb.go), so that the package must be linked wherever the migrated
// schema is checked against the registry (cmd/trove/registry.go).
//
// The port is composed of one group per work package (apt-work-packages.md, 2.2), each in a file of its own: local_store.go,
// keys_store.go, build_store.go, remote_store.go, mirror_store.go, virtual_store.go, view_store.go and journal_store.go. A group that
// is not built yet answers not_implemented, so nothing is silently empty; the package that builds a group replaces its file and no
// other. The job handlers are build.go (deb-index), remote_parse.go (deb-remote-parse) and refresh.go (deb-index-refresh).
package deb

import (
	"context"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/secrets"
)

// Options configure [New].
type Options struct {
	// DB reads outside a transaction.
	DB meta.Runner
	// Recorder records the events of the writes in their transactions (deb.package.deleted, deb.signing_key.promoted, ...).
	Recorder audit.Recorder
	// Source gives the source of an audit event for the request in ctx; nil gives none.
	Source func(ctx context.Context) audit.Source
	// Actor gives the actor of the request in ctx. A hook that learns of a removal from the item store knows the principal's id and
	// nothing else; the actor of the request completes the event. nil keeps what the hook passed.
	Actor func(ctx context.Context) audit.Actor
	// Ring seals and opens the private signing keys (R-ARCH-021). A process that never signs or creates a key (the restore's
	// replay) may leave it nil; the key operations then answer an internal error.
	Ring *secrets.KeyRing
	// KeysChanged is called in the transaction of every change of a repository's keys, after change_seq of its suites was raised: the
	// index build's job enqueues a build without delay there (R-DEB-017). nil does nothing.
	KeysChanged func(ctx context.Context, tx meta.Tx, repoID int64) error
}

// Store implements the data port of the format: the groups, each embedded from the file of the package that builds it.
type Store struct {
	localStore
	keysStore
	buildStore
	remoteStore
	mirrorStore
	virtualStore
	viewStore
	journalStore
}

// New creates the store.
func New(o Options) (*Store, error) {
	if o.DB == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "deb: the data port needs a database")
	}
	if o.Recorder == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "deb: the data port needs an audit recorder")
	}
	g := &group{o: o}
	return &Store{
		localStore: localStore{g}, keysStore: keysStore{g}, buildStore: buildStore{g}, remoteStore: remoteStore{g},
		mirrorStore: mirrorStore{g}, virtualStore: virtualStore{g}, viewStore: viewStore{g}, journalStore: journalStore{g},
	}, nil
}

// group is what every group of the port shares: the options. A group embeds a pointer to it, so the composition is one value.
type group struct{ o Options }

// errNotBuilt is the answer of a method whose work package has not landed.
func errNotBuilt(what string) error {
	return errs.E(errs.NotImplemented, errs.CodeNotImplemented, "deb: "+what+" is not built yet.")
}
