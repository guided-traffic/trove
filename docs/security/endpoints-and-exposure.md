# Endpoints and exposure

Status: implemented in M1 for the credential rules of an endpoint, endpoint-bound tokens, exposure rules as the ceiling of
the decision point, the rate-limit class of an endpoint and the operations that manage exposure rules. Metadata filtering
and the cache-fill policy of the package formats arrive with npm (M2); the replay of journaled exposure changes with the
restore (M5).

This page says what an audience that reaches a further endpoint can present, what it can retrieve, what a refusal tells it,
and how fast a change of what it may retrieve takes effect. The listener, host and surface rules that decide which endpoint a
request belongs to are in [roles-and-trust-boundaries.md](roles-and-trust-boundaries.md); the mechanics are in
[developer/endpoints.md](../developer/endpoints.md); the rules are in [spec/endpoints.md](../spec/endpoints.md).

## Two ceilings, one decision point

```text
 customers listener ──► host check (421) ──► credential rules of the endpoint ──► stage 7 ──► content route
                                              (type, binding, anonymous: 401)       │ authz.WithEndpoint
                                                                                    ▼
                         authz.Engine.Decide ──► anonymous gate ──► EXPOSURE (addressed repository, every action)
                                                                     ──► repository facts ──► grants ∩ token scope
```

| Ceiling | What it bounds | Where | Fails |
|---|---|---|---|
| Credential rules (R-EP-010) | Which credentials the endpoint accepts: token types, binding to the endpoint, anonymous | `endpointRules` ([m1_convert.go](../../internal/app/m1_convert.go)), the stage ([stage.go](../../internal/auth/stage.go)) | closed: a refused credential is the `401` of an unknown token and never falls through to anonymous |
| Exposure (R-EP-013) | What any subject may read through the endpoint, administrators and `content:bypass` included; no write | `ExposureCeiling` ([exposure.go](../../internal/authz/exposure.go)), `ceilingRefuses` ([decide.go](../../internal/authz/decide.go)) | closed: an endpoint name the configuration does not hold, a format without exposure support, a version without the format's versioning, and a rule that does not compile admit nothing |

The endpoint a request belongs to is the listener's (R-EP-002); stage 7 copies its name into the request context, and the
decision point reads it from there, so neither a client nor a content route can choose another endpoint's ceiling
(Q-EP-20). A token bound to `customers` is refused on every other endpoint and an unbound token or a PAT is refused on
`customers` by default, so a leaked customer token works only on its endpoint and an employee token not on the customer's.

## What a refusal reveals

- A credential the endpoint does not accept answers like an unknown token: the body does not tell that the token is valid
  elsewhere. A type refusal makes no database lookup. The reason is recorded in `token.rejected` (`endpoint_not_accepted`)
  and in `trove_endpoint_credentials_refused_total`, never in the answer.
- A read the exposure does not admit answers the `404` of a missing resource, decided before the repository is looked up:
  a hidden file, a missing file, an unknown repository and a repository that requires authentication give the same bytes
  apart from `X-Request-Id`, for every subject (Q-EP-4, Q-EP-21). The audit event is `authz.denied` with reason
  `deny.exposure` and the endpoint's name.
- A write on an endpoint with exposure rules answers `403 endpoint_read_only` whether or not its target exists, so the
  refusal reveals nothing either.
- A listing on such an endpoint holds only the repositories with an enabled rule (`Readable`).

## How long a change takes

A rule is a row of `endpoint_exposure_rule`. Every write runs in one transaction with the change-log note of the area
`authz` and its audit event; every replica rebuilds its grant index from PostgreSQL, so a narrowed or deleted rule stops
exposing within the bound of R-AUTHZ-028 (the integration tests hold it to `cluster.epoch_poll_interval` + 1 s), also with
Valkey stopped, and a replica that cannot confirm its index answers `503 authz_stale` instead of deciding on an old one. A
request that sends the write's `Trove-Authz-Epoch` in `Trove-Authz-Min-Epoch` sees the change at once. No rule is read from
Valkey; a forged change message costs at most an epoch read.

## Who can change what an endpoint exposes

| Capability | Holders (R-AUTHZ-010) | Allows |
|---|---|---|
| `endpoints:read` | `admin`, `security-admin`, `auditor`, `repository-admin` | the endpoint list with configuration and counts, the rules, the dry run (with `read` on the repository) |
| `endpoints:write` | `admin`, `security-admin` | creating, replacing, changing and deleting rules |

Exposure and grants are separate keys: `policies:write` alone cannot open content to an endpoint, and `endpoints:write`
alone cannot give anyone a right, because exposure only narrows. The endpoints themselves are configuration-file data and
have no write operation. Updated and deleted rules carry a revocation-journal entry with the states before and after
(R-EP-020), written in the transaction of the change.

- **A session proves a recent sign-in for every write.** The four rule writes declare `x-trove-recent-auth: true`, and the shared
  authorization step answers `401 reauthentication_required` to a session whose interactive sign-in is older than
  `auth.sessions.recent_auth_window`, after it decided that the caller holds `endpoints:write` (R-API-007, Q-API-40). A token caller is
  not asked; its scope bounds it.
- **A repository takes its rules with it, and the trail says so.** Deleting a repository removes its rules through the foreign key; the
  delete hook records one `endpoint.exposure.deleted` per rule with the cause `repository_deleted` and the journal entry of R-EP-020,
  in the transaction that removes the row (Q-EP-25). The rules stopped exposing when the repository was marked `deleting`.
- **A merge patch does not merge over a concurrent change.** `PATCH` is written conditional on the revision it was computed on, also
  for `If-Match: *` (Q-EP-26).

## Rate limit of an endpoint

The class of `server.endpoint_rate_limits` an endpoint names is a cluster limiter per class, bucketed per endpoint and
token (or client address), in front of the management API's classes. While Valkey is unavailable each replica admits its
share, the budget divided by the live replicas of its pool, so the cluster stays within the budget.

## What this does not cover

### H-166 · A decision made outside the request's context has no endpoint ceiling

Stage: every caller of `authz.Authorizer` (`Decide`, `DecideOn`, `Readable`, `Check`).

Dormant: the only callers in this build are the management API's authorization step, which passes the request's context, and
the integration tests' stand-in content route. The ceiling applies to the endpoint that stage 7 put into the context; a
coordinate without one has no ceiling, which is right for a job and wrong for a content route that decides on
`context.Background()` or a detached context, or that answers without asking the decision point. What makes it real: a files
or format route that decides on another context than the request's.

### H-167 · The fallback share counts the replicas of one pool

Stage: the endpoint rate-limit classes while Valkey is unavailable (`startServingReplicaCount`).

Dormant: live only when web replicas run in a pool that no endpoint names. Such replicas serve the endpoints without a pool, as
the main Deployment does, but each replica divides the budget by the replicas of its own pool, so the main Deployment's share
is computed without them and the cluster admits more than the class's budget during the outage, at most by the ratio of all
serving replicas to the main Deployment's. Subject: the protection of an external endpoint against one token's load. What
makes it real: a pool value without an endpoint, during a Valkey outage.
