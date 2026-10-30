package scope

import (
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

func TestR_AUTH_024_ParseScope(t *testing.T) {
	opts := Options{Action: func(a string) bool {
		return slices.Contains([]string{"read", "annotate", "deploy", "overwrite", "delete", "manage"}, a)
	}, SystemItem: func(s string) bool { return s == "identities:read" }, Endpoint: func(n string) bool { return n == "default" || n == "customers" }}
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
			if _, err := Parse([]byte(doc), opts); err != nil {
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
			_, err := Parse([]byte(doc), opts)
			if e, isErr := errs.As(err); !isErr || e.Code != errs.CodeInvalidScope || e.Status() != 400 {
				t.Fatalf("err = %v, want 400 invalid_scope", err)
			}
		})
	}
	t.Run("R-AUTH-024 no system item is accepted without a checker", func(t *testing.T) {
		if _, err := Parse([]byte(`{"system":["identities:read"]}`), Options{}); err == nil {
			t.Fatal("accepted")
		}
	})
}

func TestR_AUTH_024_MarshalRoundTrip(t *testing.T) {
	doc := `{"inherit":false,"admin":false,"repositories":[{"repositories":["npm-local"],"packages":{"include":["@acme/*"]},"actions":["read"]}],"system":[]}`
	s, err := Parse([]byte(doc), Options{Action: func(string) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(s.Marshal()); got != doc {
		t.Fatalf("Marshal = %s", got)
	}
	back, err := ParseStored(s.Marshal())
	if err != nil || len(back.Repositories) != 1 || back.Repositories[0].Packages.Include[0] != "@acme/*" {
		t.Fatalf("%+v %v", back, err)
	}
	if got := string(Default().Marshal()); got != `{"inherit":true,"admin":false,"repositories":[],"system":[]}` {
		t.Fatalf("default scope renders as %s", got)
	}
}

func TestR_AUTH_027_ScopeWithin(t *testing.T) {
	read := Scope{Repositories: []Grant{{Repositories: []string{"npm-local"}, Actions: []string{"read", "deploy"}}}}
	cases := []struct {
		name          string
		child, parent Scope
		want          bool
	}{
		{"an equal scope", read, read, true},
		{"fewer actions", Scope{Repositories: []Grant{{Repositories: []string{"npm-local"}, Actions: []string{"read"}}}}, read, true},
		{"more actions", Scope{Repositories: []Grant{{Repositories: []string{"npm-local"}, Actions: []string{"read", "delete"}}}}, read, false},
		{"another repository", Scope{Repositories: []Grant{{Repositories: []string{"npm-other"}, Actions: []string{"read"}}}}, read, false},
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
