package engine

import (
	"context"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/answers"
	"github.com/guided-traffic/trove/internal/bastion/cache"
)

// computeKey sets the cache key of the request (R-BAS-018): the path and query exactly as received, and the
// normalized values of the request headers the main installation's Vary named for that target.
func (e *engine) computeKey(q *rq) {
	q.baseKey = q.target
	q.cacheKey = e.keyFor(q.baseKey, q.r.Header)
}

func (e *engine) keyFor(base string, h http.Header) string {
	names, ok := e.vary.get(base)
	if !ok || len(names) == 0 {
		return base
	}
	var b strings.Builder
	b.WriteString(base)
	for _, n := range names {
		b.WriteString("\x00")
		b.WriteString(strings.ToLower(n))
		b.WriteString("=")
		b.WriteString(normalizeValues(h.Values(n)))
	}
	return b.String()
}

// normalizeValues lower-cases a header's values and removes the white space around the list separators, so
// that two spellings of one value share an entry. The order of a list is kept: it carries preference.
func normalizeValues(vals []string) string {
	var parts []string
	for _, v := range vals {
		for _, p := range strings.Split(v, ",") {
			if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
				parts = append(parts, strings.ReplaceAll(p, " ", ""))
			}
		}
	}
	return strings.Join(parts, ",")
}

// varyNames reads the Vary header of a response. keep is false when the response varies by something the
// Bastion cannot key on: Vary: *, a credential or a cookie.
func varyNames(h http.Header) (names []string, keep bool) {
	seen := map[string]bool{}
	for _, v := range h.Values("Vary") {
		for _, n := range strings.Split(v, ",") {
			n = http.CanonicalHeaderKey(strings.TrimSpace(n))
			switch n {
			case "":
				continue
			case "*", "Authorization", "Cookie", "X-Nuget-Apikey":
				return nil, false
			}
			if !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		}
	}
	sort.Strings(names)
	return names, true
}

// learnVary registers the Vary of a response for the request's target and returns the cache key the response
// is kept under; keep is false for a response that must not be kept.
func (e *engine) learnVary(q *rq, h http.Header) (key string, keep bool) {
	names, ok := varyNames(h)
	if !ok {
		return "", false
	}
	e.vary.put(q.baseKey, names, 0)
	q.cacheKey = e.keyFor(q.baseKey, q.r.Header)
	return q.cacheKey, true
}

// loadEntry finds the entry of a cache key through the cache (R-BAS-031, R-BAS-032): the signature is verified
// there, an entry that fails it or whose bytes are gone is deleted and counted there, and every other failure
// of the backend is counted there and is a miss here (R-BAS-030).
func (e *engine) loadEntry(ctx context.Context, key string) (cache.Entry, bool) {
	ent, _, err := e.d.Cache.LookupEntry(ctx, key)
	return ent, err == nil
}

// saveEntry writes the signed entry of a cache key; the cache counts a failure.
func (e *engine) saveEntry(ctx context.Context, key string, ent cache.Entry) error {
	return e.d.Cache.StoreEntry(ctx, key, ent)
}

// dropEntry removes an entry; the bytes stay for the sweep, other entries may point at them.
func (e *engine) dropEntry(ctx context.Context, key string) {
	_ = e.d.Cache.DeleteEntry(ctx, key)
}

// fresh reports whether an entry may be served without a request (R-BAS-022, R-BAS-023): the revalidation
// period has not passed and no feed entry that names the entry's repository (or every permission answer) was
// received after validatedAt - 15 s.
func (e *engine) fresh(ent cache.Entry) bool {
	after := e.cfg.Cache.ArtifactRevalidateAfter
	if ent.Kind == cache.EntryDocument {
		after = e.cfg.Cache.MetadataRevalidateAfter
	}
	st := answers.State{
		Now: e.now(), Period: after, Log: e.d.Link.Feed(),
		// The history rule belongs to answers; an entry is judged by the period and the feed entries only.
		HistoryStart: ent.ValidatedAt.Add(-answers.SkewMargin - time.Second),
	}
	return st.Usable(answers.Answer{Kind: answers.URL, ObtainedAt: ent.ValidatedAt, Repository: ent.Repository})
}

// entrySource is the source of a cache entry's bytes: the cache's verified read, which withholds the last
// 64 KiB until the SHA-256 and the length equal the entry's and, on a mismatch, deletes the object and the
// entry and fails the read with cache.ErrCorrupt (R-BAS-032).
func (e *engine) entrySource(ctx context.Context, key string, ent cache.Entry) *source {
	return &source{
		size: ent.Size, header: ent.Header, etag: entryETag(ent),
		open: func(c context.Context) (io.ReadCloser, error) {
			rc, err := e.d.Cache.Open(c, key, ent)
			if err != nil {
				return nil, err
			}
			e.d.Cache.Touch(ctx, ent)
			return rc, nil
		},
	}
}

// entryETag is the validator of an entry: the stored ETag, or for an artifact the content address.
func entryETag(ent cache.Entry) string {
	if v := ent.Header.Get("Etag"); v != "" {
		return v
	}
	return `"` + ent.SHA256 + `"`
}
