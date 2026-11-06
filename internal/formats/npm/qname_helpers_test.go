package npm

import "github.com/guided-traffic/trove/internal/qname"

// qr is the name of a repository key in the project the unit tests use.
func qr(key string) qname.Repo { return qname.Repo{Project: "it", Key: key} }

// qrs is qr for a list.
func qrs(keys []string) []qname.Repo {
	out := make([]qname.Repo, len(keys))
	for i, k := range keys {
		out[i] = qr(k)
	}
	return out
}
