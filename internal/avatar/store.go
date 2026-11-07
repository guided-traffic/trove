package avatar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// ErrNotFound is a digest that is no user's avatar, or one that is not a digest at all.
var ErrNotFound = errors.New("avatar: no avatar has this digest")

// DB is the database the store reads and writes: the request pool with the retry of internal/meta/postgres.
type DB interface {
	Read(ctx context.Context, fn func(q.DBTX) error) error
	InTx(ctx context.Context, fn func(meta.Tx) error) error
}

// Blobs is the blob store the images live in.
type Blobs interface {
	Open(ctx context.Context, d blob.Digest, size int64, o blob.OpenOptions) (io.ReadCloser, error)
	Ingest(ctx context.Context, body io.Reader, o blob.IngestOptions) (*blob.Spooled, error)
	Commit(ctx context.Context, sp *blob.Spooled, link blob.LinkFunc) (blob.CommitResult, error)
}

// Store keeps the avatars of the users: the PNG in the blob store (INV-4), the name of it in user_avatars (R-API-055).
type Store struct {
	db    DB
	blobs Blobs
	rec   audit.Recorder
	proc  *Processor
}

// NewStore returns the store. proc bounds the decoding of this replica; a nil proc is a Processor with the defaults.
func NewStore(db DB, blobs Blobs, rec audit.Recorder, proc *Processor) *Store {
	if proc == nil {
		proc = NewProcessor()
	}
	return &Store{db: db, blobs: blobs, rec: rec, proc: proc}
}

// User is the user an avatar belongs to.
type User struct{ ID, Name string }

// Who is the caller of a change: the actor and source of the audit event, and whether the user changed it themselves or an
// administrator did.
type Who struct {
	Actor  audit.Actor
	Source audit.Source
	// Self is true when the user changes their own avatar.
	Self bool
}

func (w Who) via() string {
	if w.Self {
		return "self"
	}
	return "admin"
}

// Put makes the avatar of u out of an upload and returns the hex SHA-256 of the stored image. Process refuses what is not an
// accepted image before anything is written. An upload that yields the image the user already has changes nothing and
// records nothing.
func (s *Store) Put(ctx context.Context, u User, who Who, declared string, body []byte) (string, error) {
	img, err := s.proc.Process(ctx, declared, body)
	if err != nil {
		return "", err
	}
	sp, err := s.blobs.Ingest(ctx, bytes.NewReader(img), blob.IngestOptions{ContentLength: int64(len(img)), MaxSize: MaxUpload})
	if err != nil {
		return "", fmt.Errorf("spool the avatar: %w", err)
	}
	defer func() { _ = sp.Close() }()
	hexSum := sp.SHA256.Hex()
	_, err = s.blobs.Commit(ctx, sp, func(ctx context.Context, tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		n, err := qs.AvatarUpsert(ctx, q.AvatarUpsertParams{UserID: u.ID, BlobSha256: sp.SHA256.Bytes(), Size: sp.Size})
		if err != nil || n == 0 {
			return err
		}
		return s.record(ctx, tx, audit.EventUserAvatarSet, u, who, map[string]any{"source": who.via(), "sha256": hexSum, "size": sp.Size})
	})
	if err != nil {
		return "", fmt.Errorf("commit the avatar of %s: %w", u.Name, err)
	}
	return hexSum, nil
}

// Remove deletes the avatar of u and reports whether there was one. The blob is queued for collection by the trigger of the
// table, and collected once no row references it (R-STOR-024).
func (s *Store) Remove(ctx context.Context, u User, who Who) (bool, error) {
	var removed bool
	err := s.db.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rows, err := qs.AvatarDelete(ctx, u.ID)
		if err != nil || len(rows) == 0 {
			removed = false
			return err
		}
		removed = true
		return s.record(ctx, tx, audit.EventUserAvatarRemoved, u, who, map[string]any{"source": who.via(), "sha256": fmt.Sprintf("%x", rows[0])})
	})
	if err != nil {
		return false, fmt.Errorf("remove the avatar of %s: %w", u.Name, err)
	}
	return removed, nil
}

func (s *Store) record(ctx context.Context, tx meta.Tx, event string, u User, who Who, details map[string]any) error {
	b, err := json.Marshal(details)
	if err != nil {
		return err
	}
	return s.rec.RecordTx(ctx, tx, audit.Event{
		Type: event, Outcome: audit.Success, Actor: who.Actor, Source: who.Source,
		Subject: audit.Subject{Kind: "user", ID: u.ID, Name: u.Name}, Details: b,
	})
}

// Open returns the stored image whose SHA-256 is hexDigest. Only a digest that some user's avatar names is served: the route
// is not a way to read any other blob. The bytes are read through the verifying path of the blob store and compared with the
// digest once more, so an object changed in the bucket is never served.
func (s *Store) Open(ctx context.Context, hexDigest string) ([]byte, error) {
	d, ok := blob.ParseDigest(hexDigest)
	if !ok {
		return nil, ErrNotFound
	}
	var rows []int64
	err := s.db.Read(ctx, func(tx q.DBTX) (err error) {
		rows, err = q.New(tx).AvatarByDigest(ctx, d.Bytes())
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("look up the avatar: %w", err)
	}
	if len(rows) == 0 {
		return nil, ErrNotFound
	}
	size := rows[0]
	if size <= 0 || size > MaxUpload {
		return nil, fmt.Errorf("avatar %s: the row names %d bytes", hexDigest, size)
	}
	body, err := s.blobs.Open(ctx, d, size, blob.OpenOptions{Verify: true})
	if err != nil {
		return nil, fmt.Errorf("open the avatar: %w", err)
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(io.LimitReader(body, size+1))
	if err != nil {
		return nil, fmt.Errorf("read the avatar: %w", err)
	}
	if int64(len(data)) != size || sha256.Sum256(data) != d {
		return nil, fmt.Errorf("read the avatar: %w: the bytes are not the row's", blob.ErrCorrupt)
	}
	return data, nil
}

// queries returns the sqlc queries on the transaction behind tx.
func queries(tx meta.Tx) (*q.Queries, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errors.New("avatar: the transaction is not one of internal/meta/postgres")
	}
	return q.New(ptx), nil
}
