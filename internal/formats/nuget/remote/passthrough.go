package remote

import (
	"bytes"
	"encoding/json"
	"strings"
)

// The deprecation and the vulnerabilities of an upstream's catalog entry are passed through to
// the client, which shows them to a person (R-NUGET-015), without the addresses of the upstream
// (Q-NUGET-85). nuget.org writes them as JSON-LD: the deprecation, each advisory and the alternate
// package carry an "@id" in the upstream's catalog or registration, and an "@type". No client
// reads those; they are dropped at every depth. What a client reads stays: the reasons, the
// message and the alternate package's id and range of a deprecation, the severity of an advisory
// and its advisoryUrl. advisoryUrl is a web link for people, like projectUrl, which trove never
// follows: it is kept when it is an absolute http or https address without user info, and dropped
// otherwise. Any other member that holds an address is dropped, as an unknown member of the entry
// is (Q-NUGET-52).

// passThrough returns the member cleaned as above, compact and with the keys of every object in
// order, or nil when it is absent, null or nothing is left of it. advisories says the member is the
// vulnerabilities list, whose objects may keep an advisoryUrl.
func passThrough(raw json.RawMessage, advisories bool) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil
	}
	v, keep := scrub(v, advisories, 0)
	if !keep {
		return nil
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil
	}
	return bytes.TrimRight(b.Bytes(), "\n")
}

// scrub cleans a value; keep is false for a value that holds an address, or an object or a list
// that is empty once cleaned. depth is 0 for the member itself: an advisoryUrl is honoured only in
// an object of the vulnerabilities list, at depth 1.
func scrub(v any, advisories bool, depth int) (any, bool) {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			switch {
			case strings.HasPrefix(k, "@"):
				continue
			case k == "advisoryUrl" && advisories && depth == 1:
				if s, ok := val.(string); ok && webLink(s) {
					out[k] = strings.TrimSpace(s)
				}
				continue
			}
			if c, ok := scrub(val, advisories, depth+1); ok {
				out[k] = c
			}
		}
		return out, len(out) > 0
	case []any:
		out := make([]any, 0, len(x))
		for _, val := range x {
			if c, ok := scrub(val, advisories, depth+1); ok {
				out = append(out, c)
			}
		}
		return out, len(out) > 0
	case string:
		return x, !looksLikeAddress(x)
	}
	return v, true
}

// webLink reports whether a string is an address a person may follow: absolute http or https,
// with a host and without user info.
func webLink(s string) bool {
	_, err := ContentURL(s)
	return err == nil
}

// looksLikeAddress reports whether a string starts like an absolute http or https address.
func looksLikeAddress(s string) bool {
	l := strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
}
