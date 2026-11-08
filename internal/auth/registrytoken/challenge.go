package registrytoken

import "strings"

// TokenPath is the path of the token service below the root of every host that serves the registry (R-OCI-003); a bound
// host has its own (R-OCI-029).
const TokenPath = "/v2/_token" //nolint:gosec // G101: a path, not a credential

// The error codes of a Bearer challenge (RFC 6750 §3.1) the registry uses.
const (
	// ErrorInvalidToken marks the challenge of a rejected credential.
	ErrorInvalidToken = "invalid_token"
	// ErrorInsufficientScope marks the challenge of a registry token whose grant lacks the route's action (R-OCI-004), so the
	// client fetches a new token for the scope the challenge names.
	ErrorInsufficientScope = "insufficient_scope"
)

// Realm is the realm of the token service for a response base URL (R-EP-003, without a trailing slash): "<base>/v2/_token".
// The service is the host of the base URL, with its port. An empty base URL gives ("", ""), which [Challenge] answers with the
// plain challenge of R-AUTH-002.
func Realm(baseURL string) (realm, service string) {
	base := strings.TrimSuffix(baseURL, "/")
	if base == "" {
		// No base URL resolved: a relative realm is one no client can follow; the challenge falls back to the plain one.
		return "", ""
	}
	host := base
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	return base + TokenPath, host
}

// Challenge renders the WWW-Authenticate value of a 401 below /v2/ (R-OCI-004 step 1):
//
//	Bearer realm="<base>/v2/_token",service="<host>"[,scope="<scope>"][,error="<code>"]
//
// scope is a scope entry ([RepositoryScope]) on a repository route, "" elsewhere; code is "", [ErrorInvalidToken] or
// [ErrorInsufficientScope]. Docker and containerd read realm, service and scope from it and ask the token service for that
// scope. An empty realm gives the plain challenge of R-AUTH-002, so a process that cannot name its base URL still answers.
func Challenge(realm, service, scope, code string) string {
	if realm == "" {
		return `Bearer realm="trove"`
	}
	var b strings.Builder
	b.WriteString(`Bearer realm=`)
	b.WriteString(quote(realm))
	if service != "" {
		b.WriteString(`,service=`)
		b.WriteString(quote(service))
	}
	if scope != "" {
		b.WriteString(`,scope=`)
		b.WriteString(quote(scope))
	}
	if code != "" {
		b.WriteString(`,error=`)
		b.WriteString(quote(code))
	}
	return b.String()
}

// quote renders a quoted-string of RFC 9110 §5.6.4. The values come from the configuration and from names the grammars
// admitted, so an escape is defence only; a control character is dropped.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c < 0x20 || c == 0x7f:
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}
