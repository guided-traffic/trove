package restore

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"

	"github.com/klauspost/compress/zstd"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/target"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// fileReader streams one object of the backup through zstd. It counts and hashes the bytes of the object as stored, and its
// Close tells whether they match the manifest's size and SHA-256 (R-BAK-003): a file that was damaged in the target fails
// the load instead of loading wrong rows. progress is called for every read, so a stalled transfer is noticed (R-BAK-013).
type fileReader struct {
	name     string
	raw      io.ReadCloser
	h        hash.Hash
	n        int64
	zr       *zstd.Decoder
	wantSize int64
	wantSum  string
	progress func()
	eof      bool
}

func openFile(ctx context.Context, st target.Store, key, name string, wantSize int64, wantSum string, progress func()) (*fileReader, error) {
	raw, _, err := st.Get(ctx, key)
	if err != nil {
		if errors.Is(err, target.ErrNotFound) {
			return nil, backup.Failf(backup.ExitPrecondition, backup.CodePrecondition, "%s of the backup is missing from the target (%s)", name, key)
		}
		return nil, err
	}
	f := &fileReader{name: name, raw: raw, h: sha256.New(), wantSize: wantSize, wantSum: wantSum, progress: progress}
	zr, err := zstd.NewReader(rawReader{f})
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	f.zr = zr
	return f, nil
}

// rawReader reads the stored bytes and feeds the count and the hash.
type rawReader struct{ f *fileReader }

func (r rawReader) Read(p []byte) (int, error) {
	n, err := r.f.raw.Read(p)
	r.f.h.Write(p[:n])
	r.f.n += int64(n)
	if n > 0 && r.f.progress != nil {
		r.f.progress()
	}
	return n, err
}

// Read returns decompressed bytes.
func (f *fileReader) Read(p []byte) (int, error) {
	n, err := f.zr.Read(p)
	if errors.Is(err, io.EOF) {
		f.eof = true
	}
	return n, err
}

// Close ends the stream. After the decoder reached the end it drains the rest of the object and compares the size and the
// SHA-256 with the manifest.
func (f *fileReader) Close() error {
	var verr error
	if f.eof {
		_, _ = io.Copy(io.Discard, rawReader{f})
		got := hex.EncodeToString(f.h.Sum(nil))
		switch {
		case f.n != f.wantSize:
			verr = backup.Failf(backup.ExitFailed, backup.CodeInternal, "%s: %d bytes in the target, the manifest says %d; run trove backup verify", f.name, f.n, f.wantSize)
		case got != f.wantSum:
			verr = backup.Failf(backup.ExitFailed, backup.CodeInternal, "%s: SHA-256 %s, the manifest says %s; run trove backup verify", f.name, got, f.wantSum)
		}
	}
	f.zr.Close()
	cerr := f.raw.Close()
	if verr != nil {
		return verr
	}
	return cerr
}

// tableSource is the load's view of the backup's table files (postgres.LoadSource).
type tableSource struct {
	st       target.Store
	layout   format.Layout
	id       format.ID
	m        *format.Manifest
	progress func()
	hook     func(string)
}

var _ postgres.LoadSource = (*tableSource)(nil)

func (s *tableSource) Open(ctx context.Context, table string) (io.ReadCloser, error) {
	t, ok := s.m.Table(table)
	if !ok {
		return nil, fmt.Errorf("table %s is not in the manifest", table)
	}
	if s.hook != nil {
		s.hook("load:" + table)
	}
	return openFile(ctx, s.st, s.layout.Dir(s.id)+t.File, t.File, t.CompressedBytes, t.SHA256, s.progress)
}

// copyText helps with the COPY text format of the table files (R-BAK-002): one row per line, tab between columns, \N for
// null, backslash escapes.
func splitRow(line string) []string { return strings.Split(line, "\t") }

// unescape decodes the backslash escapes of COPY text in one column value; null reports \N.
func unescape(v string) (out string, null bool) {
	if v == `\N` {
		return "", true
	}
	if !strings.Contains(v, `\`) {
		return v, false
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c != '\\' || i == len(v)-1 {
			b.WriteByte(c)
			continue
		}
		i++
		switch v[i] {
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte('\v')
		default:
			b.WriteByte(v[i])
		}
	}
	return b.String(), false
}

// readCanaries reads the rows of secret_key_canary from the backup: key ID to sealed canary (R-BAK-020 check 3).
func readCanaries(ctx context.Context, st target.Store, layout format.Layout, id format.ID, m *format.Manifest) (map[string]string, error) {
	t, ok := m.Table("secret_key_canary")
	if !ok {
		return nil, errors.New("the backup has no secret_key_canary file")
	}
	keyIdx, sealIdx := -1, -1
	for i, c := range t.Columns {
		switch c[0] {
		case "key_id":
			keyIdx = i
		case "ciphertext":
			sealIdx = i
		}
	}
	if keyIdx < 0 || sealIdx < 0 {
		return nil, errors.New("secret_key_canary in the backup has no key_id and ciphertext columns")
	}
	f, err := openFile(ctx, st, layout.Dir(id)+t.File, t.File, t.CompressedBytes, t.SHA256, nil)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		cols := splitRow(sc.Text())
		if len(cols) != len(t.Columns) {
			_ = f.Close()
			return nil, fmt.Errorf("secret_key_canary: a row has %d columns, the manifest lists %d", len(cols), len(t.Columns))
		}
		k, _ := unescape(cols[keyIdx])
		v, _ := unescape(cols[sealIdx])
		out[k] = v
	}
	if err := sc.Err(); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return out, nil
}
