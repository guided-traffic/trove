package observability

import (
	"context"
	"sync"
)

// RequestInfo collects the facts about one request that only the handlers
// know: the handler name, the principal, the repository. The request log
// middleware creates it, handlers fill it through the setters, and the
// middleware reads it after the response (R-OPS-010). All setters are safe for
// concurrent use and ignore a nil receiver, so code that runs without the
// middleware needs no checks.
type RequestInfo struct {
	mu sync.Mutex
	v  requestFacts
}

type requestFacts struct {
	handler       string
	principal     string
	principalType string
	principalID   string
	tokenID       string
	authMethod    string
	repo          string
	format        string
	cache         string
	denyReason    string
	limiter       string
	limitMode     string
	remoteAddr    string
}

type infoKey struct{}

// WithRequestInfo returns ctx carrying a fresh RequestInfo, or ctx itself and
// its RequestInfo when it already has one.
func WithRequestInfo(ctx context.Context) (context.Context, *RequestInfo) {
	if ri := RequestInfoFrom(ctx); ri != nil {
		return ctx, ri
	}
	ri := &RequestInfo{}
	return context.WithValue(ctx, infoKey{}, ri), ri
}

// RequestInfoFrom returns the RequestInfo of ctx, or nil.
func RequestInfoFrom(ctx context.Context) *RequestInfo {
	ri, _ := ctx.Value(infoKey{}).(*RequestInfo)
	return ri
}

func (ri *RequestInfo) set(f func(*requestFacts)) {
	if ri == nil {
		return
	}
	ri.mu.Lock()
	f(&ri.v)
	ri.mu.Unlock()
}

func (ri *RequestInfo) get() requestFacts {
	if ri == nil {
		return requestFacts{}
	}
	ri.mu.Lock()
	defer ri.mu.Unlock()
	return ri.v
}

// SetHandler names the registered handler that serves the request. It is the
// metrics label and the handler field and also the http.route of the span.
func (ri *RequestInfo) SetHandler(name string) { ri.set(func(v *requestFacts) { v.handler = name }) }

// SetPrincipal records the authenticated principal: its display name, its type
// (user, service_account, ci, breakglass, anonymous) and its opaque ID.
func (ri *RequestInfo) SetPrincipal(name, typ, id string) {
	ri.set(func(v *requestFacts) { v.principal, v.principalType, v.principalID = name, typ, id })
}

// SetToken records the ID of the token used (never the token) and the method.
func (ri *RequestInfo) SetToken(tokenID, authMethod string) {
	ri.set(func(v *requestFacts) { v.tokenID, v.authMethod = tokenID, authMethod })
}

// SetAuthMethod records how the request authenticated.
func (ri *RequestInfo) SetAuthMethod(m string) { ri.set(func(v *requestFacts) { v.authMethod = m }) }

// SetRepo records the repository key and format the request addressed.
func (ri *RequestInfo) SetRepo(repo, format string) {
	ri.set(func(v *requestFacts) { v.repo, v.format = repo, format })
}

// SetCache records the cache outcome: hit, miss, stale or negative_hit.
func (ri *RequestInfo) SetCache(c string) { ri.set(func(v *requestFacts) { v.cache = c }) }

// SetDenyReason records the short code of a 401 or 403 response.
func (ri *RequestInfo) SetDenyReason(r string) { ri.set(func(v *requestFacts) { v.denyReason = r }) }

// SetLimit records the limiter and mode of a 429 response.
func (ri *RequestInfo) SetLimit(limiter, mode string) {
	ri.set(func(v *requestFacts) { v.limiter, v.limitMode = limiter, mode })
}

// SetRemoteAddr records the client address after the trusted-proxy rule.
func (ri *RequestInfo) SetRemoteAddr(a string) { ri.set(func(v *requestFacts) { v.remoteAddr = a }) }
