package testenv

import (
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/auth/token"
)

// The seed writes rows straight into the installation's database, before the
// first replica starts, so that every replica builds its first grant index and
// snapshot with them. It writes what the production code stores (a token as the
// SHA-256 of its value, never the value, R-AUTH-022), so the requests of a test
// meet the real authentication stage and the real decision point; nothing here
// stands in for either.

// Person is a principal the seed created.
type Person struct {
	ID   string
	Name string
	Kind string // user, service_account or group
}

func (s *Stack) principal(kind, name string) Person {
	s.tb.Helper()
	id := s.Text("INSERT INTO principal (id, kind, name) VALUES (gen_random_uuid(), $1, $2) RETURNING id::text", kind, name)
	switch kind {
	case "user":
		s.Exec("INSERT INTO app_user (principal_id, origin) VALUES ($1::uuid, 'admin')", id)
	case "service_account":
		s.Exec("INSERT INTO service_account (principal_id) VALUES ($1::uuid)", id)
	case "group":
		s.Exec("INSERT INTO user_group (principal_id, origin) VALUES ($1::uuid, 'admin')", id)
	}
	return Person{ID: id, Name: name, Kind: kind}
}

// User creates a user and, for each name in groups, a member of that group (the
// group is created on first use).
func (s *Stack) User(name string, groups ...string) Person {
	s.tb.Helper()
	p := s.principal("user", name)
	for _, g := range groups {
		gid := s.Text("SELECT COALESCE((SELECT id::text FROM principal WHERE kind = 'group' AND name = $1), '')", g)
		if gid == "" {
			gid = s.principal("group", g).ID
		}
		s.Exec("INSERT INTO group_membership (group_id, member_id, source) VALUES ($1::uuid, $2::uuid, 'manual')", gid, p.ID)
	}
	return p
}

// ServiceAccount creates a service account.
func (s *Stack) ServiceAccount(name string) Person {
	s.tb.Helper()
	return s.principal("service_account", name)
}

// Bind gives a principal a built-in role (admin, security-admin, auditor,
// repository-admin, webhook-admin).
func (s *Stack) Bind(role string, p Person) {
	s.tb.Helper()
	s.Exec("INSERT INTO authz_role_binding (role, principal_id) VALUES ($1, $2::uuid)", role, p.ID)
}

// Token mints a token of the principal's kind (a PAT for a user, a SAT for a
// service account) with a scope and returns its value, the way token.Service
// stores one. A PAT expires in a day; a SAT does not expire.
func (s *Stack) Token(p Person, sc scope.Scope) string {
	s.tb.Helper()
	typ := token.TypePAT
	if p.Kind == "service_account" {
		typ = token.TypeSAT
	}
	raw, err := token.Generate(typ)
	if err != nil {
		s.tb.Fatal(err)
	}
	h := token.Hash(raw)
	expires := "now() + interval '1 day'"
	if typ == token.TypeSAT {
		expires = "NULL"
	}
	s.Exec(`INSERT INTO api_token (id, type, secret_sha256, hint, principal_id, description, scope, created_via, expires_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4::uuid, 'test', $5::text::jsonb, 'admin-cli', `+expires+`)`,
		string(typ), h[:], token.Hint(raw), p.ID, string(sc.Marshal()))
	return raw
}

// Repository inserts a repository row into the default project [DefaultProject]. A key of the form
// "<project>/<key>" names another project, created on first use. cfg is the JSON settings document ("{}"
// for the defaults). The row bypasses the repository service: it is for the state a test starts from,
// not for what it tests; a test of the service goes through the management API.
func (s *Stack) Repository(key, class, format, cfg string) {
	s.tb.Helper()
	project, k := splitName(key)
	s.RepositoryIn(project, k, class, format, cfg)
}

// RepositoryIn inserts a repository row into the project, which is created when it does not exist.
func (s *Stack) RepositoryIn(project, key, class, format, cfg string) {
	s.tb.Helper()
	if cfg == "" {
		cfg = "{}"
	}
	s.Project(project)
	s.Exec("INSERT INTO repositories (key, class, format, config, project_id) SELECT $1, $2, $3, $4::text::jsonb, id FROM project WHERE key = $5",
		key, class, format, cfg, project)
}

// Project creates the project (display name equal to the key) unless it exists. The stack's seed
// creates [DefaultProject]; every repository belongs to a project (Q-AUTHZ-206).
func (s *Stack) Project(key string) {
	s.tb.Helper()
	s.Exec("INSERT INTO project (id, key, display_name) VALUES (gen_random_uuid(), $1, $1) ON CONFLICT (key) DO NOTHING", key)
}

// Member makes a user or group a member of the project with the role (reader, developer, maintainer or
// owner); the project is created when it does not exist.
func (s *Stack) Member(project string, p Person, role string) {
	s.tb.Helper()
	s.Project(project)
	s.Exec("INSERT INTO project_member (project_id, principal_id, role) SELECT id, $2::uuid, $3 FROM project WHERE key = $1", project, p.ID, role)
}

// Policy creates a global permission policy that grants actions on the repositories
// named by key (a bare key of the default project, or "<project>/<key>") to the principals.
func (s *Stack) Policy(name string, actions []string, repositories []string, members ...Person) {
	s.tb.Helper()
	id := s.Text("INSERT INTO authz_policy (id, name) VALUES (gen_random_uuid(), $1) RETURNING id::text", name)
	for i, key := range repositories {
		project, k := splitName(key)
		s.Exec(`INSERT INTO authz_policy_selector (policy_id, ordinal, kind, repository_id)
			SELECT $1::uuid, $2, 'key', r.id FROM repositories r JOIN project p ON p.id = r.project_id WHERE p.key = $3 AND r.key = $4`, id, i, project, k)
	}
	for _, m := range members {
		s.Exec("INSERT INTO authz_policy_entry (policy_id, principal_id, actions) VALUES ($1::uuid, $2::uuid, $3)", id, m.ID, actions)
	}
}
