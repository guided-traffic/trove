package deb

import (
	"context"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

// memberActive reports a member that takes part in the virtual repository: not disabled and not being deleted. A format may not import the
// repository package, so the lifecycle is compared by the value the catalog gives an active repository.
func memberActive(disabled bool, lifecycle string) bool { return !disabled && lifecycle == "active" }

// virtualGate is the gate of R-DEB-027: the release documents, the index files, the by-hash files and signing-key.asc of a virtual
// repository need read without package restriction on every member, because the merged index is one for every caller (Q-DEB-19). A
// caller who lacks it on a member gets 403 deb_members_unreadable; an anonymous caller is asked to authenticate (401, R-AUTHZ-009 rule 6).
// It runs after the read on the virtual repository itself.
func (f *Format) virtualGate(ctx context.Context, rq *formats.Request, path string) error {
	for _, m := range rq.Repo.Members() {
		if !memberActive(m.Disabled, string(m.Lifecycle)) {
			continue
		}
		mem := m
		mrq, err := rq.Member(&mem)
		if err != nil {
			return err
		}
		err = rq.Host.Authorize(ctx, mrq, formats.Target{Path: path, Actions: []formats.Action{formats.ActionRead}})
		if err == nil {
			continue
		}
		if e, ok := errs.As(err); ok && (e.Kind == errs.Forbidden || e.Kind == errs.NotFound) {
			return errs.E(errs.Forbidden, errs.CodeDebMembersUnreadable,
				"The index of this repository merges its members, so reading it needs read without package restriction on every member.")
		}
		return err
	}
	return nil
}
