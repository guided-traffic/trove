# Management API: OpenAPI workflow

The contract is [api/openapi.yaml](../../api/openapi.yaml) (OpenAPI 3.1, R-API-002). Handlers live in
[internal/api/v1](../../internal/api/v1); the generated server interface and models in
[internal/api/v1/gen](../../internal/api/v1/gen).

## Workflow

1. Edit `api/openapi.yaml`. Every operation needs `operationId` (lowerCamelCase verb + noun), `tags`,
   `x-trove-permission` in a form the authorization step enforces (see below), and at least one problem
   response (`$ref` to the shared responses).
2. `go generate ./internal/api/...` (or `make generate`) does two things: it copies the document to
   `internal/api/v1/openapi.yaml`, which the binary embeds (`go:embed` cannot reach `../`), and it runs
   `go tool oapi-codegen -config cfg.yaml` (pinned by the `tool` directive of `go.mod`, models and
   `std-http-server` only) into `gen/api.gen.go`.
3. Implement the new method of `gen.ServerInterface` in `handlers.go`. An operation without a handler does
   not compile.
4. Run the tests. `TestDocument` fails on a stale embedded copy, an operation without a declaration, and
   a route that is registered but not documented (or the reverse); `TestAuthorizationStep` sends an anonymous
   request to every operation of the document and fails unless each non-public one answers 401.

`go generate` output is committed; `make generate-check` fails on a diff.

## Conventions

| Topic | Rule |
|---|---|
| Paths | Full path in the document (`/api/v1/system/ping`), so the generated ServeMux pattern is `GET /api/v1/system/ping` |
| Errors | `httpserver.Problem(w, r, err)` with an `*errs.Error`; never write an error body by hand. The wire members are those of `errs.Problem`: `type`, `title`, `status`, `detail`, `instance`, `code`, `requestId`, `errors` |
| JSON | `writeJSON` in `handlers.go`: `application/json; charset=utf-8`, UTC times with `Z`, empty collections as `[]`, "no value" as `null` |
| Caller | `httpserver.PrincipalFrom(ctx)`: kind, ID, name, groups and the credential that proved it (method, token ID and type, scope ceiling, endpoint binding); never read credentials from headers. Ask authorization with `v1.SubjectFrom(principal)`, see [m1-contracts.md](m1-contracts.md) |
| Permission check | done before the handler by the authorization step of [authorize.go](../../internal/api/v1/authorize.go), from the operation's declaration; a handler adds a check only where the decision depends on the request body (R-API-008) |
| Base URL | `httpserver.InfoFrom(ctx).BaseURL`, never `Host` |
| Authorization table | `Document.Operations()` maps the route pattern to the declarations, each decoded into an `Operation.Requirement` at load |
| Registration | `Server.Register(router)` registers through `httpserver.Router` behind `authorizingMux`, which wraps every handler in the check of the operation its pattern names; the composition root then calls `Document.CheckRoutes(router.Patterns())` |

`x-trove-permission` forms the step enforces in M0, and nothing else (`LoadDocument` refuses any other form,
so start-up stops before an operation can run with a declaration nobody checks):

| Declaration | Check | M0 operations |
|---|---|---|
| `{public: true}` | none | ping, the document, the problem catalog |
| `{authenticated: true}` | anonymous → 401 with `WWW-Authenticate: Bearer` | — |
| `{authenticated: true, publicSwitch: system.public_version}` | as above, unless the switch is on (`Options.PublicVersion`); the switch names are fixed in `publicSwitches()` | version |
| `{permission: <capability>}` | `Options.Access.Require(ctx, name)`: 401 for an anonymous caller who needs credentials, 403 without the capability; with `Access` nil: 401 anonymous, 403 otherwise; the name must be a capability of R-AUTHZ-010 | info, replicas (`settings:read`, `system.read` in M0) |
| `{role: <role>}` | `Access.Require(ctx, "role:<role>")`: the caller holds the system role | — |
| `{action: <action>, repository: query.<n> \| path.<n>[, package: …][, path: …]}` | `ActionAccess.RequireAction` decides the action on the coordinate built from the request (R-AUTHZ-015) | — |

A route whose pattern has no operation in the document is denied to everyone with 403 (and `CheckRoutes`
stops start-up for it). The binding of path parameters runs after the check. R-API-008 defines the full
grammar; every form that `parseRequirement` accepts is enforced by `decide` in `authorize.go`.

## Endpoints

The API is registered on every endpoint whose surface holds `api` (R-EP-005): the default endpoint always, a further
endpoint only by configuration (`surface: [packages, api]`). On any other endpoint `/api/v1/…` falls through to the
catch-all `404` problem, so `GET /api/v1/system/ping` on a package-only endpoint answers `404`, not `403`. A request on
an endpoint whose host check fails answers `421` `misdirected_request` before routing, whatever the path
([endpoints.md](endpoints.md)). The problem catalog lists the code (`GET /api/v1/problems/misdirected_request`).
`Document.CheckRoutes` runs for each endpoint that serves `api`.

## Operations in M0

| Operation | Path | Access |
|---|---|---|
| `getPing` | `GET /api/v1/system/ping` | anonymous; `200 OK` while the replica serves (ready, or draining after it was ready, Q-OPS-30), `503 UNAVAILABLE` otherwise |
| `getVersion` | `GET /api/v1/system/version` | authenticated, or anonymous with `system.public_version` |
| `getSystemInfo` | `GET /api/v1/system/info` | `settings:read` |
| `listReplicas` | `GET /api/v1/system/replicas` | `settings:read` |

`bridgeAccess` ([access.go](../../internal/app/access.go)) puts the dev-profile switch in front of the authorization
engine ([authorization.md](authorization.md)): `system.bootstrap_anonymous_read` opens `settings:read` to anonymous callers on the **default endpoint only**, and only
while that endpoint admits anonymous requests; a further endpoint that serves `api` answers 401 (Q-OPS-36). The route
check at start compares the document with the registered and the dropped patterns of each endpoint that serves `api`.
| `getOpenApiJson` / `getOpenApiYaml` | `GET /api/v1/openapi.json`, `.yaml` | anonymous; `servers[0].url` is the public base URL |
| `getProblemType` | `GET /api/v1/problems/{code}` | anonymous; resolves the `type` URI of a problem |

The jobs part of R-API-038 waits for M1.
