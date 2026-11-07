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
	web := linkBuilder{base: "https://trove.example.com", format: "npm", key: qname.Repo{Project: "web", Key: "npm-all"}}
	ops := linkBuilder{base: "https://trove.example.com", format: "npm", key: qname.Repo{Project: "ops", Key: "npm-all"}}
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
