package listing

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/target"
)

// ErrBlobList is a blob list that cannot be trusted: its bytes do not match the manifest's SHA-256, or it holds another
// number of entries than the manifest says. The sweep refuses to build a keep set from such a list (R-BAK-017).
var ErrBlobList = errors.New("blob list does not match its manifest")

// CheckBlobList downloads the blob list of a backup and compares its SHA-256 with the manifest's. A missing object wraps
// target.ErrNotFound; a different content is [ErrBlobList].
func CheckBlobList(ctx context.Context, store target.Store, l format.Layout, m *format.Manifest) error {
	sum, _, err := HashObject(ctx, store, l.BlobList(m.BackupID), nil)
	if err != nil {
		return err
	}
	if sum != m.Blobs.SHA256 {
		return fmt.Errorf("backup %s: %w: sha256 is %s, the manifest says %s", m.BackupID, ErrBlobList, sum, m.Blobs.SHA256)
	}
	return nil
}

// HashObject streams an object and returns its SHA-256 (lowercase hex) and size. pace, when set, is called with the size
// of every chunk before it is hashed, which is how verify throttles its downloads.
func HashObject(ctx context.Context, store target.Store, key string, pace func(context.Context, int) error) (string, int64, error) {
	rc, _, err := store.Get(ctx, key)
	if err != nil {
		return "", 0, err
	}
	defer rc.Close()
	var r io.Reader = rc
	if pace != nil {
		r = &pacedReader{ctx: ctx, r: rc, pace: pace}
	}
	sw := format.NewSumWriter(io.Discard)
	if _, err := io.Copy(sw, r); err != nil {
		return "", 0, err
	}
	return sw.SHA256(), sw.Bytes(), nil
}

type pacedReader struct {
	ctx  context.Context
	r    io.Reader
	pace func(context.Context, int) error
}

func (p *pacedReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		if perr := p.pace(p.ctx, n); perr != nil {
			return n, perr
		}
	}
	return n, err
}

// BlobList is the decoded blob list of a backup. It implements [format.BlobIterator] and reports [ErrBlobList] when the
// list ends with another count than the manifest says.
type BlobList struct {
	rc   io.ReadCloser
	r    *format.BlobListReader
	want int64
	n    int64
	id   format.ID
}

// OpenBlobList opens the blob list of a backup for reading. Call [CheckBlobList] first where the content must be proven.
func OpenBlobList(ctx context.Context, store target.Store, l format.Layout, m *format.Manifest) (*BlobList, error) {
	rc, _, err := store.Get(ctx, l.BlobList(m.BackupID))
	if err != nil {
		return nil, err
	}
	r, err := format.NewBlobListReader(rc)
	if err != nil {
		rc.Close()
		return nil, err
	}
	return &BlobList{rc: rc, r: r, want: m.Blobs.Referenced, id: m.BackupID}, nil
}

// Next implements format.BlobIterator.
func (b *BlobList) Next() (format.BlobEntry, error) {
	e, err := b.r.Next()
	switch {
	case err == nil:
		b.n++
	case errors.Is(err, io.EOF) && b.n != b.want:
		return format.BlobEntry{}, fmt.Errorf("backup %s: %w: %d entries, the manifest says %d", b.id, ErrBlobList, b.n, b.want)
	}
	return e, err
}

// Close releases the list.
func (b *BlobList) Close() {
	b.r.Close()
	b.rc.Close()
}

// Without returns the entries of the list that are not damaged blobs of the manifest: what is known to be in the pool.
func (b *BlobList) Without(m *format.Manifest) format.BlobIterator {
	skip := make(map[string]bool, len(m.Blobs.Damaged))
	for _, d := range m.Blobs.Damaged {
		skip[d.SHA256] = true
	}
	return format.Without(b, skip)
}
