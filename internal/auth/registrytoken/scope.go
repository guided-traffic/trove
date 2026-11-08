package registrytoken

import (
	"errors"
	"strings"

	"github.com/guided-traffic/trove/internal/qname"
)

// MaxFullNameBytes bounds the name of a repository scope entry, "<projectKey>/<repoKey>/<name>" as a client sends it
// (R-OCI-002).
const MaxFullNameBytes = 255

// ErrTooManyScopes is the refusal of a token request with more than [MaxEntries] scope entries (R-OCI-004: 400
// invalid_request).
var ErrTooManyScopes = errors.New("registrytoken: a token request carries at most 16 scope entries")

// Scope is one entry of a token request that names something trove can grant: an image name of a repository with the
// actions asked for, or the catalog.
type Scope struct {
	// Catalog is the entry registry:catalog:* (R-OCI-017).
	Catalog bool
	Repo    qname.Repo
	Name    string
	Actions Actions
}

// ParseScopes reads the scope parameters of a token request in the grammar of the distribution token protocol
// (https://distribution.github.io/distribution/spec/auth/scope/):
//
//	scope        = resourcetype [ "(" resourceclass ")" ] ":" resourcename ":" action *( "," action )
//
// Each value may hold several entries separated by spaces (the OAuth 2.0 form of the POST request sends them so; Docker sends
// one scope parameter per entry). The resource name may contain ':', so the actions are what follows the last one.
//
// Every entry read counts toward [MaxEntries]; more answer [ErrTooManyScopes]. An entry trove cannot grant - another
// resource type or class, a name outside the grammar, no known action - is skipped and grants nothing, because a partial or
// empty grant is not an error (R-OCI-004). On a host whose root is bound to a repository (bound is not the zero value), a
// repository entry names the image without the repository's prefix (R-OCI-029); elsewhere its first two segments are the
// project key and the repository key (R-OCI-001), and a repository key with the cache suffix (<key>~cache) names its base
// repository. validName is the image-name grammar of the format (R-OCI-002).
func ParseScopes(values []string, bound qname.Repo, validName func(string) bool) ([]Scope, error) {
	var out []Scope
	n := 0
	for _, v := range values {
		for _, entry := range strings.Fields(v) {
			n++
			if n > MaxEntries {
				return nil, ErrTooManyScopes
			}
			if s, ok := parseScope(entry, bound, validName); ok {
				out = append(out, s)
			}
		}
	}
	return out, nil
}

func parseScope(entry string, bound qname.Repo, validName func(string) bool) (Scope, bool) {
	typ, rest, ok := strings.Cut(entry, ":")
	i := strings.LastIndexByte(rest, ':')
	if !ok || i <= 0 {
		return Scope{}, false
	}
	name, actions := rest[:i], parseActions(rest[i+1:])
	switch typ {
	case "registry":
		// The catalog is the one registry resource; it is granted with any action list that names "*".
		return Scope{Catalog: true}, name == "catalog" && actions == All
	case "repository":
	default:
		return Scope{}, false
	}
	if actions == 0 || len(name) > MaxFullNameBytes {
		return Scope{}, false
	}
	repo, image := bound, name
	if bound.IsZero() {
		project, tail, ok1 := strings.Cut(name, "/")
		key, img, ok2 := strings.Cut(tail, "/")
		if !ok1 || !ok2 {
			return Scope{}, false
		}
		// The cache view of a remote repository (<key>~cache, R-REPO-032) is decided and served on the remote repository
		// itself, so its entry names the base repository; the enforcement point maps the address the same way.
		key = strings.TrimSuffix(key, qname.CacheSuffix)
		repo, image = qname.Repo{Project: project, Key: key}, img
	} else if len(FullName(bound, image)) > MaxFullNameBytes {
		return Scope{}, false
	}
	if !repo.Valid() || validName == nil || !validName(image) {
		return Scope{}, false
	}
	return Scope{Repo: repo, Name: image, Actions: actions}, true
}

// RepositoryScope renders the scope entry of an image name for a challenge (R-OCI-004 step 1): "repository:<name>:<actions>",
// with the full name on an unbound host and the image name alone on a bound one (R-OCI-029).
func RepositoryScope(name string, a Actions) string { return "repository:" + name + ":" + a.String() }
