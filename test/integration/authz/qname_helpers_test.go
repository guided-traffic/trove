package authz

import "github.com/guided-traffic/trove/internal/qname"

// qr is the name of a repository: "<project>/<key>", or a bare key of the project "it" the tests use; "" is the
// zero name (no repository).
func qr(key string) qname.Repo {
	if key == "" {
		return qname.Repo{}
	}
	if n, err := qname.ParseRepo(key); err == nil {
		return n
	}
	return qname.Repo{Project: "it", Key: key}
}

// qrs is qr for a list.
func qrs(keys []string) []qname.Repo {
	out := make([]qname.Repo, len(keys))
	for i, k := range keys {
		out[i] = qr(k)
	}
	return out
}
