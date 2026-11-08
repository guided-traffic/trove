package formattest

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/repository"
)

// authzPaths is check 13 (R-AUTHZ-005, Q-AUTHZ-250): a protocol request is decided on the canonical path of what it
// serves or writes, as /files/ decides the same file, so that a grant's and a token scope's path patterns cannot be
// stepped around through the protocol. The check runs every content case and every change case on a fresh Host of the
// local class, every document of the Remote fixture on a Host of the remote class and every case of the Merge fixture on
// a Host of the virtual class, and reads the targets the format handed to Authorize, CheckOwnership and Claim:
//
//   - a target that names a package names a path: the decision point does not let a package without a path pass a
//     path set with any pattern, so a format that leaves the path out refuses every caller whose grant has one;
//   - the path is canonical and, for a format with package names, Classify places it in the package the target names;
//   - every file a route served was authorized for read at its own path, so a pattern that excludes the file refuses
//     it and not only a pattern that excludes its package document;
//   - a request whose canonical path Classify places in a package authorized that package at a path: a route that
//     authorizes the repository instead matches every grant on it, whatever its patterns say.
//
// A fixture without content and change cases fails, and so does one whose cases serve or change something while no
// target with a path was handed over; a format whose every case refuses by design (its routes listed in NoContent and
// NoRevision with their reasons, as a format skeleton has) has nothing to judge yet.
func (r *run) authzPaths() {
	if len(r.f.Routes()) == 0 {
		r.failf("%s: the format has no routes and the check is not skipped (Skip[%d] needs a reason)", CheckAuthzPaths, CheckAuthzPaths)
		return
	}
	if len(r.fx.Content) == 0 && len(r.fx.Changes) == 0 {
		r.failf("%s: the fixture has no content case and no change case, and the check is not skipped", CheckAuthzPaths)
		return
	}
	judged, acted := 0, 0
	for _, c := range r.fx.Content {
		h := r.host("")
		if c.Seed != nil {
			if err := c.Seed(h); err != nil {
				r.failf("content case %q: the seed fails: %v", c.Name, err)
				continue
			}
		}
		method := c.Method
		if method == "" {
			method = http.MethodGet
		}
		// A case no route takes has no handler to judge: checks 1 and 7 report it.
		j, served, _ := r.judgeRequest(fmt.Sprintf("content case %q", c.Name), h, method, c.Path, c.Body)
		judged += j
		if served {
			acted++
		}
	}
	// The remote and the virtual class: the routes of a document an upstream serves and of a merged document decide on
	// the same coordinate as the local class. Their Hosts have no engine behind the documents, so a request may end in
	// an error after its decision; the decision is judged whatever the answer. A fixture request no route takes fails:
	// check 13 would judge nothing of the class (a format whose protocol paths are not its canonical paths names the
	// protocol path in Request).
	if r.fx.Remote != nil && r.f.Remote() != nil {
		for _, d := range r.fx.Remote.Docs {
			what := fmt.Sprintf("remote document %q", d.Name)
			j, _, routed := r.judgeRequest(what, r.host(formats.Remote), http.MethodGet, orPath(d.Request, d.Path), nil)
			judged += j
			if !routed {
				r.failf("%s: %q matches no route of the format, so check 13 judges nothing of a remote repository: give the document the protocol path it is requested at (RemoteDoc.Request)", what, orPath(d.Request, d.Path))
			}
		}
	}
	if r.fx.Merge != nil && r.f.Virtual() != nil {
		for _, c := range r.fx.Merge.Cases {
			what := fmt.Sprintf("merge case %q", c.Name)
			j, _, routed := r.judgeRequest(what, r.host(formats.Virtual), http.MethodGet, orPath(c.Request, c.Path), nil)
			judged += j
			if !routed {
				r.failf("%s: %q matches no route of the format, so check 13 judges nothing of a virtual repository: give the case the protocol path it is requested at (MergeCase.Request)", what, orPath(c.Request, c.Path))
			}
		}
	}
	for _, c := range r.fx.Changes {
		if c.Do == nil {
			continue // check 9 reports a change without a Do
		}
		h := r.host("")
		if c.Seed != nil {
			if err := c.Seed(h); err != nil {
				r.failf("change %q: the seed fails: %v", c.Name, err)
				continue
			}
		}
		h.forgetTargets()
		if err := c.Do(context.Background(), h); err != nil {
			continue // check 9 reports a change that fails
		}
		acted++
		judged += r.judgeTargets(fmt.Sprintf("change %q", c.Name), h)
	}
	if judged == 0 && acted > 0 {
		r.failf("%s: content cases served or change cases ran, and none handed the decision point a target with a path, so the check judged nothing", CheckAuthzPaths)
	}
}

// judgeRequest serves one request on h with the targets of its seed forgotten and judges what the format handed the
// decision point; served is true for an answer below 400, routed false for a request no route takes. The rules of the
// served file and of the package apply to a served answer and to every answer that asked the decision point anything:
// a route whose cases all refuse after a decision (listed in NoContent) is held to them as well, so a refusal by design
// is no way around the check.
func (r *run) judgeRequest(what string, h *Host, method, path string, body []byte) (judged int, served, routed bool) {
	h.forgetTargets()
	rq, err := h.NewRequest(context.Background(), method, path, bytes.NewReader(body))
	if err != nil {
		return 0, false, false
	}
	resp := h.ServeRequest(rq, false)
	what += fmt.Sprintf(" (route %q)", rq.Route.Name)
	judged = r.judgeTargets(what, h)
	served = resp.Code < http.StatusBadRequest
	if served || len(h.Authorizations()) > 0 {
		r.servedAuthorized(what, h)
		r.packageAuthorized(what, rq, h)
	}
	return judged, served, true
}

// orPath is the request path of a fixture case: its protocol path, else its canonical path.
func orPath(request, path string) string {
	if request != "" {
		return request
	}
	return path
}

// recordedTarget is one target a format handed the Host, with the call that took it.
type recordedTarget struct {
	call string
	t    formats.Target
}

// targetsOf returns what the Host recorded of Authorize, CheckOwnership and Claim since forgetTargets.
func targetsOf(h *Host) []recordedTarget {
	var out []recordedTarget
	for _, a := range h.Authorizations() {
		out = append(out, recordedTarget{"Authorize", a.Target})
	}
	for _, c := range h.OwnershipChecks() {
		out = append(out, recordedTarget{"CheckOwnership", c.Target})
	}
	for _, c := range h.Claims() {
		out = append(out, recordedTarget{"Claim", c.Target})
	}
	return out
}

// judgeTargets applies the first two rules of check 13 to every recorded target and returns how many targets with a
// path it judged. A repository-level target (no package, no path) is the coarse form of a search or a service index
// and has nothing to judge.
func (r *run) judgeTargets(what string, h *Host) (judged int) {
	for _, x := range targetsOf(h) {
		t := x.t
		switch {
		case t.Package == "" && t.Path == "":
			continue
		case t.Path == "":
			r.failf("%s: %s names the package %q and no path, so a grant's path patterns cannot decide it (R-AUTHZ-005): "+
				"the target names the canonical path of what the operation serves or writes", what, x.call, t.Package)
			continue
		}
		// The path is decoded once by CleanPath; a literal percent sign is escaped first, so that only a path that is
		// not canonical (a leading or trailing slash, an empty, dot or dot-dot segment) comes back different.
		clean, err := repository.CleanPath(strings.ReplaceAll(t.Path, "%", "%25"))
		if err != nil || clean.Folder || clean.Path != t.Path {
			r.failf("%s: %s names the path %q, which is no canonical repository path", what, x.call, t.Path)
			continue
		}
		if r.d.PackageNames && t.Package != "" {
			if got := r.f.Classify(clean).Package; got != t.Package {
				r.failf("%s: %s names the package %q at the path %q, which Classify places in the package %q: the path is one of the package's",
					what, x.call, t.Package, t.Path, got)
				continue
			}
		}
		judged++
	}
	return judged
}

// servedAuthorized is the third rule: every file the request served was authorized for read at its own path.
func (r *run) servedAuthorized(what string, h *Host) {
	calls := h.Authorizations()
	for _, p := range h.ServedPaths() {
		if !slices.ContainsFunc(calls, func(a AuthorizeCall) bool {
			return a.Target.Path == p && slices.Contains(a.Target.Actions, formats.ActionRead)
		}) {
			r.failf("%s served the file %q and authorized no read of that path, so a path pattern that excludes the file would not refuse it (R-AUTHZ-005)", what, p)
		}
	}
}

// packageAuthorized is the fourth rule: a request whose canonical path Classify places in a package authorized that
// package at a path. A route that addresses no canonical path (Fixture.Canonical answers false) has none to place.
func (r *run) packageAuthorized(what string, rq *formats.Request, h *Host) {
	if !r.d.PackageNames {
		return
	}
	canon := rq.Path
	if r.fx.Canonical != nil {
		p, ok := r.fx.Canonical(rq.Escaped)
		if !ok {
			return
		}
		c, err := repository.CleanPath(p)
		if err != nil {
			return // check 3 reports a canonical path that is not clean
		}
		canon = c
	}
	pkg := r.f.Classify(canon).Package
	if pkg == "" {
		return
	}
	if !slices.ContainsFunc(h.Authorizations(), func(a AuthorizeCall) bool { return a.Target.Package == pkg && a.Target.Path != "" }) {
		r.failf("%s answers for the package %q and authorized no target that names the package at a path, "+
			"so neither a grant's package patterns nor its path patterns decided it (R-AUTHZ-005)", what, pkg)
	}
}
