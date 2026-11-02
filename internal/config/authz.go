package config

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// AuthzCache bounds the staleness of the compiled grant index (R-AUTHZ-028).
type AuthzCache struct {
	MaxStaleness time.Duration `yaml:"max_staleness" default:"10s" validate:"min=1s" doc:"No decision is made on a grant index that was not confirmed current for longer; a request waits for a confirmation, then is answered 503 authz_stale (Q-AUTHZ-13). At least three times cluster.epoch_poll_interval; start-up refuses a lower value."`
	SafetyTTL    time.Duration `yaml:"safety_ttl" default:"10m" validate:"min=10s" doc:"Full rebuild of the grant index at least this often, jittered by up to 10 percent; it bounds a write path that changed an input without notifying."`
}

// AuthzAudit selects which decisions are audited besides the refused writes and
// management operations (R-AUTHZ-029).
type AuthzAudit struct {
	Reads string `yaml:"reads" default:"denied" validate:"oneof=none|denied|all" doc:"Which reads are audited: none, denied (refused reads) or all (allowed reads too; the volume follows the downloads, Q-AUTHZ-5)."`
}

// AuthzAuditReads are the values of authz.audit.reads.
const (
	AuthzReadsNone   = "none"
	AuthzReadsDenied = "denied"
	AuthzReadsAll    = "all"
)

// MinAuthzStaleness is the factor between cluster.epoch_poll_interval and the
// smallest authz.cache.max_staleness: one slow poll must not stop decisions
// (R-AUTHZ-028).
const MinAuthzStaleness = 3

// ValidateAuthz reports an authz.cache.max_staleness below three poll intervals
// (R-AUTHZ-028). The composition root calls it at start-up and names the key.
func (c *Config) ValidateAuthz(r *Report) {
	if min := MinAuthzStaleness * c.Cluster.EpochPollInterval; c.Authz.Cache.MaxStaleness < min {
		r.Add("authz.cache.max_staleness", "%s is below %d times cluster.epoch_poll_interval (%s): a single slow change-log poll must not stop authorization decisions",
			c.Authz.Cache.MaxStaleness, MinAuthzStaleness, c.Cluster.EpochPollInterval)
	}
}

// AuthzRoleBinding is one entry of authz.role_bindings (R-AUTHZ-010, Q-AUTHZ-170):
// a system role and exactly one principal, named as in the API without its kind
// prefix. trove migrate applies the list; no replica writes it.
type AuthzRoleBinding struct {
	Role           string `yaml:"role" json:"role" validate:"required" doc:"A system role: admin, security-admin, auditor, repository-admin or webhook-admin."`
	Group          string `yaml:"group" json:"group,omitempty" doc:"A group; one that does not exist is created as a local group, which the login provider's group sync then fills (R-AUTH-008)."`
	User           string `yaml:"user" json:"user,omitempty" doc:"A user; one that does not exist stops trove migrate."`
	ServiceAccount string `yaml:"service_account" json:"service_account,omitempty" doc:"A service account; one that does not exist stops trove migrate."`
}

// The principal kinds of a declared binding, as the principal table names them.
const (
	DeclaredUser           = "user"
	DeclaredGroup          = "group"
	DeclaredServiceAccount = "service_account"
)

// Principal returns the kind and the name of the one principal the entry names,
// and false when it names none or more than one.
func (b AuthzRoleBinding) Principal() (kind, name string, ok bool) {
	set := 0
	for _, p := range [][2]string{{DeclaredUser, b.User}, {DeclaredGroup, b.Group}, {DeclaredServiceAccount, b.ServiceAccount}} {
		if p[1] != "" {
			kind, name = p[0], p[1]
			set++
		}
	}
	return kind, name, set == 1
}

// Reference renders the entry's principal as the API names it (user:<name>,
// group:<name>, sa:<name>); "" when the entry names none or several.
func (b AuthzRoleBinding) Reference() string {
	kind, name, ok := b.Principal()
	switch {
	case !ok:
		return ""
	case kind == DeclaredServiceAccount:
		return "sa:" + name
	}
	return kind + ":" + name
}

// SystemRoles are the system roles of R-AUTHZ-010 that this binary implements, in
// the order of the capability table. internal/authz.Roles is the same list; a
// test there keeps the two equal, because this leaf package imports no domain
// package.
func SystemRoles() []string {
	return []string{"admin", "security-admin", "auditor", "repository-admin", "webhook-admin"}
}

// serviceAccountNamePattern is the name rule of a service account (R-AUTH-020).
var serviceAccountNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

// builtinPrincipals are the names of the built-in principals of R-AUTHZ-003 and
// R-AUTHZ-030, which never hold a role.
func builtinPrincipals() []string { return []string{"anonymous", "authenticated"} }

// Validate checks authz.role_bindings (R-AUTHZ-010, Q-AUTHZ-170): a known role,
// exactly one principal of a valid name, never a built-in principal, and no
// entry twice. These are the rules the API applies to a binding (422
// unknown_role, role_not_allowed); whether a user or service account exists is a
// question for the database, which trove migrate asks.
func (a *Authz) Validate(r *Report) {
	seen := map[string]int{}
	for i, b := range a.RoleBindings {
		key := fmt.Sprintf("role_bindings[%d]", i)
		if !slices.Contains(SystemRoles(), b.Role) {
			r.Add(key+".role", "%q is not a system role (%s)", b.Role, strings.Join(SystemRoles(), ", "))
		}
		kind, name, ok := b.Principal()
		if !ok {
			r.Add(key, "must name exactly one of group, user and service_account")
			continue
		}
		field := key + "." + kind
		switch {
		case slices.Contains(builtinPrincipals(), name):
			r.Add(field, "%q is a built-in principal: system roles are bound to users, groups and service accounts only (role_not_allowed)", name)
			continue
		case !validDeclaredName(kind, name):
			r.Add(field, "%q is not a valid %s name", name, strings.ReplaceAll(kind, "_", " "))
			continue
		}
		id := b.Role + "/" + b.Reference()
		if first, dup := seen[id]; dup {
			r.Add(key, "declares %s a second time (first in role_bindings[%d])", id, first)
			continue
		}
		seen[id] = i
	}
}

// validDeclaredName applies the name rule of the principal kind (R-AUTH-007,
// R-AUTH-018, R-AUTH-020).
func validDeclaredName(kind, name string) bool {
	switch kind {
	case DeclaredUser:
		return ValidUsername(name)
	case DeclaredGroup:
		return ValidGroupName(name)
	}
	return serviceAccountNamePattern.MatchString(name)
}
