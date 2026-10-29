package config

import (
	"net/url"
	"strings"
)

// URL-valued keys never carry credentials (S13 of the phase-1 review, R-OPS-013):
// a value in user information is printed by `trove admin config validate`,
// served by GET /api/v1/system/config and copied into error texts, while every
// credential has its own `_file` key. Sections call rejectUserinfo from their
// Validate for each URL-valued key; Effective redacts user information of every
// value defensively, so a key that forgot the check still prints no password.

// rejectUserinfo reports key when raw is a URL with user information. The
// message carries the value with the user information redacted, never the
// value itself, and names where the credential belongs (credentialKey, may be
// empty).
func rejectUserinfo(r *Report, key, raw, credentialKey string) {
	if !hasUserinfo(raw) {
		return
	}
	hint := ""
	if credentialKey != "" {
		hint = "; put the credential in " + credentialKey
	}
	r.Add(key, "%q must not carry user information%s", redactUserinfo(raw), hint)
}

// hasUserinfo reports whether raw holds a scheme://user[:password]@ part.
func hasUserinfo(raw string) bool {
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		return true
	}
	_, _, ok := userinfoSpan(raw)
	return ok
}

// userinfoSpan locates the user information of the first URL in s textually,
// for values url.Parse refuses (an unencoded "@" or quote in the password):
// from the "//" after a scheme to the last "@" before the authority ends.
func userinfoSpan(s string) (start, end int, ok bool) {
	i := strings.Index(s, "://")
	if i <= 0 {
		return 0, 0, false
	}
	start = i + 3
	stop := len(s)
	if j := strings.IndexAny(s[start:], "/?#< \t\r\n"); j >= 0 {
		stop = start + j
	}
	at := strings.LastIndexByte(s[start:stop], '@')
	if at < 0 {
		return 0, 0, false
	}
	return start, start + at, true
}

// redactUserinfo replaces the password of the user information of raw by
// Redacted, or the whole user information when there is no ":" (a token in
// the user field). Text without user information is returned unchanged.
func redactUserinfo(raw string) string {
	start, end, ok := userinfoSpan(raw)
	if !ok {
		return raw
	}
	info := raw[start:end]
	repl := Redacted
	if user, _, found := strings.Cut(info, ":"); found {
		repl = user + ":" + Redacted
	}
	return raw[:start] + repl + raw[end:]
}

// redactDisplay applies redactUserinfo to a value of the effective
// configuration: strings and the strings of lists and maps.
func redactDisplay(v any) any {
	switch x := v.(type) {
	case string:
		return redactUserinfo(x)
	case []string:
		out := make([]string, len(x))
		for i, s := range x {
			out[i] = redactUserinfo(s)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(x))
		for k, s := range x {
			out[k] = redactUserinfo(s)
		}
		return out
	}
	return v
}
