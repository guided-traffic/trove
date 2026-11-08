package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// The staging area of the multi-request upload sessions (R-STOR-011): every object of a session lives below
// <prefix>/_uploads/<session-id>/, apart from the blobs below <prefix>/sha256/ (R-STOR-001), so that the collection run and the
// orphan sweep tell a staged byte from a stored one by its key alone.

// uploadsDir is the name of the staging area below the prefix.
const uploadsDir = "_uploads/"

// UploadsPrefix is the key prefix of every staging object of every session.
func UploadsPrefix(prefix string) string {
	if prefix == "" {
		return uploadsDir
	}
	return prefix + "/" + uploadsDir
}

// SessionPrefix is the key prefix of the objects of one session; it ends with a slash, so that it never matches the objects of
// another session.
func SessionPrefix(prefix, id string) string { return UploadsPrefix(prefix) + id + "/" }

// SessionKey is the key of an object of one session: "data" is the multipart upload, "tail-<writer>" a tail.
func SessionKey(prefix, id, name string) string { return SessionPrefix(prefix, id) + name }

// ParseSessionKey is the inverse of [SessionKey]: the session id and the object name of a key below the staging area, false for
// any other key.
func ParseSessionKey(prefix, key string) (id, name string, ok bool) {
	rest, ok := strings.CutPrefix(key, UploadsPrefix(prefix))
	if !ok {
		return "", "", false
	}
	id, name, ok = strings.Cut(rest, "/")
	if !ok || id == "" || name == "" {
		return "", "", false
	}
	return id, name, true
}

// ErrStagingGone is returned by a commit of a [Staged] blob whose staging object is not in the bucket: another request of the
// same session finished first and removed it, or the session was aborted.
var ErrStagingGone = errors.New("the staging object of the upload is gone")

// IsNoSuchUpload reports that the object store does not know the multipart upload: it was completed or aborted already.
func IsNoSuchUpload(err error) bool {
	var se *StoreError
	return errors.As(err, &se) && se.Code == "NoSuchUpload"
}

// MapStoreError turns a failure of the object store into the error a request answers with (R-STOR-036): the 503 of an
// unreachable or refusing store, the caller's own cancellation as it is. It is for the code that calls the [ObjectStore] itself,
// as the store does for the writes it makes.
func MapStoreError(err error) error { return mapStoreErr(err) }

// Staged is a blob whose bytes are in the bucket already, at the staging key, with the digests of the whole content as the session
// computed them while the bytes arrived. It is committed like a spooled body (Commit, CommitAll): the commit protocol's object
// write is a server-side copy of the staging object to the content address, and a blob that is stored already copies nothing
// (R-STOR-011 finalize step 3). No byte passes through the replica. The caller removes the staging object after the commit.
func Staged(d Digests, key string) *Spooled { return &Spooled{Digests: d, staged: key} }

// StagedKey is the key of the staging object of a blob made by [Staged], empty for a spooled body.
func (s *Spooled) StagedKey() string { return s.staged }

// putStaged is the object write of a staged blob.
func (s *Store) putStaged(ctx context.Context, sp *Spooled) error {
	key := s.Key(sp.SHA256)
	if err := s.objects.Copy(ctx, sp.staged, key, sp.Size); err != nil {
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("copy %s: %w", sp.staged, ErrStagingGone)
		}
		return mapStoreErr(err)
	}
	info, err := s.objects.Head(ctx, key)
	if err != nil {
		return mapStoreErr(err)
	}
	if info.Size >= 0 && info.Size != sp.Size { // -1: the store did not say
		return fmt.Errorf("object %s has %d bytes after the copy, expected %d", key, info.Size, sp.Size)
	}
	return nil
}

// errNotReadable is what a reader of a staged blob returns: its bytes are in the bucket, not in the spool.
var errNotReadable = errors.New("a staged blob has no spooled content")

type unreadable struct{}

func (unreadable) Read([]byte) (int, error)          { return 0, errNotReadable }
func (unreadable) Seek(int64, int) (int64, error)    { return 0, errNotReadable }
func (unreadable) ReadAt([]byte, int64) (int, error) { return 0, errNotReadable }

var _ io.ReadSeeker = unreadable{}
