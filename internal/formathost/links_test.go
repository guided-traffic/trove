package formathost

import (
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/qname"
)

// Every URL a format returns to a client carries the project: two repositories with the same
// key in different projects get different links, and the project is never part of the key's
// escaping (R-REPO-001, R-ARCH-007).
func TestR_REPO_001_LinksCarryTheProjectOfTheRepository(t *testing.T) {
	web := linkBuilder{base: "https://trove.example.com", template: "/api/npm/{project}/{key}/", key: qname.Repo{Project: "web", Key: "npm-all"}}
	ops := linkBuilder{base: "https://trove.example.com", template: "/api/npm/{project}/{key}/", key: qname.Repo{Project: "ops", Key: "npm-all"}}
	var _ formats.LinkBuilder = web
	for _, tc := range []struct {
		name      string
		got, want string
	}{
		{"repository of web", web.Repository(), "https://trove.example.com/api/npm/web/npm-all/"},
		{"repository of ops", ops.Repository(), "https://trove.example.com/api/npm/ops/npm-all/"},
		{"path", web.Path("@acme/ui/-/ui-1.0.0.tgz"), "https://trove.example.com/api/npm/web/npm-all/@acme/ui/-/ui-1.0.0.tgz"},
		{"the cache view", web.Registry(qname.Repo{Project: "web", Key: "npm-remote" + qname.CacheSuffix}), "https://trove.example.com/api/npm/web/npm-remote~cache/"},
		{"a raw file", web.File(qname.Repo{Project: "web", Key: "generic-local"}, "a b/c"), "https://trove.example.com/files/web/generic-local/a%20b/c"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, tc.got, tc.want)
		}
	}
}

// The link builder renders the base path of the format: the registry root's /v2/ one for the format that declares it (Q-OCI-4).
func TestR_ARCH_007_LinksFollowTheBasePathOfTheFormat(t *testing.T) {
	l := linkBuilder{base: "https://trove.example.com", template: "/v2/{project}/{key}/", key: qname.Repo{Project: "web", Key: "docker"}}
	for got, want := range map[string]string{
		l.Repository():                "https://trove.example.com/v2/web/docker/",
		l.Path("app/blobs/uploads/x"): "https://trove.example.com/v2/web/docker/app/blobs/uploads/x",
		l.File(l.key, "app/_tags/1"):  "https://trove.example.com/files/web/docker/app/_tags/1",
	} {
		if got != want {
			t.Errorf("%s, want %s", got, want)
		}
	}
}

// On a host whose root is bound to one repository the registry has no project and key in its path and the image name is the bare
// name; on the shared host the repository is part of the name (R-EP-006, R-OCI-029, ADR-161).
func TestLinksOnABoundHostOmitTheRepository(t *testing.T) {
	key := qname.Repo{Project: "web", Key: "docker"}
	shared := linkBuilder{base: "https://trove.example.com", template: "/v2/{project}/{key}/", key: key}
	bound := shared
	bound.bound = true
	for got, want := range map[string]string{
		shared.Registry(key):         "https://trove.example.com/v2/web/docker/",
		shared.ClientName("app"):     "web/docker/app",
		bound.Registry(key):          "https://trove.example.com/v2/",
		bound.ClientName("team/app"): "team/app",
	} {
		if got != want {
			t.Errorf("%s, want %s", got, want)
		}
	}
}
