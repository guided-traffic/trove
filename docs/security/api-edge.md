# The management API's edge: rate limits, cross-origin access, stored outcomes

Perspective: what stops one caller from taking the capacity of the others, which origins a browser script may call from, and
which parts of a response trove keeps. The mechanisms are the cluster limiter ([R-ARCH-034](../spec/architecture.md)), the
general rate-limit classes ([R-API-016](../spec/api-and-ui.md)), CORS ([R-API-018](../spec/api-and-ui.md)), the response
headers of R-API-017 and the idempotency keys of R-API-014. How to use them is in
[http-api.md](../developer/http-api.md) and [valkey.md](../developer/valkey.md#rate-limit-counters).

## Rate limits

| Aspect | What the code does |
|---|---|
| Where it runs | The chain's stage 7, after authentication ([ratelimit.go](../../internal/api/v1/ratelimit.go), [wire_api.go](../../internal/app/wire_api.go)); it covers `/api/v1/` and `/files/`. The login, token-validation and CI-exchange limits are the identity area's (R-AUTH-038, R-CI-021) and use the same limiter. |
| Whose budget | `api_read` / `api_write`: the token ID, else the principal ID. `api_anonymous`: the client address the chain resolved (R-ARCH-006), so it is as trustworthy as `server.trusted_proxies`. A token is not exempt because its owner is an administrator unless `api.rate_limits.exempt_admins` is set and the authorizer says `content:bypass`. |
| What Valkey holds | Counters under the key family `rl` ([valkey_ratelimit.go](../../internal/cluster/valkey_ratelimit.go)): the limiter name, a SHA-256 of the bucket, a window number, a count. No token, no principal, no address in clear (R-ARCH-030), every key with an expiry of two windows. Valkey never holds a grant or a decision. |
| Refused requests count | The counter is incremented before the decision, so hammering does not win budget back; the `Retry-After` is computed from the same counters. |
| Without Valkey | Each replica answers from a local token bucket with `rate ÷ n` and `max(1, burst ÷ n)`; no Valkey call is attempted while the client reports itself unavailable, and a failing call costs at most `valkey.command_timeout`. The limiter never fails a request because Valkey failed. `trove_ratelimit_fallback{limiter}` shows it. |
| Memory | The local buckets of a limiter are bounded (`MaxFallbackBuckets`, 100,000); idle ones are dropped first, and a dropped bucket starts full. |

An attacker who can write to Valkey can reset or fill counters, which lets requests through or locks callers out of the API.
It cannot read a credential from them and it cannot change what a caller may do: the effect is availability and abuse budget
only. The break-glass lockout stays in PostgreSQL for that reason (R-AUTH-038).

## Cross-origin access

CORS is off until `api.cors.allowed_origins` lists exact origins (scheme, host, optional port; the configuration refuses a
wildcard, a path or another scheme). It then applies to requests that carry an `Authorization` header: the preflight is
answered for a listed origin, and the actual answer names the origin and exposes the headers of R-API-018.
`Access-Control-Allow-Credentials` is never sent, so the session cookie is not usable cross-origin, and a request without
`Authorization` gets no CORS header at all. The preflight of an allowed origin is answered before authentication and before the limiter (Q-API-138), so a preflight
does not use up budget; the other requests of that origin are authenticated as any other. Code: [cors.go](../../internal/api/v1/cors.go).

## Response headers

Every `/api/v1/` answer carries `Cache-Control: no-store`, `Content-Security-Policy: default-src 'none'; frame-ancestors 'none'`,
`Referrer-Policy: no-referrer` (stage 7) and `X-Content-Type-Options: nosniff` (the chain, for every answer). A handler that
returns something cacheable replaces `Cache-Control` (the problem catalog does).

## Request bodies

A JSON body is read up to `api.max_request_body_bytes` (1 MiB by default, `413` beyond) and decoded strictly
([decode.go](../../internal/api/v1/decode.go)). The syntax walk that finds duplicate members is iterative and refuses a nesting
deeper than 64 levels (`400 malformed_request`, code `depth`) before any decoder runs; it builds a JSON pointer only for the error
it reports, and so does the unknown-member walk. Without the bound, one anonymous `POST /api/v1/tokens/revoke` of 1 MiB of `[`
held a frame and a pointer string per level, memory in proportion to the square of the body's length (Q-API-75;
`TestR_API_017_BodyNesting` refuses that body with less than 1 MiB allocated).

## Stored outcomes of idempotent requests

`api_idempotency_keys` holds, for `api.idempotency_retention`, the status, a small set of headers and the JSON body of each
outcome, keyed by principal and key. The key is scoped to the authenticated principal, so one caller cannot replay or probe
another caller's key; a replay requires the same method, route and request body hash. An outcome that contains a secret is
declared by the operation (`Outcome.SecretID`): its body is sent once and never stored, and the table keeps only the secret's
identifier. Rows are read and written on the request pool and removed hourly by `idempotency-cleanup`.

## What this does not cover

### H-111 · The read and write budgets are per class, not per route

Live today for every caller. A route that is expensive (search, an export) shares the `api_read` budget with a cheap one, so a
caller can spend its whole read budget on expensive calls without being limited earlier. The classes of R-API-016 are the
only general limits; a route that needs its own bound registers its own limiter (R-ARCH-034) or is a job.

### H-112 · A counter lost in a Valkey failover restarts, and Valkey write access moves the limits

Live whenever Valkey fails over or is written by someone other than trove. A failover that loses recent counters admits at
most one extra window of requests per bucket (R-ARCH-034). A party with write access to Valkey (its ACL user, the node, the
network path if Valkey TLS is off) can reset any counter or fill one. The limiter is a fairness and abuse control, not a
credential guard; the break-glass lockout is in PostgreSQL.

### H-113 · The fallback share is approximate

Live while Valkey is unavailable, on every replica. The share `1 ÷ n` uses the number of live replicas from the registry,
refreshed with the heartbeat: for a few seconds after a replica joins or is lost the shares are off. A client whose keep-alive
connection stays on one replica gets `1 ÷ n` of the budget, one that is spread over replicas gets the whole of it; the
bound the acceptance test checks (300 admitted of 1,000 in 2 s with the defaults) holds for a client spread evenly.

### H-114 · Anonymous callers behind one address share one bucket

Live when `server.trusted_proxies` does not name the proxy in front of trove: every client then has the proxy's address and
the anonymous class (5 requests per second, burst 20 by default) is spent by all of them together. With the proxy trusted, one
NAT in front of many users does the same for those users.

### H-115 · Nothing detects an operation that returns a secret without declaring it

Dormant today: no M1 operation returns a secret. It becomes live with the first one, token creation (identity area). The
idempotent operation sets `Outcome.SecretID` to keep its body out of the table; if it forgets, the secret is stored in
plaintext JSON in `api_idempotency_keys` for the retention period. The check for it is a review item and the E1.2 database
dump scan, not code in `Idempotency.Do`.

### H-273 · A preflight is answered without authentication and is not counted by the rate limits

Live today when `api.cors.allowed_origins` is not empty, for every allowed origin on every API path. The answer is constant and carries
no data, and it opens nothing else; what is not bounded is the number of preflights a client sends, which only the in-flight limit of
the listener stops. What makes it real: an allowed origin, or a script that sets the `Origin` header, sending preflights in a loop. Nothing in the
rate-limit classes counts them; an operator who needs a bound sets one at the ingress.

### H-298 · A README is rendered on every request without a tag and a rendering that runs over its time limit is not stopped

Live today on `GET /api/v1/packages/{packageId}/versions/{version}/readme` for every caller who may read a version with a README.
The renderer (`internal/packages/readme`) bounds the source (`ui.readme.max_source_bytes`, 512 KiB by default), the HTML (1 MiB) and
the time (two seconds), but the Markdown and HTML libraries cannot be cancelled: a rendering that exceeds the time is abandoned and its
goroutine runs to the end of its input, and nothing is cached, so a client that sends no `If-None-Match` pays a rendering each time.
What makes it real: a publisher who may write a README that is slow to render (deeply nested constructs up to the source limit) and a
reader, or a loop of readers, who request it without a tag; the work is bounded per request by the input size and not by the number of
requests, which only the rate-limit classes of R-API-016 count. The cache table of R-API-029 (Q-API-167) would bound the work to one rendering
per source and policy.
