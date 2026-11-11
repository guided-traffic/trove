package authz

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/qname"

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
	// Projects and ProjectMembers are the rows of project and project_member in the
	// same snapshot (R-AUTHZ-026); the entries of project policies are in Entries.
	Projects       []q.AuthzBuildProjectsRow
	ProjectMembers []q.AuthzBuildProjectMembersRow
	// Exposure are the endpoint exposure rules of the active repositories
	// (endpoints.md R-EP-012), read in the same snapshot.
	Exposure []q.EndpointsExposureBuildRow
	// CIMappings are the IDs of the CI identity mappings that are enabled with an
	// active principal (Q-CI-47, ci.go): a CI token keeps a contribution only while
	// its mapping is one of them.
	CIMappings []string
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
	// artifacts and wiki are the visibility levels of the two parts of the repository (R-AUTHZ-033, R-AUTHZ-034); a stored
	// value that is no level reads as private, the most restrictive.
	artifacts, wiki Level
	// hide is the per-repository hideExistence: "inherit", "true" or "false".
	hide string
	// members and deployTarget are the keys of a virtual repository's members and
	// of its deploy target (R-AUTHZ-009).
	members      []qname.Repo
	deployTarget qname.Repo
	// cacheFill is the cacheFillAction of a remote repository.
	cacheFill string
	// ownership is the packageOwnership of a local repository, "" when the
	// document has none (R-AUTHZ-018).
	ownership string
	// proj is the project the repository belongs to (Q-AUTHZ-206); projectID and project
	// are its ID and key. Every repository has one; a row whose project the snapshot does
	// not hold (which the foreign key rules out) keeps proj nil and takes no role.
	proj      *projectInfo
	projectID string
	project   string
}

// projectInfo is a project as the index knows it: its members by principal ID, users and
// groups, with their role (Q-AUTHZ-207).
type projectInfo struct {
	id, key string
	// level is the project's visibility level (R-AUTHZ-033); a repository's levels lie at most at it.
	level   Level
	members map[string]ProjectRole
	// sources are the sources a role names in a decision, "role:<projectKey>:<role>" per role
	// (R-AUTHZ-015, R-AUTHZ-029), built once so that a decision allocates none.
	sources [ProjectRoleOwner + 1]string
}

// source is the decision source of a role in the project.
func (p *projectInfo) source(r ProjectRole) string {
	if !r.Valid() {
		return ""
	}
	return p.sources[r]
}

// membership is one membership row as the index keeps it by principal: the project and the
// role (Q-AUTHZ-207).
type membership struct {
	p    *projectInfo
	role ProjectRole
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
	// repos by qualified name.
	repos map[qname.Repo]*repoInfo
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
	// ciTexts maps the ID of a CI mapping principal or of a group to the policy entries naming it, in the text of
	// the policy: the exchange answers a CI token's policy and group grants as `<action>:<selector>` items with the
	// selectors as the policy writes them (R-CI-007, ci_scope.go).
	ciTexts map[string][]textGrant
	// ciActive holds the IDs of the active CI mappings (Snapshot.CIMappings, Q-CI-47).
	ciActive map[string]bool
	// exposure maps an endpoint name to its compiled exposure rules (R-EP-013).
	exposure map[string]*endpointExposure
	// projects maps a project key to the project (R-AUTHZ-026).
	projects map[string]*projectInfo
	// memberships maps the principal ID of a user or a group to its memberships, so that the roles
	// of a subject are derived from its user ID and group IDs alone, whatever the number of projects
	// (R-AUTHZ-015 performance target, R-ARCH-033).
	memberships map[string][]membership
	// policies and entries count what went in, for metrics and diagnostics.
	policies int
	// publicContent: the artifacts of some repository are public (PublicContent).
	publicContent bool
}

// PublicContent reports whether the artifacts of some repository are `public`: the fact behind `publicContent` of
// login-options and `anonymousReads` of a Bastion's handshake (R-AUTH-037, R-BAS-019). The default endpoint's ceiling
// (R-EP-010) is the caller's to apply.
func (x *Index) PublicContent() bool { return x.publicContent }

// Levels reports the visibility levels of a repository's artifacts and wiki; ok is false for a repository the index does not
// know. The Bastion's shape and the handlers' warnings read it (R-BAS-012, R-REPO-004).
func (x *Index) Levels(key qname.Repo) (artifacts, wiki Level, ok bool) {
	r := x.repos[key]
	if r == nil {
		return LevelPrivate, LevelPrivate, false
	}
	return r.artifacts, r.wiki, true
}

// Label returns the label of the index.
func (x *Index) Label() Label { return x.label }

// Epoch returns the authorization epoch the index was built at.
func (x *Index) Epoch() int64 { return x.label.AuthzEpoch }

func parseRepoConfig(raw []byte) (c struct {
	HideExistence string `json:"hideExistence"`
	Remote        struct {
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
			repos:       make(map[qname.Repo]*repoInfo, len(s.Repositories)),
			grants:      map[int64]map[string][]grant{},
			bindings:    map[string]roleSet{},
			ci:          map[string]string{},
			groups:      map[string]string{},
			ciTexts:     map[string][]textGrant{},
			projects:    map[string]*projectInfo{},
			memberships: map[string][]membership{},
		},
		log: log, reg: catalog.Or(reg), byID: make(map[int64]*repoInfo, len(s.Repositories)),
		selectors:  map[string][]q.AuthzBuildSelectorsRow{},
		patterns:   map[string]map[patternKey][]string{},
		entries:    map[string][]q.AuthzBuildEntriesRow{},
		matchCache: map[string][]*repoInfo{}, projectByID: map[string]*projectInfo{},
	}
	b.addProjects(s.Projects, s.ProjectMembers)
	b.addRepositories(s.Repositories, s.VirtualMembers, s.DeployTargets)
	b.x.ciActive = activeCIMappings(s.CIMappings)
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

// addProjects reads the projects and their members. A member is kept by principal ID
// whatever its kind; the table admits users and groups only (Q-AUTHZ-183), and the
// questions of project.go refuse a subject that is neither. A role that is none of the
// four is skipped (fail closed).
func (b *builder) addProjects(rows []q.AuthzBuildProjectsRow, members []q.AuthzBuildProjectMembersRow) {
	for _, r := range rows {
		level, _ := ParseLevel(r.Visibility)
		pi := &projectInfo{id: r.ID, key: r.Key, level: level, members: map[string]ProjectRole{}}
		for role := ProjectRoleReader; role <= ProjectRoleOwner; role++ {
			pi.sources[role] = "role:" + r.Key + ":" + role.String()
		}
		b.x.projects[r.Key] = pi
		b.projectByID[r.ID] = pi
	}
	for _, m := range members {
		pi := b.projectByID[m.ProjectID]
		role, ok := ParseProjectRole(m.Role)
		if pi == nil || !ok || m.PrincipalID == "" {
			continue
		}
		if m.PrincipalKind != dbUser && m.PrincipalKind != dbGroup {
			// The table admits users and groups only (a trigger of 00019); a row of another kind
			// came in some other way and grants nothing (fail closed, Q-AUTHZ-215).
			b.log.Error("authz: membership skipped, its principal is neither a user nor a group", "project", pi.key, "principal", m.PrincipalID)
			continue
		}
		if m.PrincipalAutoJoin {
			// Every user is in an auto-join group, so its role would reach every user
			// (R-AUTHZ-026, Q-AUTHZ-200); the member write
			// refuses one, so this group became auto-join later. Fail closed.
			b.log.Error("authz: membership skipped, its group is an auto-join group", "project", pi.key, "principal", m.PrincipalID)
			continue
		}
		if _, dup := pi.members[m.PrincipalID]; dup {
			continue // the primary key holds one row per principal and project
		}
		pi.members[m.PrincipalID] = role
		b.x.memberships[m.PrincipalID] = append(b.x.memberships[m.PrincipalID], membership{p: pi, role: role})
	}
}

func (b *builder) addRepositories(rows []q.AuthzBuildRepositoriesRow, members []q.AuthzBuildVirtualMembersRow, targets []q.AuthzBuildDeployTargetsRow) {
	memberKeys := map[int64][]string{} // bare keys: a virtual repository holds repositories of its own project
	for _, m := range members {
		memberKeys[m.VirtualID] = append(memberKeys[m.VirtualID], m.MemberKey)
	}
	targetKey := map[int64]string{}
	for _, t := range targets {
		targetKey[t.VirtualID] = t.TargetKey
	}
	for _, r := range rows {
		cfg, ok := parseRepoConfig(r.Config)
		project := r.ProjectKey
		var members []qname.Repo
		for _, k := range memberKeys[r.ID] {
			members = append(members, qname.Repo{Project: project, Key: k})
		}
		var target qname.Repo
		if k := targetKey[r.ID]; k != "" {
			target = qname.Repo{Project: project, Key: k}
		}
		artifacts, _ := ParseLevel(r.VisibilityArtifacts)
		wiki, _ := ParseLevel(r.VisibilityWiki)
		wiki = min(wiki, artifacts) // the database holds the order; a state that breaks it never reads wider than the artifacts
		info := &repoInfo{
			repoView:  repoView{ID: r.ID, Name: qname.Repo{Project: r.ProjectKey, Key: r.Key}, Class: repository.Class(r.Class), Format: r.Format},
			artifacts: artifacts, wiki: wiki,
			hide: cfg.HideExistence, members: members, deployTarget: target,
			cacheFill: cfg.Remote.CacheFillAction, ownership: cfg.Local.PackageOwnership,
		}
		if artifacts == LevelPublic {
			b.x.publicContent = true
		}
		if pi := b.projectByID[r.ProjectID]; pi != nil {
			info.proj, info.projectID, info.project = pi, pi.id, pi.key
		}
		if info.hide == "" {
			info.hide = "inherit"
		}
		if !ok {
			info.ownership = OwnershipEnforce // an unreadable document fails closed
		}
		b.x.repos[info.Name] = info
		b.byID[r.ID] = info
		b.list = append(b.list, info)
	}
}

// covered lists the repositories a selector covers. A project policy's selector
// covers only repositories of that project (project is its ID, "" for a global
// policy): a key selector whose repository is not in the project covers nothing
// (a move refuses to take a repository out while one names it, R-REPO-060), a
// pattern or class selector is matched against the project's repositories only.
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
		sel, err := selectorOfRow(r, b.contextOf(p.ProjectID))
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
// one format: its package-name comparison and the case fold of its paths (nil: no folding, Q-AUTHZ-254).
func (b *builder) resourcesFor(p q.AuthzBuildPoliciesRow, names, pathFold catalog.Names) (resourceSet, error) {
	pk := b.patterns[p.ID]
	pkg, err := compileSet(dimPackage, pk[patternKey{"package", "include"}], pk[patternKey{"package", "exclude"}], names)
	if err != nil {
		return resourceSet{}, err
	}
	path, err := compileSet(dimPath, pk[patternKey{"path", "include"}], pk[patternKey{"path", "exclude"}], pathFold)
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
	// effective collects, per CI mapping principal and group, the actions its entries give on some covered
	// repository, for ciTexts.
	effective := map[string]ActionSet{}
	for _, r := range repos {
		names, pathFold := namesIn(b.reg, r.Format), pathFoldIn(b.reg, r.Format)
		key := ""
		if names != nil || pathFold != nil {
			key = r.Format
		}
		if byRule[key] == nil {
			rs, err := b.resourcesFor(p, names, pathFold)
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
				// An auto-join group is every user: a project policy never names one (Q-AUTHZ-200).
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
			if e.PrincipalKind == dbCIMapping || e.PrincipalKind == dbGroup {
				effective[e.PrincipalID] |= acts
			}
		}
	}
	b.addTexts(p, effective)
	b.x.policies++
}

// addTexts records the policy entries of CI mapping principals and groups in the policy's text (ciTexts): the
// selectors as written, the actions the entry has on at least one covered repository.
func (b *builder) addTexts(p q.AuthzBuildPoliciesRow, effective map[string]ActionSet) {
	if len(effective) == 0 {
		return
	}
	nameOf := func(id int64) qname.Repo {
		if info := b.byID[id]; info != nil {
			return info.Name
		}
		return qname.Repo{}
	}
	var sels []string
	for _, r := range b.selectors[p.ID] {
		sel, err := selectorOfRow(r, b.contextOf(p.ProjectID))
		if err != nil {
			return
		}
		// The exchange answers its items in the grammar of its scope, an instance context
		// (R-CI-007): a project policy's selector gains its project, and its class, which an
		// instance context cannot spell, is named by the repositories it covers on this index.
		if t, ok := sel.instanceText(nameOf); ok {
			sels = append(sels, t)
			continue
		}
		if sel.kind == selClass {
			for _, info := range b.covered(sel, p.ProjectID) {
				sels = append(sels, info.Name.String())
			}
		}
	}
	for _, e := range b.entries[p.ID] {
		if acts := effective[e.PrincipalID]; acts != 0 {
			b.x.ciTexts[e.PrincipalID] = append(b.x.ciTexts[e.PrincipalID], textGrant{project: p.ProjectID, selectors: sels, actions: acts})
			delete(effective, e.PrincipalID)
		}
	}
}

// contextOf is the context the selectors of a policy are read in: the instance for a global policy, the
// project of a project policy (projectID is its ID).
func (b *builder) contextOf(projectID string) qname.Context {
	if pi := b.projectByID[projectID]; pi != nil {
		return projectContext(pi.key)
	}
	return instanceContext
}

// selectorOfRow reads a stored selector in the context of its policy.
func selectorOfRow(r q.AuthzBuildSelectorsRow, in qname.Context) (selector, error) {
	switch r.Kind {
	case selKey:
		return selector{kind: selKey, repoID: r.RepositoryID, project: in.Project, inProject: !in.Instance()}, nil
	case selPattern:
		sel, err := parseSelector(r.KeyPattern+formatSuffix(r.Format), in)
		if err != nil {
			return selector{}, err
		}
		return sel, nil
	case selClass:
		if r.RepoClass == "" {
			return selector{}, fmt.Errorf("class selector without a class")
		}
		return selector{kind: selClass, class: r.RepoClass, format: r.Format, project: in.Project, inProject: !in.Instance()}, nil
	}
	return selector{}, fmt.Errorf("unknown selector kind %q", r.Kind)
}

func formatSuffix(f string) string {
	if f == "" {
		return ""
	}
	return ":" + f
}

// repo returns the repository of a name, nil if the index does not know it.
func (x *Index) repo(name qname.Repo) *repoInfo { return x.repos[name] }

// grantsFor lists the grants of a principal on a repository.
func (x *Index) grantsFor(repoID int64, principalID string) []grant {
	return x.grants[repoID][principalID]
}

// RepositoryNames lists the names the index knows, sorted by project key and key.
func (x *Index) RepositoryNames() []qname.Repo {
	out := make([]qname.Repo, 0, len(x.repos))
	for k := range x.repos {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// principalIDs computes the principal set of a request (R-AUTHZ-002) as
// principal-table IDs.
func (x *Index) principalIDs(sub Subject) []string {
	if sub.Anonymous {
		return []string{AnonymousID}
	}
	out := make([]string, 0, len(sub.Groups)+2)
	add := func(id string) {
		if id != "" {
			out = append(out, id)
		}
	}
	switch kindOf(sub) {
	case kindBastion:
		return nil // a Bastion registration is no member of anything (R-BAS-004)
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
	return out
}
