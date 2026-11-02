//go:build integration

package authz

import (
	"testing"
	"time"
)

// What a project administrator can test through the policy list and the validation
// (Q-AUTHZ-197), and that its refusals leave an event (Q-AUTHZ-201), over HTTP.
func TestR_AUTHZ_026_ProjectAdministratorFiltersAndRefusalsOverHTTP(t *testing.T) {
	f := newProjectAPI(t)
	alice := f.alice.Name
	shopBot := f.e.Text("INSERT INTO principal (id, kind, name) VALUES (gen_random_uuid(), 'service_account', 'shop-bot') RETURNING id::text")
	f.e.Exec("INSERT INTO service_account (principal_id, project_id) VALUES ($1::uuid, $2::uuid)", shopBot, f.shopID)
	wantStatus(t, f.cl.Do(alice, "POST", policies, projectPolicyBody("web-publish", "web", []string{"web-npm"}, entry("user:bob", "read"))), 201, "")
	hdr := f.current()

	t.Run("an unknown principal and an unknown repository answer an empty list, like a foreign one", func(t *testing.T) {
		for _, q := range []string{
			"?project=web&principal=user:nobody", "?project=web&repository=no-such-repository",
			"?project=web&repository=shop-npm", "?project=web&principal=group:no-such-group",
		} {
			r := f.cl.Do(alice, "GET", policies+q, nil, hdr...)
			wantStatus(t, r, 200, "")
			if got := listNames(t, r); len(got) != 0 {
				t.Fatalf("%s: %v", q, got)
			}
		}
		r := f.cl.Do(alice, "GET", policies+"?project=web&principal=user:bob&expandGroups=false", nil, hdr...)
		wantStatus(t, r, 200, "")
		if got := listNames(t, r); len(got) != 1 {
			t.Fatalf("the control finds %v", got)
		}
	})
	t.Run("expandGroups=true is 403 for a project administrator, the default does not expand", func(t *testing.T) {
		wantStatus(t, f.cl.Do(alice, "GET", policies+"?project=web&principal=user:bob&expandGroups=true", nil, hdr...), 403, "forbidden")
		wantStatus(t, f.cl.Do(alice, "GET", policies+"?project=web&principal=user:bob", nil, hdr...), 200, "")
	})
	t.Run("the holder of the capability still gets 422 for an unknown principal", func(t *testing.T) {
		wantStatus(t, f.cl.Do(f.admin.Name, "GET", policies+"?principal=user:nobody", nil, hdr...), 422, "unknown_principal")
	})
	t.Run("another project's service account answers like an unknown principal in a validation", func(t *testing.T) {
		code := func(principal string) string {
			r := f.cl.Do(alice, "POST", "/api/v1/authz/validate-policy", projectPolicyBody("web-v", "web", []string{"web-npm"}, entry(principal, "deploy")), hdr...)
			wantStatus(t, r, 200, "")
			errors, _ := r.JSON(t)["errors"].([]any)
			if len(errors) != 1 {
				t.Fatalf("%s: %v", principal, r.JSON(t))
			}
			return errors[0].(map[string]any)["code"].(string)
		}
		if a, b := code("sa:shop-bot"), code("sa:no-such-bot"); a != "unknown_principal" || b != a {
			t.Fatalf("foreign %q, unknown %q", a, b)
		}
	})
	t.Run("a refusal of the narrowing is recorded as authz.denied of the capability", func(t *testing.T) {
		const count = "SELECT count(*) FROM audit_event WHERE action = 'authz.denied' AND details->>'permission' = 'policies:write'"
		before := f.e.Number(count)
		wantStatus(t, f.cl.Do(alice, "POST", policies, projectPolicyBody("shop-x", "shop", []string{"shop-npm"}), hdr...), 422, "unknown_project")
		waitFor(t, "the refusal is recorded", 5*time.Second, func() bool { return f.e.Number(count) > before })
	})
}
