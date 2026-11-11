// Package ansible is the data port of the Ansible Galaxy format (Q-ARCH-193, D-1): the statements on the Galaxy tables
// (internal/meta/postgres/queries/ansible_*.sql), behind the Store interface that internal/formats/ansible declares. The format runs
// no SQL; this package holds it, and the composition root binds one value per replica.
//
// It is a domain package: it imports neither the format nor the edge, so the values that cross the port are those of
// internal/formats/ansible/rows, a leaf both import. It owns the tables of the migrations 00068_ansible_collections and
// 00069_ansible_import_tasks and registers them with the backup (tables.go), so that the package must be linked wherever the migrated
// schema is checked against the registry (cmd/trove/registry.go).
//
// The port is three groups, each in files of its own: local_store.go (the local class: versions, tombstones, tasks, the reindex step),
// remote_store.go (the remote class: the digests a remote's version documents announce) and docs_store.go (the documentation of
// collections, R-ANS-033). A group that is not built yet answers
// not_implemented, so nothing is silently empty; the package that builds a group replaces its files and no other. The job handlers are
// in jobs.go.
package ansible

import (
	"context"
	"io"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
)

// Options configure [New].
type Options struct {
	// DB reads outside a transaction.
	DB meta.Runner
	// Recorder records the events of the writes in their transactions (ansible.version.deleted, ansible.tombstone.cleared).
	Recorder audit.Recorder
	// Source gives the source of an audit event for the request in ctx; nil gives none.
	Source func(ctx context.Context) audit.Source
	// Actor gives the actor of the request in ctx. A hook that learns of a removal from the item store knows the principal's id and
	// nothing else; the actor of the request completes the event. nil keeps what the hook passed.
	Actor func(ctx context.Context) audit.Actor
	// Jobs enqueues the reindex (reindex.go); nil enqueues nothing and the reindex answers an internal error (a process that serves no
	// request, the restore's, has none).
	Jobs Jobs
	// Blobs reads the stored archives for the README of a version (view_store.go); nil gives the screens no README text.
	Blobs BlobReader
}

// BlobReader opens a stored blob whole; the composition root implements it over the blob store. The read is verified against the
// digest as every read of a blob is.
type BlobReader interface {
	Open(ctx context.Context, sha256 [32]byte, size int64) (io.ReadCloser, error)
}

// Store implements the data port of the format: the groups, each embedded from the files of the package that builds it.
type Store struct {
	o Options
	localStore
	remoteStore
	viewStore
	docsStore
}

// New creates the store.
func New(o Options) (*Store, error) {
	if o.DB == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "ansible: the data port needs a database")
	}
	if o.Recorder == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "ansible: the data port needs an audit recorder")
	}
	g := &group{o: o}
	return &Store{o: o, localStore: localStore{g}, remoteStore: remoteStore{g}, viewStore: viewStore{g}, docsStore: docsStore{g}}, nil
}

// group is what every group of the port shares: the options. A group embeds a pointer to it, so the composition is one value.
type group struct{ o Options }

// errNotBuilt is the answer of a method whose work package has not landed.
func errNotBuilt(what string) error {
	return errs.E(errs.NotImplemented, errs.CodeNotImplemented, "ansible: "+what+" is not built yet.")
}
