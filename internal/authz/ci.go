package authz

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// CI tokens in the evaluator (R-AUTHZ-014, R-CI-012, R-CI-013, R-CI-015), the seam of Q-CI-17, built by work
// package W5 of docs/developer/ci-oidc.md.
//
// What a CI token carries is [claims.Frozen], read by the token validator from api_token.ci_context into the
// token cache entry (internal/auth/token.Info.CI) and handed down as Subject.CI. The evaluator never queries
// PostgreSQL for it (R-AUTHZ-015): the bundle is the subject's, the grants of policies and groups are the
// index's. The bundle is per contributing mapping (Q-CI-17 as amended): a token unites the match of the system
// tier with the matches of project tiers (R-CI-009, Q-CI-26). The rights of the subject are
//
//	⋃ over the contributions m of (policy grants of m.Ref ∪ group grants of m.Groups ∪ m.Inline) ∩ m.Caps
//
// then the token scope as the ceiling it is for every token (R-AUTHZ-013). Only the contributions that still count
// are in the union (ciContributes, Q-CI-47): not one after its own ExpiresAt, and not one whose mapping an index at
// least as fresh as the bundle's AuthzEpoch no longer holds as enabled with an active principal. Further,
//
//   - the policy grants of m are those of the policies whose entries name the mapping principal
//     "ci:<issuer>/<mapping>" (Index.ci resolves the name); for a project tier only the project's own policies
//     count (Q-CI-26);
//   - the group grants of m are those of the groups m lists, resolved by name through the index (Index.groups),
//     because the stage resolves no group membership for a mapping principal; a project tier lists no groups
//     (Q-CI-26), and a bundle that names some for one is not trusted with them;
//   - the inline grants of m are matched like the elements of a token scope (one matcher for policies, scopes
//     and CI grants, R-AUTHZ-014); for a project tier only against the repositories of m.Project, so `@any`
//     means "every repository of the project"; they name the project's repositories by bare key (R-AUTHZ-004),
//     and a repository is not moved out of the project while one of them names it (R-REPO-060);
//   - the cap `read` of m removes every other action of m's policy, group and inline grants and of no other
//     contribution's (R-CI-012).
//
// A CI token never holds manage, never a system role (rolesOf) and never a project administration
// (mayAdminister); it has no user identity and no auto-join group (R-AUTHZ-002). A CI subject without a bundle
// holds nothing: the validator refuses such a token, so the case is a defect and fails closed.

// CIProject is the project identity of a CI token: the owner identity "ci:<issuer>:project:<id>" of
// package ownership (R-CI-015, R-AUTHZ-018). Issuer is the issuer's name, IssuerID its immutable ID;
// the ownership store keeps the ID, the API shows the name. ProjectPath is the display value.
type CIProject struct {
	Issuer      string
	IssuerID    string
	ProjectID   string
	ProjectPath string
}

// Ref is the reference of the owner identity, "ci:<issuer>:project:<project_id>".
func (p CIProject) Ref() string { return "ci:" + p.Issuer + ":project:" + p.ProjectID }

// CIProject returns the project identity of a CI subject; ok is false for every other subject and
// for a CI token without a project claim.
func (s Subject) CIProject() (CIProject, bool) {
	if s.CI == nil || s.CI.ProjectID == "" || s.CI.IssuerID == "" {
		return CIProject{}, false
	}
	return CIProject{Issuer: s.CI.Issuer, IssuerID: s.CI.IssuerID, ProjectID: s.CI.ProjectID, ProjectPath: s.CI.ProjectPath}, true
}

// ciContribution is one contributing mapping of a CI subject resolved against the index of the request.
type ciContribution struct {
	m claims.FrozenMapping
	// name is the mapping principal's name, "<issuer>/<mapping>"; pid its ID in the index, "" when no policy
	// entry names it (then it holds no policy grant).
	name, pid string
	// groups are the IDs of the listed groups that a policy entry names, groupNames their names.
	groups, groupNames []string
	// project is the trove project ID of a project tier, "" for the system tier; in is the context
	// its inline grants are read in: the project of the tier, whose repositories they name by bare key,
	// or the instance (R-AUTHZ-004, Q-AUTHZ-216).
	project string
	in      qname.Context
	// readOnly is the cap read (R-CI-012).
	readOnly bool
}

// CIGrantSet is the grants of one CI subject resolved against the index of a request: the contributions of
// its bundle with their principals and their bounds. It is built once per decision from the token's own data
// and the index, with no query.
type CIGrantSet struct {
	contributions []ciContribution
}

// activeCIMappings is the set of the active CI mappings of a snapshot (Q-CI-47).
func activeCIMappings(ids []string) map[string]bool {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// ciContributes reports whether a contribution of the bundle still gives its grants (Q-CI-47): not after its own
// expiry (claims.FrozenMapping.ExpiresAt, a project tier's maxTtlSeconds), and not once its mapping is no longer
// active, which an index at least as fresh as the token's exchange decides. Every write that creates, enables,
// disables or deletes a mapping notifies the area authz in its transaction, and the exchange read the epoch after
// the issuer row FOR SHARE, so an index at the bundle's epoch or later holds every contributing mapping that is
// still enabled with an active principal. An older index cannot know a mapping created just before the exchange:
// the token's issuance is the newer evidence, and the contribution stays until the index catches up, within the
// bound of R-AUTHZ-028. Disabling or deleting a project mapping thus ends its contribution to the tokens other
// tiers own within that bound, without revoking them (R-CI-017).
func (x *Index) ciContributes(f *claims.Frozen, m claims.FrozenMapping, now time.Time) bool {
	if m.ExpiredAt(now) {
		return false
	}
	return x.label.AuthzEpoch < f.AuthzEpoch || x.ciActive[m.ID]
}

// CIGrants resolves the bundle of a CI subject against the index. A subject without Subject.CI yields an empty
// set that allows nothing. A contribution that ended or whose mapping is no longer active is left out
// (ciContributes).
func (x *Index) CIGrants(sub Subject) CIGrantSet { return x.ciGrantsAt(sub, time.Now()) }

func (x *Index) ciGrantsAt(sub Subject, now time.Time) CIGrantSet {
	if kindOf(sub) != kindCI || sub.CI == nil {
		return CIGrantSet{}
	}
	var out CIGrantSet
	for _, m := range sub.CI.Contributions {
		if !x.ciContributes(sub.CI, m, now) {
			continue
		}
		name := strings.TrimPrefix(m.Ref, "ci:")
		c := ciContribution{m: m, name: name, pid: x.ci[name], readOnly: m.HasCap(claims.CapRead)}
		if m.Project != nil {
			c.project, c.in = m.Project.ID, projectContext(m.Project.Key)
		} else {
			for _, g := range m.Groups {
				if id := x.groups[g]; id != "" {
					c.groups, c.groupNames = append(c.groups, id), append(c.groupNames, g)
				}
			}
		}
		out.contributions = append(out.contributions, c)
	}
	return out
}

// ciInlineSource names an inline grant in the sources of a decision (R-AUTHZ-029): "ci-inline:<issuer>/<mapping>#<n>",
// the counterpart of the explanation's {"kind": "ci-inline", "ordinal": n} (R-AUTHZ-014).
func ciInlineSource(name string, ordinal int) string {
	return "ci-inline:" + name + "#" + strconv.Itoa(ordinal)
}

// admits reports whether the contribution may give the action at all: never manage (R-CI-013), and only read
// under the cap read (R-CI-012). The bound of a project tier to its repositories is inlineFor's.
func (c *ciContribution) admits(a Action) bool {
	return a != Manage && (!c.readOnly || a == Read)
}

// ciGrantedBy is `granted` of R-AUTHZ-015 for a CI subject: the sources of every grant of every contribution that
// gives the action on the coordinate.
func (e *Engine) ciGrantedBy(g CIGrantSet, idx *Index, a Action, r *repoInfo, repoLevel bool, pkg, path string) []string {
	var out []string
	for i := range g.contributions {
		c := &g.contributions[i]
		if !c.admits(a) {
			continue
		}
		out = append(out, c.principalGrants(idx, a, r, repoLevel, pkg, path, nil)...)
		for n, ig := range c.inlineFor(r) {
			if e.inlineAllows(c.in, ig, a, r, repoLevel, pkg, path) {
				out = append(out, ciInlineSource(c.name, n))
			}
		}
	}
	return out
}

// principalGrants lists the policies of the mapping principal and of its groups that give the action; claimer,
// when set, is told the principal of each.
func (c *ciContribution) principalGrants(idx *Index, a Action, r *repoInfo, repoLevel bool, pkg, path string, claimer func(principalID string)) []string {
	var out []string
	ids := append([]string{c.pid}, c.groups...)
	for _, id := range ids {
		if id == "" {
			continue
		}
		for _, gr := range idx.grantsFor(r.ID, id) {
			if c.project != "" && gr.project != c.project {
				continue // a project tier is granted by its project's policies only (Q-CI-26)
			}
			if gr.actions.Has(a) && gr.res.matches(repoLevel, pkg, path) {
				out = append(out, gr.policy)
				if claimer != nil {
					claimer(id)
				}
			}
		}
	}
	return out
}

// inlineFor returns the inline grants of the contribution that may cover the repository: all of them for the
// system tier, none for a repository outside the project of a project tier (R-AUTHZ-026). The index is the
// ordinal of the grant in the bundle.
func (c *ciContribution) inlineFor(r *repoInfo) []claims.InlineGrant {
	if c.project != "" && r.projectID != c.project {
		return nil
	}
	return c.m.Inline
}

// inlineAllows matches one inline grant like an element of a token scope (R-AUTHZ-013, R-AUTHZ-014): the action,
// the repository selectors, the package and path patterns with the name rule of the repository's format. A
// pattern that does not compile matches nothing. On a virtual repository only read has an effect, as for
// policy grants.
func (e *Engine) inlineAllows(in qname.Context, g claims.InlineGrant, a Action, r *repoInfo, repoLevel bool, pkg, path string) bool {
	if !slices.Contains(g.Actions, claims.Action(a)) {
		return false
	}
	if r.Class == repository.Virtual && a != Read {
		return false
	}
	if !e.compile.selectorsMatch(g.Repositories, in, r.repoView) {
		return false
	}
	var pInc, pExc, fInc, fExc []string
	if g.Packages != nil {
		pInc, pExc = g.Packages.Include, g.Packages.Exclude
	}
	if g.Paths != nil {
		fInc, fExc = g.Paths.Include, g.Paths.Exclude
	}
	pkgs, err := e.compile.set(dimPackage, pInc, pExc, r.Format, e.namesOf(r.Format))
	if err != nil {
		return false
	}
	paths, err := e.compile.set(dimPath, fInc, fExc, "", nil)
	if err != nil {
		return false
	}
	return resourceSet{packages: pkgs, paths: paths}.matches(repoLevel, pkg, path)
}

// ciReadShape is readShape's grant part for a CI subject: whether any grant gives read on the repository, and
// whether one of them admits every package of it.
func (e *Engine) ciReadShape(g CIGrantSet, idx *Index, r *repoInfo) (anyRead, unrestricted bool) {
	for i := range g.contributions {
		c := &g.contributions[i]
		for _, id := range append([]string{c.pid}, c.groups...) {
			if id == "" {
				continue
			}
			for _, gr := range idx.grantsFor(r.ID, id) {
				if gr.actions.Has(Read) && (c.project == "" || gr.project == c.project) {
					anyRead = true
					unrestricted = unrestricted || gr.res.unrestrictedPackages()
				}
			}
		}
		for _, ig := range c.inlineFor(r) {
			if !slices.Contains(ig.Actions, claims.Read) || !e.compile.selectorsMatch(ig.Repositories, c.in, r.repoView) {
				continue
			}
			anyRead = true
			if (ig.Packages == nil || len(ig.Packages.Include)+len(ig.Packages.Exclude) == 0) && (ig.Paths == nil || len(ig.Paths.Include) == 0) {
				unrestricted = true
			}
		}
	}
	return anyRead, unrestricted
}

// ciWrite is what the ownership check needs to know of a CI subject's write on the index (R-CI-015,
// R-AUTHZ-018): whether an inline grant with ownership bind_project allows one of the coordinate's write actions
// (then ownership is checked whatever the repository's packageOwnership says), and the owners a claim by a
// publish records under rule 2: the project identity only when a bind_project grant allows deploy, otherwise
// the principals of every grant that allows deploy (a mapping's policy entry or its inline grant with
// ownership none makes the mapping the owner, a listed group's grant the group).
type ciWrite struct {
	binds       bool
	bindsDeploy bool
	// principals are the principal IDs of the grants that allow deploy; mappings the names (<issuer>/<mapping>)
	// of the mappings whose inline grant with ownership none allows it and whose principal the index does not
	// know (the claim resolves them in its transaction).
	principals []string
	mappings   []string
}

// ciWriteOf derives ciWrite for the write target r and the coordinate.
func (e *Engine) ciWriteOf(g CIGrantSet, idx *Index, r *repoInfo, c Coordinate) ciWrite {
	var w ciWrite
	repoLevel := c.Package == "" && c.Path == ""
	writes := c.Actions
	if len(writes) == 0 {
		writes = []Action{Deploy}
	}
	for i := range g.contributions {
		cb := &g.contributions[i]
		for _, ig := range cb.inlineFor(r) {
			if ig.ResolvedOwnership() != claims.OwnershipBindProject {
				continue
			}
			for _, a := range writes {
				if a != Read && cb.admits(a) && e.inlineAllows(cb.in, ig, a, r, repoLevel, c.Package, c.Path) {
					w.binds = true
				}
			}
			if cb.admits(Deploy) && e.inlineAllows(cb.in, ig, Deploy, r, repoLevel, c.Package, c.Path) {
				w.bindsDeploy = true
			}
		}
	}
	if w.bindsDeploy {
		return w
	}
	for i := range g.contributions {
		cb := &g.contributions[i]
		if !cb.admits(Deploy) {
			continue
		}
		cb.principalGrants(idx, Deploy, r, repoLevel, c.Package, c.Path, func(id string) {
			if !slices.Contains(w.principals, id) {
				w.principals = append(w.principals, id)
			}
		})
		for _, ig := range cb.inlineFor(r) {
			if ig.ResolvedOwnership() != claims.OwnershipNone || !e.inlineAllows(cb.in, ig, Deploy, r, repoLevel, c.Package, c.Path) {
				continue
			}
			switch {
			case cb.pid != "":
				if !slices.Contains(w.principals, cb.pid) {
					w.principals = append(w.principals, cb.pid)
				}
			case !slices.Contains(w.mappings, cb.name):
				w.mappings = append(w.mappings, cb.name)
			}
		}
	}
	return w
}

// ciIdentities are the owner identities of a CI subject other than its project (R-AUTHZ-018 rule 1, R-CI-015):
// the principal IDs the index knows for its mappings and listed groups, and the references by name, which
// match an owner row whose principal the index does not know (a mapping or a group that no policy names).
func (g CIGrantSet) ciIdentities() (ids []string, refs []string) {
	for _, c := range g.contributions {
		if c.pid != "" {
			ids = append(ids, c.pid)
		}
		ids = append(ids, c.groups...)
		refs = append(refs, "ci:"+c.name)
		if c.project == "" {
			for _, name := range c.m.Groups {
				refs = append(refs, "group:"+name)
			}
		}
	}
	return ids, refs
}
