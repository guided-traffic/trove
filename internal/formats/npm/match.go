package npm

import (
	"net/url"
	"slices"
	"strings"

	"github.com/guided-traffic/trove/internal/formats"
)

// The grammar of the paths below a registry URL (R-NPM-001). The matchers work on
// the escaped path, split at the literal "/" first and decoded segment by
// segment, so a "%2f" inside a segment never becomes a separator; the package name
// they find is handed on still escaped, and [Format.Match] decodes it exactly once
// with name.ParseEscaped (R-NPM-002).
//
//	{name}                         the package document
//	{name}/{version-or-tag}        a single version
//	{name}/-/{file}                a tarball
//	{name}/-rev/{rev}              revisioned writes
//	{name}/-/{file}/-rev/{rev}
//	-/ping  -/whoami               protocol operations, the first segment "-"
//	-/package/{name}/dist-tags[/{tag}]
//
// {name} is one segment for an unscoped name; for a name that starts with "@" it
// is one segment that holds "%2f", or two segments.

// segments splits an escaped repository-relative path; a trailing "/" yields an
// empty last segment, which no matcher accepts.
func segments(escaped string) []string {
	return strings.Split(strings.TrimPrefix(escaped, "/"), "/")
}

// decode decodes one escaped segment; an empty or malformed segment is no match.
func decode(seg string) (string, bool) {
	d, err := url.PathUnescape(seg)
	return d, err == nil && d != ""
}

// splitName takes the segments that spell a package name off the front of segs
// and returns the name as written (escaped, the segments joined with "/") and the
// segments that remain. The first segment "-" belongs to the protocol operations,
// not to a name.
func splitName(segs []string) (escaped string, rest []string, ok bool) {
	if len(segs) == 0 {
		return "", nil, false
	}
	first, ok := decode(segs[0])
	if !ok || first == "-" {
		return "", nil, false
	}
	if strings.HasPrefix(first, "@") && !strings.Contains(first, "/") && len(segs) > 1 {
		if _, ok := decode(segs[1]); !ok {
			return "", nil, false
		}
		return segs[0] + "/" + segs[1], segs[2:], true
	}
	return segs[0], segs[1:], true
}

// matchName matches a path that is a name followed by the segments tail accepts;
// tail returns the placeholders of the remaining segments.
func matchName(tail func(rest []string) (formats.Params, bool)) func(string) (formats.Params, bool) {
	return func(escaped string) (formats.Params, bool) {
		name, rest, ok := splitName(segments(escaped))
		if !ok {
			return nil, false
		}
		p, ok := tail(rest)
		if !ok {
			return nil, false
		}
		if p == nil {
			p = formats.Params{}
		}
		p["name"] = name
		return p, true
	}
}

func isLiteral(seg, lit string) bool {
	d, ok := decode(seg)
	return ok && d == lit
}

func tailDocument(rest []string) (formats.Params, bool) { return nil, len(rest) == 0 }

// tailVersion is one segment that is neither of the markers "-" and "-rev".
func tailVersion(rest []string) (formats.Params, bool) {
	if len(rest) != 1 {
		return nil, false
	}
	ref, ok := decode(rest[0])
	if !ok || ref == "-" || ref == "-rev" {
		return nil, false
	}
	return formats.Params{"ref": ref}, true
}

func tailTarball(rest []string) (formats.Params, bool) {
	if len(rest) != 2 || !isLiteral(rest[0], "-") {
		return nil, false
	}
	file, ok := decode(rest[1])
	if !ok {
		return nil, false
	}
	return formats.Params{"file": file}, true
}

func tailRev(rest []string) (formats.Params, bool) {
	if len(rest) != 2 || !isLiteral(rest[0], "-rev") {
		return nil, false
	}
	rev, ok := decode(rest[1])
	if !ok {
		return nil, false
	}
	return formats.Params{"rev": rev}, true
}

func tailTarballRev(rest []string) (formats.Params, bool) {
	if len(rest) != 4 || !isLiteral(rest[2], "-rev") {
		return nil, false
	}
	p, ok := tailTarball(rest[:2])
	if !ok {
		return nil, false
	}
	rev, ok := decode(rest[3])
	if !ok {
		return nil, false
	}
	p["rev"] = rev
	return p, true
}

// matchRoot matches the registry URL itself.
func matchRoot(escaped string) (formats.Params, bool) {
	return formats.Params{}, strings.Trim(escaped, "/") == ""
}

// matchOp matches a protocol operation that is exactly the segments after "-".
func matchOp(op ...string) func(string) (formats.Params, bool) {
	return func(escaped string) (formats.Params, bool) {
		segs := segments(escaped)
		if len(segs) != len(op)+1 || !isLiteral(segs[0], "-") {
			return nil, false
		}
		for i, o := range op {
			if !isLiteral(segs[i+1], o) {
				return nil, false
			}
		}
		return formats.Params{}, true
	}
}

// packageOp matches "-/package/{name}/<tail>".
func packageOp(tail func(rest []string) (formats.Params, bool)) func(string) (formats.Params, bool) {
	return func(escaped string) (formats.Params, bool) {
		segs := segments(escaped)
		if len(segs) < 3 || !isLiteral(segs[0], "-") || !isLiteral(segs[1], "package") {
			return nil, false
		}
		name, rest, ok := splitName(segs[2:])
		if !ok {
			return nil, false
		}
		p, ok := tail(rest)
		if !ok {
			return nil, false
		}
		if p == nil {
			p = formats.Params{}
		}
		p["name"] = name
		return p, true
	}
}

func tailDistTags(rest []string) (formats.Params, bool) {
	return nil, len(rest) == 1 && isLiteral(rest[0], "dist-tags")
}

func tailDistTag(rest []string) (formats.Params, bool) {
	if len(rest) != 2 || !isLiteral(rest[0], "dist-tags") {
		return nil, false
	}
	tag, ok := decode(rest[1])
	if !ok {
		return nil, false
	}
	return formats.Params{"tag": tag}, true
}

// deferredFamilies returns the first segments after "-" of the endpoints npm defines and
// trove does not implement (R-NPM-043, "Deferred" and the endpoint map of
// R-NPM-001): login, the user and token endpoints, search, the security, key,
// attestation and OIDC endpoints below npm/v1, organizations and teams, the
// external-dependency path, staged publishing and the listing of all packages.
func deferredFamilies() []string {
	return []string{"user", "v1", "npm", "org", "team", "all", "external", "stage"}
}

// deferredPackageOps returns the tails of "-/package/{name}/…" npm defines and trove
// does not implement: access, visibility and collaborators.
func deferredPackageOps() []string { return []string{"access", "visibility", "collaborators"} }

// auditBulkOp returns the segments of the bulk advisory endpoint after "-" (R-NPM-038).
func auditBulkOp() []string { return []string{"npm", "v1", "security", "advisories", "bulk"} }

// matchAuditLegacy matches npm 6's audit endpoints, -/npm/v1/security/audits and
// -/npm/v1/security/audits/quick.
func matchAuditLegacy(escaped string) (formats.Params, bool) {
	if p, ok := matchOp("npm", "v1", "security", "audits")(escaped); ok {
		return p, true
	}
	return matchOp("npm", "v1", "security", "audits", "quick")(escaped)
}

// isAudit says whether a path is one of the audit endpoints, which have routes of their own.
func isAudit(escaped string) bool {
	_, bulk := matchOp(auditBulkOp()...)(escaped)
	_, legacy := matchAuditLegacy(escaped)
	return bulk || legacy
}

// matchDeferred matches every path of an unimplemented npm endpoint, for any
// method; the four routes of the family answer 404 or 501 by method.
func matchDeferred(escaped string) (formats.Params, bool) {
	segs := segments(escaped)
	if len(segs) < 2 || !isLiteral(segs[0], "-") || isAudit(escaped) {
		return nil, false
	}
	if second, ok := decode(segs[1]); ok && slices.Contains(deferredFamilies(), second) {
		return formats.Params{}, true
	}
	if isLiteral(segs[1], "package") {
		if _, rest, ok := splitName(segs[2:]); ok && len(rest) >= 1 {
			if d, ok := decode(rest[0]); ok && slices.Contains(deferredPackageOps(), d) {
				return formats.Params{}, true
			}
		}
	}
	return nil, false
}
