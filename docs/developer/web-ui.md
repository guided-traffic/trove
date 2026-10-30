# Web UI: workspace, build and serving

The UI is an Angular 22 workspace in [web/](../../web/) and a small Go package,
[internal/webui/](../../internal/webui/), that embeds the build and serves it under `/ui/{locale}/`.
Requirements: [R-ARCH-004](../spec/architecture.md), [R-UI-001](../spec/api-and-ui.md),
[R-UI-045](../spec/api-and-ui.md), [R-UI-046](../spec/api-and-ui.md),
[R-UI-047](../spec/api-and-ui.md).

The shell (skip link, header with user menu, side bar pinned from 1024 CSS px and a drawer below,
banner area, notice regions, router outlet), the HTTP layer, the session handling and every
cross-cutting UI mechanism are in place, so the screens are content. Built: sign-in, signed out, the system status
screen with its jobs screen, the repository list, wizard and edit form, the file tree, the endpoint screens with the exposure rule editor and
the audit log (see [Screens of the catalog area](#screens-of-the-catalog-area)); the other routed M1 screens are placeholders with their final
path and title until their work package replaces them. Requirements: [R-UI-002 to R-UI-005](../spec/api-and-ui.md),
[R-UI-010, R-UI-011](../spec/api-and-ui.md), [R-UI-020, R-UI-021](../spec/api-and-ui.md), [R-UI-037, R-UI-038](../spec/api-and-ui.md),
[R-UI-042 to R-UI-047](../spec/api-and-ui.md), [R-EP-017](../spec/endpoints.md); decisions Q-UI-10 to Q-UI-19 on the same page.

## Workspace layout

| Path | Responsibility |
|---|---|
| [web/package.json](../../web/package.json), `package-lock.json` | Exact versions (no `^`/`~`), committed lockfile. `overrides` lets openapi-typescript 7 (peer `typescript ^5`) install next to TypeScript 6, which Angular 22 uses |
| [web/.nvmrc](../../web/.nvmrc) | The one Node.js version for local runs and CI. Angular 22.2 requires `^22.22.3 \|\| ^24.15.0 \|\| >=26.0.0` (`engines` in package.json) |
| [web/angular.json](../../web/angular.json) | Locales (`en` source, `de`), `baseHref: /ui/`, `localize: true`, `i18nMissingTranslation: error`, bundle budgets, the Vitest unit-test builder |
| [web/tsconfig.json](../../web/tsconfig.json) | `strict`, `noUncheckedIndexedAccess`, `strictTemplates`, extended diagnostics as errors |
| [web/src/index.html](../../web/src/index.html) | Root element `<trove-root ngCspNonce="TROVE_CSP_NONCE">`; `internal/webui` replaces the literal on every response |
| [web/src/main.ts](../../web/src/main.ts) | `bootstrapApplication(App, appConfig)` |
| [web/src/locale/](../../web/src/locale/) | `messages.xlf` (extracted source), `messages.de.xlf`, [GLOSSARY.md](../../web/src/locale/GLOSSARY.md) |
| [web/src/styles/](../../web/src/styles/) | `_tokens.scss` (trove custom properties, spacing, light/dark), `_icons.scss` (self-hosted icon font), `main.scss` (Material theme, density -2) |
| [web/src/app/app.ts](../../web/src/app/app.ts), `app.html` | Root standalone component: skip link, header and user menu (language, theme, sign out), side bar from `core/navigation.ts`, banner, notices, outlet; opens the session dialogs |
| [web/src/app/app.config.ts](../../web/src/app/app.config.ts) | Router (component input binding, `TroveTitleStrategy`), `HttpClient` on fetch with the XSRF configuration of R-AUTH-012 and the three interceptors, `provideApiServices()`, global error listeners |
| [web/src/app/app.routes.ts](../../web/src/app/app.routes.ts) | `/login` and `/logged-out` without a guard; everything else under `authGuard`; one lazy `loadChildren` per area |
| [web/src/app/api/](../../web/src/app/api/) | `client.ts` (`ApiClient`), `problem.ts` (`Problem`, `ApiError`), `pagination.ts` (`collectAll`, `PagedList`), `types.ts` (helpers over the generated types), one `*-api.ts` per endpoint family (abstract service class, hand-typed DTO where the OpenAPI document lacks the operation, HTTP implementation), `providers.ts`, `testing/fakes.ts`. Catalog services typed from the generated schema: `repositories-api.ts` (repositories and the format catalog), `files-api.ts` (items, children, statistics, delete, upload and folder creation; the XHR client of Q-UI-16), `endpoints-api.ts` (endpoints, exposure rules, the dry run), `audit-api.ts`, `jobs-api.ts` (jobs and tasks); `csrf.ts` holds the CSRF cookie and header names; their fakes are in `testing/fakes-catalog.ts` and `provideFakeApis()` spreads them |
| `web/src/app/api/generated/` | `schema.d.ts` from openapi-typescript, committed, never edited |
| [web/src/app/core/](../../web/src/app/core/) | `session-store.ts`, `authz-epoch.ts`, `auth-guard.ts`, `navigation.ts`, `preferences-store.ts`, `notifier.ts`, `title-strategy.ts`, `focus-on-navigation.ts`, `storage.ts` (the only user of browser storage), `browser-navigation.ts` (`safeReturnTo`), `layout.ts`, `banner-store.ts`; `http/interceptors.ts` and `http/problem-messages.ts` |
| [web/src/app/shared/](../../web/src/app/shared/) | `ErrorNotice`, `Notices`, `SessionDialog`, `ConflictDialog` with `ConflictResolver`, `Banner`, `ScreenPlaceholder` and `placeholderRoute`; `access/` (building blocks of the identity and permission screens, see below); for the forms and lists of the catalog area: `PagedResource` (a "Load more" list as signals, with `start`, `more`, `reload`, `replace`, `remove`; used by every list screen) with `asApiError`, and `valueOf` (a resource's value without the throw of the error state), `merge-patch.ts` (RFC 7396 diff and apply), `field-errors.ts` (`ErrorSummary`, `fieldMessage`, `fieldErrorAt`, `problemsAt`), `unsaved-guard.ts`, `drafts.ts` (`DraftStore`: a draft across a re-authentication and the registry behind "Copy draft as JSON"), `confirm-dialog.ts` (`Confirm`: confirm, type-to-confirm, an optional choice that starts ticked or not), `diff-dialog.ts`, `copy-button.ts`, `job-progress.ts` (polling progress component) |
| [web/src/app/features/](../../web/src/app/features/) | One folder per area with an `<area>.routes.ts` (`login`, the identity and permission areas of the section below, `system`, `repositories`, `browse`, `endpoints` and `audit` carry built screens), `not-found` |
| [web/src/app/testing/](../../web/src/app/testing/) | `axe.ts`, the accessibility helper of the component specs; `screen.ts`, `renderScreen` (a screen against the fakes, dialogs without animation, a catch-all route), `settle`, `sleep`, `expectAccessible`, `browserStorage` and `storedText`, and the small DOM helpers of the specs (`text`, `button`, `buttonByText`, `type`, `typeInto`) |
| [web/e2e/](../../web/e2e/) | The Playwright journeys of R-UI-047 and their support code, a TypeScript project of its own (`web/e2e/tsconfig.json`, type-checked with `npx tsc -p e2e/tsconfig.json`); `@playwright/test`, `@axe-core/playwright` and `@types/node` are devDependencies of this workspace, so one lockfile pins them. Not part of the Angular build or of `make web-lint`. Described in [e2e.md](e2e.md#browser-journeys) |
| [web/scripts/](../../web/scripts/) | `build-embed.mjs`, `check-budget.mjs`, `check-rules.mjs` |
| [web/eslint.config.js](../../web/eslint.config.js) | angular-eslint plus the R-UI-046 rules |

## Commands

Run in `web/` after `nvm use` (or any Node matching `.nvmrc`).

| Command | What it does |
|---|---|
| `npm ci` | Install exactly the lockfile |
| `npm start` | Dev server, English only (the `development` configuration builds one locale) |
| `npm run lint` | ESLint (including the four R-UI-046 rules) and `scripts/check-rules.mjs` |
| `npm test` | Vitest through `@angular/build:unit-test`, jsdom, single run, headless; `src/test-setup.ts` raises the per-test timeout to 30 s, because the accessibility check of a large screen takes several seconds on a shared CI runner (the default of 5 s guards against hangs, it is no budget) |
| `npm run test:coverage` | The same with a V8 coverage report (`src/app/**`, generated code excluded) |
| `npm run extract-i18n` | Rewrites `src/locale/messages.xlf`; then add the `<target>` units to `messages.de.xlf` |
| `npm run generate:api` | `openapi-typescript ../api/openapi.yaml` into `src/app/api/generated/schema.d.ts`, committed. Rerun it whenever `api/openapi.yaml` changes |
| `npm run build` | Localized production build into `web/dist/web/browser/<locale>/` |
| `npx playwright install --with-deps chromium` | The browser build of the locked Playwright version, outside the repository (needed for the journeys, not for `npm test`) |
| `npm run build:embed` | `build`, the compressed-size budget check, then copy each locale to `internal/webui/dist/<locale>/` |

`make web` is expected to chain `npm ci`, `lint`, `test` and `build:embed`; the Makefile is owned by
the skeleton work package and was not checked from here.

## How the build reaches the binary

```text
web/ ──npm run build:embed──> web/dist/web/browser/{en,de}/ ──copy──> internal/webui/dist/{en,de}/
                                                                       internal/webui/dist/index.html  (committed placeholder)
internal/webui:  //go:embed all:dist
```

- `internal/webui/dist/index.html` is committed. It is a one-page notice and carries the nonce
  placeholder, so `go build ./cmd/trove` works on a clean checkout without Node.js (R-ARCH-003).
- The locale directories `internal/webui/dist/<locale>/` are **not committed**
  ([internal/webui/.gitignore](../../internal/webui/.gitignore) ignores `/dist/*/`). Release builds
  run `npm run build:embed` first. `build:embed` deletes stale locale directories and copies the new
  ones; it never touches `dist/index.html`, so a clean checkout and a built tree both start.
- `webui.New` decides at start-up: no locale directory in the tree means placeholder mode (every
  configured locale serves the placeholder page, `Handler.Placeholder()` is true); every configured
  locale built means normal mode; a mix fails start-up, so a half-built UI cannot ship.
- The Go package does not compress. Compression belongs to the edge or a middleware of the server
  shell.

## Serving rules

[internal/webui/webui.go](../../internal/webui/webui.go), mounted at `/ui/` by the server shell:

| Request | Answer |
|---|---|
| `/ui/` or `/ui/<route>` without locale | `302` to `/ui/<locale>/<route>` with the query kept. Locale: `Options.Preference`, then `Accept-Language` (q-values, `de-DE` matches `de`), then `Options.DefaultLocale`. `Cache-Control: no-store`, `Vary: Accept-Language` |
| `/ui/<locale>` | `302` to `/ui/<locale>/` |
| `/ui/<locale>/` and any path whose last segment has no `.` | The locale's `index.html` with nonce and document headers (SPA fallback) |
| `/ui/<locale>/<file with extension>` | The file; `404` problem+json if missing or a directory. It never falls back to the index |
| Method other than `GET`, `HEAD` | `405` with `Allow` |

**Nonce.** `New` reads each `index.html` once. Per response it draws 128 bits from `crypto/rand`
(`rand.Text()`), replaces every `TROVE_CSP_NONCE` with it, and puts the same value in
`style-src 'nonce-…'`. The Angular build copies the placeholder into the `nonce` attribute of the
script, modulepreload and stylesheet tags and keeps `ngcspnonce` on the root element, from which
Angular reads the nonce for styles it inserts. `New` refuses an `index.html` without the
placeholder. The CSP is the one of R-UI-001; image origins come from `Options.ImageOrigins` and
must be exact `https://host[:port]` values.

**Caching.** Files whose name ends in `-<8 upper-case alphanumerics>.<ext>` (the build's content
hash) get `Cache-Control: public, max-age=31536000, immutable`. `index.html` and other files
(`favicon.ico`) get `no-cache`. The build runs with `outputHashing: all`, so every script,
stylesheet and font is hashed. Unhashed files have no validator yet, so `no-cache` means a full
download each time; that is acceptable for the favicon and revisited if the unhashed set grows.

Mount and options:

```go
h, err := webui.New(webui.Options{
    Locales:       []string{"en", "de"}, // ui.locales
    DefaultLocale: "en",                 // ui.default_locale
    ImageOrigins:  nil,                  // exact https origins, appended to img-src
    Preference:    nil,                  // func(*http.Request) string, the user's stored locale
})
mux.Handle("/ui/", h) // do not strip the prefix
```

## Screens of the catalog area

| Route | Component | Services | Notes |
|---|---|---|---|
| `/admin/repositories` | [repository-list](../../web/src/app/features/repositories/repository-list.ts) | `RepositoriesApi`, `JobsApi` (progress) | Filters `q`, `class`, `format`, `state` in the query; cursor "Load more"; delete with typed key and job progress; "recently viewed" in browser storage |
| `/admin/repositories/new` | [repository-wizard](../../web/src/app/features/repositories/repository-wizard.ts) | `RepositoriesApi` | Five steps; classes and formats only from `GET /api/v1/formats`; live key check; `Idempotency-Key` from the moment the wizard opens; field errors mapped by pointer |
| `/admin/repositories/:key` | [repository-detail](../../web/src/app/features/repositories/repository-detail.ts) | `RepositoriesApi` | Tabs General, Access, API; merge patch with `If-Match`; 412 conflict dialog; consumer view without a form; delete |
| `/browse`, `/browse/{repo}/{path…}` | [browse](../../web/src/app/features/browse/browse.ts) | `RepositoriesApi`, `FilesApi`, `SessionStore.check` | `@angular/aria` tree with lazy pages; details General, Statistics, API; upload and folder dialogs, delete (204 or job) |
| `/admin/endpoints` | [endpoint-list](../../web/src/app/features/endpoints/endpoint-list.ts) | `EndpointsApi` | Inert names flagged |
| `/admin/endpoints/:endpoint` | [endpoint-detail](../../web/src/app/features/endpoints/endpoint-detail.ts) | `EndpointsApi`, `RepositoriesApi` | Read-only configuration, mode `all` banner, rule table with the enable switch (`PATCH {enabled}`), client addresses |
| `/admin/endpoints/:endpoint/rules/new`, `…/rules/:name` | [exposure-rule-editor](../../web/src/app/features/endpoints/exposure-rule-editor.ts) | `EndpointsApi`, `RepositoriesApi` | Dimensions by pattern subject; preview through `check-exposure` after 500 ms; diff dialog before `PUT`; create with an idempotency key |
| `/admin/audit` | [audit-log](../../web/src/app/features/audit/audit-log.ts) | `AuditApi` | Filters and the open event in the query; keyset "Load more"; UTC switch; export links |
| `/admin/system` | [system-status](../../web/src/app/features/system/system-status.ts) | `SystemApi`, `EncryptionApi`, `JobsApi` | Key card with per-replica rings, scheduled tasks card, recent jobs |
| `/admin/system/jobs` | [jobs-page](../../web/src/app/features/system/jobs-page.ts) | `JobsApi` | Filters `kind` and `state`; refresh with a pause control; cancel |

Every screen has a loading, an empty, an error and a filled state, covered by component specs that run axe (no serious or critical violation).
Decisions: Q-UI-14 (forms), Q-UI-15 (`ui.*` constants), Q-UI-16 (upload), Q-UI-17 (tree), Q-UI-18 (repositories), Q-UI-19 (endpoints, audit, status).

## Conventions that CI enforces

- **No NgModule, no zone** ([check-rules.mjs](../../web/scripts/check-rules.mjs)): `NgModule(`,
  `provideZoneChangeDetection` and `zone.js` do not occur in `web/src` or `angular.json`
  (R-ARCH-004). Zoneless and `OnPush` are the Angular 22 defaults, so no provider or option names
  them.
- **R-UI-046 lint rules** ([eslint.config.js](../../web/eslint.config.js)): no `.subscribe(`, no
  `NgZone`, no `bypassSecurityTrust*`, no `localStorage`/`sessionStorage` outside
  `src/app/core/storage.ts`.
- **R-UI-001**: no `bypassSecurityTrust` or `addSvgIcon` in `web/src`. Icons are font ligatures
  (`<span class="material-icons" aria-hidden="true">menu</span>`).
- **R-ARCH-007**: no string literal starting with `/files/` or `/api/<id>/` (other than `/api/vN/`)
  outside `web/src/app/api/`. Content URLs come from `downloadUrl` fields.
- **Missing translation fails the build** (`i18nMissingTranslation: error`): every UI text carries an
  `i18n` attribute or `$localize` with an `@@id`; ids are dotted (`shell.nav`). Add the German
  target in the same change and any new product term to the glossary.
- **Bundle budget** (R-UI-001): `build:embed` fails when the gzip size of the scripts that
  `index.html` loads exceeds 400 kB. Angular's own budget measures raw bytes, so `angular.json`
  keeps a loose raw guard (warning 800 kB, error 1500 kB).

## Test tiers (R-UI-047)

| Tier | State |
|---|---|
| Unit | In place: Vitest through the Angular builder, specs next to code (`*.spec.ts`), requirement IDs in test names. Covers the interceptors (`HttpTestingController`), the session store, pagination, problem messages, the status model |
| Component | In place with `TestBed` on jsdom; screens run against the fakes of `api/testing/fakes.ts` and `fakes-catalog.ts` through `testing/screen.ts`. Vitest browser mode (real Chromium) is **not** set up: it needs a browser provider package and browser binaries in CI. A spec that uses fake timers switches back to real timers before it calls axe, which schedules its own |
| Contract | Partly: the clients are typed against `schema.d.ts` and compile with it. Mock answers validated against the document are not written yet |
| Accessibility | Two tiers. On jsdom: `axe-core` (pinned in the lockfile) through `src/app/testing/axe.ts`; `app.routes.spec.ts` renders every routed screen and fails on a serious or critical violation (Q-UI-11). In the browser: `@axe-core/playwright` on every screen a journey visits (`expectAccessible` in [web/e2e/support.ts](../../web/e2e/support.ts)), with `color-contrast` and the document-level rules, in English with the light theme and in German with the dark theme (Q-UI-66). Focus order across a real navigation, reflow at 320 CSS px and the manual pass of R-UI-044 are not automated |
| End-to-end, security | In place: the Playwright journeys in [web/e2e](../../web/e2e) against the chart-installed build in the `scatter` profile, run by `TestE1_11Journeys` ([e2e.md](e2e.md#browser-journeys)). Every page is watched for `securitypolicyviolation`; one fails the test. Chromium only (Firefox and WebKit are not run), and the README XSS corpus is not part of the journeys |
| Internationalization | In place as the production build itself; verified by removing one German unit, which fails the build |

Go side: [webui_test.go](../../internal/webui/webui_test.go) covers nonces, headers, deep links,
caching, 404s, locale redirects, placeholder mode and start-up validation. Subtest names carry the
requirement ID (`R-ARCH-004`, `R-UI-001`, ...) for the coverage generator.

## Cross-cutting mechanisms

**HTTP layer** ([core/http/interceptors.ts](../../web/src/app/core/http/interceptors.ts)). Interceptors apply to
`/api/v1/` and `/auth/` only. Outermost first:

| Interceptor | Does |
|---|---|
| `problemInterceptor` | Maps a failed answer to `ApiError` (`status`, `problem`, `requestId`, `retryAfterSeconds`, `afterRetry`). 401 `unauthenticated` during use ends the session (`SessionStore.ended`, dialog "Your session has ended"); the probes `whoami` and `login-options` are exempt. 401 `reauthentication_required` sets `reauthRequired`. 403 drops the cached permission decisions |
| `resilienceInterceptor` | The one automatic retry of R-UI-042 (rules in Q-UI-12) |
| `conventionsInterceptor` | `Accept`, `Trove-Authz-Min-Epoch` from `AuthzEpoch`, `Idempotency-Key` from the request context; stores `Trove-Authz-Epoch` of an answer |

CSRF is not an interceptor of ours: `withXsrfConfiguration` makes Angular send `X-CSRF-Token` from the cookie
`__Host-trv_csrf` on unsafe same-origin requests (R-AUTH-012).

**ApiClient** ([api/client.ts](../../web/src/app/api/client.ts)). `get(path)` is typed from the generated document, `getJson<T>(url)`
for endpoints it lacks. `page<T>(url, filters)` returns a `PageFetcher` for `collectAll` (walk to the last page) and `PagedList`
("Load more"). `post(url, body, { idempotencyKey })` with `newIdempotencyKey()` generated when the form opens. `put` and `patch` take the
loaded `ETag` as a required argument; there is no wildcard. Answers carry `body`, `etag`, `location`. Reads for lists may also use `httpResource`
on the same URLs; the interceptors apply to it as well.

**Errors in screens.** `<trove-error-notice [error]="apiError" (retry)="...">` renders the translated message of the `code`
(`describeProblem`), or the English `detail` plus the request ID and a copy button for an unknown code. `ApiError.fieldErrors` maps
`errors[].pointer` to form controls. A 412 goes to `ConflictResolver.resolve({ mine, theirs })` (reload, overwrite, copy). `Notifier`
feeds the two live regions of `<trove-notices>`: `status` is polite, `alert` is immediate, an action keeps a notice until dismissed.

**Session.** `SessionStore` holds `whoami`, `authz/me`, the 60-second check cache (`check()` batches 100 per call), the `ended` and
`reauthRequired` flags. `signOut()` posts `/auth/logout`, goes to the returned `redirectTo` or `/logged-out`, and tells other tabs over a
`BroadcastChannel`. `authGuard` loads the session and sends an unauthenticated caller to `/login?returnTo=<route>` unless
`login-options` allows anonymous access. `safeReturnTo` keeps only server-relative paths. The sign-in screen builds
`return_to=/ui/<locale><route>` (R-AUTH-009) and appends `purpose=reauth` for a re-authentication.

**Secrets** never enter these services (R-UI-043): token values live in component signals only. `core/storage.ts` is for non-secret
convenience data and answers as if empty when the browser blocks storage.

<!-- >>> M1 ui-b -->
## Identity, token and permission screens

Built against the real management API with the generated types of `schema.d.ts`. Requirements: R-UI-002 to R-UI-005 (sign-in, sessions),
R-UI-024 to R-UI-027, R-UI-029 to R-UI-031, R-UI-042 to R-UI-045; decisions Q-UI-20 to Q-UI-25 in [api-and-ui.md](../spec/api-and-ui.md).

| Route | Screen (folder under `web/src/app/features/`) | Requirement | Capability that shows the entry |
|---|---|---|---|
| `/login`, `/logged-out` | `login/` (`Login`, `LoggedOut`): providers from `login-options`, auto-redirect, `?local=1` break-glass form, `returnTo`, `reauth=1` notice, "continue without signing in" | R-UI-004, R-AUTH-009 | none |
| `/profile` | `profile/` `Profile`: identity, groups with source, missing-group-claim notice, system roles, preferences with `If-Match` | R-UI-029 | none |
| `/profile/tokens` | `profile/` `ProfileTokens` over `tokens/` `TokenManager`: list, create, one-time display, revoke, rotate | R-UI-029 | none |
| `/profile/sessions` | `profile/` `ProfileSessions`: list, end one, end others | R-UI-029 | none |
| `/admin/users`, `/admin/users/:username` | `users/`: list with filters and paging, "Add user" dialog; detail with attributes, identities, groups, roles, tokens, sessions | R-UI-024 | `identities:read` |
| `/admin/groups`, `/admin/groups/:name` | `groups/`: list, create dialog; editor with members and their sources, roles, policies naming the group | R-UI-025 | `identities:read` |
| `/admin/service-accounts`, `/admin/service-accounts/:name` | `service-accounts/`: list, create dialog; detail with status, roles and the token section | R-UI-026 | `identities:read` |
| `/admin/roles` | `roles/` `RoleBindings`: the five roles, holders, bind and unbind with the last-administrator guard | R-UI-024 | `roles:read` |
| `/admin/permissions`, `/new`, `/:name` | `permissions/`: `PolicyList`; `PolicyEditor` (one page for create and edit, validation preview, pattern tester, "Try it", diff before save) | R-UI-027 | `policies:read` |
| `/admin/tokens` | `tokens/` `TokenAdmin`: filters, revoke one or selected, revoke by value | R-UI-030 | `tokens:read` |
| `/admin/settings/auth` | `settings/` `AuthSettings`: runtime values with ceilings, read-only status, `If-Match` save | R-UI-031 | `settings:read` |

Services of `api/` (abstract class, HTTP implementation, fake in [api/testing/access-fakes.ts](../../web/src/app/api/testing/access-fakes.ts)); every shape is a
`Schema<'…'>` of the generated document:

| Service | File | Operations |
|---|---|---|
| `SessionApi`, `LoginApi`, `PreferencesApi` | `session-api.ts`, `login-api.ts`, `preferences-api.ts` | `whoami`, `authz/me`, `authz/check`, `login-options`, `users/me/preferences` (generated types since wave 3; `PermissionCheck.action` stays a string) |
| `IdentityApi` | `identity-api.ts` | users, groups and service accounts; reads return `Versioned<T>` (body and `ETag`), merge patches take the loaded tag (`ApiClient.patch`), memberships as plain JSON without a tag (`patchJson`) |
| `TokensApi` | `tokens-api.ts` | list with filters, create (own and for a service account), rotate, revoke, revoke by value, revoke all |
| `SessionsApi` | `sessions-api.ts` | own sessions, `keepCurrent`, sessions of a user |
| `AuthSettingsApi` | `auth-settings-api.ts` | `GET`, `PATCH /settings/auth` with `If-Match` |
| `PolicyApi`, `RolesApi` | `authz-api.ts` | policies (`PUT` with `If-Match`, `validate-policy`), roles, role bindings (`ApiClient.putBinding`) |
| `PolicyExplainer` | `policy-explainer.ts` | the preview's "why can X do Y": `DraftPolicyExplainer` today, an implementation of `POST /authz/explain` once R-AUTHZ-024 is built |

Building blocks under [shared/access/](../../web/src/app/shared/access/): `TextField` and `SelectField` (label, hint,
error text tied with `aria-describedby`), `Needs` (the reason behind a missing write control, R-UI-005),
`TokenForm`, `TokenSecret` (one-time display), `TokenTable`, `PrincipalRoles`, `formatDuration`; the shared pieces they use (`PagedResource`, `Confirm`, `DraftStore`, `fieldErrorAt`) live one level up in `shared/`. [features/tokens/token-manager.ts](../../web/src/app/features/tokens/token-manager.ts)
is the one token list-and-create component used by the profile, the user detail and the service account detail. `TokenTable` marks a token whose `unknownEndpoints` is not empty (its binding names an endpoint that is not configured, R-EP-011) with a warning in the scope cell.

Conventions these screens follow:

- **Writes.** Creates send the `Idempotency-Key` generated when the form or dialog opened. Resources with an `ETag` (users, groups, service accounts, preferences, authentication settings, policies, repositories) are written
  with it; a 412 opens the conflict dialog and "Overwrite" sends the user's version with the tag read at that moment (Q-UI-22, reversed by Q-API-42 and Q-AUTH-50). The detail screens of
  users, groups and service accounts do this through `ConflictResolver.save` ([conflict-dialog.ts](../../web/src/app/shared/conflict-dialog.ts)): the merge patch, the loaded tag and
  a re-read of the record go in, the outcome (`saved`, `reloaded`, `dismissed`) comes out. Membership changes, disable, enable and unlock carry no tag.
- **Own permission changes.** A write of a policy or role binding answers `Trove-Authz-Epoch`; `conventionsInterceptor` keeps the highest one in `AuthzEpoch` and sends it as
  `Trove-Authz-Min-Epoch` on whatever follows, so the screens re-read without extra code ([access-http.spec.ts](../../web/src/app/api/access-http.spec.ts) proves the hand-off for a
  policy `PUT` and a role binding). After such a write the screen also calls `SessionStore.refreshCapabilities()`.
- **Re-authentication and expiry.** An editor that gets 401 `reauthentication_required` stores its non-secret entries with `DraftStore.keep` (session storage, keyed by route);
  after the sign-in returns to the route, `DraftStore.take` fills the form again. Editors register their unsaved state with `DraftStore.track`, which is what "Copy draft as JSON"
  in the "Your session has ended" dialog copies. Forms that hold a secret (a token value, a pasted token) use neither.
- **Secrets.** A created or rotated token value lives in a signal of the owning component and is passed to `<trove-token-secret>` as an input; it is shown in a password input with a
  reveal toggle and a copy button, and dropped when the panel is dismissed or the component is destroyed. "Revoke by value" clears its field whatever the answer was.
- **Lost token value.** A retried create that answers 409 `token_already_issued` carries the `tokenId`; `TokenManager` revokes it and creates again with a new key (R-UI-042).
- **Permission-driven rendering.** Entries show with `identities:read`, `policies:read` and so on; controls that need a write capability are disabled with `<trove-needs>` next to them
  (list pages) or absent with the same note at the top (detail pages). The API decides in any case.
- **Tests.** Component specs use `renderScreen` from [testing/screen.ts](../../web/src/app/testing/screen.ts) against the fakes; each screen's spec runs axe on its states
  (loaded, error, read-only, open dialog), including the one-time token display and the policy editor with validation errors.
<!-- <<< M1 ui-b -->

## How to add a feature area

1. Create `web/src/app/features/<area>/` with a standalone component per screen (`Component` with
   `selector: 'trove-…'`, no NgModule).
2. Replace the placeholder in `<area>.routes.ts` with a `loadComponent` and keep the `$localize` title with its `@@route.<area>….title` id. A new area gets a
   `<area>.routes.ts` and one `loadChildren` entry in [app.routes.ts](../../web/src/app/app.routes.ts), behind `authGuard` unless it must work without a session.
3. Read server data through the service of `api/` (add `api/<family>-api.ts`: abstract class, DTO, HTTP implementation; bind it in `providers.ts`;
   add a fake to `api/testing/fakes.ts`). Use the generated type when `schema.d.ts` has the operation. Shared state goes in an injectable store
   exposing read-only signals.
4. Create forms with an idempotency key from `newIdempotencyKey()` for creates and the loaded `ETag` for edits; show failures with `<trove-error-notice>`
   and field errors from `ApiError.fieldErrors` (`ErrorSummary` and `problemsAt` of `shared/field-errors.ts`). Forms use signals and native controls (Q-UI-14);
   read a resource's value with `valueOf(resource)` wherever the error state is shown next to it, because `resource.value()` throws in that state.
5. Add the navigation entry to `NAV_GROUPS` in [core/navigation.ts](../../web/src/app/core/navigation.ts) with the capability that shows it.
6. Mark every text with `i18n`/`$localize`, run `npm run extract-i18n`, add the German `<target>`s to `messages.de.xlf`, extend the glossary.
7. Write specs next to the code with the requirement ID in the test name, add the route to `SCREENS` in `app.routes.spec.ts` (heading, title and the axe check come with it),
   run `npm run lint` and `npm test`.
8. If the area changes the route table of R-UI-003, update the spec in the same change.

## What is not done yet

- `GET /api/v1/system/banner` is not wired to `BannerStore`. The catalog services, `system/encryption`, `whoami`, `login-options`, `authz/me`, `authz/check` and `users/me/preferences` use the generated types (Q-UI-12).
- System status: the backups card and route are placeholders (R-UI-048).
- What the API lacks for the catalog screens, and the UI works around (Q-UI-15, Q-UI-18, Q-UI-19): the values of `ui.browser.page_size` and
  `ui.deploy.max_upload_bytes`; a creation time and a sort by creation on the repository summary view; a settings schema in the format descriptor; a flag for
  formats with tags in the descriptor.
- The tree does not label the members of a virtual repository ("resolves from") or the cache of a remote one: M1 has neither class. Copy, move, properties and the trash can are P1.
- The repository settings forms cover the generic format; NuGet's `deleteMode` view, upstream presets, "Test connection", the member editor and the other format
  panels arrive with those formats.
- The session expiry warning of R-UI-002 (WCAG 2.2.1) is not built: it needs the session deadlines in an answer the UI can read (`whoami` shows `type: session` without an
  expiry; the timeouts are in `settings/auth`, which needs `settings:read`). The `reauthentication_required` dialog leads to `/login?...&reauth=1`; the identity, token,
  settings and policy editors and the catalog editors keep and restore their non-secret drafts (see above).
- No project filter, global search, help link or keyboard-shortcut switch in the header (P1 entries of R-UI-002).
- Forms: the identity, token, settings and policy forms use plain signals with `TextField` and `SelectField` (Q-UI-20), the catalog forms plain signals with native controls (Q-UI-14); in both the server's field errors by JSON pointer carry the validation, and the client checks only what saves a round trip. Signal Forms with validators derived from the OpenAPI constraints are not used.
- Browser-mode component tests and contract mocks. Of the Playwright tier, reflow at 320 CSS px, focus order across navigations, Firefox and WebKit, the client-setup journey with real `npm` and `dotnet` commands and the claim tester arrive with the screens and formats they need; the journeys of the M1 subset exist ([e2e.md](e2e.md#browser-journeys)).
- Local runs on a Node.js other than `web/.nvmrc` work only while the version satisfies `engines`; `make versions-check` names a mismatch.
- npm 11 prints `allow-scripts` warnings for the install scripts of `esbuild`, `lmdb`,
  `msgpackr-extract` and `@parcel/watcher`. Build, test and lint pass without running them; if a
  later npm turns the warning into an error, approve the scripts in the workspace configuration.
