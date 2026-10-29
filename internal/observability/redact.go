package observability

import (
	"log/slog"
	"net/url"
	"regexp"
	"strings"
)

// RedactedValue replaces every secret in logs, spans and error texts (R-OPS-013).
const RedactedValue = "[REDACTED]"

// Secret is a secret held in memory (R-OPS-013 rule 1). Its LogValue, String,
// GoString, MarshalJSON and MarshalText all return the redaction marker; only
// Reveal returns the value, and only the code that needs to send it calls that.
type Secret string

// Reveal returns the secret itself.
func (s Secret) Reveal() string { return string(s) }

// String returns the redaction marker.
func (Secret) String() string { return RedactedValue }

// GoString returns the redaction marker.
func (Secret) GoString() string { return RedactedValue }

// LogValue implements slog.LogValuer.
func (Secret) LogValue() slog.Value { return slog.StringValue(RedactedValue) }

// MarshalJSON returns the redaction marker as a JSON string.
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + RedactedValue + `"`), nil }

// MarshalText returns the redaction marker.
func (Secret) MarshalText() ([]byte, error) { return []byte(RedactedValue), nil }

// sensitiveKeyParts are the substrings of attribute keys whose value is
// replaced (R-OPS-013 rule 2).
func sensitiveKeyParts() []string {
	return []string{"password", "secret", "token", "authorization", "cookie", "api_key", "apikey", "private_key", "credential"}
}

// exemptKey reports the ID fields that contain a sensitive word but carry no secret.
func exemptKey(k string) bool {
	return k == "token_id" || k == "token_type"
}

// SensitiveKey reports whether the value of an attribute with this key is
// redacted regardless of its content.
func SensitiveKey(key string) bool {
	k := strings.ToLower(key)
	if exemptKey(k) {
		return false
	}
	for _, p := range sensitiveKeyParts() {
		if strings.Contains(k, p) {
			return true
		}
	}
	return false
}

// Scrubber removes secrets from free text (R-OPS-013 rule 3).
type Scrubber struct {
	token    *regexp.Regexp
	bearer   *regexp.Regexp
	basic    *regexp.Regexp
	userinfo *regexp.Regexp
	cliPoll  *regexp.Regexp
	callback *regexp.Regexp
	idHint   *regexp.Regexp
	query    *regexp.Regexp
}

// DefaultQueryParams returns the default of log.request.redact_query_params.
func DefaultQueryParams() []string {
	return []string{"token", "access_token", "password", "secret", "sig", "signature", "X-Amz-Signature", "X-Amz-Credential", "X-Amz-Security-Token"}
}

// NewScrubber builds a scrubber that redacts the values of the given query
// parameters in addition to the fixed patterns.
func NewScrubber(queryParams []string) *Scrubber {
	s := &Scrubber{
		token:  regexp.MustCompile(`trv_([a-z]{2,8})_[0-9A-Za-z_\-]+`),
		bearer: regexp.MustCompile(`(?i)\bBearer\s+[^\s",;]+`),
		basic:  regexp.MustCompile(`(?i)\bBasic\s+[A-Za-z0-9+/=_\-]{8,}`),
		// The user information of a URL runs from "//" to the last "@" before
		// the authority ends (the first "/", "?", "#", "<", ">" or whitespace).
		// The class is greedy, so a password with an unencoded "@", a "'" or
		// a quote (as in the %q-quoted text of a URL parse error) is redacted
		// whole. Text that merely continues after a URL without a path, up to
		// a later "@", is redacted too: the rule errs towards hiding.
		userinfo: regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.\-]*://)([^/?#\s<>]*)@`),
		cliPoll:  regexp.MustCompile(`(/auth/cli/)[^/?#\s"']+`),
		// The query of an OIDC callback holds the authorization code and the
		// flow's state (and session_state, iss): one-time secrets of the login
		// that the configurable list cannot name without hiding the state filter
		// of the token listing too (Q-OPS-91).
		callback: regexp.MustCompile(`(/auth/oidc/[^/?#\s"']+/callback\?)[^#\s"']*`),
		// An ID token presented as a logout hint is a signed identity statement.
		idHint: regexp.MustCompile(`(?i)([?&;]id_token_hint)=[^&#\s"']*`),
	}
	if len(queryParams) > 0 {
		quoted := make([]string, len(queryParams))
		for i, p := range queryParams {
			quoted[i] = regexp.QuoteMeta(p)
		}
		s.query = regexp.MustCompile(`(?i)([?&;](?:` + strings.Join(quoted, "|") + `))=[^&#\s"']*`)
	}
	return s
}

var defaultScrubber = NewScrubber(DefaultQueryParams())

// Scrub removes secrets from s with the default query parameter list.
func Scrub(s string) string { return defaultScrubber.String(s) }

// String removes secrets from s.
func (s *Scrubber) String(in string) string {
	if in == "" {
		return in
	}
	out := in
	if strings.Contains(out, "trv_") {
		out = s.token.ReplaceAllString(out, "trv_${1}_"+RedactedValue)
	}
	if len(out) >= 6 {
		out = s.bearer.ReplaceAllString(out, "Bearer "+RedactedValue)
		out = s.basic.ReplaceAllString(out, "Basic "+RedactedValue)
	}
	if strings.Contains(out, "@") {
		out = s.userinfo.ReplaceAllStringFunc(out, s.redactUserinfo)
	}
	if strings.Contains(out, "/auth/cli/") {
		out = s.cliPoll.ReplaceAllString(out, "${1}"+RedactedValue)
	}
	if strings.Contains(out, "/callback?") {
		out = s.callback.ReplaceAllString(out, "${1}"+RedactedValue)
	}
	if strings.Contains(out, "id_token_hint") {
		out = s.idHint.ReplaceAllString(out, "${1}="+RedactedValue)
	}
	if s.query != nil && strings.ContainsAny(out, "?&;") {
		out = s.query.ReplaceAllString(out, "${1}="+RedactedValue)
	}
	return out
}

// redactUserinfo rewrites one match of the userinfo pattern: "user:secret@"
// keeps the user name, a bare "secret@" (a token in the user field) keeps
// nothing.
func (s *Scrubber) redactUserinfo(m string) string {
	sub := s.userinfo.FindStringSubmatch(m)
	if len(sub) != 3 {
		return m
	}
	if user, _, ok := strings.Cut(sub[2], ":"); ok {
		return sub[1] + user + ":" + RedactedValue + "@"
	}
	return sub[1] + RedactedValue + "@"
}

// URL renders u without user information and with redacted query parameters,
// the form of the url field of the upstream log (R-OPS-011).
func (s *Scrubber) URL(u *url.URL) string {
	if u == nil {
		return ""
	}
	c := *u
	c.User = nil
	return s.String(c.String())
}

// Error returns the scrubbed text of err, for errors that leave the process.
func (s *Scrubber) Error(err error) string {
	if err == nil {
		return ""
	}
	return s.String(err.Error())
}

// ScrubError returns the scrubbed text of err with the default parameter list.
func ScrubError(err error) string { return defaultScrubber.Error(err) }
