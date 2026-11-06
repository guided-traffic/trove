package authz

import "github.com/guided-traffic/trove/internal/qname"

// testProject is the project key of a fixture repository: the repositories of the project fixtures live in their
// projects, every other one in the project "it".
func testProject(key string) string {
	switch key {
	case "web-npm", "web-files", "npm-web":
		return "web"
	case "shop-npm":
		return "shop"
	}
	return "it"
}

// qr is the name of a repository key of the fixtures.
func qr(key string) qname.Repo { return qname.Repo{Project: testProject(key), Key: key} }

// qrs is qr for a list.
func qrs(keys []string) []qname.Repo {
	out := make([]qname.Repo, len(keys))
	for i, k := range keys {
		out[i] = qr(k)
	}
	return out
}

// qkeysErr is qkeys of a result with its error.
func qkeysErr(names []qname.Repo, err error) ([]string, error) { return qkeys(names), err }

// qkeys is the keys of a list of names.
func qkeys(names []qname.Repo) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = n.Key
	}
	return out
}
