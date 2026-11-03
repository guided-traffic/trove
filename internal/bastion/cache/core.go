package cache

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/metrics"
)

// store is the keyed object storage a [Backend] is built on: the volume (a directory) and the bucket (S3 with
// five operations). Keys are the paths of the layout and are built only from validated parts (layout.go). A
// missing key is [ErrNotFound] everywhere.
type store interface {
	// tmpDir is an existing directory for spooled fills; on the volume it is on the same filesystem as the
	// objects, so that a fill is committed by a rename.
	tmpDir() string
	// putFile stores the spooled file at path under key and removes it from the spool when it can (volume); the
	// caller removes what is left. sum is the raw SHA-256 of the bytes.
	putFile(ctx context.Context, key, path string, size int64, sum []byte) error
	putBytes(ctx context.Context, key string, b []byte) error
	get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error)
	stat(ctx context.Context, key string) (size int64, modified time.Time, err error)
	// list calls fn for every object below prefix; fn may return an error to stop.
	list(ctx context.Context, prefix string, fn func(key string, size int64, modified time.Time) error) error
	remove(ctx context.Context, key string) error
	// touch sets the modification time of key to now, creating an empty object when it does not exist.
	touch(ctx context.Context, key string) error
}

// maxEntryBytes bounds the read of an entry record, maxSpoolBytes the read of a spool file.
const (
	maxEntryBytes = 1 << 20
	maxSpoolBytes = 256 << 20
)

// backend implements [Backend] over a [store]. It counts the errors of the store in
// trove_bastion_cache_errors_total{op} (R-BAS-030); a cancelled context and a missing key are not errors.
type backend struct {
	s         store
	now       func() time.Time
	count     func(op string)
	lastSpool atomic.Int64
}

var _ Backend = (*backend)(nil)

func (b *backend) fail(op string, err error) error {
	if err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalid) || errors.Is(err, context.Canceled) {
		return err
	}
	if b.count != nil {
		b.count(op)
	}
	return err
}

// Check implements [Backend]: it writes, reads and deletes a probe object.
func (b *backend) Check(ctx context.Context) error {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	key := dirProbe + "/" + hex.EncodeToString(id[:])
	payload := []byte("trove-bastion-cache-probe " + hex.EncodeToString(id[:]))
	if err := b.s.putBytes(ctx, key, payload); err != nil {
		return fmt.Errorf("cache check: write: %w", err)
	}
	got, err := readAll(ctx, b.s, key, 1<<10)
	if err != nil {
		_ = b.s.remove(ctx, key)
		return fmt.Errorf("cache check: read: %w", err)
	}
	if !bytes.Equal(got, payload) {
		_ = b.s.remove(ctx, key)
		return errors.New("cache check: the probe read back differs from what was written")
	}
	if err := b.s.remove(ctx, key); err != nil {
		return fmt.Errorf("cache check: delete: %w", err)
	}
	return nil
}

func readAll(ctx context.Context, s store, key string, limit int64) ([]byte, error) {
	rc, err := s.get(ctx, key, 0, -1)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("cache: %s is larger than %d bytes", key, limit)
	}
	return data, nil
}

// spool copies r into a file of the store's spool directory and returns its path and digest. It reads at most
// size+1 bytes, so an oversize source is told from an exact one.
func (b *backend) spool(r io.Reader, size int64) (name string, sum [32]byte, n int64, err error) {
	f, err := os.CreateTemp(b.s.tmpDir(), "fill-*")
	if err != nil {
		return "", sum, 0, err
	}
	h := sha256.New()
	n, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(r, size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", sum, n, err
	}
	h.Sum(sum[:0])
	return f.Name(), sum, n, nil
}

// PutObject implements [Backend]. It spools the bytes, checks them against sha256 and size once more (the
// caller has verified them, a cheap second check keeps a wrong object from ever being committed), then commits.
func (b *backend) PutObject(ctx context.Context, sha string, size int64, r io.Reader) error {
	if _, err := objectKey(areaOf(ctx), sha); err != nil {
		return err
	}
	if size < 0 {
		return fmt.Errorf("%w: size %d", ErrInvalid, size)
	}
	name, sum, n, err := b.spool(r, size)
	if err != nil {
		return b.fail(metrics.OpPut, err)
	}
	defer func() { _ = os.Remove(name) }()
	if n != size || hex.EncodeToString(sum[:]) != sha {
		return fmt.Errorf("%w: read %d bytes with SHA-256 %s, expected %d bytes with %s", ErrMismatch, n, hex.EncodeToString(sum[:]), size, sha)
	}
	return b.commitFile(ctx, sha, size, name, sum[:])
}

// commitFile stores a spooled, verified file under its content address.
func (b *backend) commitFile(ctx context.Context, sha string, size int64, name string, sum []byte) error {
	key, err := objectKey(areaOf(ctx), sha)
	if err != nil {
		return err
	}
	return b.fail(metrics.OpPut, b.s.putFile(ctx, key, name, size, sum))
}

// GetObject implements [Backend].
func (b *backend) GetObject(ctx context.Context, sha string, offset, length int64) (io.ReadCloser, error) {
	key, err := objectKey(areaOf(ctx), sha)
	if err != nil {
		return nil, err
	}
	if offset < 0 || length < -1 {
		return nil, fmt.Errorf("%w: range %d,%d", ErrInvalid, offset, length)
	}
	rc, err := b.s.get(ctx, key, offset, length)
	return rc, b.fail(metrics.OpGet, err)
}

// HeadObject implements [Backend].
func (b *backend) HeadObject(ctx context.Context, sha string) (ObjectInfo, error) {
	key, err := objectKey(areaOf(ctx), sha)
	if err != nil {
		return ObjectInfo{}, err
	}
	size, mod, err := b.s.stat(ctx, key)
	if err != nil {
		return ObjectInfo{}, b.fail(metrics.OpGet, err)
	}
	return ObjectInfo{Size: size, LastAccess: mod}, nil
}

// DeleteObject implements [Backend]: the bytes and their marker.
func (b *backend) DeleteObject(ctx context.Context, sha string) error {
	key, err := objectKey(areaOf(ctx), sha)
	if err != nil {
		return err
	}
	if err := b.s.remove(ctx, key); err != nil {
		return b.fail(metrics.OpDelete, err)
	}
	mk, err := accessKey(sha)
	if err != nil {
		return err
	}
	return b.fail(metrics.OpDelete, b.s.remove(ctx, mk))
}

// PutEntry implements [Backend].
func (b *backend) PutEntry(ctx context.Context, keyHash string, record []byte) error {
	key, err := entryKey(keyHash)
	if err != nil {
		return err
	}
	return b.fail(metrics.OpPut, b.s.putBytes(ctx, key, record))
}

// GetEntry implements [Backend].
func (b *backend) GetEntry(ctx context.Context, keyHash string) ([]byte, error) {
	key, err := entryKey(keyHash)
	if err != nil {
		return nil, err
	}
	data, err := readAll(ctx, b.s, key, maxEntryBytes)
	return data, b.fail(metrics.OpGet, err)
}

// DeleteEntry implements [Backend].
func (b *backend) DeleteEntry(ctx context.Context, keyHash string) error {
	key, err := entryKey(keyHash)
	if err != nil {
		return err
	}
	return b.fail(metrics.OpDelete, b.s.remove(ctx, key))
}

// Touch implements [Backend].
func (b *backend) Touch(ctx context.Context, sha string) error {
	key, err := accessKey(sha)
	if err != nil {
		return err
	}
	return b.fail(metrics.OpPut, b.s.touch(ctx, key))
}

// Usage implements [Backend].
func (b *backend) Usage(ctx context.Context) (Usage, error) {
	var u Usage
	for _, a := range []struct {
		dir string
		sum *int64
	}{{dirArtifacts + "/", &u.Artifacts}, {dirDocuments + "/", &u.Documents}} {
		err := b.s.list(ctx, a.dir, func(key string, size int64, _ time.Time) error {
			if validHash(base(key)) {
				*a.sum += size
			}
			return nil
		})
		if err != nil {
			return Usage{}, b.fail(metrics.OpList, err)
		}
	}
	return u, nil
}

type held struct {
	sha  string
	size int64
	last time.Time
}

// Sweep implements [Backend] (R-BAS-031, Q-BAS-43): two listings, the area's bytes and the access markers; when
// the area holds more than high bytes it deletes by last access, oldest first, until at most target remain. The
// last access of an object is the later of its marker and its own time, so a fill counts as an access.
func (b *backend) Sweep(ctx context.Context, area string, high, target int64) (int, error) {
	kind, dir, err := areaDir(area)
	if err != nil {
		return 0, err
	}
	var items []held
	var total int64
	err = b.s.list(ctx, dir, func(key string, size int64, mod time.Time) error {
		if sha := base(key); validHash(sha) {
			items = append(items, held{sha: sha, size: size, last: mod})
			total += size
		}
		return nil
	})
	if err != nil {
		return 0, b.fail(metrics.OpList, err)
	}
	if total <= high {
		return 0, nil
	}
	marks := map[string]time.Time{}
	err = b.s.list(ctx, dirAccess+"/", func(key string, _ int64, mod time.Time) error {
		if sha := base(key); validHash(sha) {
			marks[sha] = mod
		}
		return nil
	})
	if err != nil {
		return 0, b.fail(metrics.OpList, err)
	}
	for i := range items {
		if m, ok := marks[items[i].sha]; ok && m.After(items[i].last) {
			items[i].last = m
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].last.Equal(items[j].last) {
			return items[i].last.Before(items[j].last)
		}
		return items[i].sha < items[j].sha
	})
	var evicted int
	var errs []error
	dctx := WithArea(ctx, kind)
	for _, it := range items {
		if total <= target {
			break
		}
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if err := b.DeleteObject(dctx, it.sha); err != nil {
			errs = append(errs, err)
			continue
		}
		total -= it.size
		evicted++
	}
	return evicted, errors.Join(errs...)
}

// SpoolWrite implements [Backend]: audit-spool/<replica>/<unix ms>.jsonl. The name is the time of the write,
// raised by one millisecond where two writes of this process fall in the same one.
func (b *backend) SpoolWrite(ctx context.Context, replicaName string, jsonl []byte) error {
	for {
		last := b.lastSpool.Load()
		ms := max(b.now().UnixMilli(), last+1)
		if !b.lastSpool.CompareAndSwap(last, ms) {
			continue
		}
		key, err := spoolKey(replicaName, strconv.FormatInt(ms, 10)+".jsonl")
		if err != nil {
			return err
		}
		return b.fail(metrics.OpSpool, b.s.putBytes(ctx, key, jsonl))
	}
}

// SpoolList implements [Backend], oldest first.
func (b *backend) SpoolList(ctx context.Context) ([]SpoolFile, error) {
	var out []SpoolFile
	err := b.s.list(ctx, dirSpool+"/", func(key string, size int64, _ time.Time) error {
		parts := strings.Split(strings.TrimPrefix(key, dirSpool+"/"), "/")
		if len(parts) != 2 {
			return nil
		}
		if _, err := spoolKey(parts[0], parts[1]); err == nil {
			out = append(out, SpoolFile{Replica: parts[0], Name: parts[1], Size: size})
		}
		return nil
	})
	if err != nil {
		return nil, b.fail(metrics.OpList, err)
	}
	sort.Slice(out, func(i, j int) bool {
		a, c := spoolMs(out[i].Name), spoolMs(out[j].Name)
		if a != c {
			return a < c
		}
		return out[i].Replica < out[j].Replica
	})
	return out, nil
}

func spoolMs(name string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSuffix(path.Base(name), ".jsonl"), 10, 64)
	return n
}

// SpoolRead implements [Backend].
func (b *backend) SpoolRead(ctx context.Context, f SpoolFile) ([]byte, error) {
	key, err := spoolKey(f.Replica, f.Name)
	if err != nil {
		return nil, err
	}
	data, err := readAll(ctx, b.s, key, maxSpoolBytes)
	return data, b.fail(metrics.OpSpool, err)
}

// SpoolDelete implements [Backend].
func (b *backend) SpoolDelete(ctx context.Context, f SpoolFile) error {
	key, err := spoolKey(f.Replica, f.Name)
	if err != nil {
		return err
	}
	return b.fail(metrics.OpSpool, b.s.remove(ctx, key))
}
