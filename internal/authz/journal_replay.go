package authz

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Replay of the authorization events of the revocation journal (R-HA-054, R-AUTHZ-031). A replay removes or narrows and never
// grants: a role binding is removed, a policy is deleted or narrowed to its journaled after-state when that grants nothing the
// restored policy does not, an owner is removed, a repository's access settings move to the more restrictive value. Every
// entry names its objects by ID; an entry that cannot be read, that names no object of the restored database or whose
// comparator cannot tell is skipped, reported unknown or suspended, never applied.
//
// The comparators answer true only when they can prove that the after-state grants nothing the current state does not. They
// compare IDs and patterns as stored, exactly: no glob containment, no case folding, no defaults guessed. A pattern list
// that is spelled differently is therefore never narrowing, which makes a replay suspend more, never grant more.
func init() {
	journal.Register(journal.Handler{
		Domain: "authz",
		Events: []string{
			EventRoleUnbound, EventPolicyDeleted, EventPolicyUpdated,
			EventOwnershipRemoved, EventOwnershipReleased, EventSettingsChanged,
		},
		Apply: replayAuthz,
	})
}

func replayAuthz(ctx context.Context, tx meta.Tx, env journal.Env, o journal.Object) (journal.Result, error) {
	qs, err := queriesOf(tx)
	if err != nil {
		return journal.Result{}, err
	}
	switch o.Event {
	case EventRoleUnbound:
		return replayUnbound(ctx, qs, env, tx, o)
	case EventPolicyDeleted:
		return replayPolicyDeleted(ctx, qs, env, tx, o)
	case EventPolicyUpdated:
		return replayPolicyUpdated(ctx, qs, env, tx, o)
	case EventSettingsChanged:
		return replaySettings(ctx, qs, env, tx, o)
	default:
		return replayOwnerRemoved(ctx, qs, o)
	}
}

// HasAdminBinding reports whether the database holds an admin role binding, for the line the replay prints when none is left
// (R-HA-054).
func HasAdminBinding(ctx context.Context, tx meta.Tx) (bool, error) {
	qs, err := queriesOf(tx)
	if err != nil {
		return false, err
	}
	n, err := qs.JournalAdminBindingCount(ctx)
	return n > 0, err
}

// bindableRole reports whether the name is a system role a binding can hold.
func bindableRole(s string) bool {
	switch s {
	case "admin", "security-admin", "auditor", "repository-admin", "webhook-admin":
		return true
	}
	return false
}

func replayUnbound(ctx context.Context, qs *q.Queries, env journal.Env, tx meta.Tx, o journal.Object) (journal.Result, error) {
	var e struct {
		Role        string `json:"role"`
		PrincipalID string `json:"principalId"`
		Principal   string `json:"principal"`
	}
	if !o.Into(&e) || !journal.IsUUID(e.PrincipalID) || !bindableRole(e.Role) {
		return o.Malformed("role or principalId is invalid"), nil
	}
	subject := e.Role + "/" + e.PrincipalID
	// Whatever the restored database holds, the configuration of the restore must not bind this principal again: mark it
	// before the row count is known. A declared binding that was added and removed after the restore point is absent from
	// the snapshot and a delete finds nothing, but trove migrate would still bind it (Q-AUTHZ-236). The mark is by name,
	// from the entry and from the principal the ID names in this database.
	if err := suppress(ctx, qs, e.Role, e.PrincipalID, e.Principal); err != nil {
		return journal.Result{}, err
	}
	n, err := qs.AuthzDeleteBinding(ctx, q.AuthzDeleteBindingParams{Role: e.Role, PrincipalID: e.PrincipalID})
	if err != nil {
		return journal.Result{}, err
	}
	if n > 0 {
		return o.Result(journal.Applied, subject), env.Notify(tx, cluster.AreaAuthz, payloadVersion, changePayload{Type: "role_binding", ID: e.PrincipalID})
	}
	exists, err := qs.JournalRoleBindingExists(ctx, e.PrincipalID)
	if err != nil {
		return journal.Result{}, err
	}
	if !exists {
		return o.Result(journal.SubjectUnknown, subject), nil
	}
	return o.Result(journal.AlreadyInEffect, subject), nil
}

// suppress marks the principal of an unbinding, by every name it has: the one the entry gives and the one the database gives
// for the ID.
func suppress(ctx context.Context, qs *q.Queries, role, principalID, entryRef string) error {
	refs := map[string]bool{}
	if r, err := ParseRef(entryRef); err == nil && r.Kind != "" && r.Name != "" {
		refs[r.String()] = true
	}
	rows, err := qs.IdentityPrincipalByID(ctx, principalID)
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		if r, err := ParseRef(refOfRow(rows[0].Kind, rows[0].Name)); err == nil {
			refs[r.String()] = true
		}
	}
	for ref := range refs {
		if err := qs.JournalSuppressBinding(ctx, role+"|"+ref); err != nil {
			return err
		}
	}
	return nil
}

func replayPolicyDeleted(ctx context.Context, qs *q.Queries, env journal.Env, tx meta.Tx, o journal.Object) (journal.Result, error) {
	var e struct {
		PolicyID string `json:"policyId"`
	}
	if !o.Into(&e) || !journal.IsUUID(e.PolicyID) {
		return o.Malformed("policyId is not an ID"), nil
	}
	head, err := qs.JournalPolicyByID(ctx, e.PolicyID)
	if err != nil {
		return journal.Result{}, err
	}
	if len(head) == 0 {
		return o.Result(journal.SubjectUnknown, e.PolicyID), nil
	}
	if _, err := qs.JournalPolicyDelete(ctx, e.PolicyID); err != nil {
		return journal.Result{}, err
	}
	return o.Result(journal.Applied, head[0].Name), env.Notify(tx, cluster.AreaAuthz, payloadVersion, changePayload{Type: "policy", ID: e.PolicyID})
}

// policyState reads the access-bearing state of a stored policy in the shape the journal carries it.
func policyState(ctx context.Context, qs *q.Queries, head q.JournalPolicyByIDRow) (*journalState, error) {
	st := &journalState{
		ResourceType: head.ResourceType, Project: head.ProjectKey, Selectors: []journalSelector{}, Entries: []journalEntry{},
		Packages: PatternLists{Include: []string{}, Exclude: []string{}}, Paths: PatternLists{Include: []string{}, Exclude: []string{}},
	}
	sels, err := qs.AuthzPolicySelectors(ctx, head.ID)
	if err != nil {
		return nil, err
	}
	for _, r := range sels {
		st.Selectors = append(st.Selectors, journalSelector{Kind: r.Kind, RepositoryID: r.RepositoryID, Pattern: r.KeyPattern, Class: r.RepoClass, Format: r.Format})
	}
	pats, err := qs.AuthzPolicyPatterns(ctx, head.ID)
	if err != nil {
		return nil, err
	}
	for _, r := range pats {
		var l *PatternLists
		switch r.Dimension {
		case "package":
			l = &st.Packages
		case "path":
			l = &st.Paths
		default:
			continue
		}
		if r.Mode == "include" {
			l.Include = append(l.Include, r.Pattern)
		} else {
			l.Exclude = append(l.Exclude, r.Pattern)
		}
	}
	ents, err := qs.AuthzPolicyEntries(ctx, head.ID)
	if err != nil {
		return nil, err
	}
	for _, r := range ents {
		st.Entries = append(st.Entries, journalEntry{PrincipalID: r.PrincipalID, Actions: r.Actions})
	}
	return st, nil
}

func replayPolicyUpdated(ctx context.Context, qs *q.Queries, env journal.Env, tx meta.Tx, o journal.Object) (journal.Result, error) {
	var e struct {
		PolicyID string        `json:"policyId"`
		Before   *journalState `json:"before"`
		After    *journalState `json:"after"`
	}
	if !o.Into(&e) || !journal.IsUUID(e.PolicyID) || e.Before == nil || e.After == nil {
		return o.Malformed("policyId, before or after is missing"), nil
	}
	heads, err := qs.JournalPolicyByID(ctx, e.PolicyID)
	if err != nil {
		return journal.Result{}, err
	}
	if len(heads) == 0 {
		return o.Result(journal.SubjectUnknown, e.PolicyID), nil
	}
	head := heads[0]
	current, err := policyState(ctx, qs, head)
	if err != nil {
		return journal.Result{}, err
	}
	var cmp policyOrder
	if cmp.Narrows(current, e.After) && cmp.Narrows(e.After, current) {
		return o.Result(journal.AlreadyInEffect, head.Name), nil
	}
	touch := func() error {
		if err := qs.JournalPolicyTouch(ctx, head.ID); err != nil {
			return err
		}
		return env.Notify(tx, cluster.AreaAuthz, payloadVersion, changePayload{Type: "policy", ID: head.ID})
	}
	act := journal.UpdateActions{
		Apply: func() error {
			if err := qs.JournalPolicyClearSelectorsPatterns(ctx, head.ID); err != nil {
				return err
			}
			for i, s := range e.After.Selectors {
				err := qs.AuthzInsertSelector(ctx, q.AuthzInsertSelectorParams{
					PolicyID: head.ID, Ordinal: int16(i), Kind: s.Kind, RepositoryID: s.RepositoryID, //nolint:gosec // bounded by the stored selectors it is a subset of
					KeyPattern: s.Pattern, RepoClass: s.Class, Format: s.Format,
				})
				if err != nil {
					return err
				}
			}
			for _, l := range []struct {
				dim, mode string
				list      []string
			}{
				{"package", "include", e.After.Packages.Include},
				{"package", "exclude", e.After.Packages.Exclude},
				{"path", "include", e.After.Paths.Include},
				{"path", "exclude", e.After.Paths.Exclude},
			} {
				for _, p := range slices.Compact(slices.Sorted(slices.Values(l.list))) {
					if err := qs.AuthzInsertPattern(ctx, q.AuthzInsertPatternParams{PolicyID: head.ID, Dimension: l.dim, Mode: l.mode, Pattern: p}); err != nil {
						return err
					}
				}
			}
			keep := map[string][]string{}
			for _, en := range e.After.Entries {
				keep[en.PrincipalID] = en.Actions
			}
			for _, en := range current.Entries {
				acts, ok := keep[en.PrincipalID]
				if !ok {
					if err := qs.JournalPolicyEntryDelete(ctx, q.JournalPolicyEntryDeleteParams{PolicyID: head.ID, PrincipalID: en.PrincipalID}); err != nil {
						return err
					}
					continue
				}
				if err := qs.JournalPolicyEntrySetActions(ctx, q.JournalPolicyEntrySetActionsParams{PolicyID: head.ID, PrincipalID: en.PrincipalID, Actions: acts}); err != nil {
					return err
				}
			}
			return touch()
		},
		Suspend: func() (json.RawMessage, error) {
			if err := qs.JournalPolicyClearEntries(ctx, head.ID); err != nil {
				return nil, err
			}
			detail, err := json.Marshal(map[string]any{"policy": head.Name, "after": e.After})
			if err != nil {
				return nil, err
			}
			return detail, touch()
		},
	}
	out, detail, err := journal.ApplyUpdate[*journalState](cmp, current, e.Before, e.After, act)
	r := o.Result(out, head.Name)
	r.Detail = detail
	return r, err
}

// policyOrder is the policy comparator of R-AUTHZ-031: Narrows(current, after) is true only when after grants nothing that
// current does not. Selectors, patterns and principals are compared exactly as stored.
type policyOrder struct{}

var _ journal.Narrower[*journalState] = policyOrder{}

// Narrows implements journal.Narrower.
func (policyOrder) Narrows(current, after *journalState) bool {
	if current == nil || after == nil {
		return false
	}
	// 1. Same resource type and project.
	if current.ResourceType != after.ResourceType || current.Project != after.Project {
		return false
	}
	// 2. The selectors of after are a subset of those of current, as sets.
	have := map[string]bool{}
	for _, s := range current.Selectors {
		have[selectorKey(s)] = true
	}
	for _, s := range after.Selectors {
		if !have[selectorKey(s)] {
			return false
		}
	}
	// 3. Per dimension: after excludes everything current excludes, and an include list of current that restricts is kept
	// restricting and shrinks.
	for _, d := range [][2]PatternLists{{current.Packages, after.Packages}, {current.Paths, after.Paths}} {
		cur, aft := d[0], d[1]
		if !subset(cur.Exclude, aft.Exclude) {
			return false
		}
		if len(cur.Include) > 0 && (len(aft.Include) == 0 || !subset(aft.Include, cur.Include)) {
			return false
		}
	}
	// 4. Every entry of after is an entry of current for the same principal with at least its actions.
	byPrincipal := map[string][]journalEntry{}
	for _, e := range current.Entries {
		byPrincipal[e.PrincipalID] = append(byPrincipal[e.PrincipalID], e)
	}
	for _, e := range after.Entries {
		ok := false
		for _, c := range byPrincipal[e.PrincipalID] {
			if subset(e.Actions, c.Actions) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func selectorKey(s journalSelector) string {
	return fmt.Sprintf("%s|%d|%s|%s|%s", s.Kind, s.RepositoryID, s.Pattern, s.Class, s.Format)
}

// subset reports whether every element of a is in b.
func subset(a, b []string) bool {
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	return true
}

var domainPattern = regexp.MustCompile(`^(repo:[0-9]+|domain:.+)$`)

func replayOwnerRemoved(ctx context.Context, qs *q.Queries, o journal.Object) (journal.Result, error) {
	var e struct {
		Domain      string `json:"domain"`
		Format      string `json:"format"`
		Package     string `json:"package"`
		PrincipalID string `json:"principalId"`
		CIIssuerID  string `json:"ciIssuerId"`
		CIProjectID string `json:"ciProjectId"`
	}
	if !o.Into(&e) || !domainPattern.MatchString(e.Domain) || e.Format == "" || e.Package == "" {
		return o.Malformed("domain, format or package is invalid"), nil
	}
	ci := e.CIIssuerID != ""
	if ci && (!journal.IsUUID(e.CIIssuerID) || e.CIProjectID == "" || len(e.CIProjectID) > 64) || !ci && !journal.IsUUID(e.PrincipalID) {
		return o.Malformed("the owner is not named by ID"), nil
	}
	subject := e.Package
	claim, err := qs.JournalOwnership(ctx, q.JournalOwnershipParams{Domain: e.Domain, Format: e.Format, PackageName: e.Package})
	if err != nil {
		return journal.Result{}, err
	}
	if len(claim) == 0 {
		return o.Result(journal.SubjectUnknown, subject), nil
	}
	// The released state is never set by a replay: a released name without versions could be claimed by any deployer, so
	// a name left without owners stays claimed and needs manage for every mutating operation (R-AUTHZ-031).
	var n int64
	if ci {
		n, err = qs.JournalOwnerRemoveCIProject(ctx, q.JournalOwnerRemoveCIProjectParams{
			Domain: e.Domain, Format: e.Format, PackageName: e.Package, CiIssuerID: e.CIIssuerID, CiProjectID: e.CIProjectID,
		})
	} else {
		n, err = qs.JournalOwnerRemovePrincipal(ctx, q.JournalOwnerRemovePrincipalParams{
			Domain: e.Domain, Format: e.Format, PackageName: e.Package, PrincipalID: e.PrincipalID,
		})
	}
	if err != nil {
		return journal.Result{}, err
	}
	if n == 0 {
		return o.Result(journal.AlreadyInEffect, subject), nil
	}
	return o.Result(journal.Applied, subject), nil
}

// accessOrder lists the values of an access setting, most restrictive first (R-AUTHZ-031 settings comparator); nil for a
// setting the journal does not order.
func accessOrder(key string) []string {
	switch key {
	case "cacheFillAction":
		return []string{"deploy", "read"}
	case "packageOwnership":
		return []string{"enforce", "off"}
	case "requireAuthentication", "hideExistence":
		return []string{"true", "false"}
	}
	return nil
}

// repoSettings is the access-bearing part of a repository's configuration document as strings, "" for a member the document
// does not set.
type repoSettings map[string]string

// settingsOrder is the settings comparator. A value narrows when it comes no later in the order than the current one. A
// hideExistence of "inherit" depends on authz.hide_existence, which a replay does not read: as the current value it counts as
// the most restrictive, as the after-value as the least, so it never proves a narrowing. A setting that the current state does
// not set, or a value outside the order, is "cannot tell" and does not narrow.
type settingsOrder struct{}

var _ journal.Narrower[repoSettings] = settingsOrder{}

// Narrows implements journal.Narrower: current holds the settings of the repository, after the journaled ones.
func (settingsOrder) Narrows(current, after repoSettings) bool {
	if len(after) == 0 {
		return false
	}
	for k, av := range after {
		order := accessOrder(k)
		if order == nil {
			return false
		}
		cv := current[k]
		if k == "hideExistence" {
			if cv == "inherit" {
				cv = "true"
			}
			if av == "inherit" {
				av = "false"
			}
		}
		ai, ci := slices.Index(order, av), slices.Index(order, cv)
		if ai < 0 || ci < 0 || ai > ci {
			return false
		}
	}
	return true
}

// settingString renders a JSON value of a setting as the string the order uses; ok is false for a type that no setting has.
func settingString(raw json.RawMessage) (string, bool) {
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return strconv.FormatBool(b), true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	return "", false
}

// readSettings reads the settings of a repository document.
func readSettings(class string, doc map[string]json.RawMessage) repoSettings {
	out := repoSettings{}
	if v, ok := doc["requireAuthentication"]; ok {
		out["requireAuthentication"], _ = settingString(v)
	} else {
		out["requireAuthentication"] = "false"
	}
	out["hideExistence"] = "inherit"
	if v, ok := doc["hideExistence"]; ok {
		if s, ok := settingString(v); ok && s != "" {
			out["hideExistence"] = s
		}
	}
	if sub := objectOf(doc["local"]); sub != nil {
		if v, ok := sub["packageOwnership"]; ok {
			out["packageOwnership"], _ = settingString(v)
		}
	}
	if class == "remote" {
		out["cacheFillAction"] = "read" // the default of R-AUTHZ-008
		if sub := objectOf(doc["remote"]); sub != nil {
			if v, ok := sub["cacheFillAction"]; ok {
				if s, ok := settingString(v); ok && s != "" {
					out["cacheFillAction"] = s
				}
			}
		}
	}
	return out
}

func objectOf(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

// writeSetting stores value for a setting into the document; it reports false for a setting that does not apply to the
// repository's class (a cacheFillAction of a local repository).
func writeSetting(class string, doc map[string]json.RawMessage, key, value string) bool {
	set := func(m map[string]json.RawMessage, k string, v any) {
		b, _ := json.Marshal(v)
		m[k] = b
	}
	nested := func(name string) map[string]json.RawMessage {
		m := objectOf(doc[name])
		if m == nil {
			m = map[string]json.RawMessage{}
		}
		return m
	}
	switch key {
	case "requireAuthentication":
		set(doc, key, value == "true")
	case "hideExistence":
		set(doc, key, value)
	case "packageOwnership":
		if class != "local" {
			return false
		}
		m := nested("local")
		set(m, key, value)
		set(doc, "local", m)
	case "cacheFillAction":
		if class != "remote" {
			return false
		}
		m := nested("remote")
		set(m, key, value)
		set(doc, "remote", m)
	default:
		return false
	}
	return true
}

func replaySettings(ctx context.Context, qs *q.Queries, env journal.Env, tx meta.Tx, o journal.Object) (journal.Result, error) {
	var e struct {
		RepositoryID string                     `json:"repositoryId"`
		Before       map[string]json.RawMessage `json:"before"`
		After        map[string]json.RawMessage `json:"after"`
	}
	if !o.Into(&e) || len(e.After) == 0 || e.Before == nil {
		return o.Malformed("repositoryId, before or after is missing"), nil
	}
	id, perr := strconv.ParseInt(e.RepositoryID, 10, 64)
	if perr != nil {
		return o.Malformed("repositoryId is not an ID"), nil //nolint:nilerr // an unreadable ID in an untrusted entry is a skipped entry, not a failed replay
	}
	before, after := repoSettings{}, repoSettings{}
	for k, v := range e.After {
		s, ok := settingString(v)
		if !ok {
			return o.Malformed("a setting has no plain value"), nil
		}
		after[k] = s
	}
	for k, v := range e.Before {
		if s, ok := settingString(v); ok {
			before[k] = s
		}
	}
	rows, err := qs.JournalRepositoryLock(ctx, id)
	if err != nil {
		return journal.Result{}, err
	}
	if len(rows) == 0 || rows[0].Lifecycle != "active" {
		return o.Result(journal.SubjectUnknown, e.RepositoryID), nil
	}
	repo := rows[0]
	subject := repo.ProjectKey + "/" + repo.Key
	doc := objectOf(repo.Config)
	if doc == nil {
		return o.Result(journal.SubjectUnknown, subject), nil
	}
	current := readSettings(repo.Class, doc)
	// A setting that does not apply to the repository's class is not compared or written.
	for k := range after {
		if k == "cacheFillAction" && repo.Class != "remote" || k == "packageOwnership" && repo.Class != "local" {
			delete(after, k)
			delete(before, k)
		}
	}
	if len(after) == 0 {
		return o.Result(journal.SubjectUnknown, subject), nil
	}
	same := true
	for k, v := range after {
		same = same && current[k] == v
	}
	if same {
		return o.Result(journal.AlreadyInEffect, subject), nil
	}
	save := func(apply func(key string) string) error {
		for k := range after {
			if v := apply(k); v != "" {
				writeSetting(repo.Class, doc, k, v)
			}
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		ver, err := qs.JournalRepositorySetConfig(ctx, q.JournalRepositorySetConfigParams{ID: id, Config: string(raw)})
		if err != nil || len(ver) == 0 {
			return err
		}
		if err := env.Notify(tx, cluster.AreaRepoConfig, 1, map[string]any{"id": e.RepositoryID, "key": subject, "op": "updated", "version": ver[0]}); err != nil {
			return err
		}
		return env.Notify(tx, cluster.AreaAuthz, payloadVersion, changePayload{Type: "repository", ID: e.RepositoryID})
	}
	act := journal.UpdateActions{
		Apply: func() error { return save(func(k string) string { return after[k] }) },
		Suspend: func() (json.RawMessage, error) {
			// A setting without an order has no most restrictive value to take: it is left as it is and named in the output,
			// and the others are suspended. The entry is never skipped on that account (audit finding L1, Q-AUTHZ-237).
			var unordered []string
			for k := range after {
				if accessOrder(k) == nil {
					unordered = append(unordered, k)
				}
			}
			slices.Sort(unordered)
			detail, err := json.Marshal(map[string]any{"repository": subject, "after": e.After, "unordered": unordered})
			if err != nil {
				return nil, err
			}
			return detail, save(func(k string) string {
				if order := accessOrder(k); order != nil {
					return order[0]
				}
				return ""
			})
		},
	}
	out, detail, err := journal.ApplyUpdate[repoSettings](settingsOrder{}, current, before, after, act)
	r := o.Result(out, subject)
	r.Detail = detail
	return r, err
}
