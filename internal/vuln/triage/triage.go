// Package triage changes the status of findings (R-VULN-013): the transitions of R-VULN-012 (vuln.CheckTransition), the history
// row, the audit event vulnerability.finding.status_changed with the block the change sets or lifts under the repository's
// current policy, and, for a block that is set, the journal entry of the revocation journal (R-OPS-014, R-BAS-015).
//
// The package decides nothing about who may triage: the capability is checked by the API's authorization step and the read on
// the finding is the scope the caller passes in, which narrows every statement (R-VULN-019). A finding outside the scope does
// not exist for the caller (404).
package triage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// MaxBulk is the number of findings one set-status request changes (R-VULN-013).
const MaxBulk = 500

// MaxComment is the longest comment, in characters.
const MaxComment = 4000

// Options configure [New].
type Options struct {
	Store        *store.API
	Repositories repository.Reader
	Recorder     audit.Recorder
	// Enabled and Enforcement are vulnerabilities.enabled and vulnerabilities.enforcement: with the repository's member they
	// give the policy under which a change sets or lifts a block.
	Enabled     bool
	Enforcement vuln.Enforcement
}

// Service changes the status of findings.
type Service struct{ o Options }

// New returns the service.
func New(o Options) (*Service, error) {
	if o.Store == nil || o.Repositories == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "vuln triage: the store and the repository snapshot are required")
	}
	if o.Recorder == nil {
		o.Recorder = audit.Nop{}
	}
	return &Service{o: o}, nil
}

// Actor is who changes the findings and from where.
type Actor struct {
	// ID is the principal's ID, "" for none (the break-glass session has no table row).
	ID     string
	Audit  audit.Actor
	Source audit.Source
}

// Request is a requested change. A member that is not sent leaves the finding's value as it is.
type Request struct {
	// Status is the new status; "" keeps the finding's.
	Status vuln.Status
	// DismissalReason is required with the status dismissed (sent now or already stored) and refused otherwise.
	DismissalReason string
	HasReason       bool
	// Comment is at most 4,000 characters; an empty comment clears it.
	Comment    string
	HasComment bool
}

// Outcome is the result of one finding of a change.
type Outcome string

// The outcomes.
const (
	Changed   Outcome = "changed"
	Unchanged Outcome = "unchanged"
	Failed    Outcome = "failed"
)

// Result is what happened to one id.
type Result struct {
	ID      string
	Outcome Outcome
	// Status is the HTTP status of the id: 200 for changed and unchanged, 404 for an id the caller cannot read, 409 for a
	// refused transition.
	Status int
	// Code is the problem code of a failure.
	Code string
	// Err is the failure as the error of a single change (PATCH), nil for a success.
	Err error
	// Revision is the finding's revision after the change.
	Revision int64
}

// Validate checks the parts of a request that do not depend on the finding: the status is one a person sets (R-VULN-012:
// resolved is trove's, 422 status_not_settable), the comment's length, and a dismissal reason that is a reason.
func (r Request) Validate(requireStatus bool) error {
	if r.Status == "" {
		if requireStatus {
			return invalid("/status", "required", "The status is required.")
		}
	} else if err := vuln.CheckTransition(vuln.StatusConfirmed, r.Status); err != nil {
		// From a confirmed finding the rules refuse only a status that is no status and resolved, which is trove's to set.
		return err
	}
	if r.HasComment && utf8.RuneCountInString(r.Comment) > MaxComment {
		return invalid("/comment", "too_long", "The comment is at most 4,000 characters.")
	}
	if r.HasReason && r.DismissalReason != "" && !vuln.DismissalReason(r.DismissalReason).Valid() {
		return invalid("/dismissalReason", "invalid_value", "The dismissal reason is not_exploitable, false_positive, risk_accepted, mitigated or test_only.")
	}
	return nil
}

// ValidateBulk is [Request.Validate] for a change of many findings, which has no stored reason to fall back on: a dismissal
// names its reason in the request, and any other status takes none.
func (r Request) ValidateBulk() error {
	if err := r.Validate(true); err != nil {
		return err
	}
	switch {
	case r.Status == vuln.StatusDismissed && r.DismissalReason == "":
		return invalid("/dismissalReason", "required", "A dismissal needs a dismissal reason.")
	case r.Status != vuln.StatusDismissed && r.DismissalReason != "":
		return invalid("/dismissalReason", "not_allowed", "A dismissal reason goes with the status dismissed only.")
	}
	return nil
}

func invalid(pointer, code, detail string) error {
	return errs.E(errs.Invalid, errs.CodeValidation, "1 field is invalid.",
		errs.WithFields(errs.FieldError{Pointer: pointer, Code: code, Detail: detail}))
}

// Apply changes the findings of ids that the scope lets the caller read, in the transaction tx, and returns one result per
// id in the order of ids (an id that repeats is answered once, at its first position). expected is the revision the caller
// read for a single change, errs.AnyRevision for none; a stale one is the Err of the result. bulk is the identifier every
// event of a bulk change carries, "" for a single change.
func (s *Service) Apply(ctx context.Context, tx meta.Tx, a Actor, sc store.Scope, ids []string, req Request, expected int64, bulk string) ([]Result, error) {
	ids = dedupe(ids)
	locked, err := s.o.Store.Lock(ctx, tx, sc, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]store.Locked, len(locked))
	for _, l := range locked {
		byID[l.ID] = l
	}
	repos, err := s.o.Repositories.List(ctx)
	if err != nil {
		return nil, err
	}
	// The lock query names a finding's repository by its bare key (a stop-gap until WP3 returns the project
	// key with it), so the policy is looked up by the bare key.
	policy := map[string]vuln.Policy{}
	for _, r := range repos {
		policy[r.Name.Key] = r.VulnerabilityPolicy(s.o.Enabled, s.o.Enforcement)
	}
	out := make([]Result, 0, len(ids))
	for _, id := range ids {
		l, ok := byID[id]
		if !ok {
			out = append(out, Result{
				ID: id, Outcome: Failed, Status: http.StatusNotFound, Code: errs.CodeNotFound,
				Err: errs.E(errs.NotFound, errs.CodeNotFound, "The finding does not exist."),
			})
			continue
		}
		res, err := s.one(ctx, tx, a, l, policy[l.Repository], req, expected, bulk)
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, nil
}

func dedupe(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// target resolves the status and the dismissal reason a request leaves the finding with, or the 422 that says they do not fit.
func target(l store.Locked, req Request) (vuln.Status, string, error) {
	to := req.Status
	if to == "" {
		to = l.Status
	}
	reason := ""
	if to == vuln.StatusDismissed {
		reason = l.DismissalReason
		if l.Status != vuln.StatusDismissed {
			reason = ""
		}
		if req.HasReason {
			reason = req.DismissalReason
		}
		if reason == "" {
			return to, "", invalid("/dismissalReason", "required", "A dismissal needs a dismissal reason.")
		}
	} else if req.HasReason && req.DismissalReason != "" {
		return to, "", invalid("/dismissalReason", "not_allowed", "A dismissal reason goes with the status dismissed only.")
	}
	return to, reason, nil
}

func (s *Service) one(ctx context.Context, tx meta.Tx, a Actor, l store.Locked, p vuln.Policy, req Request, expected int64, bulk string) (Result, error) {
	fail := func(err error) (Result, error) {
		var e *errs.Error
		status, code := http.StatusConflict, errs.CodeConflict
		if errors.As(err, &e) {
			status, code = e.Status(), e.Code
		}
		return Result{ID: l.ID, Outcome: Failed, Status: status, Code: code, Err: err}, nil
	}
	if expected != errs.AnyRevision && l.Revision != expected {
		return fail(errs.Stale("The finding", l.Revision))
	}
	to, reason, err := target(l, req)
	if err != nil {
		return fail(err)
	}
	if err := vuln.CheckTransition(l.Status, to); err != nil {
		return fail(err)
	}
	commentChanged := req.HasComment && req.Comment != l.Comment
	if to == l.Status && reason == l.DismissalReason && !commentChanged {
		return Result{ID: l.ID, Outcome: Unchanged, Status: http.StatusOK, Revision: l.Revision}, nil
	}
	details := ""
	if bulk != "" {
		b, _ := json.Marshal(map[string]string{"bulk": bulk})
		details = string(b)
	}
	rev, err := s.o.Store.SetStatus(ctx, tx, store.Change{
		ID: l.ID, From: string(l.Status), To: string(to), DismissalReason: reason, Comment: req.Comment, HasComment: req.HasComment,
		Actor: a.ID, Expected: l.Revision, Details: details,
	})
	if errors.Is(err, store.ErrStale) {
		return fail(errs.Stale("The finding", l.Revision))
	}
	if err != nil {
		return Result{}, err
	}
	block := blockChange(p, l, to)
	if err := s.record(ctx, tx, a, l, to, reason, bulk, block); err != nil {
		return Result{}, err
	}
	return Result{ID: l.ID, Outcome: Changed, Status: http.StatusOK, Revision: rev}, nil
}

// BlockSet and BlockLifted are the values of `block` in the audit event.
const (
	BlockSet    = "set"
	BlockLifted = "lifted"
)

// blockChange says whether the change sets or lifts a download block under the repository's current policy: the block of the
// finding before and after, by the verdict the download applies (vuln.Verdict, R-VULN-015).
func blockChange(p vuln.Policy, l store.Locked, to vuln.Status) string {
	active := func(st vuln.Status) bool {
		return vuln.Verdict(p, vuln.BlockRow{FindingID: l.ID, AdvisoryID: l.Advisory, Kind: l.Kind, Status: st}, advisory.DataState{Complete: true}).Active()
	}
	before, after := active(l.Status), active(to)
	switch {
	case !before && after:
		return BlockSet
	case before && !after:
		return BlockLifted
	}
	return ""
}

// record writes the audit event of one change, in the transaction of the change (R-OPS-015). A block that is set carries the
// journal entry: a restore to a point before the change would bring the version back (R-OPS-014, R-HA-054); lifting a block
// widens access and is not journaled.
func (s *Service) record(ctx context.Context, tx meta.Tx, a Actor, l store.Locked, to vuln.Status, reason, bulk, block string) error {
	data := map[string]string{
		"finding": l.ID, "package": l.Package, "version": l.Version, "advisory": l.Advisory, "from": string(l.Status), "to": string(to),
	}
	if reason != "" {
		data["reason"] = reason
	}
	if bulk != "" {
		data["bulk"] = bulk
	}
	if block != "" {
		data["block"] = block
	}
	details, err := json.Marshal(data)
	if err != nil {
		return err
	}
	ev := audit.Event{
		Type: audit.EventVulnFindingStatusChanged, Actor: a.Audit, Outcome: audit.Success, Source: a.Source,
		Subject:    audit.Subject{Kind: "finding", ID: l.ID, Name: l.Package + "@" + l.Version},
		Repository: l.Repository, Details: details,
	}
	if block == BlockSet {
		ev.Journal, err = json.Marshal(map[string]string{
			"finding": l.ID, "repository": l.Repository, "ecosystem": l.Ecosystem, "package": l.Package, "version": l.Version,
			"advisory": l.Advisory, "block": BlockSet,
		})
		if err != nil {
			return err
		}
	}
	return s.o.Recorder.RecordTx(ctx, tx, ev)
}

// NewBulkID returns the identifier a bulk change's events carry.
func NewBulkID() string { return principal.NewID() }
