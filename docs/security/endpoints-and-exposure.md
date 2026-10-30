# Endpoints and exposure

Status: implemented in M1 for the credential rules of an endpoint, endpoint-bound tokens, exposure rules as the ceiling of
the decision point, the rate-limit class of an endpoint and the operations that manage exposure rules. M2 adds the package
formats on an endpoint: the filter of npm's local documents, member names in the virtual engine and the cache-fill policy in
the remote and virtual engines; npm's remote and virtual documents are filtered too (H-295 stays for later formats). The replay
of journaled exposure changes arrives with the restore (M5).

This page says what an audience that reaches a further endpoint can present, what it can retrieve, what a refusal tells it,
and how fast a change of what it may retrieve takes effect. The listener, host and surface rules that decide which endpoint a
request belongs to are in [roles-and-trust-boundaries.md](roles-and-trust-boundaries.md); the mechanics are in
[developer/endpoints.md](../developer/endpoints.md); the rules are in [spec/endpoints.md](../spec/endpoints.md).

## Two ceilings, one decision point

```text
 customers listener ──► host check (421) ──► credential rules of the endpoint ──► stage 7 ──► content route
                                              (type, binding, anonymous: 401)       │ edge.SubjectFrom
                                                                                    ▼  (Subject.Endpoint)
                         authz.Engine.DecideOn ─► anonymous gate ──► EXPOSURE (addressed repository, every action)
                                                                     ──► repository facts ──► grants ∩ token scope
```

| Ceiling | What it bounds | Where | Fails |
|---|---|---|---|
| Credential rules (R-EP-010) | Which credentials the endpoint accepts: token types, binding to the endpoint, anonymous | `endpointRules` ([m1_convert.go](../../internal/app/m1_convert.go)), the stage ([stage.go](../../internal/auth/stage.go)) | closed: a refused credential is the `401` of an unknown token and never falls through to anonymous |
| Exposure (R-EP-013) | What any subject may read through the endpoint, administrators and `content:bypass` included; no write | `ExposureCeiling` ([exposure.go](../../internal/authz/exposure.go)), `ceilingRefuses` ([decide.go](../../internal/authz/decide.go)) | closed: an endpoint name the configuration does not hold, a format without exposure support, a version without the format's versioning, and a rule that does not compile admit nothing |

The endpoint a request belongs to is the listener's (R-EP-002); the one conversion `edge.SubjectFrom` copies its name into
`Subject.Endpoint`, and the decision point reads the ceiling from the subject, so neither a client nor a content route can
choose another endpoint's ceiling and a decision on a detached context keeps it (Q-EP-20). The architecture test
(`CheckSubjectConstruction`, [subject.go](../../internal/archtest/subject.go)) names a `Subject` built or edited outside
`internal/authz` and `internal/api/edge`. A token bound to `customers` is refused on every other endpoint and an unbound token or a PAT is refused on
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
- A raw path of an npm repository under `/files/`, and an item of the items API, passes the ceiling with the package and the
  version the format gives it, so a rule's versions, range and pre-release setting hold for a tarball as for the npm route, and a
  listing of `<package>/-/` shows the exposed versions only. A path the format places in no package (a scope folder) is not
  exposed (R-EP-014, Q-EP-59; `TestR_EP_014_FilesOnAnEndpointWithRules`).
- A listing on such an endpoint holds only the repositories with an enabled rule (`Readable`). A holder of
  `repositories:read-config`, who needs no read grant to list or read a repository's configuration, is bound by the
  exposure alone (`Decider.ExposedOn` in [ceiling.go](../../internal/authz/ceiling.go), through `edge.Access.Exposed`, used by
  [repositories_api.go](../../internal/api/v1/repositories_api.go) `visibleKeys`): on such an endpoint it lists the exposed
  repositories only, and `GET /api/v1/repositories/{key}` of another one answers the `404` of a missing one, decided before the
  lookup (R-EP-014; `TestR_EP_014_ExposedBindsConfigurationReaders`, `TestR_EP_014_ConfigurationReadersAreBoundByTheCeiling`).

## What a package document reveals

The ceiling decides a request; a package document lists many versions in one answer, so the format filters it with the
same rules (R-EP-018, [developer/endpoints.md](../developer/endpoints.md#package-formats-on-an-endpoint-r-ep-018-r-ep-019)).

- **One set of rules per answer.** The dispatcher reads the exposure view and the exposure fingerprint from the index the
  request decides on, and the view matches as the ceiling does (`ExposureView` in [exposure.go](../../internal/authz/exposure.go)),
  so a version a filtered document lists is one the ceiling lets the same caller download, and a hidden version is refused as
  an artifact. The rendition and the validator are keyed by the endpoint and the fingerprint: a rule change misses every cached
  rendition, and a `304` never confirms a copy that lists a version hidden since (`TestR_EP_018_ARuleChangeFailsTheEarlierValidatorOnEveryReplica`).
- **npm** (R-NPM-048): `versions`, `time`, the dist-tags, `maintainers` and the top-level fields follow the exposed versions;
  `latest` moves to the highest exposed release, and the README of a hidden former `latest` is not shown (Q-NPM-39). A hidden
  version, a dropped tag, a hidden tarball and a package without an exposed version answer the `404` of a missing one, decided
  for a version before any lookup. `?write=true`, which returns the unfiltered document a write starts from, answers `403
  endpoint_read_only`. A merged or rewritten document is read the way `JSON.parse` reads it, so a member repeated in an upstream
  document cannot carry an unfiltered copy past the filter (Q-NPM-40). What stays visible: `time.created`, `time.modified` and
  `Last-Modified` (H-60).
- **Member names.** On an endpoint with rules no answer carries `Trove-Resolved-From`, and a member's failure is reported as the
  addressed repository's, with its code and `Retry-After` (Q-EP-56; H-294).
- **Cache state.** On an endpoint with rules no answer of a format route carries `Cache-Status`: `formats.ServeRoute` removes it
  when the status line goes out, whoever set it, the npm handlers, the remote engine, the virtual merge or the headers of an error
  ([serve.go](../../internal/formats/serve.go), Q-EP-60). It would tell that the repository proxies an upstream, when an item was
  fetched, and a negative entry's `404` from a hidden item's (`TestR_EP_018_NoCacheStatusOnAnEndpointWithRules`).
- **A format without a filter is not served.** A format without the exposure adapter answers every read on an endpoint with rules
  with the `404` of a missing resource and every write with `403 endpoint_read_only`, and its repositories take no rule (Q-EP-57).

## Upstream contact from an endpoint

With `cache_fill: false`, the default of a further endpoint, no request on the endpoint makes trove contact an upstream
(R-EP-019): the subject carries the flag from the listener, the populate check refuses every subject, the remote engine serves
what its cache holds and answers an uncached item `403 populate_not_permitted` without a negative entry, and the virtual engine
treats such a member as a miss. A customer can therefore neither fill trove's cache with packages of its choosing nor make
trove's upstream traffic depend on its requests. Every prevented contact is counted in `trove_endpoint_cache_fill_refused_total`
(`TestR_EP_019_TheCacheFillPolicyOfAnEndpoint`).

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

### H-166 · A job that acts on behalf of a request decides without the request's endpoint

Stage: a background job or a command that decides for a request after the request is over.

Dormant: no job decides today. The request-driven work that decides off the request's context, a collapsed fetch and a fan-out
over the members of a virtual repository, decides with the subject it was given, and the subject carries the endpoint, so it keeps
the ceiling; the item-delete job runs what the request decided. A subject without an endpoint, which a job or a command has,
decides without an exposure ceiling, which is right for a maintenance job and wrong for one that serves a request's content
later. What makes it real: a job that decides on behalf of a request, started by a request on an endpoint with exposure rules,
and whose payload does not record the endpoint and does not put it on the job's subject.

### H-167 · The fallback share counts the replicas of one pool

Stage: the endpoint rate-limit classes while Valkey is unavailable (`startServingReplicaCount`).

Dormant: live only when web replicas run in a pool that no endpoint names. Such replicas serve the endpoints without a pool, as
the main Deployment does, but each replica divides the budget by the replicas of its own pool, so the main Deployment's share
is computed without them and the cluster admits more than the class's budget during the outage, at most by the ratio of all
serving replicas to the main Deployment's. Subject: the protection of an external endpoint against one token's load. What
makes it real: a pool value without an endpoint, during a Valkey outage.

### H-294 · The code of a member's failure tells that the addressed repository reaches an upstream

Stage: the answer of the virtual engine on an endpoint with rules when a remote member fails and nothing else has the item
(`Caller.asAddressed`, Q-EP-56).

Live wherever an npm virtual repository with a remote member is addressed through an endpoint with rules. The detail names only
the addressed repository, but the kind and the code stay (`502 upstream_error`, `504 upstream_timeout`, `503
upstream_assumed_offline` with `Retry-After`), so a consumer learns that the repository it was given resolves through an upstream
and when that upstream is failing. It learns no member name, no upstream host and nothing of the content; the `Cache-Status` of
the answer, which told the same and more, is removed on such an endpoint (Q-EP-60). Subject: the topology of
the addressed virtual repository. What makes it real: a deployment whose consumers must not know that a repository proxies a public
registry.

### H-295 · A merged or rewritten document is filtered only where its handler calls the filter

Stage: the npm handlers of the remote and virtual classes, and every later format's document handlers (R-EP-018).

Dormant for npm: its local, remote and virtual handlers filter (`exposeRows`, `exposedDocument`, tested on the R-EP-012 fixture of a virtual repository with a remote member), and
generic has no documents; live for every later format. The filter is the format's call: nothing in the core stops a handler that serves a merged or rewritten
document on an endpoint with rules without calling `FilterDocument` with `rq.Endpoint.Exposure`, and the ceiling, which decided the
package, would let every version of that document through to the listing. Downloads stay bound by the ceiling. Subject: the version
list of every exposed package of such a class. What makes it real: a remote or virtual document handler without the call; the
checklist in [developer/endpoints.md](../developer/endpoints.md#extension-checklists) and a test on the R-EP-012 fixture per class
are the guard.
