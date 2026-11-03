package repository

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The project side of the repository writes (R-AUTHZ-026, Q-AUTHZ-173, Q-AUTHZ-178,
// Q-AUTHZ-184). The assignment of an existing repository belongs to the project
// service; here a repository is assigned only by the transaction that creates it,
// and a project administrator's write is narrowed to its project.

// Write carries what the management API knows about a write beyond the document.
// The zero value is the write of a holder of repositories:write-config that names
// no project.
type Write struct {
	// Project is the "project" member of the body: nil when the body has none, the
	// key otherwise ("" for null). On a create it assigns the repository to the
	// project in the transaction that creates it; on a replace or a patch it must
	// equal the stored project, which it never changes (Q-AUTHZ-184).
	Project *string
	// Delegate is the key of the project the writer administers, set when it does
	// not hold repositories:write-config and acts as a project administrator; empty
	// for a holder. On a create the key then starts with Delegate+"-" and the
	// project is Delegate; on an update the repository must belong to it; a
	// virtual repository's new members and its deploy target must be repositories of
	// it (Q-AUTHZ-178). The handler has checked the right and the switch.
	Delegate string
	// MayWeakenVulnerabilities is whether the writer may weaken the member `vulnerabilities` (R-VULN-002): it holds
	// vulnerabilities:triage (a holder of repositories:write-config is no delegate and is never refused). The handler
	// sets it; it matters only while Delegate is set.
	MayWeakenVulnerabilities bool
}

// projectKey is the key the body names, "" for none.
func (w Write) projectKey() string {
	if w.Project == nil {
		return ""
	}
	return *w.Project
}

// resolveProject locks the project a create assigns the repository to and returns
// its id ("" for none). The violations a request can have are added to v: an
// unknown project (422 unknown_project) and, for a project administrator, a key
// without the project's prefix (422 project_prefix_required).
func (s *Service) resolveProject(ctx context.Context, qs *q.Queries, v *validation, d Document, w Write) (string, error) {
	project := w.projectKey()
	if w.Delegate != "" && project != w.Delegate {
		return "", errs.E(errs.Forbidden, errs.CodeForbidden, "A project administrator creates a repository in the project it names.")
	}
	if project == "" {
		return "", nil
	}
	rows, err := qs.RepositoryProjectShare(ctx, project)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		v.add("/project", errs.CodeUnknownProject, "no project has the key "+strconv.Quote(project))
		return "", nil
	}
	// The key and the hyphen alone are no name in the project's space, as for a project
	// service account (Q-AUTH-193).
	if w.Delegate != "" && (!strings.HasPrefix(d.Key, project+"-") || len(d.Key) == len(project)+1) {
		v.add("/key", errs.CodeProjectPrefixRequired, "the key of a repository in the project "+project+" starts with "+project+"- and goes on after it")
	}
	return rows[0].ID, nil
}

// checkProjectOfUpdate resolves the stored project of a locked row for an update and
// applies what the write says about it: a delegate acts on its project's repositories
// only (404 for another, the repository having moved since the handler decided), and a
// project member of the body must equal the stored one (422 immutable). It returns the
// stored project's key.
func checkProjectOfUpdate(ctx context.Context, qs *q.Queries, row q.Repository, w Write) (string, error) {
	project, err := projectKeyOf(ctx, qs, row)
	if err != nil {
		return "", err
	}
	if w.Delegate != "" && project != w.Delegate {
		return "", notFound(row.Key)
	}
	return project, nil
}

// refuseProjectChange adds the violation of a body that names another project than
// the stored one.
func refuseProjectChange(v *validation, w Write, stored string) {
	if w.Project != nil && *w.Project != stored {
		v.add("/project", codeImmutable, "the project of a repository is set when it is created; the project API moves it")
	}
}

// projectKeyOf resolves the project of a row to its key, "" for none.
func projectKeyOf(ctx context.Context, qs *q.Queries, row q.Repository) (string, error) {
	if !row.ProjectID.Valid {
		return "", nil
	}
	keys, err := projectKeys(ctx, qs)
	if err != nil {
		return "", err
	}
	return keys[row.ProjectID.String()], nil
}

// projectKeys maps the id of every project to its key.
func projectKeys(ctx context.Context, qs *q.Queries) (map[string]string, error) {
	rows, err := qs.RepositoryProjectKeys(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.ID] = r.Key
	}
	return out, nil
}

// virtualGuard is the rule of Q-AUTHZ-178 for one write: what a project
// administrator may add to a virtual repository.
type virtualGuard struct {
	project string
	// members and target are what the repository holds now; a member or a target
	// that stays is not new, whatever project it belongs to.
	members []string
	target  string
}

// newVirtualGuard builds the guard of a write, nil for a holder of
// repositories:write-config. cur is the stored relation of an update, nil on a create.
func newVirtualGuard(w Write, cur *virtualRel) *virtualGuard {
	if w.Delegate == "" {
		return nil
	}
	g := &virtualGuard{project: w.Delegate}
	if cur != nil {
		g.members = keysOf(cur.members)
		if cur.target != nil {
			g.target = cur.target.key
		}
	}
	return g
}

// outside returns the keys among keys that are not repositories of the project. An
// unknown key is outside as well, so the answer is no oracle on the repositories the
// writer cannot see.
func (g *virtualGuard) outside(ctx context.Context, qs *q.Queries, keys []string) (map[string]bool, error) {
	if g == nil || len(keys) == 0 {
		return nil, nil
	}
	rows, err := qs.RepositoryProjectsByKeys(ctx, keys)
	if err != nil {
		return nil, err
	}
	in := make(map[string]bool, len(rows))
	for _, r := range rows {
		in[r.Key] = r.Project == g.project
	}
	out := map[string]bool{}
	for _, k := range keys {
		if !in[k] {
			out[k] = true
		}
	}
	return out, nil
}

// checkTarget refuses a deploy target that the writer picks and that is outside the
// project. A new member outside the project is reported with the members; here the
// target is one of the members an administrator of trove placed.
func (g *virtualGuard) checkTarget(v *validation, target *string, outside map[string]bool) {
	if g == nil || target == nil || !outside[*target] || !g.isNewTarget(*target) || g.isNewMember(*target) {
		return
	}
	v.add("/virtual/deployTarget", errs.CodeRepositoryOutsideProject, outsideDetail)
}

// isNewMember reports whether the member is not one the repository holds already.
func (g *virtualGuard) isNewMember(key string) bool { return !slices.Contains(g.members, key) }

// isNewTarget reports whether the deploy target differs from the stored one.
func (g *virtualGuard) isNewTarget(key string) bool { return key != g.target }

const outsideDetail = "a virtual repository of a project holds repositories of the project; this is not one of them"
