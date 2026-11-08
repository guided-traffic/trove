package uploads

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
)

// maxBinding is the longest binding the table takes (the CHECK of migration 00054).
const maxBinding = 512

// maxParts is the most parts an S3 multipart upload has.
const maxParts = 10_000

// Scope is what a request must match for a session to be its session: the repository the client addressed, the binding the format
// started the session with (the OCI image name) and the principal. A session that differs in any of them does not exist for the
// request (R-STOR-011 step 1, Q-OCI-12).
type Scope struct {
	// Addressed is the id of the repository the client addressed.
	Addressed int64
	// Binding is the format's string, repeated by every request.
	Binding string
	// Principal is the id of the principal of the request, empty for an anonymous one.
	Principal string
}

// Session is a session as the host reads it.
type Session struct {
	ID uuid.UUID
	// Target is the id of the repository the session writes to.
	Target int64
	// Addressed is the id of the repository the client addressed.
	Addressed int64
	Binding   string
	Received  int64
	PartSize  int64
	State     State
}

// ObjectStore is the part of the object-store client the service uses.
type ObjectStore = blob.ObjectStore

// Options configure [New].
type Options struct {
	// DB runs the service's transactions on the main pool.
	DB blob.DB
	// Objects is the object-store client of the replica.
	Objects ObjectStore
	// Spool holds the segment of a part while it is sent.
	Spool *blob.Spool
	// Storage is the storage section: the prefix, the payload checksum, the maximum upload size and storage.uploads.
	Storage config.Storage
	// Metrics counts the sessions by how they ended (trove_storage_upload_sessions_total); nil counts nothing.
	Metrics *blob.Metrics
	Log     *slog.Logger
}

// Service is the session service of one replica. It holds no state of a session: every call reads the row.
type Service struct {
	rows      rows
	objects   ObjectStore
	spool     *blob.Spool
	cfg       config.StorageUploads
	prefix    string
	algorithm string
	maxSize   int64
	log       *slog.Logger
	metrics   *blob.Metrics

	// Fault is a test hook called at named points of a request. A non-nil error ends the request at once as if the process had been
	// killed there: no release of the claim, no clean-up. The error is returned wrapped in [blob.ErrFault]. A hook may also block,
	// to hold a request at a point while others run.
	Fault func(point string) error
}

// New builds the service over PostgreSQL.
func New(o Options) (*Service, error) {
	if o.DB == nil || o.Objects == nil || o.Spool == nil {
		return nil, errors.New("uploads: the service needs a database, an object store and a spool")
	}
	return newService(pgRows{db: o.DB}, o), nil
}

func newService(r rows, o Options) *Service {
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	algorithm := o.Storage.S3.PayloadChecksum
	if algorithm == "none" {
		algorithm = ""
	}
	return &Service{
		rows: r, objects: o.Objects, spool: o.Spool, cfg: o.Storage.Uploads, prefix: o.Storage.S3.Prefix, algorithm: algorithm,
		maxSize: int64(o.Storage.Upload.MaxSize), log: log, metrics: o.Metrics,
	}
}

// PartSize is the size of the parts a session started now cuts the blob into (storage.uploads.part_size).
func (s *Service) PartSize() int64 { return int64(s.cfg.PartSize) }

// unknown is the answer for a session that does not exist for the request: absent, over, of another principal, repository or
// binding (OCI BLOB_UPLOAD_UNKNOWN). The cases are not told apart, so a session id tells a caller nothing.
func unknown() error {
	return errs.E(errs.NotFound, errs.CodeOCIBlobUploadUnknown, "The upload does not exist.")
}

// outOfOrder is the 416 of a chunk that is not at the received count or of a session another request holds.
func outOfOrder(detail string) error {
	return errs.E(errs.RangeNotSatisfiable, errs.CodeOCIBlobUploadInvalid, detail)
}

func digestInvalid() error {
	return errs.E(errs.Invalid, errs.CodeOCIDigestInvalid, "The digest of the content differs from the digest the request names.")
}

func (s *Service) fault(point string) error {
	if s.Fault == nil {
		return nil
	}
	if err := s.Fault(point); err != nil {
		return fmt.Errorf("%w at %s: %w", blob.ErrFault, point, err)
	}
	return nil
}

func (s *Service) dataKey(id uuid.UUID) string { return blob.SessionKey(s.prefix, id.String(), "data") }

func (s *Service) tailKey(id uuid.UUID, writer string) string {
	return blob.SessionKey(s.prefix, id.String(), "tail-"+writer)
}

// canonicalPrincipal is the stored form of a principal id: the lower-case UUID text, empty for none.
func canonicalPrincipal(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	u, err := uuid.Parse(p)
	if err != nil {
		return "", errs.E(errs.Internal, errs.CodeInternal, "The principal of the request has no valid id.", errs.WithCause(err))
	}
	return u.String(), nil
}

// matches reports that the row is a session of the scope.
func (r row) matches(sc Scope, principal string) bool {
	return r.AddressedID == sc.Addressed && r.Binding == sc.Binding && r.Principal == principal
}

// Start opens a session that writes to the repository target for the scope: it records the session (R-STOR-011 State) and nothing
// else; the multipart upload of the staging object is created with the first part. The decisions of who may start it are the host's.
func (s *Service) Start(ctx context.Context, target int64, sc Scope) (Session, error) {
	principal, err := canonicalPrincipal(sc.Principal)
	if err != nil {
		return Session{}, err
	}
	if len(sc.Binding) > maxBinding {
		return Session{}, errs.E(errs.Invalid, errs.CodeInvalid, "The binding of an upload is at most "+strconv.Itoa(maxBinding)+" bytes.")
	}
	hs, err := newHashes().marshal()
	if err != nil {
		return Session{}, errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(err))
	}
	// A session costs a row and nothing in the bucket: its multipart upload is created with the first full part, so a session that
	// is started and never continued leaves nothing for the store to keep (Q-STOR-174).
	r := row{
		ID: uuid.New(), RepoID: target, AddressedID: sc.Addressed, Binding: sc.Binding, Principal: principal, HashState: hs,
		PartSize: s.PartSize(), State: StateOpen,
	}
	started, err := s.rows.insert(ctx, r, s.cfg.MaxOpenSessions)
	if err != nil {
		return Session{}, err
	}
	if !started {
		return Session{}, errs.E(errs.RateLimited, errs.CodeRateLimited, "Too many uploads are open for this caller on this repository; finish or cancel one, or wait for them to expire.",
			errs.WithRetryAfter(30*time.Second))
	}
	return sessionOf(r), nil
}

func sessionOf(r row) Session {
	return Session{
		ID: r.ID, Target: r.RepoID, Addressed: r.AddressedID, Binding: r.Binding, Received: r.Received, PartSize: r.PartSize, State: r.State,
	}
}

// lookup reads the session of the scope. A session that is absent, of another scope, over, or past its maximum age is unknown;
// the last is also set to aborted, so that the collection run removes what it staged (R-STOR-011 step 2).
func (s *Service) lookup(ctx context.Context, id uuid.UUID, sc Scope) (row, error) {
	principal, err := canonicalPrincipal(sc.Principal)
	if err != nil {
		return row{}, err
	}
	r, found, err := s.rows.get(ctx, id, s.cfg.SessionMaxAge)
	if err != nil {
		return row{}, err
	}
	if !found || !r.matches(sc, principal) {
		return row{}, unknown()
	}
	switch {
	case r.State == StateAborted:
		return row{}, unknown()
	case r.State == StateOpen && r.TooOld:
		live, _, err := s.rows.abort(ctx, id)
		if err != nil {
			return row{}, err
		}
		if live {
			s.metrics.UploadSession(blob.SessionExpired, 1)
		}
		return row{}, unknown()
	}
	return r, nil
}

// Lookup reads the session of the scope in any state but aborted, a finished one included: the host resolves the repository a
// session writes to from it before it decides what the caller may do, and a repeated closing request must find a session that is
// done. A session that is absent, of another scope, aborted or past its maximum age is unknown.
func (s *Service) Lookup(ctx context.Context, id uuid.UUID, sc Scope) (Session, error) {
	r, err := s.lookup(ctx, id, sc)
	if err != nil {
		return Session{}, err
	}
	return sessionOf(r), nil
}

// Get reads the session of the scope: the received count a client continues from (OCI GET of the upload location). A finished
// session does not exist any more for it.
func (s *Service) Get(ctx context.Context, id uuid.UUID, sc Scope) (Session, error) {
	r, err := s.lookup(ctx, id, sc)
	if err != nil {
		return Session{}, err
	}
	if r.State == StateDone {
		return Session{}, unknown()
	}
	return sessionOf(r), nil
}

// Abort ends the session of the scope and removes what it staged. A session that is done or over does not exist for the caller.
func (s *Service) Abort(ctx context.Context, id uuid.UUID, sc Scope) error {
	r, err := s.lookup(ctx, id, sc)
	if err != nil {
		return err
	}
	if r.State == StateDone {
		return unknown()
	}
	if err := s.fault("abort.read"); err != nil {
		return err
	}
	live, uploadID, err := s.rows.abort(ctx, id)
	if err != nil {
		return err
	}
	if !live {
		return unknown()
	}
	r.UploadID = uploadID // the upload as it was at the abort, not as it was when the row was read
	s.metrics.UploadSession(blob.SessionAborted, 1)
	s.endAndRemove(ctx, r)
	return nil
}

// cleanup removes the staging objects of a session that is over, with a context of its own because the request's may be what
// ended. A failure is logged: the collection run removes what is left (R-STOR-011 Expiry).
func (s *Service) cleanup(ctx context.Context, r row) bool {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := blob.RemoveSession(cctx, s.objects, s.prefix, r.ID.String(), r.UploadID); err != nil {
		s.log.Warn("could not remove the staging objects of an upload session; the collection run will", "session", r.ID, "error", err)
		return false
	}
	return true
}

// endAndRemove removes what an ended session staged and, when that succeeded, its row: the request that ended it has nothing more to wait
// for, and a row left behind is work for the collection run and a place in the table (Q-STOR-176). A failed removal leaves the row for the run.
func (s *Service) endAndRemove(ctx context.Context, r row) {
	if !s.cleanup(ctx, r) {
		return
	}
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := s.rows.deleteCleaned(dctx, r.ID); err != nil {
		s.log.Warn("could not delete the row of an ended upload session; the collection run will", "session", r.ID, "error", err)
	}
}

// release gives a claim back after a request that recorded nothing more, with a context of its own because the request's may be
// what ended (a drain, R-STOR-004 step 8).
func (s *Service) release(ctx context.Context, id uuid.UUID, writer string) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.rows.release(rctx, id, writer); err != nil {
		s.log.Warn("could not release the claim on an upload session; it expires with its lease", "session", id, "error", err)
	}
}
