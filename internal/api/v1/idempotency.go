package v1

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Idempotency keys for creates and job starts (R-API-014, R-API-051). The state
// is one row of api_idempotency_keys per (principal, key), inserted in the
// transaction of the change, so a retry behaves the same on whichever replica it
// reaches:
//
//   - first request: the row is inserted, the operation runs in the same
//     transaction, the outcome is stored in the row, the transaction commits;
//   - retry after the commit: the stored outcome is replayed with
//     Idempotency-Replayed: true; another method, route or body is 422;
//   - concurrent duplicate: its insert blocks on the first one's row for at most
//     one second (lock_timeout), then replays, or answers 409
//     idempotency_in_progress with Retry-After: 1;
//   - a 5xx or any other failure rolls the transaction back, row included, so a
//     retry runs again; a 4xx outcome is stored and replayed.

// HeaderIdempotencyKey is the request header; HeaderIdempotencyReplayed marks a
// replayed answer.
const (
	HeaderIdempotencyKey      = "Idempotency-Key"
	HeaderIdempotencyReplayed = "Idempotency-Replayed"
)

// idempotencyWait is how long a duplicate waits on the first request's row. It
// is below the 5 s statement timeout, so the wait never ends as a
// database_timeout.
const idempotencyWait = time.Second

// Outcome is what an idempotent operation answers.
type Outcome struct {
	// Status is the success status: 200, 201, 202 or 204.
	Status int
	// Header holds the answer headers worth replaying: Location, ETag and the
	// like. Content-Type is application/json; charset=utf-8 when Body is set.
	Header map[string]string
	// Body is the JSON body; nil means none.
	Body any
	// SecretID, when set, says the body contains a secret (a created token):
	// it is sent to this client and never stored. A replay answers
	// IdempotentOp.SecretReplay instead of the body.
	SecretID string
	// Finish, when set, completes Status, Header and Body after the commit hooks
	// of the operation ran and before the outcome is stored, for a value that
	// exists only then: the epoch a write produced is raised by the change log
	// in a hook that runs just before COMMIT (R-AUTHZ-032). Run cannot know it,
	// and a replay must still carry it. Finish may change out and returns an
	// error to roll the transaction back.
	Finish func(ctx context.Context, tx meta.Tx, out *Outcome) error
}

// IdempotentOp is one operation behind an Idempotency-Key.
type IdempotentOp struct {
	// Run does the change in tx and returns its outcome. An *errs.Error below
	// 500 is the operation's refusal and is stored and replayed; any other error
	// rolls everything back and is answered as it is.
	Run func(ctx context.Context, tx meta.Tx) (Outcome, error)
	// SecretReplay builds the answer to a replay of an outcome that carried a
	// secret, for example 409 token_already_issued with the token ID. Nil means
	// a plain 409 conflict that names the secret ID.
	SecretReplay func(secretID string) error
}

// finish completes an outcome whose Finish is set, on the transaction, after
// every commit hook the operation registered ran.
func finish(ctx context.Context, tx meta.Tx, out *Outcome) error {
	if out.Finish == nil {
		return nil
	}
	f := out.Finish
	out.Finish = nil
	return f(ctx, tx, out)
}

// Idempotency executes IdempotentOps.
type Idempotency struct {
	db        Store
	retention time.Duration
	wait      time.Duration
}

// NewIdempotency creates the executor; retention is api.idempotency_retention.
func NewIdempotency(db Store, retention time.Duration) *Idempotency {
	return &Idempotency{db: db, retention: retention, wait: idempotencyWait}
}

// idemKey validates the header: 1 to 255 printable ASCII characters. ok is
// false when the header is absent.
func idemKey(r *http.Request) (key string, ok bool, err error) {
	vals := r.Header.Values(HeaderIdempotencyKey)
	if len(vals) == 0 {
		return "", false, nil
	}
	key = vals[0]
	if len(vals) > 1 || len(key) < 1 || len(key) > 255 {
		return "", false, errs.E(errs.Invalid, errs.CodeInvalidHeader, "Idempotency-Key must be 1 to 255 printable ASCII characters.")
	}
	for i := range len(key) {
		if key[i] < 0x20 || key[i] > 0x7e {
			return "", false, errs.E(errs.Invalid, errs.CodeInvalidHeader, "Idempotency-Key must be 1 to 255 printable ASCII characters.")
		}
	}
	return key, true, nil
}

// canonicalSHA256 hashes the request body in a canonical form: JSON documents
// are re-encoded with sorted members and without white space, so the same
// content in another layout is the same request; anything else is hashed as it
// is.
func canonicalSHA256(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err == nil && !dec.More() {
		if canon, err := json.Marshal(v); err == nil {
			body = canon
		}
	}
	sum := sha256.Sum256(body)
	return sum[:]
}

// Do runs op once per Idempotency-Key and writes the answer. body is the
// request body as read by the handler (nil if none). Without the header the
// operation runs in a transaction of its own and the key logic is skipped.
// The caller answers a returned error with httpserver.Problem.
func (i *Idempotency) Do(w http.ResponseWriter, r *http.Request, body []byte, op IdempotentOp) error {
	key, has, err := idemKey(r)
	if err != nil {
		return err
	}
	if !has {
		var out Outcome
		if err := i.db.InTx(r.Context(), func(tx meta.Tx) (err error) {
			out, err = op.Run(r.Context(), tx)
			if err == nil && out.Finish != nil {
				// A hook of the operation's own transaction: it runs after the ones Run registered.
				tx.Before(func(ctx context.Context, tx meta.Tx) error { return finish(ctx, tx, &out) })
			}
			return err
		}); err != nil {
			return err
		}
		writeOutcome(w, r, out, nil)
		return nil
	}
	p := httpserver.PrincipalFrom(r.Context())
	if p.Anonymous || p.ID == "" {
		return errs.E(errs.Invalid, errs.CodeInvalidHeader, "Idempotency-Key needs an authenticated caller.")
	}
	req := idemRequest{
		principal: p.ID, key: key, method: r.Method, route: r.Pattern + " " + r.URL.EscapedPath(),
		sum: canonicalSHA256(body), requestID: httpserver.RequestID(r.Context()), instance: r.URL.Path,
	}
	return i.run(w, r, req, op)
}

type idemRequest struct {
	principal, key, method, route string
	sum                           []byte
	requestID, instance           string
}

// stored is a row read back: the outcome to replay.
type stored struct {
	status   int
	headers  map[string]string
	body     string
	secretID string
}

// hookScope is what a transaction of internal/meta/postgres offers to undo the
// hooks of work that was rolled back to a savepoint (meta.TxState).
type hookScope interface {
	Mark() meta.HookMark
	Discard(meta.HookMark)
}

func (i *Idempotency) run(w http.ResponseWriter, r *http.Request, req idemRequest, op IdempotentOp) error {
	ctx := r.Context()
	var (
		out      Outcome
		bodyText string
		replay   *stored
		refusal  *errs.Error
	)
	err := i.db.InTx(ctx, func(tx meta.Tx) error {
		out, bodyText, replay, refusal = Outcome{}, "", nil, nil
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return errors.New("api: idempotency needs a transaction of internal/meta/postgres")
		}
		scope, ok := tx.(hookScope)
		if !ok {
			return errors.New("api: idempotency needs a transaction with hook marks")
		}
		qs := q.New(ptx)
		// The short wait is for the claim only: a duplicate waits on the row of the
		// first request, the operation itself runs under the session's own limit.
		session, err := qs.CurrentLockTimeout(ctx)
		if err != nil {
			return err
		}
		if _, err := qs.SetLocalLockTimeout(ctx, strconv.FormatInt(i.wait.Milliseconds(), 10)); err != nil {
			return err
		}
		claimed, err := qs.InsertIdempotencyKey(ctx, q.InsertIdempotencyKeyParams{
			PrincipalID: req.principal, IdemKey: req.key, Method: req.method, Route: req.route,
			RequestSha256: req.sum, RetentionSecs: i.retention.Seconds(),
		})
		if err != nil {
			return err
		}
		if _, err := qs.SetLocalLockTimeout(ctx, session); err != nil {
			return err
		}
		if len(claimed) == 0 {
			replay, err = i.read(ctx, qs, req)
			return err
		}
		// The operation runs behind a savepoint: a refusal rolls its change back,
		// keeps the claimed row and stores the refusal on it in this transaction, so
		// a concurrent duplicate that was waiting on the row finds the stored
		// answer and does not run the operation again (R-API-014).
		if err := qs.IdempotencySavepoint(ctx); err != nil {
			return err
		}
		mark := scope.Mark()
		out, err = op.Run(ctx, tx)
		if err != nil {
			e, ok := errs.As(err)
			if !ok || e.Status() >= 500 {
				return err
			}
			if err := qs.IdempotencyRollbackToSavepoint(ctx); err != nil {
				return err
			}
			scope.Discard(mark)
			refusal = e
			return i.storeRefusal(ctx, qs, req, e)
		}
		if err := qs.IdempotencyReleaseSavepoint(ctx); err != nil {
			return err
		}
		complete := func(ctx context.Context, tx meta.Tx) error {
			if err := finish(ctx, tx, &out); err != nil {
				return err
			}
			hdr, body, err := encodeOutcome(out)
			if err != nil {
				return err
			}
			bodyText, err = qs.CompleteIdempotencyKey(ctx, q.CompleteIdempotencyKeyParams{
				PrincipalID: req.principal, IdemKey: req.key, Status: int16(out.Status), //nolint:gosec // 200..204
				Headers: hdr, Body: storedBody(body, out.SecretID), SecretID: out.SecretID,
			})
			return err
		}
		if out.Finish == nil {
			return complete(ctx, tx)
		}
		tx.Before(complete)
		return nil
	})
	switch {
	case postgres.IsLockTimeout(err):
		return errs.E(errs.Conflict, errs.CodeIdempotencyInFlight,
			"A request with this Idempotency-Key is still being processed; retry in a moment to receive its outcome.",
			errs.WithRetryAfter(time.Second))
	case err != nil:
		return err
	case refusal != nil:
		return refusal
	}
	if replay != nil {
		return i.writeReplay(w, req, replay, op)
	}
	if out.SecretID != "" {
		writeOutcome(w, r, out, nil) // the secret goes to this client only
		return nil
	}
	writeOutcome(w, r, out, &bodyText)
	return nil
}

// read loads the row that holds the key and decides whether the retry is the
// same request.
func (i *Idempotency) read(ctx context.Context, qs *q.Queries, req idemRequest) (*stored, error) {
	rows, err := qs.GetIdempotencyKey(ctx, q.GetIdempotencyKeyParams{PrincipalID: req.principal, IdemKey: req.key})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		// The row expired between the claim and the read; the retry runs again.
		return nil, errs.E(errs.Conflict, errs.CodeIdempotencyInFlight, "Retry the request.", errs.WithRetryAfter(time.Second))
	}
	row := rows[0]
	if row.Method != req.method || row.Route != req.route || !bytes.Equal(row.RequestSha256, req.sum) {
		return nil, errs.E(errs.Unprocessable, errs.CodeIdempotencyKeyReused,
			"This Idempotency-Key was used for a different request; use a new key for a different method, route or body.")
	}
	if row.ResponseStatus == 0 {
		return nil, errs.E(errs.Conflict, errs.CodeIdempotencyInFlight,
			"A request with this Idempotency-Key is still being processed.", errs.WithRetryAfter(time.Second))
	}
	s := &stored{status: int(row.ResponseStatus), body: row.ResponseBody, secretID: row.SecretIssuedID.String}
	if err := json.Unmarshal([]byte(row.ResponseHeaders), &s.headers); err != nil {
		return nil, errs.Wrap(err, "api.idempotency")
	}
	return s, nil
}

func (i *Idempotency) writeReplay(w http.ResponseWriter, req idemRequest, s *stored, op IdempotentOp) error {
	if s.secretID != "" {
		if op.SecretReplay != nil {
			return op.SecretReplay(s.secretID)
		}
		return errs.E(errs.Conflict, errs.CodeConflict,
			"The first request with this Idempotency-Key created a secret that is not stored; use a new key.",
			errs.WithExtension("secretId", s.secretID))
	}
	w.Header().Set(HeaderIdempotencyReplayed, "true")
	for k, v := range s.headers {
		w.Header().Set(k, v)
	}
	body := s.body
	if w.Header().Get("Content-Type") == errs.ProblemContentType {
		body = withRequestID(body, req.requestID, req.instance)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(s.status)
	if body != "" {
		_, _ = w.Write([]byte(body + "\n"))
	}
	return nil
}

// storeRefusal stores a 4xx outcome on the claimed row, in the transaction that
// claimed it: the change was rolled back to the savepoint and only the refusal is
// kept, so a retry, and a duplicate that waited, get the same answer.
func (i *Idempotency) storeRefusal(ctx context.Context, qs *q.Queries, req idemRequest, e *errs.Error) error {
	p, _ := errs.ProblemOf(e, "", "")
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	h, _ := json.Marshal(map[string]string{"Content-Type": errs.ProblemContentType})
	_, err = qs.CompleteIdempotencyKey(ctx, q.CompleteIdempotencyKeyParams{
		PrincipalID: req.principal, IdemKey: req.key, Status: int16(p.Status), //nolint:gosec // 400..499
		Headers: string(h), Body: string(b),
	})
	return err
}

// withRequestID puts the current request's ID and path into a stored problem.
func withRequestID(body, requestID, instance string) string {
	var m map[string]any
	if json.Unmarshal([]byte(body), &m) != nil {
		return body
	}
	m["requestId"], m["instance"] = requestID, instance
	b, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return string(b)
}

func encodeOutcome(out Outcome) (headers, body string, err error) {
	h := map[string]string{}
	for k, v := range out.Header {
		h[k] = v
	}
	if out.Body != nil {
		h["Content-Type"] = "application/json; charset=utf-8"
	}
	hb, err := json.Marshal(h)
	if err != nil {
		return "", "", err
	}
	if out.Body == nil {
		return string(hb), "", nil
	}
	bb, err := json.Marshal(out.Body)
	return string(hb), string(bb), err
}

func storedBody(body, secretID string) string {
	if secretID != "" {
		return ""
	}
	return body
}

// writeOutcome answers an outcome. stored is the body in the database's
// rendering when it was stored, so the first answer and a replay are the same
// bytes; nil sends the body as JSON (no key, or a secret).
func writeOutcome(w http.ResponseWriter, r *http.Request, out Outcome, stored *string) {
	for k, v := range out.Header {
		w.Header().Set(k, v)
	}
	switch {
	case out.Body == nil:
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(out.Status)
	case stored != nil && *stored != "":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(out.Status)
		_, _ = w.Write([]byte(*stored + "\n"))
	default:
		writeJSON(w, r, out.Status, out.Body)
	}
}
