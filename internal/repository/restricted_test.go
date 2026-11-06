package repository

import (
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

// restrictedFields validates two remote sections of the npmlike format, the stored one
// ("" for none) and the next, and returns the field codes the rules of a project
// administrator add to next (Q-REPO-127, Q-REPO-128).
func restrictedFields(t *testing.T, stored, next string) map[string]string {
	t.Helper()
	var cur *Document
	if stored != "" {
		d, v := validateRemoteDoc(t, stored)
		if err := v.err(); err != nil {
			t.Fatalf("stored: %v", err)
		}
		cur = &d
	}
	nd, v := validateRemoteDoc(t, next)
	if err := v.err(); err != nil {
		t.Fatalf("next: %v", err)
	}
	restrictedRemote(v, Caller{Restricted: true}, cur, &nd)
	out := map[string]string{}
	for _, f := range v.fields {
		out[f.Pointer] = f.Code
	}
	return out
}

func TestR_AUTHZ_026_RestrictedRemoteRules(t *testing.T) {
	const (
		sealed  = `{"url":"https://partner.example.test/","authMode":"bearer","password":{"set":true}}`
		ref     = `{"url":"https://partner.example.test/","authMode":"bearer","password":{"secretRef":{"file":"partner"}}}`
		routed  = `{"url":"https://partner.example.test/","sourceNetwork":"10.99.0.0/24","outboundProxy":{"mode":"direct"},"extraHosts":[{"pattern":"cdn.example.test"}]}`
		elsewhr = `"url":"https://evil.example.test/"`
	)
	cases := []struct {
		name, stored, next string
		want               map[string]string
	}{
		{"Q-REPO-127 a value of its own is always allowed", "", `{"url":"https://evil.example.test/","authMode":"bearer","password":{"value":"mine"}}`, nil},
		{"Q-REPO-127 a create names no reference", "", ref, map[string]string{"/remote/password/secretRef": "secret_value_required"}},
		{"Q-REPO-127 the stored reference sent back is kept", ref, ref, nil},
		{
			"Q-REPO-127 another reference is refused", ref, `{"url":"https://partner.example.test/","authMode":"bearer","password":{"secretRef":{"env":"TROVE_CAC_OTHER"}}}`,
			map[string]string{"/remote/password/secretRef": "secret_value_required"},
		},
		{"Q-REPO-127 a kept value with another url", sealed, `{` + elsewhr + `,"authMode":"bearer","password":{"set":true}}`, map[string]string{"/remote/password": "secret_value_required"}},
		{"Q-REPO-127 the kept reference with another url", ref, `{` + elsewhr + `,"authMode":"bearer","password":{"secretRef":{"file":"partner"}}}`, map[string]string{"/remote/password": "secret_value_required"}},
		{"Q-REPO-127 a kept value whatever the auth mode says", sealed, `{` + elsewhr + `,"authMode":"none","password":{"set":true}}`, map[string]string{"/remote/password": "secret_value_required"}},
		{"Q-REPO-127 a new value with another url", sealed, `{` + elsewhr + `,"authMode":"bearer","password":{"value":"new"}}`, nil},
		{"Q-REPO-127 no credential with another url", sealed, `{` + elsewhr + `}`, nil},
		{
			"Q-REPO-127 a new host that receives credentials", sealed,
			`{"url":"https://partner.example.test/","authMode":"bearer","password":{"set":true},"extraHosts":[{"pattern":"*.example.test","sendCredentials":true}]}`,
			map[string]string{"/remote/password": "secret_value_required"},
		},
		{
			"Q-REPO-127 a new host without credentials", sealed,
			`{"url":"https://partner.example.test/","authMode":"bearer","password":{"set":true},"extraHosts":[{"pattern":"*.example.test"}]}`, nil,
		},
		{
			"Q-REPO-127 forwarding on redirects", sealed, `{"url":"https://partner.example.test/","authMode":"bearer","password":{"set":true},"forwardAuthOnRedirect":true}`,
			map[string]string{"/remote/password": "secret_value_required"},
		},
		{
			"Q-REPO-128 a create chooses no outbound path", "", routed,
			map[string]string{"/remote/sourceNetwork": "outbound_path_restricted", "/remote/outboundProxy/mode": "outbound_path_restricted"},
		},
		{"Q-REPO-128 the stored path stays with url and hosts", routed, routed, nil},
		{
			"Q-REPO-128 the stored path does not follow a new url", routed,
			`{` + elsewhr + `,"sourceNetwork":"10.99.0.0/24","outboundProxy":{"mode":"direct"},"extraHosts":[{"pattern":"cdn.example.test"}]}`,
			map[string]string{"/remote/sourceNetwork": "outbound_path_restricted", "/remote/outboundProxy/mode": "outbound_path_restricted"},
		},
		{
			"Q-REPO-128 nor a new extra host", routed,
			`{"url":"https://partner.example.test/","sourceNetwork":"10.99.0.0/24","outboundProxy":{"mode":"direct"},"extraHosts":[{"pattern":"cdn.example.test"},{"pattern":"*"}]}`,
			map[string]string{"/remote/sourceNetwork": "outbound_path_restricted", "/remote/outboundProxy/mode": "outbound_path_restricted"},
		},
		{
			"Q-REPO-128 another source network", routed,
			`{"url":"https://partner.example.test/","sourceNetwork":"10.98.0.0/24","outboundProxy":{"mode":"direct"},"extraHosts":[{"pattern":"cdn.example.test"}]}`,
			map[string]string{"/remote/sourceNetwork": "outbound_path_restricted"},
		},
		{"Q-REPO-128 the defaults are always allowed", routed, `{` + elsewhr + `,"sourceNetwork":null,"outboundProxy":{"mode":"default"}}`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := restrictedFields(t, c.stored, c.next)
			if len(got) != len(c.want) {
				t.Fatalf("fields = %v, want %v", got, c.want)
			}
			for p, code := range c.want {
				if got[p] != code {
					t.Errorf("%s = %q, want %q (all: %v)", p, got[p], code, got)
				}
			}
		})
	}
	t.Run("Q-REPO-127 a holder of repositories:write-config is not bound by the rules", func(t *testing.T) {
		nd, v := validateRemoteDoc(t, ref)
		restrictedRemote(v, Caller{}, nil, &nd)
		if err := v.err(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("Q-REPO-127 the rule's code answers at the top", func(t *testing.T) {
		nd, v := validateRemoteDoc(t, ref)
		restrictedRemote(v, Caller{Restricted: true}, nil, &nd)
		e, ok := errs.As(v.err())
		if !ok || e.Code != errs.CodeSecretValueRequired || e.Status() != 422 {
			t.Fatalf("%v", v.err())
		}
	})
}
