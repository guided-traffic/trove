// Package format is the on-target format of a backup (R-BAK-001 to R-BAK-003, R-BAK-006): the backup ID, the object
// layout below backup.target.prefix, the target marker, the manifest with its JSON encoding, and the sorted blob list
// with the merge that makes a backup incremental.
//
// Every command of the backup lane reads or writes these shapes, so they are fixed here once and the packages that move
// bytes (internal/backup/target, the create pipeline, the sweep, verify, restore) never spell a key or a field
// themselves. The package imports only the standard library and the zstd codec; it does no I/O of its own beyond the
// readers and writers it is handed.
//
// Layout (R-BAK-002):
//
//	<prefix>/trove-backup-target.json                  target marker
//	<prefix>/pool/sha256/<h0h1>/<h2h3>/<h0…h63>        shared blob pool
//	<prefix>/backups/<backup-id>/db/<table>.copy.zst   one file per exported table
//	<prefix>/backups/<backup-id>/blobs.idx.zst         blob list
//	<prefix>/backups/<backup-id>/manifest.json         written last
//
// The manifest is the commit point: a backup exists if and only if its manifest exists.
package format
