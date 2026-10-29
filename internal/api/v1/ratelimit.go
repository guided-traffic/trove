package v1

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The general rate-limit classes of /api/v1/ and /files/ (R-API-016). Each class
// is a cluster-scoped limiter of R-ARCH-034, so a bucket admits the same budget
// whichever replica a request reaches.
//
//	read       safe methods                       bucket: the principal (token ID, or session user)
//	write      unsafe methods                     bucket: the principal
//	anonymous  requests without credentials       bucket: the client address
//
// The stage is the chain's stage 7 (R-ARCH-005): it runs after authentication,
// so the principal is known. Package-protocol paths are not limited here; the
// class an endpoint applies to them is R-EP-015.

// Names of the limiters and values of the class label.
const (
	LimiterRead      = "api_read"
	LimiterWrite     = "api_write"
	LimiterAnonymous = "api_anonymous"
)

func classOf(name string) string { return strings.TrimPrefix(name, "api_") }

// RateLimitOptions configure [NewRateLimit].
type RateLimitOptions struct {
	Config   config.APIRateLimits
	Limiters *cluster.LimiterSet
	// IsAdmin reports whether the caller holds the admin capability; with
	// api.rate_limits.exempt_admins it bypasses the limits. Nil means nobody is
	// exempt.
	IsAdmin func(r *http.Request) bool
	// Registry receives trove_api_rate_limited_total{class}; nil registers nothing.
	Registry prometheus.Registerer
}

type classLimiter struct {
	l       *cluster.Limiter
	policy  string // the RateLimit-Policy member after the class name, "q=200;w=4"
	limited prometheus.Counter
}

// NewRateLimit builds stage 7. With api.rate_limits.enabled false, or without
// limiters, it passes every request through.
func NewRateLimit(o RateLimitOptions) (func(http.Handler) http.Handler, error) {
	if !o.Config.Enabled || o.Limiters == nil {
		return func(next http.Handler) http.Handler { return next }, nil
	}
	rejected := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "trove_api_rate_limited_total",
		Help: "Requests refused with 429 by the general rate limits, by class.",
	}, []string{"class"})
	if o.Registry != nil {
		if err := o.Registry.Register(rejected); err != nil {
			return nil, err
		}
	}
	mk := func(name string, rate float64, burst int) classLimiter {
		l := o.Limiters.NewLimiter(name, cluster.ScopeCluster, rate, burst)
		w := int64(l.Window() / 1e9)
		return classLimiter{
			l: l, limited: rejected.WithLabelValues(classOf(name)),
			policy: `"` + classOf(name) + `";q=` + strconv.Itoa(burst) + ";w=" + strconv.FormatInt(w, 10),
		}
	}
	classes := map[string]classLimiter{
		LimiterRead:      mk(LimiterRead, o.Config.Read.RequestsPerSecond, o.Config.Read.Burst),
		LimiterWrite:     mk(LimiterWrite, o.Config.Write.RequestsPerSecond, o.Config.Write.Burst),
		LimiterAnonymous: mk(LimiterAnonymous, o.Config.Anonymous.RequestsPerSecond, o.Config.Anonymous.Burst),
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !limitedPath(r.URL.Path) || (o.Config.ExemptAdmins && o.IsAdmin != nil && o.IsAdmin(r)) {
				next.ServeHTTP(w, r)
				return
			}
			name, bucket := classify(r)
			c := classes[name]
			v := c.l.Take(r.Context(), bucket)
			setRateLimitFields(w, c, name, v)
			if v.Allowed {
				next.ServeHTTP(w, r)
				return
			}
			c.limited.Inc()
			httpserver.Problem(w, r, errs.E(errs.RateLimited, errs.CodeRateLimited,
				"Too many requests; retry after the period in the Retry-After header.", errs.WithRetryAfter(v.RetryAfter)))
		})
	}, nil
}

func limitedPath(p string) bool { return isAPIPath(p) || strings.HasPrefix(p, "/files/") }

// classify picks the limiter and the bucket of a request.
func classify(r *http.Request) (limiter, bucket string) {
	p := httpserver.PrincipalFrom(r.Context())
	if p.Anonymous {
		return LimiterAnonymous, httpserver.InfoFrom(r.Context()).Client.String()
	}
	limiter = LimiterWrite
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		limiter = LimiterRead
	}
	switch {
	case p.Credential.TokenID != "":
		bucket = "token:" + p.Credential.TokenID
	case p.ID != "":
		bucket = "principal:" + p.ID
	default:
		bucket = "name:" + p.Name
	}
	return limiter, bucket
}

// setRateLimitFields sets RateLimit-Policy and RateLimit of
// draft-ietf-httpapi-ratelimit-headers (R-API-016). During the fallback q and
// r describe the replica's share.
func setRateLimitFields(w http.ResponseWriter, c classLimiter, name string, v cluster.Verdict) {
	class := classOf(name)
	policy := c.policy
	if v.Mode != cluster.ModeCluster {
		policy = `"` + class + `";q=` + strconv.Itoa(v.Limit) + ";w=" + strconv.FormatInt(int64(v.Window/1e9), 10)
	}
	t := int64((v.Reset + 999_999_999) / 1e9)
	w.Header().Set("RateLimit-Policy", policy)
	w.Header().Set("RateLimit", `"`+class+`";r=`+strconv.Itoa(v.Remaining)+";t="+strconv.FormatInt(t, 10))
}

// EdgeStage composes the stages of the management API that sit at the chain's
// stage 7: the response headers of R-API-017, CORS (R-API-018) and the rate
// limits (R-API-016), in this order, so a preflight is answered before it
// counts against a limit.
func EdgeStage(cors func(http.Handler) http.Handler, limit func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		h := next
		if limit != nil {
			h = limit(h)
		}
		if cors != nil {
			h = cors(h)
		}
		return apiHeaders(h)
	}
}

// apiHeaders sets the response headers every /api/v1/ answer carries
// (R-API-017). X-Content-Type-Options and X-Request-Id come from the chain. A
// handler may replace Cache-Control (the problem catalog is cacheable).
func apiHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAPIPath(r.URL.Path) {
			h := w.Header()
			h.Set("Cache-Control", "no-store")
			h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
			h.Set("Referrer-Policy", "no-referrer")
		}
		next.ServeHTTP(w, r)
	})
}
