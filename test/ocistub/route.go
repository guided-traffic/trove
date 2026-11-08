package ocistub

import (
	"regexp"
	"strings"
)

// Kinds of request the stub tells apart; each is also the class of the request for delays, failures and throttles.
const (
	KindBase      = ClassBase      // GET /v2/
	KindCatalog   = ClassCatalog   // GET /v2/_catalog
	KindTags      = ClassTags      // GET /v2/<name>/tags/list
	KindManifest  = ClassManifest  // /v2/<name>/manifests/<reference>
	KindBlob      = ClassBlob      // /v2/<name>/blobs/<digest>
	KindReferrers = ClassReferrers // GET /v2/<name>/referrers/<digest>
	KindToken     = ClassToken     // GET /token on the realm host
	KindCDN       = ClassCDN       // GET /cdn/<digest> on the CDN host
	KindOther     = ClassOther
)

// Paths outside /v2/.
const (
	tokenPath     = "/token"
	cdnPrefix     = "/cdn/"
	v2Prefix      = "/v2/"
	controlPrefix = "/-/stub/"
)

// route is what a request path addresses.
type route struct {
	kind string // a Kind constant
	name string // the repository
	ref  string // a tag or a digest (manifests), a digest (blobs, referrers, CDN)
	bad  bool   // the repository name is not valid
}

var (
	nameRE   = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
	digestRE = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// parseRoute classifies a request path (not escaped: repository names have no characters that need escaping).
func parseRoute(path string) route {
	switch {
	case path == tokenPath:
		return route{kind: KindToken}
	case strings.HasPrefix(path, cdnPrefix):
		return route{kind: KindCDN, ref: strings.TrimPrefix(path, cdnPrefix)}
	case path == "/v2" || path == v2Prefix:
		return route{kind: KindBase}
	case !strings.HasPrefix(path, v2Prefix):
		return route{kind: KindOther}
	}
	rest := strings.TrimPrefix(path, v2Prefix)
	if rest == "_catalog" {
		return route{kind: KindCatalog}
	}
	seg := strings.Split(rest, "/")
	n := len(seg)
	named := func(kind string, tail int, ref string) route {
		name := strings.Join(seg[:n-tail], "/")
		return route{kind: kind, name: name, ref: ref, bad: !nameRE.MatchString(name)}
	}
	switch {
	case n >= 3 && seg[n-2] == "manifests":
		return named(KindManifest, 2, seg[n-1])
	case n >= 3 && seg[n-2] == "blobs" && seg[n-1] != "uploads":
		return named(KindBlob, 2, seg[n-1])
	case n >= 3 && seg[n-2] == "referrers":
		return named(KindReferrers, 2, seg[n-1])
	case n >= 3 && seg[n-2] == "tags" && seg[n-1] == "list":
		return named(KindTags, 2, "")
	}
	return route{kind: KindOther}
}
