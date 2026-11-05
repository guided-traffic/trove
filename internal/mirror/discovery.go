package mirror

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/mirror/discover"
	"github.com/guided-traffic/trove/internal/repository"
)

// The codes a failed pass attempt records (mirror_state.last_error_code, the event mirror.pass_failed, the status's
// errors.lastCode, R-REPO-056).
const (
	// CodeDiscoveryUnavailable: the discovery method that `discovery` names is not offered by the upstream (R-REPO-051).
	CodeDiscoveryUnavailable = "discovery_unavailable"
	// CodeDiscoveryFailed: the format's discovery failed other than by throttling.
	CodeDiscoveryFailed = "discovery_failed"
	// CodeDiscoveryStalled: a page without names and without progress that did not end the discovery.
	CodeDiscoveryStalled = "discovery_stalled"
	// CodeCursorTooLarge: the format's cursor is longer than discover.MaxCursorBytes.
	CodeCursorTooLarge = "cursor_too_large"
	// CodeNotApplicable: the repository's format does not discover.
	CodeNotApplicable = "not_applicable"
	// CodeUpstream: the pass could not be given its upstream (the host refused it).
	CodeUpstream = "upstream_unavailable"
	// CodeInternal: a database error or another failure of the engine itself.
	CodeInternal = "internal"
)

// passError is an error of the pass itself with the code it is recorded under.
type passError struct {
	code string
	err  error
}

func (e *passError) Error() string { return e.err.Error() }
func (e *passError) Unwrap() error { return e.err }

// pageSize is the number of names a step of the held-names phase reads.
const pageSize = 1000

// The engine's own cursors of the phase that adds the names of the packages the repository holds (R-REPO-051), stored in
// pass_cursor before the format's first page and never handed to a format: {"mirrorHeldAfter": <last name>} while the held
// names are paged, {"mirrorHeldDone": true} once they are, until the first page of the format replaces it (Q-REPO-186). An
// empty cursor is a phase that has not begun.
const (
	heldAfterKey = "mirrorHeldAfter"
	heldDoneKey  = "mirrorHeldDone"
)

// parseEngineCursor reads an engine cursor by its structure: an object with exactly one member, one of the two keys above with
// a value of its type. Anything else is a cursor of the format.
func parseEngineCursor(c json.RawMessage) (after string, done, ok bool) {
	var m map[string]json.RawMessage
	if len(c) == 0 || json.Unmarshal(c, &m) != nil || len(m) != 1 {
		return "", false, false
	}
	if v, has := m[heldAfterKey]; has && json.Unmarshal(v, &after) == nil {
		return after, false, true
	}
	if v, has := m[heldDoneKey]; has && json.Unmarshal(v, &done) == nil && done {
		return "", true, true
	}
	return "", false, false
}

// discover adds names to the pending table until 10,000 rows are pending or the discovery has ended (R-REPO-053 step 2). A
// full pass first adds the exact include names and pages the held names, then asks the format for its pages.
func (p *pass) discover(ctx context.Context) error {
	if p.method == "" {
		if err := p.chooseMethod(ctx); err != nil || p.isHalted() {
			return err
		}
	}
	pending, err := p.e.o.Store.PendingCount(ctx, p.id)
	if err != nil {
		return &passError{code: CodeInternal, err: err}
	}
	cursor := p.state.PassCursor
	after, heldDone, engine := parseEngineCursor(cursor)
	held := p.kind == discover.PassFull && (len(cursor) == 0 || (engine && !heldDone))
	first := len(cursor) == 0
	if engine {
		cursor = nil
	}
	for pending < MaxPending && !p.done && !p.isHalted() {
		// The repository may have been paused, switched or taken offline since the last page (R-REPO-053 stops).
		if p.checkStop(ctx) {
			break
		}
		var added int64
		if held {
			added, held, after, err = p.heldStep(ctx, after, first)
			first = false
			if !held {
				cursor = nil
			}
		} else {
			added, cursor, err = p.pageStep(ctx, pending, cursor)
		}
		if err != nil {
			return err
		}
		pending += added
	}
	return nil
}

// heldStep adds the next names of the packages the repository holds to the pending table, after the exact names of the
// include patterns on the first step. more is false when the held names are done. The position, or the end of the phase, is
// stored in pass_cursor in the same statement.
func (p *pass) heldStep(ctx context.Context, after string, first bool) (added int64, more bool, next string, err error) {
	repo, _ := p.current()
	var names []string
	if first {
		names = p.exactIncludes(repo)
	}
	rows, err := p.e.o.Store.HeldNames(ctx, p.id, after, pageSize)
	if err != nil {
		return 0, false, after, &passError{code: CodeInternal, err: err}
	}
	next = after
	if len(rows) > 0 {
		next = rows[len(rows)-1]
	}
	more = len(rows) == pageSize
	admits := p.admitFn()
	for _, n := range rows {
		if admits(n) {
			names = append(names, n)
		}
	}
	progress := Names{Names: fitNames(names), SetProgress: true, Done: false}
	if more {
		progress.Cursor = mustJSON(map[string]string{heldAfterKey: next})
	} else {
		progress.Cursor = mustJSON(map[string]bool{heldDoneKey: true})
	}
	added, err = p.e.o.Store.AddNames(ctx, p.fence, progress)
	if err != nil {
		p.fatal(err)
		return 0, false, next, nil
	}
	p.add(func(c *counters) { c.names += added })
	p.mu.Lock()
	p.state.PassCursor = progress.Cursor
	p.mu.Unlock()
	return added, more, next, nil
}

// exactIncludes returns the include patterns without a wildcard as normalized, admitted names (R-REPO-051).
func (p *pass) exactIncludes(repo repository.Repository) []string {
	include, _, err := patterns(repo)
	if err != nil {
		return nil
	}
	var out []string
	admits := p.admitFn()
	for _, pat := range include {
		prefix, wildcard := discover.LiteralPrefix(pat)
		if wildcard || prefix == "" {
			continue
		}
		if n, ok := p.e.o.NormalizeName(repo.Format, prefix); ok && admits(n) {
			out = append(out, n)
		}
	}
	return out
}

// pageStep asks the format for the next page of names and stores it with its cursor in one statement.
func (p *pass) pageStep(ctx context.Context, pending int64, cursor json.RawMessage) (added int64, next json.RawMessage, err error) {
	repo, _ := p.current()
	include, exclude, perr := patterns(repo)
	if perr != nil {
		return 0, cursor, &passError{code: CodeInternal, err: perr}
	}
	if _, _, engine := parseEngineCursor(cursor); engine {
		cursor = nil
	}
	admits := p.admitFn()
	rq := discover.PageRequest{
		Method: p.method, Kind: p.kind, Cursor: cursor, Limit: int(MaxPending - pending),
		Scope: discover.Scope{Include: include, Exclude: exclude, Admits: admits},
	}
	if p.kind == discover.PassIncremental {
		rq.Since = p.state.FeedCursor
	}
	var page discover.Page
	err = p.callDiscovery(ctx, func(c context.Context) (e error) {
		page, e = p.lister.MirrorNames(c, p.up, rq)
		return e
	})
	if err != nil {
		return 0, cursor, p.discoveryError(err)
	}
	switch {
	case len(page.Cursor) > discover.MaxCursorBytes:
		return 0, cursor, &passError{code: CodeCursorTooLarge, err: fmt.Errorf("the cursor of the %s method is %d bytes", p.method, len(page.Cursor))}
	case !page.Done && len(page.Names) == 0 && len(page.Removed) == 0 && bytes.Equal(page.Cursor, cursor):
		return 0, cursor, &passError{code: CodeDiscoveryStalled, err: fmt.Errorf("the %s method returned a page without names or progress", p.method)}
	}
	for _, name := range page.Removed {
		if admits(name) {
			p.refresh(ctx, name)
		}
	}
	names := make([]string, 0, len(page.Names))
	for _, n := range page.Names {
		if admits(n) {
			names = append(names, n)
		}
	}
	n := Names{
		Names: fitNames(names), SetProgress: true, Cursor: page.Cursor, Done: page.Done,
		Truncated: page.Truncated, Note: trimNote(page.Note),
	}
	if added, err = p.e.o.Store.AddNames(ctx, p.fence, n); err != nil {
		if errors.Is(err, ErrCursorTooLarge) {
			return 0, cursor, &passError{code: CodeCursorTooLarge, err: err}
		}
		p.fatal(err)
		return 0, cursor, nil
	}
	p.add(func(c *counters) { c.names += added })
	p.mu.Lock()
	p.state.PassCursor = page.Cursor
	p.done = page.Done
	if page.Truncated {
		p.state.PassDiscoveryComplete, p.state.PassDiscoveryNote = false, n.Note
	}
	p.mu.Unlock()
	return added, page.Cursor, nil
}

// discoveryError maps the error of a discovery call to the pass's error; a halt is no error.
func (p *pass) discoveryError(err error) error {
	switch {
	case p.isHalted():
		return nil
	case errors.Is(err, discover.ErrMethodUnavailable):
		return &passError{code: CodeDiscoveryUnavailable, err: err}
	}
	var pe *passError
	if errors.As(err, &pe) {
		return err
	}
	return &passError{code: CodeDiscoveryFailed, err: err}
}

// chooseMethod asks the format which methods the upstream offers and picks the one `discovery` names, or the first that is
// offered for `auto` (R-REPO-051); the pass is full unless its method is the feed of the last complete pass.
func (p *pass) chooseMethod(ctx context.Context) error {
	var offered []discover.Method
	err := p.callDiscovery(ctx, func(c context.Context) (e error) {
		offered, e = p.lister.MirrorMethods(c, p.up)
		return e
	})
	if err != nil {
		return p.discoveryError(err)
	}
	_, rs := p.current()
	want := rs.Mirror.Discovery
	var method discover.Method
	for _, m := range discover.Order() {
		available := m == discover.MethodNames || slices.Contains(offered, m)
		if available && (want == repository.DiscoveryAuto || want == string(m)) {
			method = m
			break
		}
	}
	if method == "" {
		return &passError{code: CodeDiscoveryUnavailable, err: fmt.Errorf("the upstream does not offer the discovery method %q", want)}
	}
	kind := FinalKind(p.kind, method, p.state)
	if err := p.e.o.Store.SetMethod(ctx, p.fence, method, kind); err != nil {
		p.fatal(err)
		return nil
	}
	p.mu.Lock()
	p.method, p.kind = method, kind
	p.state.PassMethod, p.state.PassKind = method, kind
	p.mu.Unlock()
	return nil
}

// refresh refreshes the documents of a package the upstream reports as deleted and stores nothing (R-REPO-051, Q-REPO-144).
func (p *pass) refresh(ctx context.Context, name string) {
	err := p.callDiscovery(ctx, func(c context.Context) error {
		_, e := p.lister.MirrorPlan(c, p.up, name)
		return e
	})
	if err != nil && ctx.Err() == nil && !p.isHalted() {
		p.log.Debug("the document of a deleted package could not be refreshed", "error", err)
	}
}

// fitNames drops the names the table cannot hold.
func fitNames(names []string) []string {
	out := names[:0:0]
	for _, n := range names {
		if n != "" && len(n) <= 1024 && utf8.ValidString(n) {
			out = append(out, n)
		}
	}
	return out
}

// trimNote cuts a discovery note to the 256 characters of the column.
func trimNote(s string) string {
	if utf8.RuneCountInString(s) <= 256 {
		return s
	}
	r := []rune(s)
	return string(r[:256])
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// rateLimitStatus returns the HTTP status of a rate-limit error that says it (HTTPStatus), 0 when it does not.
func rateLimitStatus(rl discover.RateLimited) int {
	var s interface{ HTTPStatus() int }
	if errors.As(rl, &s) {
		return s.HTTPStatus()
	}
	return 0
}

// eventRateLimited writes mirror.rate_limited, at most once per pass and hour (R-REPO-054).
func (p *pass) eventRateLimited(ctx context.Context, rl discover.RateLimited, wait int64, hinted bool) {
	p.mu.Lock()
	sent := p.rlEvent
	p.rlEvent = true
	p.mu.Unlock()
	if sent || !p.e.rateLimitEvent(p.id, p.e.o.Clock.Now()) {
		return
	}
	d := map[string]any{"status": rateLimitStatus(rl), "reason": rl.RateLimitReason(), "waitSeconds": wait, "hinted": hinted}
	if p.host != "" {
		d["host"] = p.host
	}
	p.record(ctx, audit.EventMirrorRateLimited, audit.Success, "", d)
}
