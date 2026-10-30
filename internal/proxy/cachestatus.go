package proxy

import (
	"strconv"
	"strings"
	"time"
)

// CacheStatus is what the engine reports of how a response came about, rendered as
// the Cache-Status header of RFC 9211 with the cache name `trove` (R-REPO-047).
type CacheStatus struct {
	// Hit: served from the cache without asking, or after a failed ask.
	Hit bool
	// Stale: served beyond its period.
	Stale bool
	// Stored: this response's fetch stored an entry.
	Stored bool
	// Negative: answered 404 from a negative entry.
	Negative bool
	// Collapsed: the response came from another request's fetch, on this replica or
	// another.
	Collapsed bool
	// Forwarded is "uri-miss" for a request the cache could not answer and "stale"
	// for one it answered stale and revalidated; "" when nothing was forwarded.
	Forwarded string
	// FwdStatus is the upstream status of a forwarded request, 0 when there was no
	// response.
	FwdStatus int
	// TTL is the remaining freshness of a fresh hit; a negative value says the item has
	// none (an artifact never expires) and leaves the parameter out.
	TTL time.Duration
	// Detail is "served-stale", "negative", or "".
	Detail string
}

// The detail values.
const (
	detailServedStale = "served-stale"
	detailNegative    = "negative"
)

// Header renders the status:
//
//	trove; hit; ttl=300
//	trove; fwd=uri-miss; fwd-status=200; stored
//	trove; fwd=stale; fwd-status=304
//	trove; fwd=stale; fwd-status=503; detail=served-stale
//	trove; hit; detail=negative
//	trove; hit; ttl=300; collapsed
func (c CacheStatus) Header() string {
	var b strings.Builder
	b.WriteString("trove")
	if c.Hit {
		b.WriteString("; hit")
		if c.Detail == "" && !c.Stale && c.TTL >= 0 {
			b.WriteString("; ttl=")
			b.WriteString(strconv.FormatInt(max(0, int64(c.TTL/time.Second)), 10))
		}
	}
	if c.Forwarded != "" {
		b.WriteString("; fwd=")
		b.WriteString(c.Forwarded)
		if c.FwdStatus > 0 {
			b.WriteString("; fwd-status=")
			b.WriteString(strconv.Itoa(c.FwdStatus))
		}
	}
	if c.Stored {
		b.WriteString("; stored")
	}
	if c.Detail != "" {
		b.WriteString("; detail=")
		b.WriteString(c.Detail)
	}
	if c.Collapsed {
		b.WriteString("; collapsed")
	}
	return b.String()
}

// hitStatus is a fresh hit.
func hitStatus(ttl time.Duration) CacheStatus { return CacheStatus{Hit: true, TTL: max(ttl, 0)} }

// servedStale is a stored entry served without asking the upstream (offline, circuit
// open, no right to fill): a hit that says it is stale.
func servedStale() CacheStatus {
	return CacheStatus{Hit: true, Stale: true, Detail: detailServedStale}
}

// staleAfterFailure is a stored entry served because the refresh failed or ran past
// the refresh timeout; status is the upstream's, 0 when it gave none.
func staleAfterFailure(status int) CacheStatus {
	return CacheStatus{Stale: true, Forwarded: "stale", FwdStatus: status, Detail: detailServedStale}
}
