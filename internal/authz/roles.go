package authz

// Role is a system role (R-AUTHZ-010). The set is fixed: trove has no custom
// roles.
type Role string

// The system roles. admin is MVP; the others are P1 (R-AUTHZ-011,
// R-AUTHZ-012) but the table of capabilities and the role bindings are fixed
// now, so the API names them from the start.
const (
	RoleAdmin           Role = "admin"
	RoleSecurityAdmin   Role = "security-admin"
	RoleAuditor         Role = "auditor"
	RoleRepositoryAdmin Role = "repository-admin"
	RoleWebhookAdmin    Role = "webhook-admin"
)

// RolePermission is the permission string that asks Require for a role itself,
// the form {role: <name>} of x-trove-permission (R-API-008).
func RolePermission(r Role) string { return rolePrefix + string(r) }

// Roles lists the system roles in a fixed order.
func Roles() []Role {
	return []Role{RoleAdmin, RoleSecurityAdmin, RoleAuditor, RoleRepositoryAdmin, RoleWebhookAdmin}
}

// Valid reports whether r is a system role.
func (r Role) Valid() bool {
	for _, x := range Roles() {
		if x == r {
			return true
		}
	}
	return false
}

func (r Role) bit() roleSet {
	for i, x := range Roles() {
		if x == r {
			return 1 << i
		}
	}
	return 0
}

// roleSet is the set of roles bound to one principal.
type roleSet uint8

func (s roleSet) has(r Role) bool { return r.bit() != 0 && s&r.bit() == r.bit() }

// capSet is a set of capabilities.
type capSet uint32

// Capabilities lists the capabilities of R-AUTHZ-010 in a fixed order. Every other
// list of them (the system items of a token scope, R-AUTH-025) is this one.
func Capabilities() []Capability {
	return []Capability{
		ContentBypass, IdentitiesRead, IdentitiesWrite, PoliciesRead, PoliciesWrite,
		RolesRead, RolesWrite, CIRead, CIWrite, TokensRead, TokensRevoke,
		RepositoriesRead, RepositoriesWrite, ProjectsRead, ProjectsWrite,
		WebhooksRead, WebhooksWrite, SettingsRead, SettingsWrite, SecuritySettingsWrite,
		AuditRead, EndpointsRead, EndpointsWrite,
	}
}

func (c Capability) bit() capSet {
	for i, x := range Capabilities() {
		if x == c {
			return 1 << i
		}
	}
	return 0
}

// Valid reports whether c is a capability of R-AUTHZ-010.
func (c Capability) Valid() bool { return c.bit() != 0 }

func capsOf(cs ...Capability) capSet {
	var s capSet
	for _, c := range cs {
		s |= c.bit()
	}
	return s
}

func (s capSet) has(c Capability) bool { return c.bit() != 0 && s&c.bit() == c.bit() }

// list returns the capabilities of the set in the table's order.
func (s capSet) list() []Capability {
	var out []Capability
	for _, c := range Capabilities() {
		if s.has(c) {
			out = append(out, c)
		}
	}
	return out
}

// RoleCapabilities returns the capabilities a role bundles: the table of
// R-AUTHZ-010, one column per role.
func RoleCapabilities(r Role) []Capability { return roleCaps(r).list() }

func roleCaps(r Role) capSet {
	switch r {
	case RoleAdmin:
		var all capSet
		for _, c := range Capabilities() {
			all |= c.bit()
		}
		return all
	case RoleSecurityAdmin:
		return capsOf(IdentitiesRead, IdentitiesWrite, PoliciesRead, PoliciesWrite, RolesRead,
			CIRead, CIWrite, TokensRead, TokensRevoke, RepositoriesRead, ProjectsRead,
			SettingsRead, SecuritySettingsWrite, AuditRead, EndpointsRead, EndpointsWrite)
	case RoleAuditor:
		return capsOf(IdentitiesRead, PoliciesRead, RolesRead, CIRead, TokensRead,
			RepositoriesRead, ProjectsRead, WebhooksRead, SettingsRead, AuditRead, EndpointsRead)
	case RoleRepositoryAdmin:
		return capsOf(RepositoriesRead, RepositoriesWrite, ProjectsRead, EndpointsRead)
	case RoleWebhookAdmin:
		return capsOf(WebhooksRead, WebhooksWrite)
	}
	return 0
}

// capsOfRoles is the union of the capabilities of a set of roles.
func capsOfRoles(s roleSet) capSet {
	var out capSet
	for _, r := range Roles() {
		if s.has(r) {
			out |= roleCaps(r)
		}
	}
	return out
}

// Me is the answer to "what may I do" (R-AUTHZ-022 GET /authz/me): the
// capabilities the subject's credential exercises, the roles behind them and
// the principal set by reference.
type Me struct {
	Roles        []Role
	Capabilities []Capability
	// Principals are the references of the principal set.
	Principals []string
	// Epoch is the epoch of the index that answered.
	Epoch int64
}
