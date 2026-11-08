package registrytoken

import (
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/qname"
)

// validName is the image-name grammar of R-OCI-002 (the format's, internal/formats/oci, which this package may not import).
var validName = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`).MatchString

// R-OCI-004: named in the test name by WP3 once /v2/ is mounted and the grant check runs.
func TestScopeGrammar(t *testing.T) {
	t.Run("repository entries name project, repository and image; several per value; actions after the last colon", func(t *testing.T) {
		got, err := ParseScopes([]string{
			"repository:web/docker-local/app:pull,push",
			"repository:web/docker/library/nginx:pull repository:web/docker-local/team/app:*",
			"registry:catalog:*",
		}, qname.Repo{}, validName)
		if err != nil {
			t.Fatal(err)
		}
		want := []Scope{
			{Repo: webRepo, Name: "app", Actions: Pull | Push},
			{Repo: qname.Repo{Project: "web", Key: "docker"}, Name: "library/nginx", Actions: Pull},
			{Repo: webRepo, Name: "team/app", Actions: All},
			{Catalog: true},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("entries trove cannot grant are skipped, not refused", func(t *testing.T) {
		got, err := ParseScopes([]string{
			"repository(plugin):web/docker-local/app:pull", // a resource class
			"repository:web/docker-local/App:pull",         // upper case
			"repository:web/docker-local:pull",             // no image name
			"repository:Web/docker-local/app:pull",         // project key outside its grammar
			"repository:web/docker-local/app:fly",          // no known action
			"repository:web/docker-local/a%2Fb:pull",       // an escape
			"registry:catalog:pull",                        // the catalog asks for "*"
			"registry:other:*",
			"something",
			"repository:web/docker-local/" + strings.Repeat("a", MaxFullNameBytes) + ":pull", // longer than 255 bytes
		}, qname.Repo{}, validName)
		if err != nil || len(got) != 0 {
			t.Fatalf("got %+v %v", got, err)
		}
	})
	t.Run("a name with a colon keeps it: the actions follow the last colon", func(t *testing.T) {
		got, _ := ParseScopes([]string{"repository:web/docker-local/a:b:pull"}, qname.Repo{}, func(string) bool { return true })
		if len(got) != 1 || got[0].Name != "a:b" || got[0].Actions != Pull {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("more than 16 entries answer the refusal, counted over every scope parameter", func(t *testing.T) {
		var vals []string
		for i := 0; i < 9; i++ {
			vals = append(vals, "repository:web/docker-local/app:pull repository:web/docker-local/other:pull")
		}
		if _, err := ParseScopes(vals, qname.Repo{}, validName); !errors.Is(err, ErrTooManyScopes) {
			t.Fatalf("err = %v", err)
		}
		if _, err := ParseScopes(vals[:8], qname.Repo{}, validName); err != nil {
			t.Fatalf("16 entries: %v", err)
		}
	})
	t.Run("the cache view of a remote repository is granted under its base repository", func(t *testing.T) {
		got, err := ParseScopes([]string{"repository:web/docker-hub~cache/library/alpine:pull"}, qname.Repo{}, validName)
		if err != nil || len(got) != 1 || got[0].Repo != (qname.Repo{Project: "web", Key: "docker-hub"}) || got[0].Name != "library/alpine" {
			t.Fatalf("got %+v %v", got, err)
		}
	})
	// R-OCI-029: named in the test name by WP3 once bound hosts are mounted.
	t.Run("on a bound host the entry names the image without the repository's prefix", func(t *testing.T) {
		hub := qname.Repo{Project: "web", Key: "docker-hub"}
		got, err := ParseScopes([]string{"repository:library/alpine:pull"}, hub, validName)
		if err != nil || len(got) != 1 || got[0].Repo != hub || got[0].Name != "library/alpine" {
			t.Fatalf("got %+v %v", got, err)
		}
	})
	t.Run("the challenge's scope entry is rendered in the same grammar", func(t *testing.T) {
		if s := RepositoryScope(FullName(webRepo, "app"), Push|Pull); s != "repository:web/docker-local/app:pull,push" {
			t.Fatalf("scope %q", s)
		}
	})
}

// R-OCI-004: named in the test name by WP3 once /v2/ is mounted and the grant check runs.
func TestChallenge(t *testing.T) {
	realm, service := Realm("https://trove.example.com:8443/")
	if realm != "https://trove.example.com:8443/v2/_token" || service != "trove.example.com:8443" {
		t.Fatalf("realm %q service %q", realm, service)
	}
	for _, c := range []struct{ scope, code, want string }{
		{"", "", `Bearer realm="https://trove.example.com:8443/v2/_token",service="trove.example.com:8443"`},
		{
			"repository:web/docker-local/app:push", ErrorInsufficientScope,
			`Bearer realm="https://trove.example.com:8443/v2/_token",service="trove.example.com:8443",scope="repository:web/docker-local/app:push",error="insufficient_scope"`,
		},
		{"", ErrorInvalidToken, `Bearer realm="https://trove.example.com:8443/v2/_token",service="trove.example.com:8443",error="invalid_token"`},
	} {
		if got := Challenge(realm, service, c.scope, c.code); got != c.want {
			t.Errorf("got  %s\nwant %s", got, c.want)
		}
	}
	if got := Challenge("", "", "x", ""); got != `Bearer realm="trove"` {
		t.Errorf("without a realm: %s", got)
	}
	if realm, service := Realm(""); realm != "" || service != "" {
		t.Errorf("without a base URL: realm %q service %q, want the plain challenge", realm, service)
	}
	if realm, service := Realm(""); Challenge(realm, service, "x", ErrorInsufficientScope) != `Bearer realm="trove"` {
		t.Errorf("without a base URL the challenge is not the plain one")
	}
	if got := Challenge(`a"b\c`+"\n", "", "", ""); got != `Bearer realm="a\"b\\c"` {
		t.Errorf("quoting: %s", got)
	}
}

// R-OCI-005: named in the test name by WP3 once /v2/ is mounted and the grant check runs.
func TestGrantAllows(t *testing.T) {
	g := Grant{Entries: []Entry{{Repo: webRepo, Name: "app", Actions: Pull}}}
	g.Add(Entry{Repo: webRepo, Name: "app", Actions: Push})
	other := qname.Repo{Project: "web", Key: "team-local"}
	switch {
	case len(g.Entries) != 1:
		t.Fatalf("two entries of one name were not merged: %+v", g.Entries)
	case !g.Allows(webRepo, "app", Pull), !g.Allows(webRepo, "app", Push):
		t.Fatal("a granted action is refused")
	case g.Allows(webRepo, "app", Delete):
		t.Fatal("an action outside the grant is allowed")
	case g.Allows(webRepo, "other", Pull):
		t.Fatal("another image name is allowed")
	case g.Allows(other, "app", Pull):
		t.Fatal("another repository is allowed")
	case g.Allows(webRepo, "app", 0):
		t.Fatal("the empty action is allowed")
	}
	if All.String() != "pull,push,delete" || Actions(0).String() != "" {
		t.Errorf("String: %q", All.String())
	}
}
