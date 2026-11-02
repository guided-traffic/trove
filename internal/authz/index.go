package authz

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/repository"
)

// Label identifies the state of the database an index was built from: the epoch
// and changed_at of the two change areas it depends on (R-AUTHZ-028). Two
// labels are equal only if both pairs are equal, so an epoch number that later
// writes reused after a failover that lost commits is still told apart.
type Label struct {
	AuthzEpoch int64
	AuthzAt    time.Time
	RepoEpoch  int64
	RepoAt     time.Time
}

// Equal compares both pairs, instants included.
func (l Label) Equal(o Label) bool {
	return l.AuthzEpoch == o.AuthzEpoch && l.AuthzAt.Equal(o.AuthzAt) &&
		l.RepoEpoch == o.RepoEpoch && l.RepoAt.Equal(o.RepoAt)
}

// Snapshot is every input of the grant index as read in one transaction
// (R-AUTHZ-028 "Consistent build").
type Snapshot struct {
	Label Label
	// Started is the local time at which the build began; it is the instant at
	// which the index counts as confirmed (R-AUTHZ-028).
	Started      time.Time
	Policies     []q.AuthzBuildPoliciesRow
	Selectors    []q.AuthzBuildSelectorsRow
	Patterns     []q.AuthzBuildPatternsRow
	Entries      []q.AuthzBuildEntriesRow
	Bindings     []q.AuthzBuildBindingsRow
	Repositories []q.AuthzBuildRepositoriesRow
	// Projects and ProjectAdmins are the rows of project and project_admin in the
	// same snapshot (R-AUTHZ-026); the entries of project policies are in Entries.
	Projects      []q.AuthzBuildProjectsRow
	ProjectAdmins []q.AuthzBuildProjectAdminsRow
	// Exposure are the endpoint exposure rules of the active repositories
	// (endpoints.md R-EP-012), read in the same snapshot.
	Exposure []q.EndpointsExposureBuildRow
	// >>> M2 virtual

	// VirtualMembers and DeployTargets are the rows of virtual_members and
	// virtual_deploy_targets in the same snapshot: the members and the deploy
	// target of a virtual repository are rows, not part of its document
	// (R-REPO-017, Q-REPO-104).
	VirtualMembers []q.AuthzBuildVirtualMembersRow
	DeployTargets  []q.AuthzBuildDeployTargetsRow

	// <<< M2 virtual
}

// repoInfo is a repository as the index knows it.
type repoInfo struct {
	repoView
	// requireAuth: anonymous requests are challenged whatever the grants say
	// (R-AUTHZ-003).
	requireAuth bool
	// hide is the per-repository hideExistence: "inherit", "true" or "false".
	hide string
	// members and deployTarget are the keys of a virtual repository's members and
	// of its deploy target (R-AUTHZ-009).
	members      []string
	deployTarget string
	// cacheFill is the cacheFillAction of a remote repository.
	cacheFill string
	// ownership is the packageOwnership of a local repository, "" when the
	// document has none (R-AUTHZ-018).
	ownership string
	// projectID and project are the ID and the key of the project the repository
	// is assigned to, empty for none (R-AUTHZ-026).
	projectID string
	project   string
}

// projectInfo is a project as the index knows it: its switches and the IDs of
// its administrators, users and groups (Q-AUTHZ-183).
type projectInfo struct {
	id, key                            string
	managePolicies, manageRepositories bool
	admins                             map[string]bool
}

// grant is one entry of a policy seen from the evaluator: the actions the
// principal holds on the repositories the policy's selectors cover, narrowed to
// the policy's package and path patterns (R-AUTHZ-015).
type grant struct {
	policy string
	// project is the ID of the policy's project, "" for a global policy: a CI mapping of a project tier is
	// granted by its project's policies only (Q-CI-26).
	project string
	actions ActionSet
	res     resourceSet
}

// Index is the compiled grant index of one replica (R-AUTHZ-028). It is
// immutable once built: requests read it without locks and a new one replaces it
// atomically.
type Index struct {
	label   Label
	started time.Time
	// repos by key.
	repos map[string]*repoInfo
	// grants maps repository ID, then principal ID, to the compiled grants.
	grants map[int64]map[string][]grant
	// bindings maps a principal ID to the system roles bound to it.
	bindings map[string]roleSet
	// ci maps the name of a CI identity mapping principal to its ID.
	ci map[string]string
	// groups maps the name of a group that a policy entry names to its ID: a CI token's mappings list
	// groups by name, and the stage resolves no membership for a mapping principal (R-AUTHZ-014). A
	// group no entry names holds no grant, so it needs no entry here.
	groups map[string]string
	// exposure maps an endpoint name to its compiled exposure rules (R-EP-013).
	exposure map[string]*endpointExposure
	// projects maps a project key to the project (R-AUTHZ-026).
	projects map[string]*projectInfo
	// policies and entries count what went in, for metrics and diagnostics.
	policies int
}

// Label returns the label of the index.
func (x *Index) Label() Label { return x.label }

// Epoch returns the authorization epoch the index was built at.
func (x *Index) Epoch() int64 { return x.label.AuthzEpoch }

func parseRepoConfig(raw []byte) (c struct {
	RequireAuthentication bool   `json:"requireAuthentication"`
	HideExistence         string `json:"hideExistence"`
	Remote                struct {
		CacheFillAction string `json:"cacheFillAction"`
	} `json:"remote"`
	Local struct {
		PackageOwnership string `json:"packageOwnership"`
	} `json:"local"`
}, ok bool,
) {
	if len(raw) == 0 {
		return c, true
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, false
	}
	return c, true
}

type patternKey struct{ dim, mode string }

// builder holds the lookups of one BuildIndex run.
type builder struct {
	x    *Index
	log  *slog.Logger
	reg  *catalog.Registry
	byID map[int64]*repoInfo
	list []*repoInfo
	// selectors, patterns and entries are the snapshot's rows by policy ID.
	selectors map[string][]q.AuthzBuildSelectorsRow
	patterns  map[string]map[patternKey][]string
	entries   map[string][]q.AuthzBuildEntriesRow
	// matchCache remembers which repositories a pattern or class selector covers,
	// so policies that repeat a selector pay for it once. The key names the project
	// the match is restricted to, so a global selector and the same selector of a
	// project policy never share a result (R-AUTHZ-026).
	matchCache map[string][]*repoInfo
	// projectByID maps a project ID to its entry, for the policies that name it.
	projectByID map[string]*projectInfo
}

// BuildIndex compiles a snapshot. It is a pure function: the same snapshot gives
// the same index. A policy that cannot be compiled (it passed validation when
// written, so this signals a corrupt row) is skipped and logged: skipping only
// removes rights, it never adds any.
func BuildIndex(s *Snapshot, reg *catalog.Registry, log *slog.Logger) *Index {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	b := &builder{
		x: &Index{
			label: s.Label, started: s.Started,
			repos:    make(map[string]*repoInfo, len(s.Repositories)),
			grants:   map[int64]map[string][]grant{},
			bindings: map[string]roleSet{},
			ci:       map[string]string{},
			groups:   map[string]string{},
			projects: map[string]*projectInfo{},
		},
		log: log, reg: catalog.Or(reg), byID: make(map[int64]*repoInfo, len(s.Repositories)),
		selectors:  map[string][]q.AuthzBuildSelectorsRow{},
		patterns:   map[string]map[patternKey][]string{},
		entries:    map[string][]q.AuthzBuildEntriesRow{},
		matchCache: map[string][]*repoInfo{}, projectByID: map[string]*projectInfo{},
	}
	b.addProjects(s.Projects, s.ProjectAdmins)
	b.addRepositories(s.Repositories, s.VirtualMembers, s.DeployTargets)
	b.x.exposure = buildExposure(s.Exposure, b.byID, b.reg, log)
	for _, r := range s.Bindings {
		if r.PrincipalProject != "" {
			// A project service account takes no system role (Q-AUTHZ-175); the write
			// refuses it, so a row like this came in some other way. Fail closed.
			log.Error("authz: role binding skipped, its principal is a project service account", "role", r.Role, "principal", r.PrincipalID)
			continue
		}
		b.x.bindings[r.PrincipalID] |= Role(r.Role).bit()
	}
	for _, r := range s.Selectors {
		b.selectors[r.PolicyID] = append(b.selectors[r.PolicyID], r)
	}
	for _, r := range s.Patterns {
		m := b.patterns[r.PolicyID]
		if m == nil {
			m = map[patternKey][]string{}
			b.patterns[r.PolicyID] = m
		}
		k := patternKey{r.Dimension, r.Mode}
		m[k] = append(m[k], r.Pattern)
	}
	for _, r := range s.Entries {
		b.entries[r.PolicyID] = append(b.entries[r.PolicyID], r)
		switch r.PrincipalKind {
		case dbCIMapping:
			b.x.ci[r.PrincipalName] = r.PrincipalID
		case dbGroup:
			b.x.groups[r.PrincipalName] = r.PrincipalID
		}
	}
	for _, p := range s.Policies {
		if p.ResourceType != "repository" {
			continue // build policies are P1 (R-AUTHZ-027)
		}
		if p.ProjectID != "" && b.projectByID[p.ProjectID] == nil {
			continue // a project the snapshot does not hold covers nothing
		}
		b.addPolicy(p)
	}
	return b.x
}

// addProjects reads the projects and their administrators. An administrator is
// kept by principal ID whatever its kind; the table admits users and groups only
// (Q-AUTHZ-183), and the questions of project.go refuse a subject that is neither.
func (b *builder) addProjects(rows []q.AuthzBuildProjectsRow, admins []q.AuthzBuildProjectAdminsRow) {
	for _, r := range rows {
		pi := &projectInfo{id: r.ID, key: r.Key, managePolicies: r.ManagePolicies, manageRepositories: r.ManageRepositories, admins: map[string]bool{}}
		b.x.projects[r.Key] = pi
		b.projectByID[r.ID] = pi
	}
	for _, a := range admins {
		if pi := b.projectByID[a.ProjectID]; pi != nil {
			pi.admins[a.PrincipalID] = true
		}
	}
}

func (b *builder) addRepositories(rows []q.AuthzBuildRepositoriesRow, members []q.AuthzBuildVirtualMembersRow, targets []q.AuthzBuildDeployTargetsRow) {
	memberKeys := map[int64][]string{}
	for _, m := range members {
		memberKeys[m.VirtualID] = append(memberKeys[m.VirtualID], m.MemberKey)
	}
	targetKey := map[int64]string{}
	for _, t := range targets {
		targetKey[t.VirtualID] = t.TargetKey
	}
	for _, r := range rows {
		cfg, ok := parseRepoConfig(r.Config)
		info := &repoInfo{
			repoView:    repoView{ID: r.ID, Key: r.Key, Class: repository.Class(r.Class), Format: r.Format},
			requireAuth: cfg.RequireAuthentication || !ok, // an unreadable document fails closed
			hide:        cfg.HideExistence, members: memberKeys[r.ID], deployTarget: targetKey[r.ID],
			cacheFill: cfg.Remote.CacheFillAction, ownership: cfg.Local.PackageOwnership,
		}
		if pi := b.projectByID[r.ProjectID]; pi != nil {
			info.projectID, info.project = pi.id, pi.key
		}
		if info.hide == "" {
			info.hide = "inherit"
		}
		if !ok {
			info.ownership = OwnershipEnforce // fails closed like requireAuth
		}
		b.x.repos[r.Key] = info
		b.byID[r.ID] = info
		b.list = append(b.list, info)
	}
}

// covered lists the repositories a selector covers. A project policy's selector
// covers only repositories of that project (project is its ID, "" for a global
// policy): a key selector whose repository is not in the project covers nothing
// (Q-AUTHZ-179), a pattern or class selector is matched against the project's
// repositories only.
func (b *builder) covered(sel selector, project string) []*repoInfo {
	if sel.kind == selKey {
		if r := b.byID[sel.repoID]; r != nil && (project == "" || r.projectID == project) {
			return []*repoInfo{r}
		}
		return nil
	}
	k := project + "\x00" + sel.text(nil)
	if got, ok := b.matchCache[k]; ok {
		return got
	}
	var out []*repoInfo
	for _, r := range b.list {
		if project != "" && r.projectID != project {
			continue
		}
		if sel.matches(r.repoView) {
			out = append(out, r)
		}
	}
	b.matchCache[k] = out
	return out
}

// policyRepos resolves the selectors of a policy and lists the repositories they
// cover, each once. It returns nil for an inert policy or one with a selector
// that does not compile.
func (b *builder) policyRepos(p q.AuthzBuildPoliciesRow) []*repoInfo {
	var sels []selector
	for _, r := range b.selectors[p.ID] {
		sel, err := selectorOfRow(r)
		if err != nil {
			b.log.Error("authz: policy skipped, its selector does not compile", "policy", p.Name, "err", err)
			return nil
		}
		sels = append(sels, sel)
	}
	seen := map[int64]bool{}
	var repos []*repoInfo
	for _, sel := range sels {
		for _, r := range b.covered(sel, p.ProjectID) {
			if !seen[r.ID] {
				seen[r.ID] = true
				repos = append(repos, r)
			}
		}
	}
	return repos
}

// resourcesFor compiles the pattern lists of a policy for the repositories of
// one format's package-name comparison (nil names: no folding).
func (b *builder) resourcesFor(p q.AuthzBuildPoliciesRow, names catalog.Names) (resourceSet, error) {
	pk := b.patterns[p.ID]
	pkg, err := compileSet(dimPackage, pk[patternKey{"package", "include"}], pk[patternKey{"package", "exclude"}], names)
	if err != nil {
		return resourceSet{}, err
	}
	path, err := compileSet(dimPath, pk[patternKey{"path", "include"}], pk[patternKey{"path", "exclude"}], nil)
	if err != nil {
		return resourceSet{}, err
	}
	return resourceSet{packages: pkg, paths: path}, nil
}

// addPolicy expands one policy into the grants of the repositories it covers.
func (b *builder) addPolicy(p q.AuthzBuildPoliciesRow) {
	repos := b.policyRepos(p)
	if len(repos) == 0 {
		return // an inert policy matches nothing
	}
	// The patterns are compiled once per name rule: the repositories of formats
	// without package names share one set, every other format has its own.
	byRule := map[string]*resourceSet{}
	for _, r := range repos {
		names := namesIn(b.reg, r.Format)
		key := ""
		if names != nil {
			key = r.Format
		}
		if byRule[key] == nil {
			rs, err := b.resourcesFor(p, names)
			if err != nil {
				b.log.Error("authz: policy skipped, its patterns do not compile", "policy", p.Name, "err", err)
				return
			}
			byRule[key] = &rs
		}
		for _, e := range b.entries[p.ID] {
			if e.PrincipalProject != "" && e.PrincipalProject != p.ProjectID {
				// A project service account is granted by the policies of its project
				// only (Q-AUTHZ-175), whatever the stored entry says.
				b.log.Error("authz: entry skipped, its principal is a service account of another project", "policy", p.Name, "principal", e.PrincipalName)
				continue
			}
			if e.PrincipalAutoJoin && p.ProjectID != "" {
				// An auto-join group is every user: in a project policy it would stand in
				// for authenticated, which a project policy never names (Q-AUTHZ-200).
				b.log.Error("authz: entry skipped, a project policy names an auto-join group", "policy", p.Name, "principal", e.PrincipalName)
				continue
			}
			acts, err := actionsFromStored(e.Actions)
			if err != nil {
				b.log.Error("authz: entry skipped", "policy", p.Name, "err", err)
				continue
			}
			if r.Class == repository.Virtual {
				acts &= virtualActions
			}
			if acts == 0 {
				continue
			}
			m := b.x.grants[r.ID]
			if m == nil {
				m = map[string][]grant{}
				b.x.grants[r.ID] = m
			}
			m[e.PrincipalID] = append(m[e.PrincipalID], grant{policy: p.Name, project: p.ProjectID, actions: acts, res: *byRule[key]})
		}
	}
	b.x.policies++
}

func selectorOfRow(r q.AuthzBuildSelectorsRow) (selector, error) {
	switch r.Kind {
	case selKey:
		return selector{kind: selKey, repoID: r.RepositoryID}, nil
	case selPattern:
		sel, err := parseSelector(r.KeyPattern + formatSuffix(r.Format))
		if err != nil {
			return selector{}, err
		}
		return sel, nil
	case selClass:
		if r.RepoClass == "" {
			return selector{}, fmt.Errorf("class selector without a class")
		}
		return selector{kind: selClass, class: r.RepoClass, format: r.Format}, nil
	}
	return selector{}, fmt.Errorf("unknown selector kind %q", r.Kind)
}

func formatSuffix(f string) string {
	if f == "" {
		return ""
	}
	return ":" + f
}

// repo returns the repository of a key, nil if the index does not know it.
func (x *Index) repo(key string) *repoInfo { return x.repos[key] }

// grantsFor lists the grants of a principal on a repository.
func (x *Index) grantsFor(repoID int64, principalID string) []grant {
	return x.grants[repoID][principalID]
}

// RepositoryKeys lists the keys the index knows, sorted.
func (x *Index) RepositoryKeys() []string {
	out := make([]string, 0, len(x.repos))
	for k := range x.repos {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// principalIDs computes the principal set of a request (R-AUTHZ-002) as
// principal-table IDs.
func (x *Index) principalIDs(sub Subject) []string {
	if sub.Anonymous {
		return []string{AnonymousID}
	}
	out := make([]string, 0, len(sub.Groups)+3)
	add := func(id string) {
		if id != "" {
			out = append(out, id)
		}
	}
	switch kindOf(sub) {
	case kindServiceAccount:
		add(sub.ID)
	case kindCI:
		for _, m := range sub.Mappings {
			add(x.ci[strings.TrimPrefix(m, "ci:")])
		}
	default:
		add(sub.ID)
	}
	for _, g := range sub.Groups {
		add(g.ID)
	}
	if kindOf(sub) == kindUser {
		out = append(out, AuthenticatedID)
	}
	return out
}
