package token

import (
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
)

func TestR_AUTH_024_ParseScope(t *testing.T) {
	opts := ScopeOptions{SystemItem: func(s string) bool { return s == "identities:read" }, Endpoint: func(n string) bool { return n == "default" || n == "customers" }}
	ok := map[string]string{
		"R-AUTH-024 the default":                  ``,
		"R-AUTH-024 inherit":                      `{"inherit": true}`,
		"R-AUTH-024 admin with inherit":           `{"inherit": true, "admin": true}`,
		"R-AUTH-024 packages as a nested pattern": `{"repositories":[{"repositories":["npm-local","npm-team-*"],"packages":{"include":["@acme/*"]},"actions":["read","deploy"]}]}`,
		"R-AUTH-024 system item":                  `{"system":["identities:read"]}`,
		"R-EP-011 bound to endpoints":             `{"inherit":true,"endpoints":["customers"]}`,
		"R-AUTH-024 paths with excludes":          `{"repositories":[{"repositories":["gen"],"paths":{"include":["a/**"],"exclude":["a/b/**"]},"actions":["read"]}]}`,
	}
	for name, doc := range ok {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseScope([]byte(doc), opts); err != nil {
				t.Fatalf("%v", err)
			}
		})
	}
	bad := map[string]string{
		"R-AUTH-024 include at the top level of a grant": `{"repositories":[{"repositories":["npm-local"],"include":["@acme/*"],"actions":["read"]}]}`,
		"R-AUTH-024 an unknown field":                    `{"inherit":true,"colour":"red"}`,
		"R-AUTH-024 builds are not available yet":        `{"builds":[{"builds":{"include":["a/**"]},"actions":["read"]}]}`,
		"R-AUTH-024 an unknown action":                   `{"repositories":[{"repositories":["a"],"actions":["publish"]}]}`,
		"R-AUTH-024 inherit excludes grants":             `{"inherit":true,"repositories":[{"repositories":["a"],"actions":["read"]}]}`,
		"R-AUTH-024 a grant without actions":             `{"repositories":[{"repositories":["a"],"actions":[]}]}`,
		"R-AUTH-024 a grant without repositories":        `{"repositories":[{"repositories":[],"actions":["read"]}]}`,
		"R-AUTH-024 an unknown system item":              `{"system":["everything:write"]}`,
		"R-EP-011 an unknown endpoint":                   `{"endpoints":["nope"]}`,
		"R-AUTH-024 trailing data":                       `{"inherit":true} {}`,
		"R-AUTH-024 not an object":                       `[]`,
		"R-AUTH-024 51 grants":                           `{"repositories":[` + strings.TrimSuffix(strings.Repeat(`{"repositories":["a"],"actions":["read"]},`, 51), ",") + `]}`,
		"R-AUTH-024 more than 16 KiB":                    `{"repositories":[{"repositories":["` + strings.Repeat("a", 17<<10) + `"],"actions":["read"]}]}`,
	}
	for name, doc := range bad {
		t.Run(name, func(t *testing.T) {
			_, err := ParseScope([]byte(doc), opts)
			if e, isErr := errs.As(err); !isErr || e.Code != errs.CodeInvalidScope || e.Status() != 400 {
				t.Fatalf("err = %v, want 400 invalid_scope", err)
			}
		})
	}
	t.Run("R-AUTH-024 no system item is accepted without a checker", func(t *testing.T) {
		if _, err := ParseScope([]byte(`{"system":["identities:read"]}`), ScopeOptions{}); err == nil {
			t.Fatal("accepted")
		}
	})
}

func TestR_AUTH_024_MarshalRoundTrip(t *testing.T) {
	doc := `{"inherit":false,"admin":false,"repositories":[{"repositories":["npm-local"],"packages":{"include":["@acme/*"]},"actions":["read"]}],"system":[]}`
	s, err := ParseScope([]byte(doc), ScopeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(s.Marshal()); got != doc {
		t.Fatalf("Marshal = %s", got)
	}
	back, err := ParseStoredScope(s.Marshal())
	if err != nil || len(back.Repositories) != 1 || back.Repositories[0].Packages.Include[0] != "@acme/*" {
		t.Fatalf("%+v %v", back, err)
	}
	if got := string(DefaultScope().Marshal()); got != `{"inherit":true,"admin":false,"repositories":[],"system":[]}` {
		t.Fatalf("default scope renders as %s", got)
	}
}

func TestR_AUTH_027_ScopeWithin(t *testing.T) {
	read := Scope{Repositories: []ScopeGrant{{Repositories: []string{"npm-local"}, Actions: []string{"read", "deploy"}}}}
	cases := []struct {
		name          string
		child, parent Scope
		want          bool
	}{
		{"an equal scope", read, read, true},
		{"fewer actions", Scope{Repositories: []ScopeGrant{{Repositories: []string{"npm-local"}, Actions: []string{"read"}}}}, read, true},
		{"more actions", Scope{Repositories: []ScopeGrant{{Repositories: []string{"npm-local"}, Actions: []string{"read", "delete"}}}}, read, false},
		{"another repository", Scope{Repositories: []ScopeGrant{{Repositories: []string{"npm-other"}, Actions: []string{"read"}}}}, read, false},
		{"inherit under a restricted token", Scope{Inherit: true}, read, false},
		{"restricted under inherit", read, Scope{Inherit: true}, true},
		{"admin kept", Scope{Inherit: true, Admin: true}, Scope{Inherit: true, Admin: true}, true},
		{"admin gained", Scope{Inherit: true, Admin: true}, Scope{Inherit: true}, false},
		{"admin dropped", Scope{Inherit: true}, Scope{Inherit: true, Admin: true}, true},
		{"system item gained", Scope{Inherit: true, System: []string{"x"}}, Scope{Inherit: true}, false},
		{"a bound parent needs a bound child", Scope{Inherit: true}, Scope{Inherit: true, Endpoints: []string{"a", "b"}}, false},
		{"a bound child within a bound parent", Scope{Inherit: true, Endpoints: []string{"a"}}, Scope{Inherit: true, Endpoints: []string{"a", "b"}}, true},
		{"a child bound elsewhere", Scope{Inherit: true, Endpoints: []string{"c"}}, Scope{Inherit: true, Endpoints: []string{"a", "b"}}, false},
	}
	for _, c := range cases {
		t.Run("R-AUTH-027 "+c.name, func(t *testing.T) {
			if got := c.child.Within(c.parent); got != c.want {
				t.Fatalf("Within = %v, want %v", got, c.want)
			}
		})
	}
}

func i64(v int64) *int64 { return &v }

func TestR_AUTH_026_ResolveLifetime(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	def, maxTTL := 2160*time.Hour, 8760*time.Hour
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	code := func(err error) string {
		if e, ok := errs.As(err); ok {
			return e.Code
		}
		return ""
	}
	cases := []struct {
		name     string
		typ      Type
		req      TTLRequest
		allowNE  bool
		admin    bool
		derived  bool
		parent   *time.Time
		wantCode string
		wantTTL  time.Duration
		wantNE   bool
	}{
		{name: "neither applies the default", typ: TypePAT, wantTTL: def},
		{name: "ttlSeconds", typ: TypePAT, req: TTLRequest{Seconds: i64(3600)}, wantTTL: time.Hour},
		{name: "expiresAt", typ: TypePAT, req: TTLRequest{ExpiresAt: at(2 * time.Hour)}, wantTTL: 2 * time.Hour},
		{name: "both are invalid_ttl", typ: TypePAT, req: TTLRequest{Seconds: i64(3600), ExpiresAt: at(time.Hour)}, wantCode: "invalid_ttl"},
		{name: "negative is invalid_ttl", typ: TypePAT, req: TTLRequest{Seconds: i64(-5)}, wantCode: "invalid_ttl"},
		{name: "30 is invalid_ttl", typ: TypePAT, req: TTLRequest{Seconds: i64(30)}, wantCode: "invalid_ttl"},
		{name: "60 is the shortest", typ: TypePAT, req: TTLRequest{Seconds: i64(60)}, wantTTL: time.Minute},
		{name: "expiresAt 30 s ahead is invalid_ttl", typ: TypePAT, req: TTLRequest{ExpiresAt: at(30 * time.Second)}, wantCode: "invalid_ttl"},
		{name: "above the maximum", typ: TypePAT, req: TTLRequest{Seconds: i64(40000000)}, wantCode: "ttl_exceeds_maximum"},
		{name: "an expiresAt above the maximum", typ: TypePAT, req: TTLRequest{ExpiresAt: at(maxTTL + time.Hour)}, wantCode: "ttl_exceeds_maximum"},
		{name: "the maximum itself", typ: TypePAT, req: TTLRequest{Seconds: i64(int64(maxTTL / time.Second))}, wantTTL: maxTTL},
		{name: "a PAT is never non-expiring", typ: TypePAT, req: TTLRequest{Seconds: i64(0)}, allowNE: true, admin: true, wantCode: "non_expiring_not_allowed"},
		{name: "a SAT needs the configuration", typ: TypeSAT, req: TTLRequest{Seconds: i64(0)}, admin: true, wantCode: "non_expiring_not_allowed"},
		{name: "a SAT needs an administrator", typ: TypeSAT, req: TTLRequest{Seconds: i64(0)}, allowNE: true, wantCode: "non_expiring_restricted"},
		{name: "a non-expiring SAT", typ: TypeSAT, req: TTLRequest{Seconds: i64(0)}, allowNE: true, admin: true, wantNE: true},
		{name: "never under the derivation rule", typ: TypeSAT, req: TTLRequest{Seconds: i64(0)}, allowNE: true, admin: true, derived: true, wantCode: "non_expiring_not_allowed"},
		{name: "the default is capped at the presenting token's expiry", typ: TypePAT, derived: true, parent: at(24 * time.Hour), wantTTL: 24 * time.Hour},
		{name: "a creation path with its own default", typ: TypePAT, req: TTLRequest{Default: 15 * time.Minute}, wantTTL: 15 * time.Minute},
	}
	for _, c := range cases {
		t.Run("R-AUTH-026 "+c.name, func(t *testing.T) {
			got, err := resolveLifetime(c.typ, c.req, now, def, maxTTL, c.allowNE, c.admin, c.derived, c.parent)
			if c.wantCode != "" {
				if code(err) != c.wantCode {
					t.Fatalf("err = %v, want %s", err, c.wantCode)
				}
				return
			}
			if err != nil || got.nonExpiring != c.wantNE || (!c.wantNE && got.ttl != c.wantTTL) {
				t.Fatalf("got %+v, %v; want ttl %s non-expiring %v", got, err, c.wantTTL, c.wantNE)
			}
		})
	}
}
