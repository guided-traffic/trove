package blob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// This file is the collection run's contact with the multi-request upload sessions (R-STOR-011 Expiry, R-STOR-025): the run aborts
// the sessions that nobody continues, removes the multipart upload and the staging objects of the sessions that are aborted or
// done, and, in a full sweep, removes the staging objects and multipart uploads that no session row stands for (a replica that
// crashed between creating the upload and recording the session, a repository that was deleted with its sessions). The orphan
// sweep of R-STOR-026 is the same pass over the same prefix; until it is built as a task of its own, the full collection sweep
// runs it.

// sessionBatch bounds the rows one statement of the session step takes.
const sessionBatch = 200

// maxSessionBatches bounds the pages the staging sweep takes in one run (the expiry and the cleaning have a time budget instead): it leaves
// the rest to the next run, which goes on from the saved cursor, so that a large staging area never keeps the blob collection below it
// waiting (R-STOR-025).
const maxSessionBatches = 25

// sweepPage is the number of multipart uploads the staging sweep lists at a time.
const sweepPage = 500

// WithUploadSessions makes the run expire and clean up the upload sessions with the idle time and the maximum age of cfg
// (storage.uploads.session_ttl, session_max_age). Without it a run leaves the sessions alone.
func WithUploadSessions(cfg config.StorageUploads) CollectorOption {
	return func(c *Collector) { c.uploads = &cfg }
}

// WithSessionStepLimits sets the pages the sweep of the staging area takes in one run and the directories it lists per page (25 and
// 200 otherwise); tests use small values to see the cursor work.
func WithSessionStepLimits(pages, batch int) CollectorOption {
	return func(c *Collector) { c.sweepPages, c.sweepBatch = pages, batch }
}

func (c *Collector) pages() int {
	if c.sweepPages > 0 {
		return c.sweepPages
	}
	return maxSessionBatches
}

func (c *Collector) batch() int {
	if c.sweepBatch > 0 {
		return c.sweepBatch
	}
	return sessionBatch
}

// RemoveSession removes everything a session staged: its multipart upload and every object below its prefix. An upload the store
// does not know any more (completed or aborted already) and an object that is gone are success. The first failure is returned,
// after every step was tried.
func RemoveSession(ctx context.Context, objects ObjectStore, prefix, id, uploadID string) error {
	var errs []error
	if uploadID != "" {
		if err := objects.AbortMultipart(ctx, SessionKey(prefix, id, "data"), uploadID); err != nil && !IsNoSuchUpload(err) && !errors.Is(err, ErrNotFound) {
			errs = append(errs, fmt.Errorf("abort the multipart upload of session %s: %w", id, err))
		}
	}
	token := ""
	for {
		page, err := objects.List(ctx, SessionPrefix(prefix, id), ListOptions{Token: token})
		if err != nil {
			errs = append(errs, fmt.Errorf("list the objects of session %s: %w", id, err))
			break
		}
		for _, o := range page.Objects {
			if err := objects.Delete(ctx, o.Key); err != nil && !errors.Is(err, ErrNotFound) {
				errs = append(errs, fmt.Errorf("delete %s: %w", o.Key, err))
			}
		}
		if page.Next == "" {
			break
		}
		token = page.Next
	}
	return errors.Join(errs...)
}

// uploadSessions is the session step of a run.
func (r *run) uploadSessions(ctx context.Context) error {
	u := r.c.uploads
	if u == nil {
		return nil
	}
	if err := r.expireSessions(ctx, u); err != nil {
		return err
	}
	if r.opts.DryRun {
		return nil
	}
	if err := r.cleanSessions(ctx); err != nil {
		return err
	}
	if r.opts.Mode == GCFull {
		return r.sweepUploads(ctx, u)
	}
	return nil
}

// expireSessions aborts the live sessions without a recorded request for the idle time and the open ones older than the maximum
// age. A dry run only counts them.
func (r *run) expireSessions(ctx context.Context, u *config.StorageUploads) error {
	if r.opts.DryRun {
		var n int64
		err := r.c.db.InTx(ctx, func(tx meta.Tx) error {
			qs, err := queries(tx)
			if err != nil {
				return err
			}
			n, err = qs.UploadCountExpired(ctx, q.UploadCountExpiredParams{TtlSeconds: u.SessionTTL.Seconds(), MaxAgeSeconds: u.SessionMaxAge.Seconds()})
			return err
		})
		r.res.UploadSessionsAborted += int(n)
		return err
	}
	for r.inBudget() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if stop, err := r.sessionPause(ctx); stop || err != nil {
			return err
		}
		var ids []string
		err := r.c.db.InTx(ctx, func(tx meta.Tx) error {
			qs, err := queries(tx)
			if err != nil {
				return err
			}
			ids, err = qs.UploadExpire(ctx, q.UploadExpireParams{
				TtlSeconds: u.SessionTTL.Seconds(), MaxAgeSeconds: u.SessionMaxAge.Seconds(), MaxRows: sessionBatch,
			})
			return err
		})
		if err != nil {
			return fmt.Errorf("expire upload sessions: %w", err)
		}
		r.res.UploadSessionsAborted += len(ids)
		r.c.metrics.UploadSession(SessionExpired, len(ids))
		if len(ids) < sessionBatch {
			return nil
		}
	}
	return nil // the rest is the next run's
}

// sessionPause re-reads the deletion pause before a batch of the session step (R-STOR-024): while it is set the step removes and aborts
// nothing, and the run ends as a paused one. The refusal of a request past its age does not depend on it.
func (r *run) sessionPause(ctx context.Context) (stop bool, err error) {
	paused, err := r.paused(ctx)
	if err != nil {
		return true, err
	}
	if paused {
		r.res.Paused = true
	}
	return paused, nil
}

// canonicalID reports that id is a session id in the one form the service writes, the lower-case hyphenated UUID: the parser accepts
// others (braced, 32 hexadecimal digits, urn:uuid:), and a directory in one of those is not a session of trove.
func canonicalID(id string) bool {
	u, err := uuid.Parse(id)
	return err == nil && u.String() == id
}

// cleanRow is a session that waits for the removal of its objects.
type cleanRow struct{ ID, S3UploadID string }

// sessionBudget is the time the expiry and the cleaning of one run may take: they go on batch after batch until it is spent, and the rest
// is the next run's, so a backlog never keeps the blob collection below them waiting for hours (R-STOR-025).
const sessionBudget = 2 * time.Minute

// doneReplayWindow is how long a finished session stays, so that a closing request repeated after a cut answers from its record
// (Q-STOR-176). It is not the idle time: a finished session has nothing left to wait for.
const doneReplayWindow = 15 * time.Minute

// inBudget reports that the session step has time left; the first call starts the clock.
func (r *run) inBudget() bool {
	if r.sessionStart.IsZero() {
		r.sessionStart = time.Now()
	}
	return time.Since(r.sessionStart) < sessionBudget
}

// cleanSessions deletes the rows that need no call to the store (the sessions that staged nothing and those whose objects the request
// that ended them removed), then removes the objects of the others and deletes their rows. A session whose objects cannot be removed goes to
// the back of the line, so that a few bad rows do not stop the cleaning.
func (r *run) cleanSessions(ctx context.Context) error {
	r.sessionStart = time.Time{} // the cleaning has a budget of its own: a backlog of expiry does not starve it
	window := doneReplayWindow.Seconds()
	for r.inBudget() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if stop, err := r.sessionPause(ctx); stop || err != nil {
			return err
		}
		var bulk int64
		var rows []cleanRow
		err := r.c.db.InTx(ctx, func(tx meta.Tx) error {
			qs, err := queries(tx)
			if err != nil {
				return err
			}
			if bulk, err = qs.UploadDeleteCleanedBulk(ctx, q.UploadDeleteCleanedBulkParams{WindowSeconds: window, MaxRows: sessionBatch}); err != nil {
				return err
			}
			aborted, err := qs.UploadListAborted(ctx, sessionBatch)
			if err != nil {
				return err
			}
			for _, a := range aborted {
				rows = append(rows, cleanRow{a.ID, a.S3UploadID})
			}
			if len(rows) >= sessionBatch {
				return nil
			}
			done, err := qs.UploadListDone(ctx, q.UploadListDoneParams{WindowSeconds: window, MaxRows: int32(sessionBatch - len(rows))}) //nolint:gosec // at most sessionBatch
			if err != nil {
				return err
			}
			for _, d := range done {
				rows = append(rows, cleanRow{d.ID, d.S3UploadID})
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("list upload sessions to clean: %w", err)
		}
		r.res.UploadSessionsCleaned += int(bulk)
		cleaned := 0
		for _, row := range rows {
			if err := RemoveSession(ctx, r.c.objects, r.c.prefix, row.ID, row.S3UploadID); err != nil {
				if stopsRun(ctx, err) {
					return err
				}
				r.res.Errors++
				r.c.log.Warn("could not remove the objects of an upload session", "session", row.ID, "error", err)
				if berr := r.c.db.InTx(ctx, func(tx meta.Tx) error {
					qs, err := queries(tx)
					if err != nil {
						return err
					}
					return qs.UploadBumpActivity(ctx, row.ID)
				}); berr != nil {
					return fmt.Errorf("move an upload session back in the line: %w", berr)
				}
				continue
			}
			if err := r.c.db.InTx(ctx, func(tx meta.Tx) error {
				qs, err := queries(tx)
				if err != nil {
					return err
				}
				_, err = qs.UploadDeleteCleaned(ctx, row.ID)
				return err
			}); err != nil {
				return fmt.Errorf("delete an upload session: %w", err)
			}
			cleaned++
		}
		r.res.UploadSessionsCleaned += cleaned
		if bulk < sessionBatch && len(rows) < sessionBatch {
			return nil
		}
	}
	return nil
}

// sweepCursor is where the sweep of the staging area stopped: the last session directory it looked at, whether the directories are done,
// and the last multipart upload it looked at. It is kept in system_state, so that successive runs go on (a run takes a bounded number of
// pages, and the live sessions at the start of the listing would otherwise use them all every time).
type sweepCursor struct {
	Dir       string `json:"dir,omitempty"`
	DirsDone  bool   `json:"dirsDone,omitempty"`
	UploadKey string `json:"uploadKey,omitempty"`
	UploadID  string `json:"uploadId,omitempty"`
}

func (r *run) loadSweepCursor(ctx context.Context) (c sweepCursor, err error) {
	var raw string
	err = r.c.db.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		raw, err = qs.UploadSweepCursorGet(ctx)
		return err
	})
	if err != nil || raw == "" {
		return sweepCursor{}, err
	}
	if json.Unmarshal([]byte(raw), &c) != nil {
		return sweepCursor{}, nil //nolint:nilerr // // a cursor that cannot be read starts the sweep again
	}
	return c, nil
}

func (r *run) saveSweepCursor(ctx context.Context, c sweepCursor) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return r.c.db.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		return qs.UploadSweepCursorSet(ctx, string(raw))
	})
}

// sweepUploads removes what no session stands for below the staging area (the leftovers of R-STOR-011 Expiry): the objects of a
// session id without a row, and the multipart uploads of an id without a row or of a session that is over or whose upload is another.
// Only what is older than the session maximum age goes, so that a session being started, whose row is not committed yet, is out of
// reach. A directory that is not a session id in the canonical form is reported by the log and never deleted. Both listings are paged,
// a run takes a bounded number of pages and saves where it stopped, and the next run goes on from there; when both listings are done
// the cursor is cleared and the next full sweep starts again.
func (r *run) sweepUploads(ctx context.Context, u *config.StorageUploads) error {
	cutoff := r.c.now().Add(-u.SessionMaxAge)
	root := UploadsPrefix(r.c.prefix)
	cur, err := r.loadSweepCursor(ctx)
	if err != nil {
		return fmt.Errorf("read the sweep cursor: %w", err)
	}

	// Objects, one session directory at a time.
	for page := 0; !cur.DirsDone; page++ {
		if page == r.c.pages() {
			return r.saveSweepCursor(ctx, cur)
		}
		if stop, err := r.sessionPause(ctx); stop || err != nil {
			return err
		}
		opts := ListOptions{Delimiter: "/", Max: r.c.batch()}
		if cur.Dir != "" {
			opts.StartAfter = cur.Dir
		}
		listed, err := r.c.objects.List(ctx, root, opts)
		if err != nil {
			return fmt.Errorf("list the staging area: %w", err)
		}
		ids := make([]string, 0, len(listed.Prefixes))
		for _, p := range listed.Prefixes {
			id := strings.TrimSuffix(strings.TrimPrefix(p, root), "/")
			if !canonicalID(id) {
				r.c.log.Warn("a foreign directory below the staging area is left alone", "prefix", p)
				continue
			}
			ids = append(ids, id)
		}
		// The next page starts after the last key this one returned, an object or a directory (after everything below it: '0' sorts right
		// after '/'), so that a page of plain objects under _uploads/ moves the cursor too.
		next := cur.Dir
		for _, p := range listed.Prefixes {
			if k := root + strings.TrimSuffix(strings.TrimPrefix(p, root), "/") + "0"; k > next {
				next = k
			}
		}
		for _, o := range listed.Objects {
			if o.Key > next {
				next = o.Key
			}
		}
		progress := next != cur.Dir
		cur.Dir = next
		known, err := r.knownSessions(ctx, ids)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if _, ok := known[id]; ok {
				continue // its row decides: the session is live, or the cleanup above removes it
			}
			if err := r.removeUnknownSession(ctx, id, cutoff); err != nil {
				if stopsRun(ctx, err) {
					return err
				}
				r.res.Errors++
				r.c.log.Warn("could not remove the staging objects of an unknown upload session", "session", id, "error", err)
			}
		}
		// A listing that carries a continuation token is not at its end; without one it is, however many prefixes it returned.
		if listed.Next == "" {
			cur.DirsDone = true
			continue
		}
		if !progress {
			// a store that ignores StartAfter, or a page that returns nothing though it says there is more: do not ask again
			r.c.log.Warn("the listing of the staging area makes no progress; the sweep of the directories ends here", "after", cur.Dir)
			cur.DirsDone = true
			continue
		}
		if err := r.saveSweepCursor(ctx, cur); err != nil {
			return err
		}
	}

	// Multipart uploads, a page at a time from a store that pages.
	after := MultipartMarker{Key: cur.UploadKey, UploadID: cur.UploadID}
	for page := 0; ; page++ {
		if page == r.c.pages() {
			cur.UploadKey, cur.UploadID = after.Key, after.UploadID
			return r.saveSweepCursor(ctx, cur)
		}
		if stop, err := r.sessionPause(ctx); stop || err != nil {
			return err
		}
		ups, next, err := r.listUploads(ctx, root, after)
		if err != nil {
			return fmt.Errorf("list the multipart uploads of the staging area: %w", err)
		}
		if err := r.abortStrays(ctx, ups, cutoff); err != nil {
			return err
		}
		if next == (MultipartMarker{}) {
			r.sweepProbes(ctx, cutoff)
			return r.saveSweepCursor(ctx, sweepCursor{}) // both listings are done: the next sweep starts at the first session
		}
		after = next
	}
}

// sweepProbes removes the probe objects of the startup and conformance checks that a failed clean-up left (they are named for the replica and
// the check and are written and removed within a call), once they are older than the cutoff. It is best effort.
func (r *run) sweepProbes(ctx context.Context, cutoff time.Time) {
	prefix := probePrefix(r.c.prefix)
	page, err := r.c.objects.List(ctx, prefix, ListOptions{Max: 1000})
	if err != nil {
		r.c.log.Warn("could not list the probe objects", "error", err)
		return
	}
	for _, o := range page.Objects {
		if o.LastModified.After(cutoff) {
			continue
		}
		if err := r.c.objects.Delete(ctx, o.Key); err != nil && !errors.Is(err, ErrNotFound) {
			r.c.log.Warn("could not delete a leftover probe object", "key", o.Key, "error", err)
			continue
		}
		r.res.UploadObjectsDeleted++
	}
	// and the multipart uploads of a check that was cut between its upload and its abort
	ups, _, err := r.listUploads(ctx, prefix, MultipartMarker{})
	if err != nil {
		r.c.log.Warn("could not list the probe uploads", "error", err)
		return
	}
	for _, up := range ups {
		if up.Initiated.After(cutoff) {
			continue
		}
		if err := r.c.objects.AbortMultipart(ctx, up.Key, up.UploadID); err != nil && !IsNoSuchUpload(err) {
			r.c.log.Warn("could not abort a leftover probe upload", "key", up.Key, "error", err)
			continue
		}
		r.res.UploadsAborted++
	}
}

// listUploads reads one page of the unfinished multipart uploads below prefix; a store that cannot page answers with all of them as one.
func (r *run) listUploads(ctx context.Context, prefix string, after MultipartMarker) ([]MultipartUpload, MultipartMarker, error) {
	if p, ok := r.c.objects.(MultipartPager); ok {
		return p.ListMultipartPage(ctx, prefix, after, sweepPage)
	}
	if after != (MultipartMarker{}) {
		return nil, MultipartMarker{}, nil
	}
	ups, err := r.c.objects.ListMultipart(ctx, prefix)
	return ups, MultipartMarker{}, err
}

// abortStrays aborts the uploads of a page that no live session stands for and that are older than cutoff.
func (r *run) abortStrays(ctx context.Context, ups []MultipartUpload, cutoff time.Time) error {
	ids := make([]string, 0, len(ups))
	for _, up := range ups {
		if id, _, ok := ParseSessionKey(r.c.prefix, up.Key); ok && canonicalID(id) {
			ids = append(ids, id)
		}
	}
	known, err := r.knownSessions(ctx, ids)
	if err != nil {
		return err
	}
	for _, up := range ups {
		id, _, ok := ParseSessionKey(r.c.prefix, up.Key)
		if !ok || !canonicalID(id) || up.Initiated.After(cutoff) {
			continue
		}
		if row, ok := known[id]; ok && row.S3UploadID == up.UploadID && row.State != "done" && row.State != "aborted" {
			continue
		}
		if err := r.c.objects.AbortMultipart(ctx, up.Key, up.UploadID); err != nil && !IsNoSuchUpload(err) {
			if stopsRun(ctx, err) {
				return err
			}
			r.res.Errors++
			r.c.log.Warn("could not abort a multipart upload of the staging area", "key", up.Key, "error", err)
			continue
		}
		r.res.UploadsAborted++
	}
	return nil
}

// knownSessions returns the rows of the session ids that have one, asked in chunks.
func (r *run) knownSessions(ctx context.Context, ids []string) (map[string]q.UploadKnownSessionsRow, error) {
	out := make(map[string]q.UploadKnownSessionsRow, len(ids))
	for len(ids) > 0 {
		chunk := ids[:min(len(ids), sessionBatch)]
		ids = ids[len(chunk):]
		var rows []q.UploadKnownSessionsRow
		err := r.c.db.InTx(ctx, func(tx meta.Tx) error {
			qs, err := queries(tx)
			if err != nil {
				return err
			}
			rows, err = qs.UploadKnownSessions(ctx, chunk)
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("read upload sessions: %w", err)
		}
		for _, row := range rows {
			out[row.ID] = row
		}
	}
	return out, nil
}

// removeUnknownSession deletes the objects below the directory of a session without a row that are older than cutoff.
func (r *run) removeUnknownSession(ctx context.Context, id string, cutoff time.Time) error {
	var errs []error
	token := ""
	for {
		page, err := r.c.objects.List(ctx, SessionPrefix(r.c.prefix, id), ListOptions{Token: token})
		if err != nil {
			return err
		}
		for _, o := range page.Objects {
			if o.LastModified.After(cutoff) {
				continue
			}
			if err := r.c.objects.Delete(ctx, o.Key); err != nil && !errors.Is(err, ErrNotFound) {
				errs = append(errs, err)
				continue
			}
			r.res.UploadObjectsDeleted++
		}
		if page.Next == "" {
			break
		}
		token = page.Next
	}
	return errors.Join(errs...)
}
