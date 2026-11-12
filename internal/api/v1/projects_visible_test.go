package v1

import (
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/project"
)

// TestR_AUTHZ_035_TheProjectsACallerSees: a holder of projects:read sees every project, a member its own, any caller the projects its
// level admits (internal for an internal subject, public for every request); membership keeps the caller's own (R-AUTHZ-035 rule 8,
// R-API-057).
func TestR_AUTHZ_035_TheProjectsACallerSees(t *testing.T) {
	user := authz.Subject{Kind: "user", ID: "u1", Name: "alice"}
	external := user
	external.External = true
	viaBastion := user
	viaBastion.ViaBastion = true
	service := authz.Subject{Kind: "service_account", ID: "s1", Name: "bot"}
	anonymous := authz.Subject{Anonymous: true}

	priv := project.Project{Key: "shut", Visibility: "private"}
	inner := project.Project{Key: "inner", Visibility: "internal"}
	open := project.Project{Key: "open", Visibility: "public"}
	member := map[string]authz.ProjectRole{"shut": authz.ProjectRoleReader}

	cases := []struct {
		name       string
		sub        authz.Subject
		p          project.Project
		roles      map[string]authz.ProjectRole
		all        bool
		membership bool
		want       bool
	}{
		{"an internal user sees an internal project", user, inner, nil, false, false, true},
		{"an internal user sees a public project", user, open, nil, false, false, true},
		{"an internal user does not see a private project", user, priv, nil, false, false, false},
		{"a member sees its private project", user, priv, member, false, false, true},
		{"a holder of projects:read sees every project", user, priv, nil, true, false, true},
		{"an external user is not admitted by internal", external, inner, nil, false, false, false},
		{"an external user sees a public project", external, open, nil, false, false, true},
		{"a request through a Bastion is not internal", viaBastion, inner, nil, false, false, false},
		{"a service account is never internal", service, inner, nil, false, false, false},
		{"an anonymous caller sees a public project", anonymous, open, nil, false, false, true},
		{"an anonymous caller does not see an internal one", anonymous, inner, nil, false, false, false},
		{"membership keeps the memberships", user, priv, member, false, true, true},
		{"membership drops an internal project", user, inner, nil, false, true, false},
		{"membership drops every project for a holder of projects:read", user, open, nil, true, true, false},
	}
	for _, c := range cases {
		t.Run("R-AUTHZ-035 "+c.name, func(t *testing.T) {
			if got := projectListed(c.sub, c.p, c.roles, c.all, c.membership); got != c.want {
				t.Errorf("projectListed = %v, want %v", got, c.want)
			}
		})
	}
}
