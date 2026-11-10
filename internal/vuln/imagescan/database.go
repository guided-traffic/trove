package imagescan

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/vuln/store"
)

// The bounds of the database archive: only its two files are taken, each within a size limit (R-VULN-035).
const (
	maxDBFileBytes  = int64(8) << 30
	maxMetaBytes    = int64(1) << 20
	dbFileName      = "trivy.db"
	dbMetaName      = "metadata.json"
	dbSchemaVersion = 2
)

// DBMetadata is the part of the archive's metadata.json that decides freshness.
type DBMetadata struct {
	Version    int       `json:"Version"`
	UpdatedAt  time.Time `json:"UpdatedAt"`
	NextUpdate time.Time `json:"NextUpdate"`
}

// ReadDBMetadata reads metadata.json from the archive and checks its schema version. The archive is read only as far as that file.
func ReadDBMetadata(archive io.Reader) (DBMetadata, error) {
	var meta DBMetadata
	err := walkArchive(archive, func(name string, r io.Reader) (bool, error) {
		if name != dbMetaName {
			return false, nil
		}
		dec := json.NewDecoder(io.LimitReader(r, maxMetaBytes))
		if err := dec.Decode(&meta); err != nil {
			return false, fmt.Errorf("metadata.json: %w", err)
		}
		return true, nil
	})
	if err != nil {
		return DBMetadata{}, err
	}
	if meta.Version != dbSchemaVersion {
		return DBMetadata{}, fmt.Errorf("the database has schema version %d, this release reads %d", meta.Version, dbSchemaVersion)
	}
	if meta.UpdatedAt.IsZero() {
		return DBMetadata{}, errors.New("the database's metadata.json names no build time")
	}
	return meta, nil
}

// walkArchive reads a gzip tar and calls fn for each regular file at the root that the archive is expected to hold; fn returns true to stop.
// Any other kind of entry is skipped, never written anywhere, and a name with a directory in it is ignored, so that the archive decides
// nothing about a path (GHSA-mcj4-mphf-j9ff was a database archive that could write outside the cache directory).
func walkArchive(archive io.Reader, fn func(name string, r io.Reader) (bool, error)) error {
	zr, err := gzip.NewReader(archive)
	if err != nil {
		return fmt.Errorf("the database archive is not gzip: %w", err)
	}
	defer func() { _ = zr.Close() }()
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("the database archive: %w", err)
		}
		if h.Typeflag != tar.TypeReg || (h.Name != dbFileName && h.Name != dbMetaName) {
			continue
		}
		done, err := fn(h.Name, tr)
		if err != nil || done {
			return err
		}
	}
}

// DBBlobs opens the archive of a database from the object store.
type DBBlobs interface {
	Open(ctx context.Context, sha256 [32]byte, size int64) (io.ReadCloser, error)
}

// DBReader reads which database is in use.
type DBReader interface {
	DB(ctx context.Context) (store.ImageDB, bool, error)
}

// LocalDatabase is the client mode of the [Database] seam (Q-VULN-137): the database that the sync put in the object store is unpacked once
// into a directory of the worker's local cache, by digest, and each detection reads that directory. A replica has no state that matters: an
// empty cache is filled from the object store on the first detection after a start.
type LocalDatabase struct {
	// Dir is the root of the local cache.
	Dir   string
	Blobs DBBlobs
	Rows  DBReader

	mu   sync.Mutex
	uses map[string]int
}

var _ Database = (*LocalDatabase)(nil)

// Acquire implements [Database].
func (d *LocalDatabase) Acquire(ctx context.Context) (DatabaseAccess, error) {
	db, found, err := d.Rows.DB(ctx)
	if err != nil {
		return DatabaseAccess{}, err
	}
	if !found {
		return DatabaseAccess{}, ErrNoDatabase
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.uses == nil {
		d.uses = map[string]int{}
	}
	dir := filepath.Join(d.Dir, hexOf(db.Digest))
	if err := d.ensure(ctx, db, dir); err != nil {
		return DatabaseAccess{}, err
	}
	d.uses[db.Digest]++
	d.dropOldLocked(db.Digest)
	digest := db.Digest
	return DatabaseAccess{
		Args:   []string{"--cache-dir", dir},
		Digest: digest,
		Release: func() {
			d.mu.Lock()
			defer d.mu.Unlock()
			d.uses[digest]--
		},
	}, nil
}

// ensure unpacks the archive into dir unless it is there, through a sibling directory that is renamed into place, so that a directory under
// the final name is complete and a worker lost halfway leaves only a sibling to clean.
func (d *LocalDatabase) ensure(ctx context.Context, db store.ImageDB, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "db", dbMetaName)); err == nil {
		return nil
	}
	if err := os.MkdirAll(d.Dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(d.Dir, "unpack-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if err := os.MkdirAll(filepath.Join(tmp, "db"), 0o700); err != nil {
		return err
	}
	rc, err := d.Blobs.Open(ctx, db.Blob, db.Size)
	if err != nil {
		return fmt.Errorf("the database archive from the object store: %w", err)
	}
	defer func() { _ = rc.Close() }()
	seen := map[string]bool{}
	err = walkArchive(contextReader{ctx, rc}, func(name string, r io.Reader) (bool, error) {
		seen[name] = true
		return false, writeDBFile(filepath.Join(tmp, "db", name), r)
	})
	if err != nil {
		return err
	}
	if !seen[dbFileName] || !seen[dbMetaName] {
		return errors.New("the database archive does not hold trivy.db and metadata.json")
	}
	_ = os.RemoveAll(dir)
	return os.Rename(tmp, dir)
}

func writeDBFile(path string, r io.Reader) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // a path below the local cache directory, named by this package
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	n, err := io.Copy(f, io.LimitReader(r, maxDBFileBytes+1))
	if err != nil {
		return err
	}
	if n > maxDBFileBytes {
		return fmt.Errorf("a file of the database exceeds %d bytes", maxDBFileBytes)
	}
	return nil
}

// dropOldLocked removes the directories of other databases that no detection uses, and what an interrupted unpack left.
func (d *LocalDatabase) dropOldLocked(current string) {
	entries, err := os.ReadDir(d.Dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if name == hexOf(current) || !e.IsDir() {
			continue
		}
		if !strings.HasPrefix(name, "unpack-") && d.uses["sha256:"+name] > 0 {
			continue
		}
		_ = os.RemoveAll(filepath.Join(d.Dir, name))
	}
}
