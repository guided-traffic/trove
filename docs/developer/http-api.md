# Management API: OpenAPI workflow

The contract is [api/openapi.yaml](../../api/openapi.yaml) (OpenAPI 3.1, R-API-002). Handlers live in
[internal/api/v1](../../internal/api/v1); the generated server interface and models in
[internal/api/v1/gen](../../internal/api/v1/gen).

## Workflow

1. Edit `api/openapi.yaml`. Every operation needs `operationId` (lowerCamelCase verb + noun), `tags`,
   `x-trove-permission`, and at least one problem response (`$ref` to the shared responses).
2. `go generate ./internal/api/...` (or `make generate`) does two things: it copies the document to
   `internal/api/v1/openapi.yaml`, which the binary embeds (`go:embed` cannot reach `../`), and it runs
   `go tool oapi-codegen -config cfg.yaml` (pinned by the `tool` directive of `go.mod`, models and
   `std-http-server` only) into `gen/api.gen.go`.
3. Implement the new method of `gen.ServerInterface` in `handlers.go`. An operation without a handler does
   not compile.
4. Run the tests. `TestDocument` fails on a stale embedded copy, an operation without a declaration, and
   a route that is registered but not documented (or the reverse).

`go generate` output is committed; `make generate-check` fails on a diff.

## Conventions

| Topic | Rule |
|---|---|
| Paths | Full path in the document (`/api/v1/system/ping`), so the generated ServeMux pattern is `GET /api/v1/system/ping` |
| Errors | `httpserver.Problem(w, r, err)` with an `*errs.Error`; never write an error body by hand. The wire members are those of `errs.Problem`: `type`, `title`, `status`, `detail`, `instance`, `code`, `requestId`, `errors` |
| JSON | `writeJSON` in `handlers.go`: `application/json; charset=utf-8`, UTC times with `Z`, empty collections as `[]`, "no value" as `null` |
| Caller | `httpserver.PrincipalFrom(ctx)`; permissions through the `Access` interface, never by reading headers |
| Base URL | `httpserver.InfoFrom(ctx).BaseURL`, never `Host` |
| Authorization table | `Document.Operations()` maps the route pattern to the declarations; the shared authorization step of R-API-008 will look up `r.Pattern` there |
| Registration | `Server.Register(router)` registers through `httpserver.Router`; the composition root then calls `Document.CheckRoutes(router.Patterns())` |

`x-trove-permission` values in M0: `{public: true}` (ping, the document, the problem catalog),
`{authenticated: true, publicSwitch: system.public_version}` (version) and `{permission: system.read}`
(info, replicas). R-API-008 defines the full grammar.

## Operations in M0

| Operation | Path | Access |
|---|---|---|
| `getPing` | `GET /api/v1/system/ping` | anonymous; `200 OK` while ready, `503 UNAVAILABLE` otherwise |
| `getVersion` | `GET /api/v1/system/version` | authenticated, or anonymous with `system.public_version` |
| `getSystemInfo` | `GET /api/v1/system/info` | `system.read` |
| `listReplicas` | `GET /api/v1/system/replicas` | `system.read` |
| `getOpenApiJson` / `getOpenApiYaml` | `GET /api/v1/openapi.json`, `.yaml` | anonymous; `servers[0].url` is the public base URL |
| `getProblemType` | `GET /api/v1/problems/{code}` | anonymous; resolves the `type` URI of a problem |

The jobs part of R-API-038 waits for M1.
