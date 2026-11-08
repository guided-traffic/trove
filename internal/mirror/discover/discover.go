// Package discover is the contract between the mirror engine (internal/mirror) and a format whose remote adapter can
// discover the packages of its upstream: repositories-and-proxy.md R-REPO-050 to R-REPO-055, the format pages' sections
// "Mirror discovery", architecture.md Q-ARCH-284 and ADR-152.
//
// It is a leaf: it imports the standard library only, so that the format contract (internal/formats, an edge package),
// the formats themselves and the engine (a domain package) all speak its types. A format implements [Lister] on its
// remote adapter; formats.CapabilitiesOf finds it when the format registers (formats.RemoteCaps.Mirror), and the engine
// reaches it through the composition root. The engine lends the format an [Upstream] for one pass: every request a
// format sends for discovery goes through it, so the egress rules, the credential policy, the background share of the
// fetch budget and the throttling of R-REPO-054 apply to it as they apply to the engine's own fills.
//
// The rules a format follows:
//
//   - Names are normalized by the format's name rule and checked against [Scope.Admits] before they are returned: a name
//     the patterns or a claim refuse is never returned, and no request is sent for it.
//   - A rate-limit answer of the upstream reaches the format as an error for which [AsRateLimited] reports true. The
//     format returns it unchanged or wrapped with %w and sends nothing more in that call: the engine waits, halves its
//     window and calls again with the same request (R-REPO-054).
//   - A [Cursor] is the format's own JSON, at most [MaxCursorBytes]; the engine stores it and hands it back, never reads it.
//   - Calls are idempotent: after a drain or a crash the engine repeats the last call with the cursor it had stored.
package discover

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Method is a way of discovering the names of an upstream (R-REPO-051).
type Method string

// The methods. [Order] is the order in which `discovery: auto` tries them.
const (
	// MethodFeed reads an append-only change feed or catalog with a position: a full pass from its beginning, an
	// incremental pass after the cursor of the last complete pass (npm's replication service, NuGet's catalog).
	MethodFeed Method = "feed"
	// MethodListing reads a paged listing of every package; every pass is full (NuGet's search).
	MethodListing Method = "listing"
	// MethodNames takes the include patterns without a wildcard as exact names; every pass is full. The engine adds the
	// names of the packages the repository holds in every full pass, whatever the method.
	MethodNames Method = "names"
)

// Order returns the methods in the order `discovery: auto` tries them.
func Order() []Method { return []Method{MethodFeed, MethodListing, MethodNames} }

// Valid reports whether m is one of the methods.
func (m Method) Valid() bool { return m == MethodFeed || m == MethodListing || m == MethodNames }

// PassKind is whether a pass reads everything or only what changed (R-REPO-051).
type PassKind string

// The kinds of a pass.
const (
	PassFull        PassKind = "full"
	PassIncremental PassKind = "incremental"
)

// Cursor is a position of the format: opaque JSON to the engine, stored in mirror_state (pass_cursor within a pass,
// feed_cursor between passes), at most [MaxCursorBytes].
type Cursor = json.RawMessage

// MaxCursorBytes bounds a [Cursor] (the CHECK of mirror_state); a longer one fails the pass.
const MaxCursorBytes = 4096

// Scope is what bounds discovery (R-REPO-005, R-REPO-023, R-REPO-051).
type Scope struct {
	// Include and Exclude are the repository's include and exclude patterns as stored: a format may narrow its
	// requests by the literal prefix of an include pattern ([LiteralPrefix]); it still checks every name with Admits.
	Include, Exclude []string
	// Admits reports whether a normalized name is admitted by the patterns, the claims and the format's name rule. The
	// engine always sets it.
	Admits func(name string) bool
}

// PageRequest asks a [Lister] for the next page of names of a pass.
type PageRequest struct {
	Method Method
	Kind   PassKind
	// Cursor is the position the previous page of this pass returned; nil for the first page.
	Cursor Cursor
	// Since is the cursor of the last complete pass ([Page.Cursor] of its last page): set for an incremental pass, nil for
	// a full one.
	Since Cursor
	Scope Scope
	// Limit is the most names the engine takes in this page; a format returns at most Limit names (and may return fewer,
	// none included, with Done false).
	Limit int
}

// Page is one page of discovered names.
type Page struct {
	// Names are normalized names that Scope.Admits accepted; a name may appear in several pages.
	Names []string
	// Removed are names the upstream reports as deleted (a feed's deletion record): the engine refreshes their documents
	// through the plan and stores nothing new (R-REPO-051, Q-REPO-144). Admitted names only.
	Removed []string
	// Cursor is the position after this page, passed back as PageRequest.Cursor. With Done it is the Since of the next
	// incremental pass; nil there means that the method has no incremental pass.
	Cursor Cursor
	// Done ends the discovery of the pass.
	Done bool
	// Truncated says that the upstream ended the method before its end (a listing whose window is capped); Note says how,
	// in words for the status (at most 256 characters, no URL, no credential).
	Truncated bool
	Note      string
}

// Item is one artifact of a [Plan].
type Item struct {
	// Version is the version, normalized by the format.
	Version string
	// Path is the canonical path of the artifact in the repository (R-REPO-003), the path a client's request has.
	Path string
	// Size is the size the upstream announced, in bytes; 0 when unknown (R-REPO-052).
	Size int64
	// Prerelease marks a prerelease version by the format's rule (`includePrereleases`).
	Prerelease bool
	// Pinned makes the version count whatever the version bounds say (npm's latest dist-tag, Q-NPM-71).
	Pinned bool
}

// Plan is what a package needs in the mirror: the format has filled its documents through [Upstream.Document] when it
// returns the plan; the engine stores the artifacts.
type Plan struct {
	Name string
	// Items are the artifacts of the package's versions, newest first by the format's version order; the engine applies
	// maxVersionsPerPackage and includePrereleases to this order (R-REPO-051).
	Items []Item
}

// Response is an answer of the upstream to [Upstream.Get] that is not a rate limit.
type Response struct {
	Status int
	Header http.Header
	// Body is the body, read up to the bound of the call.
	Body []byte
}

// Upstream is what the engine lends a format for one pass of one remote repository.
type Upstream interface {
	// Base is the normalized remote.url of the repository.
	Base() *url.URL
	// Settings is the format's member of the repository document (R-REPO-004), nil when the document has none.
	Settings() json.RawMessage
	// Get sends one GET without a body to u through the repository's upstream client, as a background request of the
	// pass: the egress rules, TLS verification and the redirect rules apply; the credential goes only where it goes for
	// clients (the upstream origin, extraHosts entries with sendCredentials) and never to a host of
	// [Lister.MirrorHosts]; the request takes a background slot (R-REPO-055) and counts in the pass's window
	// (R-REPO-054). The host of u must be the upstream's, an extraHosts entry or one of MirrorHosts(Base()); another
	// host fails with foreign_host_denied and nothing is sent. accept is the Accept header ("" for none); max bounds the
	// body (0: the engine's bound for discovery documents). A rate-limit answer is an error for which [AsRateLimited]
	// reports true; any other status is a Response for the format to judge; a transport failure is an error.
	Get(ctx context.Context, u *url.URL, accept string, max int64) (*Response, error)
	// Document returns a metadata document of the repository through the remote engine as a background request
	// (R-REPO-025 steps 4 to 10): the stored copy while it is fresh, else revalidated or fetched and stored. found is
	// false when the upstream does not have it (404, 410) or it is gone (R-REPO-029); the format then plans nothing for it.
	Document(ctx context.Context, path, variant string) (doc []byte, found bool, err error)
	// Artifact fills an artifact of the repository through the format's gate and the remote engine as the host's Fill does
	// for a plan item: a background request of the pass, counted as an item it stored (R-REPO-051, R-REPO-054). It returns
	// the stored bytes when they are at most max; found is false when the upstream does not have the artifact (R-OCI-026). An artifact above max
	// is filled and counted all the same and answers found true with a nil body: the plan tells "stored, too large to read" from "not there".
	// A format whose plan names a document that is itself an artifact - an image index's manifests - reads it with this call
	// before it plans what the index names.
	Artifact(ctx context.Context, path string, max int64) (body []byte, found bool, err error)
}

// Lister is the optional interface of a format's remote adapter whose upstream packages can be discovered (ADR-152).
// The engine calls MirrorNames from one goroutine per pass, and MirrorPlan from up to the pass's window at once, for
// different names.
type Lister interface {
	// MirrorMethods reports the methods the repository's upstream offers, in the order of [Order]; it may read the
	// upstream's discovery document through u.Document. MethodNames is always offered and may be left out.
	MirrorMethods(ctx context.Context, u Upstream) ([]Method, error)
	// MirrorNames returns the next page of names of a pass. A method the upstream does not offer answers
	// [ErrMethodUnavailable].
	MirrorNames(ctx context.Context, u Upstream, rq PageRequest) (Page, error)
	// MirrorPlan fills the package's documents through u.Document and returns its artifacts. A package the upstream does
	// not have, or one that is gone, is an empty plan and no error.
	MirrorPlan(ctx context.Context, u Upstream, name string) (Plan, error)
	// MirrorHosts names the hosts besides the upstream's and the extraHosts entries that MirrorNames may reach for this
	// upstream URL; requests to them carry no credential (npm's replication host of the public registry, Q-NPM-72).
	MirrorHosts(base *url.URL) []string
}

// RateLimited is implemented by the error of an upstream's rate-limit answer (R-REPO-054, Q-REPO-137). The error of
// internal/proxy/upstream (*upstream.RateLimitError) implements it by its method set.
type RateLimited interface {
	error
	// RetryAfter is the wait the upstream asked for, and whether it named one (Retry-After or a reset header).
	RetryAfter() (time.Duration, bool)
	// RateLimitReason is one of too_many_requests, slow_down, retry_after and quota_header
	// (the reason label of trove_mirror_rate_limited_total).
	RateLimitReason() string
}

// AsRateLimited reports whether err, or an error it wraps, is a rate-limit answer.
func AsRateLimited(err error) (RateLimited, bool) {
	var rl RateLimited
	if errors.As(err, &rl) {
		return rl, true
	}
	return nil, false
}

// ErrMethodUnavailable answers a request for a method that the upstream does not offer; the pass ends with
// discovery_unavailable (R-REPO-051).
var ErrMethodUnavailable = errors.New("discover: the upstream does not offer the discovery method")

// LiteralPrefix returns the literal part of a name pattern before its first wildcard (`*`, `?`) with the escapes of
// R-AUTHZ-005 resolved, and whether the pattern has a wildcard at all. A pattern without a wildcard is an exact name.
// R-REPO-052 counts an include pattern as a bound when its literal prefix is not empty and contains a character other
// than `@` and `/`, so that `@*/*` is not one.
func LiteralPrefix(pattern string) (prefix string, wildcard bool) {
	var b strings.Builder
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '\\':
			if i+1 < len(pattern) {
				i++
				b.WriteByte(pattern[i])
			}
		case '*', '?':
			return b.String(), true
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), false
}

// Bounding reports whether an include pattern bounds a mirror of a well-known upstream (R-REPO-052, Q-REPO-132): an exact
// name, or a literal prefix before the first wildcard that holds more than the scope sigils `@` and `/`.
func Bounding(pattern string) bool {
	prefix, _ := LiteralPrefix(pattern)
	return strings.Trim(prefix, "@/") != ""
}

// SameUpstream reports whether base is the format's well-known upstream, the catalog's default upstream URL known
// (R-REPO-052, Q-REPO-194): the same scheme, host, effective port and path, and no user information, query or fragment.
// The bound rule of the repository model and a format's lister decide "is this the well-known upstream" with this one
// predicate, so that a URL that differs only in a part the format's requests ignore is not a way around the bound.
func SameUpstream(base *url.URL, known string) bool {
	k, err := url.Parse(known)
	if err != nil || base == nil {
		return false
	}
	if base.User != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" {
		return false
	}
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if strings.EqualFold(u.Scheme, "https") {
			return "443"
		}
		return "80"
	}
	return strings.EqualFold(base.Scheme, k.Scheme) && strings.EqualFold(base.Hostname(), k.Hostname()) &&
		port(base) == port(k) && strings.Trim(base.Path, "/") == strings.Trim(k.Path, "/")
}
