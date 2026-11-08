package deb

// Store is the data port of the Debian (APT) format (Q-ARCH-193, D-1): everything the format needs of its own tables
// (internal/meta/postgres/migrations/00060_deb.sql, 00061_deb_signing_keys.sql, 00062_deb_remote.sql), behind an interface the format
// declares and internal/formatdata/deb implements. The format runs no SQL (R-ARCH-008); the composition root binds the
// implementation per format ID and refuses to start without it ([Format.Descriptor] names the type).
//
// The port is one interface that embeds a group interface per work package, each in a file of its own, so that the packages that
// build in parallel never edit the same file (apt-work-packages.md, 2.2):
//
//	LocalStore    store_local.go    uploads, coordinates, removal, the stored file          internal/formatdata/deb/local_store.go
//	KeyStore      store_keys.go     signing keys, rotation, the key journal                 internal/formatdata/deb/keys_store.go
//	BuildStore    store_build.go    suites, the index state, a build's inputs and record    internal/formatdata/deb/build_store.go
//	RemoteStore   store_remote.go   releases, parsed index rows, the name check             internal/formatdata/deb/remote_store.go
//	MirrorStore   store_mirror.go   the names and entries a mirror plans from               internal/formatdata/deb/mirror_store.go
//	VirtualStore  store_virtual.go  a virtual build's inputs and its check                  internal/formatdata/deb/virtual_store.go
//	ViewStore     store_view.go     the package screens                                     internal/formatdata/deb/view_store.go
//	JournalStore  store_journal.go  deletion events and the journal replay of files         internal/formatdata/deb/journal_store.go
//
// The values that cross the port are those of internal/formats/deb/rows. Keys cross it as armored bytes, fingerprints and times;
// the OpenPGP library is a leaf of its own that only the implementation of the key group imports.
//
// The group interfaces are frozen with this file: a change after WP0 goes through the architecture reviewer, because several
// packages build against them at once.
type Store interface {
	LocalStore
	KeyStore
	BuildStore
	RemoteStore
	MirrorStore
	VirtualStore
	ViewStore
	JournalStore
}
