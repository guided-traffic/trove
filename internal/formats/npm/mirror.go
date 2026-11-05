package npm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/guided-traffic/trove/internal/formats/npm/name"
	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// The npm format's part of the mirror mode (R-REPO-050 to R-REPO-052, the section "Mirror discovery" of
// docs/spec/formats/npm.md, Q-NPM-69 to Q-NPM-74): the remote adapter is a [discover.Lister]. The format sends no
// request of its own; every request goes through the [discover.Upstream] the engine lends it for one pass, so the egress
// rules, the credential policy and the throttling of R-REPO-054 apply to it. This file holds the interface and the
// `names` method; mirror_feed.go reads the replication service, mirror_plan.go plans a package.

const (
	// publicRegistryHost is the host of the public registry, the one upstream whose replication service is known.
	publicRegistryHost = "registry.npmjs.org"
	// replicationHost is the replication service of the public registry (Q-NPM-72), a discovery host of the format:
	// requests to it carry no credential.
	replicationHost = "replicate.npmjs.com"
	// mirrorPageLimit is the `limit` of every replication request: the most the service answers (R-REPO-051).
	mirrorPageLimit = 10000
)

var _ discover.Lister = remoteAdapter{}

// isPublicRegistry reports whether base is https://registry.npmjs.org/, the catalog's default upstream.
func isPublicRegistry(base *url.URL) bool {
	if base == nil || !strings.EqualFold(base.Scheme, "https") || !strings.EqualFold(base.Hostname(), publicRegistryHost) {
		return false
	}
	if port := base.Port(); port != "" && port != "443" {
		return false
	}
	return base.User == nil && strings.Trim(base.Path, "/") == ""
}

// MirrorHosts is `replicate.npmjs.com` for the public registry and nothing for any other upstream (Q-NPM-72).
func (remoteAdapter) MirrorHosts(base *url.URL) []string {
	if isPublicRegistry(base) {
		return []string{replicationHost}
	}
	return nil
}

// MirrorMethods offers `feed` for the public registry only, never `listing` (Q-NPM-70), and `names` always (Q-NPM-69).
func (remoteAdapter) MirrorMethods(_ context.Context, u discover.Upstream) ([]discover.Method, error) {
	if isPublicRegistry(u.Base()) {
		return []discover.Method{discover.MethodFeed, discover.MethodNames}, nil
	}
	return []discover.Method{discover.MethodNames}, nil
}

// MirrorNames returns the next page of names of a pass.
func (a remoteAdapter) MirrorNames(ctx context.Context, u discover.Upstream, rq discover.PageRequest) (discover.Page, error) {
	switch rq.Method {
	case discover.MethodFeed:
		if !isPublicRegistry(u.Base()) {
			return discover.Page{}, discover.ErrMethodUnavailable
		}
		if rq.Kind == discover.PassIncremental {
			return feedIncremental(ctx, u, rq)
		}
		return feedFull(ctx, u, rq)
	case discover.MethodNames:
		return exactNames(rq)
	}
	return discover.Page{}, discover.ErrMethodUnavailable
}

// mirrorCursor is the cursor of the format (opaque to the engine, at most discover.MaxCursorBytes): `seq` is the
// replication position, `after` the last id of a key range read and `range` the index of the range (R-REPO-051).
//
// The full pass's cursor carries all three, because the `update_seq` read before the listing has to reach the last page
// as the position of the next incremental pass (Q-NPM-73); the cursor a pass ends with, and that of an incremental pass,
// is the `seq` alone.
type mirrorCursor struct {
	Seq   string `json:"seq,omitempty"`
	After string `json:"after,omitempty"`
	Range *int   `json:"range,omitempty"`
}

func readCursor(raw discover.Cursor) (mirrorCursor, error) {
	var c mirrorCursor
	if len(raw) == 0 {
		return c, nil
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("npm mirror: the cursor is not the format's: %w", err)
	}
	return c, nil
}

func (c mirrorCursor) rangeIndex() int {
	if c.Range == nil {
		return 0
	}
	return *c.Range
}

func (c mirrorCursor) encode() (discover.Cursor, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	if len(raw) > discover.MaxCursorBytes {
		return nil, fmt.Errorf("npm mirror: the cursor is %d bytes, at most %d", len(raw), discover.MaxCursorBytes)
	}
	return raw, nil
}

// mirrorAdmitted normalizes an id by the name rule of R-NPM-002 and applies the scope's check: ok is false for an id that is no
// name, and for a name the patterns, the claims or the rule refuse. Nothing is ever requested for such an id.
func mirrorAdmitted(sc discover.Scope, id string) (string, bool) {
	n, err := name.Parse(id)
	if err != nil {
		return "", false
	}
	norm := n.String()
	if sc.Admits == nil || !sc.Admits(norm) {
		return "", false
	}
	return norm, true
}

// pageSize is the number of names a page may return.
func pageSize(rq discover.PageRequest) int {
	if rq.Limit <= 0 {
		return mirrorPageLimit
	}
	return rq.Limit
}

// exactNames is the method `names`: the include patterns without a wildcard, read as package names, in name order and
// paged by the last name returned. The engine adds the names the repository holds itself.
func exactNames(rq discover.PageRequest) (discover.Page, error) {
	cur, err := readCursor(rq.Cursor)
	if err != nil {
		return discover.Page{}, err
	}
	seen := map[string]bool{}
	var all []string
	for _, p := range rq.Scope.Include {
		lit, wildcard := discover.LiteralPrefix(p)
		if wildcard {
			continue
		}
		if norm, ok := mirrorAdmitted(rq.Scope, lit); ok && !seen[norm] {
			seen[norm] = true
			all = append(all, norm)
		}
	}
	sort.Strings(all)
	limit := pageSize(rq)
	var page discover.Page
	for _, n := range all {
		if cur.After != "" && n <= cur.After {
			continue
		}
		if len(page.Names) == limit {
			c, err := mirrorCursor{After: page.Names[len(page.Names)-1]}.encode()
			if err != nil {
				return discover.Page{}, err
			}
			page.Cursor = c
			return page, nil
		}
		page.Names = append(page.Names, n)
	}
	page.Done = true
	return page, nil
}

// replicationURL is a request to the replication host (Q-NPM-72). rawQuery is built by the caller in the order of the
// service's documentation.
func replicationURL(path, rawQuery string) *url.URL {
	return &url.URL{Scheme: "https", Host: replicationHost, Path: path, RawQuery: rawQuery}
}

// jsonString quotes s as CouchDB expects a key: a JSON string, without the HTML escapes of encoding/json.
func jsonString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a string always encodes
	return strings.TrimSuffix(b.String(), "\n")
}

// replicationGet reads a JSON document of the replication service. A rate-limit answer of the upstream is returned as it
// came (discover.AsRateLimited stays true); an answer that says the service is not there ends the method.
func replicationGet(ctx context.Context, u discover.Upstream, path, rawQuery string, into any) error {
	resp, err := u.Get(ctx, replicationURL(path, rawQuery), "application/json", 0)
	if err != nil {
		if _, limited := discover.AsRateLimited(err); limited {
			return err
		}
		return fmt.Errorf("npm mirror: %s: %w", replicationHost+path, err)
	}
	switch {
	case resp.Status == 404 || resp.Status == 410 || resp.Status == 501:
		return fmt.Errorf("%w: %s answered %d", discover.ErrMethodUnavailable, replicationHost+path, resp.Status)
	case resp.Status != 200:
		return fmt.Errorf("npm mirror: %s answered %d", replicationHost+path, resp.Status)
	}
	if err := json.Unmarshal(resp.Body, into); err != nil {
		return fmt.Errorf("npm mirror: %s: the answer is not the expected JSON: %w", replicationHost+path, err)
	}
	return nil
}

// seqText reads a replication sequence, a JSON number or string, as text. The service's sequences are opaque; they are
// stored and sent back as they came.
func seqText(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", errors.New("npm mirror: the replication service sent no sequence")
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil || s == "" {
			return "", errors.New("npm mirror: the replication service sent an unreadable sequence")
		}
		return s, nil
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return "", errors.New("npm mirror: the replication service sent an unreadable sequence")
	}
	return n.String(), nil
}
