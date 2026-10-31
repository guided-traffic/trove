package v1

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The rate-limit class of an endpoint (R-EP-015): a cluster-scoped limiter of
// R-ARCH-034 per class of server.endpoint_rate_limits, which runs at the chain's
// stage 7 for every request on the endpoint, package protocols included, before
// the classes of R-API-016 and R-AUTH-038, which still apply. The bucket key
// holds the endpoint name, so two endpoints that share a class have separate
// budgets.

// EndpointLimiterName is the limiter of a class: endpoint_<class>, with the
// hyphens of a class name as underscores, because a limiter name is lower-case
// words joined by "_" (R-ARCH-034). Class names hold no "_", so no two classes
// share a limiter.
func EndpointLimiterName(class string) string {
	return "endpoint_" + strings.ReplaceAll(class, "-", "_")
}

// EndpointRateLimitOptions configure [NewEndpointRateLimit].
type EndpointRateLimitOptions struct {
	// Endpoint is the endpoint's name, part of every bucket key.
	Endpoint string
	// Class names the class; Limit is its entry of server.endpoint_rate_limits.
	Class string
	Limit config.EndpointRateLimit
	// Limiter is the class's limiter (EndpointLimiterName).
	Limiter *cluster.Limiter
}

// NewEndpointRateLimit builds the stage of one endpoint's class. The class name
// of the RateLimit-Policy and RateLimit fields is endpoint:<class>; a refusal is
// 429 rate_limited with Retry-After. The bucket is the token ID, the principal,
// or the client address of an anonymous request (key principal), or always the
// client address (key address).
func NewEndpointRateLimit(o EndpointRateLimitOptions) func(http.Handler) http.Handler {
	class := "endpoint:" + o.Class
	w := int64(o.Limiter.Window() / 1e9)
	policy := `"` + class + `";q=` + strconv.Itoa(o.Limiter.Burst()) + ";w=" + strconv.FormatInt(w, 10)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			v := o.Limiter.TakeAt(r.Context(), o.Endpoint+"\x00"+endpointBucket(r, o.Limit.Key), httpserver.InfoFrom(r.Context()).Received)
			p := policy
			if v.Mode != cluster.ModeCluster {
				p = `"` + class + `";q=` + strconv.Itoa(v.Limit) + ";w=" + strconv.FormatInt(int64(v.Window/1e9), 10)
			}
			t := int64((v.Reset + 999_999_999) / 1e9)
			rw.Header().Add("RateLimit-Policy", p)
			rw.Header().Add("RateLimit", `"`+class+`";r=`+strconv.Itoa(v.Remaining)+";t="+strconv.FormatInt(t, 10))
			if v.Allowed {
				next.ServeHTTP(rw, r)
				return
			}
			httpserver.Problem(rw, r, errs.E(errs.RateLimited, errs.CodeRateLimited,
				"Too many requests on this endpoint; retry after the period in the Retry-After header.", errs.WithRetryAfter(v.RetryAfter)))
		})
	}
}

// endpointBucket is the bucket of a request under the class's key.
func endpointBucket(r *http.Request, key string) string {
	p := httpserver.PrincipalFrom(r.Context())
	if key == "address" || p.Anonymous {
		return "addr:" + httpserver.InfoFrom(r.Context()).Client.String()
	}
	switch {
	case p.Credential.TokenID != "":
		return "token:" + p.Credential.TokenID
	case p.ID != "":
		return "principal:" + p.ID
	}
	return "name:" + p.Name
}
