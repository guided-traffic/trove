package config

import (
	"strings"
	"testing"
	"time"
)

func loadRoleBindings(t *testing.T, file string, env ...string) (*Loaded, error) {
	t.Helper()
	return Load(Options{File: "/c.yaml", ReadFile: fakeFS(map[string]string{"/c.yaml": file}), Environ: minimalEnv(env...), Command: CommandMigrate})
}

func TestR_AUTHZ_010_RoleBindingsFromTheConfiguration(t *testing.T) {
	t.Run("R-AUTHZ-010 the owner's declaration loads", func(t *testing.T) {
		l, err := loadRoleBindings(t, `authz:
  role_bindings:
    - role: admin
      group: gh-wds18-k8s-main-admins
    - role: auditor
      user: alice@example.com
    - role: auditor
      service_account: audit-bot
`)
		if err != nil {
			t.Fatal(err)
		}
		got := l.Config.Authz.RoleBindings
		want := []AuthzRoleBinding{
			{Role: "admin", Group: "gh-wds18-k8s-main-admins"},
			{Role: "auditor", User: "alice@example.com"},
			{Role: "auditor", ServiceAccount: "audit-bot"},
		}
		if len(got) != len(want) {
			t.Fatalf("%+v", got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("entry %d = %+v, want %+v", i, got[i], want[i])
			}
		}
		refs := []string{got[0].Reference(), got[1].Reference(), got[2].Reference()}
		if strings.Join(refs, " ") != "group:gh-wds18-k8s-main-admins user:alice@example.com sa:audit-bot" {
			t.Fatalf("references %v", refs)
		}
		if l.Sources["authz.role_bindings"].Kind != "file" {
			t.Fatalf("source %+v", l.Sources["authz.role_bindings"])
		}
	})

	t.Run("R-AUTHZ-010 the default is no declaration", func(t *testing.T) {
		l, err := loadRoleBindings(t, "")
		if err != nil || len(l.Config.Authz.RoleBindings) != 0 {
			t.Fatalf("%v %+v", err, l)
		}
	})

	t.Run("R-ARCH-020 the environment carries the list as JSON", func(t *testing.T) {
		l, err := loadRoleBindings(t, "", `TROVE_AUTHZ_ROLE_BINDINGS=[{"role":"admin","group":"platform-admins"}]`)
		if err != nil {
			t.Fatal(err)
		}
		if got := l.Config.Authz.RoleBindings; len(got) != 1 || got[0] != (AuthzRoleBinding{Role: "admin", Group: "platform-admins"}) {
			t.Fatalf("%+v", got)
		}
	})

	refusals := []struct {
		name  string
		entry string
		want  []string
	}{
		{"an unknown role", "{role: root, group: ops}", []string{`authz.role_bindings[0].role "root" is not a system role (admin, security-admin, auditor, repository-admin, webhook-admin)`}},
		{"a missing role", "{group: ops}", []string{`authz.role_bindings[0].role "" is not a system role`}},
		{"no principal", "{role: admin}", []string{"authz.role_bindings[0] must name exactly one of group, user and service_account"}},
		{"two principals", "{role: admin, group: ops, user: alice}", []string{"authz.role_bindings[0] must name exactly one of group, user and service_account"}},
		{"anonymous", "{role: auditor, user: anonymous}", []string{`authz.role_bindings[0].user "anonymous" is a built-in principal`, "role_not_allowed"}},
		{"authenticated as a group", "{role: admin, group: authenticated}", []string{`authz.role_bindings[0].group "authenticated" is a built-in principal`}},
		{"an invalid group name", "{role: admin, group: Ops Team}", []string{`authz.role_bindings[0].group "Ops Team" is not a valid group name`}},
		{"an invalid user name", "{role: admin, user: system}", []string{`authz.role_bindings[0].user "system" is not a valid user name`}},
		{"an invalid service account name", "{role: admin, service_account: a}", []string{`authz.role_bindings[0].service_account "a" is not a valid service account name`}},
	}
	for _, tc := range refusals {
		t.Run("R-AUTHZ-010 "+tc.name+" is refused at load and names the key", func(t *testing.T) {
			_, err := loadRoleBindings(t, "authz:\n  role_bindings:\n    - "+tc.entry+"\n")
			containsAll(t, issuesOf(t, err), tc.want...)
		})
	}

	t.Run("R-AUTHZ-010 an entry declared twice is refused", func(t *testing.T) {
		_, err := loadRoleBindings(t, `authz:
  role_bindings:
    - {role: admin, group: ops}
    - {role: auditor, group: ops}
    - {role: admin, group: ops}
`)
		msgs := issuesOf(t, err)
		containsAll(t, msgs, "authz.role_bindings[2] declares admin/group:ops a second time (first in role_bindings[0])")
		if len(msgs) != 1 {
			t.Fatalf("one issue expected: %v", msgs)
		}
	})

	t.Run("R-OPS-028 every bad entry is reported in one pass", func(t *testing.T) {
		_, err := loadRoleBindings(t, `authz:
  role_bindings:
    - {role: root, group: ops}
    - {role: admin}
    - {role: admin, user: authenticated}
`)
		containsAll(t, issuesOf(t, err), "role_bindings[0].role", "role_bindings[1] must name", "role_bindings[2].user")
	})

	t.Run("R-ARCH-020 an unknown key of an entry is refused", func(t *testing.T) {
		_, err := loadRoleBindings(t, "authz:\n  role_bindings:\n    - {role: admin, groups: ops}\n")
		if err == nil || !strings.Contains(err.Error(), "groups") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestR_AUTHZ_028_StalenessFloor(t *testing.T) {
	check := func(staleness, poll time.Duration) []string {
		var c Config
		c.Authz.Cache.MaxStaleness = staleness
		c.Cluster.EpochPollInterval = poll
		var issues []Issue
		c.ValidateAuthz(&Report{issues: &issues})
		out := make([]string, len(issues))
		for i, is := range issues {
			out[i] = is.Message
		}
		return out
	}
	t.Run("R-AUTHZ-028 the shipped values are accepted", func(t *testing.T) {
		if got := check(10*time.Second, time.Second); len(got) != 0 {
			t.Fatalf("issues: %v", got)
		}
	})
	t.Run("R-AUTHZ-028 a value below three poll intervals is refused and names the key", func(t *testing.T) {
		got := check(2*time.Second, time.Second)
		if len(got) != 1 || !strings.Contains(got[0], "authz.cache.max_staleness") || !strings.Contains(got[0], "cluster.epoch_poll_interval") {
			t.Fatalf("issues: %v", got)
		}
	})
	t.Run("R-AUTHZ-028 exactly three poll intervals are accepted", func(t *testing.T) {
		if got := check(3*time.Second, time.Second); len(got) != 0 {
			t.Fatalf("issues: %v", got)
		}
	})
}
