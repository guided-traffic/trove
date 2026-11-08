package oci

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// The token handshake of a registry (R-OCI-021): the upstream answers 401 with `WWW-Authenticate: Bearer realm=…,service=…`, the
// client asks the realm for a token for the scope it needs and repeats the request with `Authorization: Bearer <token>`. The format
// reads the protocol; the remote engine does the I/O, applies the host rules (the realm must be the upstream or an extraHosts entry,
// the credential goes to it only when the host may have it, R-REPO-033) and keeps the token in the replica's memory (R-OCI-032).

const (
	// MaxTokenBytes bounds a token of an upstream's realm: a longer one is refused (R-OCI-020).
	MaxTokenBytes = 16 << 10
	// maxChallengeBytes bounds the challenge that is read.
	maxChallengeBytes = 8 << 10
	// catalogScope is the scope of the catalog request.
	catalogScope = "registry:catalog:*"
)

// challengeParams is one challenge of a WWW-Authenticate field: its scheme and its parameters, names in lower case.
type challengeParams struct {
	scheme string
	params map[string]string
}

func isChallengeToken(c byte) bool {
	switch {
	case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		return true
	}
	return strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}

// parseChallenges reads the challenges of one WWW-Authenticate value (RFC 9110 11.6.1): a scheme, then comma-separated
// name=value parameters whose value is a token or a quoted string. A value above the bound reads as none.
func parseChallenges(s string) []challengeParams {
	if len(s) > maxChallengeBytes {
		return nil
	}
	var out []challengeParams
	i := 0
	skip := func() {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == ',') {
			i++
		}
	}
	for {
		skip()
		if i >= len(s) {
			return out
		}
		j := i
		for j < len(s) && isChallengeToken(s[j]) {
			j++
		}
		if j == i {
			return out // not a token: the rest is not a challenge
		}
		tok := s[i:j]
		i = j
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) || s[i] != '=' || len(out) == 0 {
			out = append(out, challengeParams{scheme: strings.ToLower(tok), params: map[string]string{}})
			continue
		}
		i++ // '='
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		var val strings.Builder
		if i < len(s) && s[i] == '"' {
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				val.WriteByte(s[i])
				i++
			}
			i++ // closing quote
		} else {
			for i < len(s) && s[i] != ',' && s[i] != ' ' && s[i] != '\t' {
				val.WriteByte(s[i])
				i++
			}
		}
		last := &out[len(out)-1]
		if _, set := last.params[strings.ToLower(tok)]; !set {
			last.params[strings.ToLower(tok)] = val.String()
		}
	}
}

// bearerChallenge finds the Bearer challenge of the answer: the realm and the service. The scope the challenge names is not read,
// because the remembered challenge of an earlier request can be the one handed in (Q-OCI-82).
func bearerChallenge(h http.Header) (realm, service string, ok bool) {
	for _, v := range h.Values("Www-Authenticate") {
		for _, c := range parseChallenges(v) {
			if c.scheme == "bearer" && c.params["realm"] != "" {
				return c.params["realm"], c.params["service"], true
			}
		}
	}
	return "", "", false
}

// scopeOf is the scope of a registry token for a request, taken from the request and never from the challenge: the repository the
// request addresses for a fetch of a document or an artifact (path), and the upstream request for the catalog and for a tags list or
// a referrers query, which have no repository path (R-OCI-021, Q-OCI-82). "" is no scope: the request needs none.
func scopeOf(rc formats.RemoteContext, orig *http.Request, p formats.Path) string {
	if t, err := parseTarget(p); err == nil {
		return "repository:" + upstreamName(rc.Upstream, t.name) + ":pull"
	}
	if orig == nil || orig.URL == nil {
		return ""
	}
	rest, ok := strings.CutPrefix(orig.URL.EscapedPath(), "/v2/")
	if !ok {
		return ""
	}
	if rest == "_catalog" {
		return catalogScope
	}
	// The protocol words are read from the right: an image name may itself hold a component that is one (a/manifests/b is a valid
	// name), so the first word from the left is not the end of the name.
	segs := strings.Split(rest, "/")
	n := len(segs)
	switch {
	case n >= 3 && segs[n-2] == wordTags && segs[n-1] == wordList:
		return "repository:" + strings.Join(segs[:n-2], "/") + ":pull"
	case n >= 3 && (segs[n-2] == wordManifests || segs[n-2] == wordBlobs || segs[n-2] == wordReferrers):
		return "repository:" + strings.Join(segs[:n-2], "/") + ":pull"
	}
	return ""
}

// TokenRequest reads the upstream's Bearer challenge: the realm request for the scope of orig and p. The realm and the service come
// from the challenge; the scope, and so the key the token is cached under, come from the request. A challenge that is no Bearer
// challenge (Basic is answered by the credential alone) is no handshake.
func (remoteAdapter) TokenRequest(ctx context.Context, rc formats.RemoteContext, orig *http.Request, p formats.Path, _ int, challenge http.Header) (*http.Request, string, bool, error) {
	realm, service, ok := bearerChallenge(challenge)
	if !ok {
		return nil, "", false, nil
	}
	u, err := url.Parse(realm)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, "", false, errs.E(errs.UpstreamFailed, errs.CodeUpstreamError, "The upstream's token realm is no usable address.")
	}
	scope := scopeOf(rc, orig, p)
	q := u.Query()
	if service != "" {
		q.Set("service", service)
	}
	if scope != "" {
		q.Set("scope", scope)
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", false, errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(err))
	}
	req.Header.Set("Accept", "application/json")
	return req, "scope=" + scope, true, nil
}

// ReadToken reads the realm's 2xx answer: `{"token":…}` or `{"access_token":…}` and, when it names one, `expires_in` in seconds.
// A token that is empty, longer than 16 KiB or not a header field value is refused.
func (remoteAdapter) ReadToken(_ context.Context, _ formats.RemoteContext, _ int, body []byte) (string, time.Duration, error) {
	var doc struct {
		Token       string  `json:"token"`
		AccessToken string  `json:"access_token"`
		ExpiresIn   float64 `json:"expires_in"`
	}
	if err := untrusted.UnmarshalJSON(body, untrusted.Limits{DocumentBytes: 64 << 10}, &doc); err != nil {
		return "", 0, errs.E(errs.UpstreamFailed, errs.CodeUpstreamError, "The upstream's token service answered with something that is no token document.")
	}
	tok := doc.Token
	if tok == "" {
		tok = doc.AccessToken
	}
	switch {
	case tok == "":
		return "", 0, errs.E(errs.UpstreamFailed, errs.CodeUpstreamError, "The upstream's token service answered without a token.")
	case len(tok) > MaxTokenBytes:
		return "", 0, errs.E(errs.UpstreamFailed, errs.CodeUpstreamError, "The upstream's token is longer than 16 KiB.")
	}
	for i := 0; i < len(tok); i++ {
		if tok[i] <= ' ' || tok[i] >= 0x7f {
			return "", 0, errs.E(errs.UpstreamFailed, errs.CodeUpstreamError, "The upstream's token is not a header value.")
		}
	}
	var ttl time.Duration
	if doc.ExpiresIn > 0 && doc.ExpiresIn < float64(24*time.Hour/time.Second) {
		ttl = time.Duration(doc.ExpiresIn * float64(time.Second))
	}
	return "Bearer " + tok, ttl, nil
}
