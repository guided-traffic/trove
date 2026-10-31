# Web UI: workspace, build and serving

The UI is an Angular 22 workspace in [web/](../../web/) and a small Go package,
[internal/webui/](../../internal/webui/), that embeds the build and serves it under `/ui/{locale}/`.
Requirements: [R-ARCH-004](../spec/architecture.md), [R-UI-001](../spec/api-and-ui.md),
[R-UI-045](../spec/api-and-ui.md), [R-UI-046](../spec/api-and-ui.md),
[R-UI-047](../spec/api-and-ui.md).

The shell (skip link, dark top bar with the repository search and the user menu, dark navigation sidebar pinned from 1024 CSS px
with folding groups and an icon rail and a drawer below, breadcrumbs, banner area, notice regions, router outlet; see
[Shell, design tokens and the list components](#shell-design-tokens-and-the-list-components)), the HTTP layer, the session handling and every
cross-cutting UI mechanism are in place, so the screens are content. Built: sign-in, signed out, the system status
screen with its jobs screen, the repository list, wizard and edit form with the format settings rendered from the format's schema (see
[The repository settings form](#the-repository-settings-form-rendered-from-the-formats-schema)), the file tree, the endpoint screens with the exposure rule editor and
the audit log (see [Screens of the catalog area](#screens-of-the-catalog-area)), and the package screens: the list, the details with the install panel and the README, and the client setup page (see [Package screens and client setup](#package-screens-and-client-setup)), and the guides area with the How to use tab of the repository screen (see [Guides](#guides)); the other routed M1 screens are placeholders with their final
path and title until their work package replaces them. Requirements: [R-UI-002 to R-UI-005](../spec/api-and-ui.md),
[R-UI-010, R-UI-011](../spec/api-and-ui.md), [R-UI-020, R-UI-021](../spec/api-and-ui.md), [R-UI-037, R-UI-038](../spec/api-and-ui.md),
[R-UI-042 to R-UI-047](../spec/api-and-ui.md), [R-EP-017](../spec/endpoints.md); decisions Q-UI-10 to Q-UI-19 and Q-UI-68 (shell and list layout) on the same page.

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
| [web/src/styles/](../../web/src/styles/) | `_tokens.scss` (trove custom properties: spacing, type, radii, elevation, content colours, the dark navigation colours, light/dark), `_icons.scss` (self-hosted icon font), `_shell.scss` (top bar, sidebar, rail, breadcrumbs), `_list-screens.scss` (data table, filter toggle, filter panel, list layout), `_catalog-screens.scss` (older shared classes), `main.scss` (Material theme, density -2) |
| [web/src/app/app.ts](../../web/src/app/app.ts), `app.html` | Root standalone component: skip link, top bar (wordmark, repository search, user menu with language, theme, sign out), sidebar from `core/navigation.ts` with the rail toggle, breadcrumbs, banner, notices, outlet; opens the session dialogs |
| [web/src/app/app.config.ts](../../web/src/app/app.config.ts) | Router (component input binding, `TroveTitleStrategy`), `HttpClient` on fetch with the XSRF configuration of R-AUTH-012 and the three interceptors, `provideApiServices()`, global error listeners |
| [web/src/app/app.routes.ts](../../web/src/app/app.routes.ts) | `/login` and `/logged-out` without a guard; everything else under `authGuard`; one lazy `loadChildren` per area |
| [web/src/app/api/](../../web/src/app/api/) | `client.ts` (`ApiClient`), `problem.ts` (`Problem`, `ApiError`), `pagination.ts` (`collectAll`, `PagedList`), `types.ts` (helpers over the generated types), one `*-api.ts` per endpoint family (abstract service class, hand-typed DTO where the OpenAPI document lacks the operation, HTTP implementation), `providers.ts`, `testing/fakes.ts`. Catalog services typed from the generated schema: `repositories-api.ts` (repositories and the format catalog), `files-api.ts` (items, children, statistics, delete, upload and folder creation; the XHR client of Q-UI-16), `endpoints-api.ts` (endpoints, exposure rules, the dry run), `audit-api.ts`, `jobs-api.ts` (jobs and tasks); `csrf.ts` holds the CSRF cookie and header names; their fakes are in `testing/fakes-catalog.ts` and `provideFakeApis()` spreads them |
| `web/src/app/api/generated/` | `schema.d.ts` from openapi-typescript, committed, never edited |
| [web/src/app/core/](../../web/src/app/core/) | `session-store.ts`, `authz-epoch.ts`, `auth-guard.ts`, `navigation.ts` (areas, groups, icons, capabilities), `nav-state.ts` (rail and folded groups per principal), `breadcrumbs.ts`, `preferences-store.ts`, `notifier.ts`, `title-strategy.ts`, `focus-on-navigation.ts`, `storage.ts` (the only user of browser storage), `browser-navigation.ts` (`safeReturnTo`), `layout.ts`, `banner-store.ts`; `http/interceptors.ts` and `http/problem-messages.ts` |
| [web/src/app/shared/](../../web/src/app/shared/) | `ErrorNotice`, `Notices`, `SessionDialog`, `ConflictDialog` with `ConflictResolver`, `Banner`, `ScreenPlaceholder` and `placeholderRoute`; `access/` (building blocks of the identity and permission screens, see below); for the forms and lists of the catalog area: `PagedResource` (a "Load more" list as signals, with `start`, `more`, `reload`, `replace`, `remove`; used by every list screen) with `asApiError`, and `valueOf` (a resource's value without the throw of the error state), `merge-patch.ts` (RFC 7396 diff and apply), `field-errors.ts` (`ErrorSummary`, `fieldMessage`, `fieldErrorAt`, `problemsAt`), `unsaved-guard.ts`, `drafts.ts` (`DraftStore`: a draft across a re-authentication and the registry behind "Copy draft as JSON"), `ui/data-table.ts` and `ui/filter-panel.ts` (the list components, see below), `confirm-dialog.ts` (`Confirm`: confirm, type-to-confirm, an optional choice that starts ticked or not), `diff-dialog.ts`, `copy-button.ts`, `job-progress.ts` (polling progress component) |
| [web/src/app/features/](../../web/src/app/features/) | One folder per area with an `<area>.routes.ts` (`login`, the identity and permission areas of the section below, `system`, `repositories`, `browse`, `endpoints` and `audit` carry built screens), `not-found` |
| [web/src/app/testing/](../../web/src/app/testing/) | `axe.ts`, the accessibility helper of the component specs; `screen.ts`, `renderScreen` (a screen against the fakes, dialogs without animation, a catch-all route), `settle`, `sleep`, `expectAccessible`, `browserStorage` and `storedText`, and the small DOM helpers of the specs (`text`, `button`, `buttonByText`, `type`, `typeInto`, `openFilters`) |
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
| `/admin/repositories` | [repository-list](../../web/src/app/features/repositories/repository-list.ts) | `RepositoriesApi`, `JobsApi` (progress) | Data table (no sort control: the API takes no `sort`); filter panel with `q`, `class`, `format`, `state`, applied into the query; cursor "Load more"; delete with typed key and job progress; "recently viewed" in browser storage |
| `/admin/repositories/new` | [repository-wizard](../../web/src/app/features/repositories/repository-wizard.ts) | `RepositoriesApi` | Five steps; classes and formats only from `GET /api/v1/formats`; live key check; the format's settings in the fourth step ("Access and npm settings") and in the review; `Idempotency-Key` from the moment the wizard opens; field errors mapped by pointer |
| `/admin/repositories/:key` | [repository-detail](../../web/src/app/features/repositories/repository-detail.ts) | `RepositoriesApi`, `GuidesApi` | Tabs General, Access, the format's settings ("npm settings", only when its schema has a member for the class), How to use (only for a format with clients, rendered when opened), API; merge patch with `If-Match`; 412 conflict dialog; consumer view without a form, with the guide below its facts; delete |
| `/browse`, `/browse/{repo}/{path…}` | [browse](../../web/src/app/features/browse/browse.ts) | `RepositoriesApi`, `FilesApi`, `SessionStore.check` | `@angular/aria` tree with lazy pages; details General, Statistics, API; upload and folder dialogs, delete (204 or job) |
| `/admin/endpoints` | [endpoint-list](../../web/src/app/features/endpoints/endpoint-list.ts) | `EndpointsApi` | Data table; name and rule count sort in the browser because the API answers the whole list; inert names flagged |
| `/admin/endpoints/:endpoint` | [endpoint-detail](../../web/src/app/features/endpoints/endpoint-detail.ts) | `EndpointsApi`, `RepositoriesApi` | Read-only configuration, mode `all` banner, rule table with the enable switch (`PATCH {enabled}`), client addresses |
| `/admin/endpoints/:endpoint/rules/new`, `…/rules/:name` | [exposure-rule-editor](../../web/src/app/features/endpoints/exposure-rule-editor.ts) | `EndpointsApi`, `RepositoriesApi` | Dimensions by pattern subject; preview through `check-exposure` after 500 ms; diff dialog before `PUT`; create with an idempotency key |
| `/admin/audit` | [audit-log](../../web/src/app/features/audit/audit-log.ts) | `AuditApi` | Data table (always newest first, no sort control); filter panel; filters and the open event in the query; keyset "Load more"; UTC switch; export links |
| `/admin/system` | [system-status](../../web/src/app/features/system/system-status.ts) | `SystemApi`, `EncryptionApi`, `JobsApi` | Key card with per-replica rings, scheduled tasks card, recent jobs |
| `/admin/system/jobs` | [jobs-page](../../web/src/app/features/system/jobs-page.ts) | `JobsApi` | Filters `kind` and `state`; refresh with a pause control; cancel |

Every screen has a loading, an empty, an error and a filled state, covered by component specs that run axe (no serious or critical violation).
Decisions: Q-UI-14 (forms), Q-UI-15 (`ui.*` constants), Q-UI-16 (upload), Q-UI-17 (tree), Q-UI-18 (repositories), Q-UI-19 (endpoints, audit, status),
Q-UI-78 to Q-UI-81 (the format settings).

## The repository settings form rendered from the format's schema

R-UI-021 and R-ARCH-004: the member of the repository document named after the format (`npm`) is edited in a form rendered from the
format's settings schema, `settingsSchema` of `GET /api/v1/formats` (Q-ARCH-229; npm's is
[settings.schema.json](../../internal/formats/npm/settings.schema.json)). Nothing in the UI names a setting: a format adds, changes or removes one
by editing its schema, and the wizard and the edit form follow. The server validates the member against the whole schema; the form only
offers controls ([Q-UI-78](../spec/api-and-ui.md#q-ui-78--the-repository-settings-form-renders-the-subset-of-json-schema-the-formats-use)).

| File | Responsibility |
|---|---|
| [settings-schema.ts](../../web/src/app/features/repositories/settings-schema.ts) | `settingNodes(schema, class, format)`: the members that apply to the class, as a tree of nodes with pointer, control id, label, description and default; `pruneDefaults`, `valueAt`, `withValue`, `numberOrText`, `targetOf` and `errorTarget` (the control a field error belongs to), `settingEntries` (the review) |
| [format-settings.ts](../../web/src/app/features/repositories/format-settings.ts) | `<trove-format-settings [nodes] [pointer] [(value)] [fieldErrors]>`: the controls, the defaults, the descriptions and the field errors |
| [repository-model.ts](../../web/src/app/features/repositories/repository-model.ts) | `RepositoryForm.formatMember` holds the member; `settingsOf(form, {name, schema})` writes it without its defaults; `documentOf` names it after the format |

**What each schema construct becomes.**

| Schema | Control |
|---|---|
| `type: object` with `properties` | a fieldset, legend = `title` or the member name, the description below it, its members inside; left out when no member applies to the class |
| `type: boolean` | a checkbox showing the value in effect (stored, else `default`); "Default: on" or "Default: off" |
| `enum` (any type) | a select of the values as written; the default is selected while the member is absent; "Not set" as a first option when there is no default |
| `type: string` | a text field, empty while absent; `format: uri` makes it a URL field. Durations and byte sizes (`30s`, `10MiB`) are strings with a pattern: the field takes the text, the server checks the pattern |
| `type: integer` or `number` | a text field with a numeric keyboard; a number is sent when the text is one as JSON writes it, otherwise the text, which the server refuses at the field |
| `type: [<type>, "null"]` | the control of `<type>`; an empty field leaves the member out, so the default (which may be `null`) applies |
| `x-trove-classes` | at every level, a member whose list lacks the repository's class is not rendered (the server checks it at the top level and answers `not_applicable`) |
| `default`, `description`, `title` | the value shown while a member is absent, the hint below the control (`lang="en"`), the label |
| anything else (`array`, `oneOf`, `$ref`, an object without `properties`) | the label, the description, the stored value as JSON and "Set it through the API"; the value is kept as it is |

**Rules.**

- *A default is not written* ([Q-UI-80](../spec/api-and-ui.md#q-ui-80--a-setting-equal-to-its-default-is-not-written)). `settingsOf` leaves out a setting equal to its default, an
  object left empty by that, and the member when nothing is left; a `required` member and a member the schema does not know stay. Both sides of the
  edit form's merge patch are pruned the same way, so a stored explicit default is no change, and a value set back to its default sends `null`.
- *Texts* ([Q-UI-79](../spec/api-and-ui.md#q-ui-79--the-labels-and-descriptions-of-the-settings-form-are-the-schemas)). Labels and descriptions are the schema's, in English;
  only the texts the form adds are translated (`repo.format.*`).
- *Where it is* ([Q-UI-81](../spec/api-and-ui.md#q-ui-81--the-format-settings-are-a-section-of-the-class-specific-step-and-a-tab-of-the-edit-form)). The fourth wizard step and a
  tab of the edit form, only when `settingNodes` is not empty. Choosing another class or format in the wizard empties the member.
- *Field errors.* The control id is the pointer with every run of other characters than letters, digits and `_` turned into `-`, prefixed with `f`
  (`/npm/audit/timeout` is `f-npm-audit-timeout`). An error goes to the deepest rendered member its pointer names, else to the enclosing object, else
  to the section (`f-npm`); the error summary focuses the same element.
- *Tests.* [settings-schema.spec.ts](../../web/src/app/features/repositories/settings-schema.spec.ts) and
  [format-settings.spec.ts](../../web/src/app/features/repositories/format-settings.spec.ts) run against `RICH_SETTINGS_SCHEMA` of
  [fakes-catalog.ts](../../web/src/app/api/testing/fakes-catalog.ts) (npm's members plus a nested object of the remote class with an enum, a nullable URL,
  durations, an integer and an array); the wizard and edit specs against `NPM_SETTINGS_SCHEMA`, a copy of npm's schema. The browser journey
  `08-packages.spec.ts` creates and edits an npm repository with it ([e2e.md](e2e.md#browser-journeys)).

**A format that needs more.** A setting the subset cannot express (a list of strings, a choice between shapes) is first a case for extending
`settingNodes` and the component with one more kind; a panel under `features/formats/<id>/` is for an interaction a schema cannot express at all
(R-ARCH-004), such as NuGet's view of the effective `deleteMode`.

## Package screens and client setup

R-UI-006 to R-UI-009, R-UI-016 and R-UI-017 against the contract of R-API-029 and R-API-030, which the server implements
([packages.md](packages.md)); the component specs run against the generated types and the fakes of
[fakes-packages.ts](../../web/src/app/api/testing/fakes-packages.ts), and the Playwright journeys against the real server. The clients are the
format's own ids (`npm`, `pnpm`, `yarn`, `yarn-classic`, Q-UI-77).

| Route | Component | Services | Notes |
|---|---|---|---|
| `/packages` | [package-list](../../web/src/app/features/packages/package-list.ts) | `PackagesApi`, `RepositoriesApi`, `PackageMemory` | Shared data table and filter panel; columns type, name, latest version, description, first created, updated (relative, absolute in `datetime` and the title), versions, repositories, downloads; sortable: name, first created, updated, as the API sorts; filters package type, repository, name or description, keyword, created period; chips; tabs All, Recently viewed, Saved views (`tab`) |
| `/packages/{format}/{name…}` | [package-detail](../../web/src/app/features/packages/package-detail.ts) | `PackagesApi` | `packageMatcher` in [packages.routes.ts](../../web/src/app/features/packages/packages.routes.ts); `lookup`, then the versions (all pages, newest first by version), then the selected version; `?version=`, `?tab=`, `?prerelease=true`; header facts, version combobox, tabs README (only with `hasReadme`), Metadata, Files, Repositories, All versions; install panel above the tabs and, from 60 rem of content width, in a sticky right column |
| (inside the package page) | [install-panel](../../web/src/app/features/packages/install-panel.ts) | `ClientSetupApi` | Client and repository pickers; the install snippet with `credentials=none`; copy; collapsed above three lines; link to `/setup` |
| (inside the package page) | [package-readme](../../web/src/app/features/packages/package-readme.ts) | none | Binds the server's sanitized HTML; finishes it (below) |
| `/setup` | [client-setup](../../web/src/app/features/setup/client-setup.ts) | `ClientSetupApi`, `RepositoriesApi`, `TokensApi`, `SessionStore` | Steps package type, client, repository, credentials; tabs Configure, Install, Publish; `?format=&client=&repository=` |

Files by purpose: [clients.ts](../../web/src/app/features/setup/clients.ts) (the clients per format and the explanation by snippet `id`),
[token-shape.ts](../../web/src/app/features/setup/token-shape.ts) (prefix, length, alphabet and CRC-32 of a pasted token),
[package-links.ts](../../web/src/app/features/packages/package-links.ts) (`safeHttpUrl`: only `http` and `https` metadata links become links; `packageCommands`),
[package-memory.ts](../../web/src/app/features/packages/package-memory.ts) (recent packages and saved views in browser storage per principal),
[format-size.ts](../../web/src/app/features/packages/format-size.ts), [format-time.ts](../../web/src/app/features/packages/format-time.ts).

**Rules the screens follow.**

- *Sorting and filtering are the server's.* The list screen sorts and filters nothing; a column is sortable only where `PackageSort` of
  [packages-api.ts](../../web/src/app/api/packages-api.ts) names it. Every change of filter or sort restarts the cursor walk.
- *The setup page owns its token* (R-UI-043, Q-UI-74). The token is a private signal of the component; `fillToken` replaces `{{token}}` literally; a
  pasted value that fails `checkTokenShape` never reaches the snippets; choosing another repository or credential mode, and leaving the route, clears it.
  The specs scan both storage areas and the URL. Security note: [tokens-and-credentials.md](../security/tokens-and-credentials.md#tokens-in-the-browser-the-client-setup-page).
- *The README component finishes the HTML* (Q-UI-71). Angular's sanitizer removes every `id` and `name`, so the component derives `user-content-<slug>`
  from each heading with `headingSlug`; the server's renderer uses the same rule for its table-of-contents links
  (`readme.HeadingSlug`), and both tests carry one corpus of 35 headings (Q-UI-76: a change of the rule is made in both). The CSP lists the image origins
  of `ui.readme.external_image_origins` ([csp.go](../../internal/webui/csp.go)); the component keeps an `https` image or a path of this origin and removes
  anything else.
- *Metadata links* (`homepage`, `repositoryUrl`, `bugsUrl`) become links only for `http` and `https`, with `rel="nofollow noopener noreferrer"`.
- *Breadcrumbs.* A matcher route with `data.crumbParam` adds one step with that parameter (`@team-a/ui`); see `collect` in
  [breadcrumbs.ts](../../web/src/app/core/breadcrumbs.ts).
- *New texts.* Snippet explanations are `$localize` texts keyed by the snippet's stable `id`; a snippet with an unknown `id` is shown without one.

## Guides

R-UI-049 against the operations of R-API-053; decisions Q-UI-83 to Q-UI-87. The text of a guide is the format's, rendered and sanitized by the server;
the UI knows no format. Everything it offers comes from `GET /api/v1/formats`: a format with `clients` has a guide (kit check 8).

| Route or place | Component | Services | Notes |
|---|---|---|---|
| `/guides` | [guide-page](../../web/src/app/features/guides/guide-page.ts) | `GuidesApi`, `RepositoriesApi` | Fieldsets package type (formats with clients), client, repository (none, or a readable repository of the format, virtual first) and, without a repository, the class (virtual preselected); `?format=&client=&repository=&class=`; a repository opens `GuidesApi.repository`, a class `GuidesApi.format`; a link to `/setup` with the same choice |
| "How to use" tab and the consumer view | [repository-guide](../../web/src/app/features/repositories/repository-guide.ts) | `GuidesApi` | The format's clients as radios; links to `/setup` and `/guides` with format, client and repository |
| (inside both) | [guide-view](../../web/src/app/features/guides/guide-view.ts) | none | A `nav` contents list whose links focus the section heading; one `section` per guide section with its `h2`, the HTML through `trove-package-readme`, the snippets with a copy button; `lang="en"` on the guide's own text (Q-UI-86) |
| `/setup` | [client-setup](../../web/src/app/features/setup/client-setup.ts) | | A link "Read the guide" with the page's format, client and repository |

Files: [guides-api.ts](../../web/src/app/api/guides-api.ts) (`GuidesApi`, `HttpGuidesApi`, the types `Guide`, `GuideSection`, `FormatClient`),
[fakes-guides.ts](../../web/src/app/api/testing/fakes-guides.ts) (`FakeGuidesApi`: npm's section ids and topics, snippets taken from the setup fake, `anonymous`
and `exposureRules` switches, `failure`), [guides.routes.ts](../../web/src/app/features/guides/guides.routes.ts); the side bar entry in
[navigation.ts](../../web/src/app/core/navigation.ts). Specs: [guides.spec.ts](../../web/src/app/features/guides/guides.spec.ts) (the formats offered, the
defaults, a repository and a class from the query, the snippets equal the setup fake's answer with the variable, the anonymous reader, the contents
links, the README binding, a failure), the How to use tab and the consumer view in
[repository-detail.spec.ts](../../web/src/app/features/repositories/repository-detail.spec.ts), `/guides` in the route map of `app.routes.spec.ts`. The browser
journey is `09-guides.spec.ts` ([e2e.md](e2e.md#browser-journeys)).

**Rules.**

- *The guide never handles a token.* Its answer carries `credentials` `env` or `none` (Q-API-176); the copy buttons copy what the server sent. A token is the
  setup page's (Q-UI-74), which the guide links to.
- *A section's HTML is bound like a README* (Q-UI-85): the same component finishes it (link targets, image sources, heading anchors). The anchors of the
  sections are `<prefix>-<section id with dots as hyphens>`, so the guides page (`guide-…`) and the tab (`repo-guide-…`) never share an id.
- *Clients come from the catalog.* The guides area and the tab read `clients` of the format descriptor; the setup page and the install panel still read
  the table of [clients.ts](../../web/src/app/features/setup/clients.ts) (Q-UI-70, revisited for NuGet).

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

## Shell, design tokens and the list components

Decision [Q-UI-68](../spec/api-and-ui.md#q-ui-68--layout-of-the-application-shell-and-the-list-screens); requirements
[R-UI-002](../spec/api-and-ui.md), [R-UI-005](../spec/api-and-ui.md), [R-UI-044](../spec/api-and-ui.md).

### The shell

| Piece | Where | What it does |
|---|---|---|
| Top bar | [app.html](../../web/src/app/app.html), `mat-toolbar.topbar` | Wordmark, the repository search (`form[role=search]`; submit goes to `/admin/repositories?q=`; shown only with `repositories:read-config`, because the repository list is its API), user menu. The slot for the global search (R-UI-015), the project filter and the help link stays empty until their APIs exist |
| Navigation model | [core/navigation.ts](../../web/src/app/core/navigation.ts) | `NAV_AREAS`: areas, each with items and folding groups; an item has `route`, `label`, `icon` (a ligature of the icon font, decoration only) and the `capability` that shows it. `Navigation.areas()` drops entries the caller lacks and groups left empty |
| Sidebar | `mat-sidenav.sidebar` | Pinned from 1024 CSS px (`Layout.pinned`), a drawer below. Group headings are buttons with `aria-expanded`; the current page is `aria-current="page"` with a filled row, an accent bar and bold text. The foot button (`aria-pressed`) compacts it to the rail: `.rail` hides the labels visually (they stay in the DOM as accessible names), always shows the groups' entries and shrinks the sidebar to `--trove-nav-width-collapsed`; `App` calls `MatSidenavContainer.updateContentMargins()` after the change, because Material measures the sidebar once |
| Persisted state | [core/nav-state.ts](../../web/src/app/core/nav-state.ts) | Rail on or off and the folded group ids, in `localStorage` through `BrowserStorage` under `trove.nav.<principal name>`; blocked storage means the state lasts for the tab. Non-secret (R-UI-043) |
| Breadcrumbs | [core/breadcrumbs.ts](../../web/src/app/core/breadcrumbs.ts) | `collect()` walks the activated route snapshot: `data.crumbs` of a route (`{ label, link }`, used for the area names and for lazy wrapper routes, see `wrapper()` in [app.routes.ts](../../web/src/app/app.routes.ts)), a string `title`, a `:param` segment (shows the value), and the file tree's matcher (one crumb per segment). A trail of one step is dropped. A new area adds `data: wrapper(<title>)` to its `loadChildren` route, otherwise its detail screens lose the link to the list |
| Styles | [styles/_shell.scss](../../web/src/styles/_shell.scss) | Global and scoped under `trove-root`, because the root component's own stylesheet is held to the component style budget of `angular.json` |

### Design tokens

[styles/_tokens.scss](../../web/src/styles/_tokens.scss) holds the custom properties; components use them and no literal colour or length. Material's own
`--mat-sys-*` tokens come from `mat.theme` in `main.scss`.

| Group | Properties | Notes |
|---|---|---|
| Spacing | `--trove-space-1` to `-8` | 4, 8, 12, 16, 20, 24, 32 px |
| Type | `--trove-font-mono`, `--trove-text-muted-size`; sizes from `--mat-sys-*` | One system font stack (set in `main.scss`) |
| Shape and elevation | `--trove-radius-s`, `-m`, `--trove-elevation-1`, `-2` | The shadows use `light-dark()`, so the dark scheme gets darker ones |
| Content | `--trove-border`, `--trove-border-strong`, `--trove-text-muted`, `--trove-accent`, `--trove-accent-container`, `--trove-surface-raised`, `--trove-table-head-bg`, `--trove-row-hover`, `--trove-focus-ring` | The accent is the Material primary: one colour for active and primary states |
| Status | `--trove-status-ok`, `-warn`, `-bad` | Always with an icon and a text |
| Navigation | `--trove-nav-bg`, `-bg-deep`, `-fg`, `-fg-muted`, `-hover`, `-active`, `-accent`, `-border`, `--trove-nav-width`, `-width-collapsed`, `--trove-topbar-height` | Dark in both schemes (the sidebar and the top bar do not follow the theme). Contrast was measured, not assumed: text 8.3:1 to 17:1, muted text on the hover fill 6.3:1, the accent mark on the active fill 4.8:1, the search field's outline 3.7:1 (AA asks 4.5:1 and 3:1). Recompute when a value changes |

The themes are the ones of [R-API-021](../spec/api-and-ui.md): `color-scheme: light dark`, and `data-theme` on the root element for an explicit choice.

### The data table

[shared/ui/data-table.ts](../../web/src/app/shared/ui/data-table.ts), `<trove-data-table>`. It owns the frame; the rows stay in the screen.

```html
<trove-data-table
  [caption]="caption"            <!-- accessible name, rendered as a visually hidden <caption> -->
  [columns]="columns"            <!-- TableColumn[]: id, label, help?, sortable?, numeric?, hideLabel? -->
  [sort]="sort()"                <!-- TableSort | null: { column, direction: 'asc' | 'desc' } -->
  (sortChange)="sortBy($event)"
  [loaded]="list.loaded()" [loading]="list.loading()" [error]="list.error()"
  [empty]="list.items().length === 0" emptyText="No user matches."
  [hasMore]="list.hasMore()" (more)="list.more()" (retry)="list.reload()"
>
  @for (u of list.items(); track u.id) {
    <tr><th scope="row"><a [routerLink]="...">{{ u.username }}</a></th><td>...</td></tr>
  }
</trove-data-table>
```

- **Sorting is a request.** `sortChange` carries the column and direction; the screen restarts its list with the `sort` parameter the API understands (`'-username'` for descending) or sorts a complete list itself (endpoints). Only mark a column `sortable` where one of the two is true. The `<th>` gets `aria-sort`; the header is a `<button>` that toggles ascending and descending.
- **Help.** A column with `help` gets a "?" button with the accessible name "About the column X" and a `matTooltip` whose message is also its `aria-describedby`; the tooltip opens on focus and hover and closes with Escape.
- **States.** Not loaded and loading: a status line. Loaded and `empty`: `emptyText`. `error`: `<trove-error-notice>` with the retry output. Otherwise the table in a focusable scroll region (`tabindex="0"`, `role="region"`, labelled by the caption) with a sticky header; the region scrolls, not the page. `hasMore` shows "Load more".
- **Rows.** Columns and cells must agree in number and order; an actions column uses `hideLabel` and `td.actions`; a figure column uses `numeric` and `td.num`. `.secondary` is the muted second line of a cell. A long identifier in a `<code>` wraps.
- The token table ([shared/access/token-table.ts](../../web/src/app/shared/access/token-table.ts)) wraps the data table for the three screens that list tokens and passes `hasMore`, `loading` and `more` through.

### The filter panel

[shared/ui/filter-panel.ts](../../web/src/app/shared/ui/filter-panel.ts): `<trove-filter-toggle>`, `<trove-filter-panel>`, `<trove-filter-category>`.

```html
<div class="list-layout" [class.panel-open]="panelOpen()">
  <div class="list-main">
    <div class="list-toolbar">
      <trove-filter-toggle [(open)]="panelOpen" [activeCount]="activeCount()" [controls]="panel.panelId" />
    </div>
    <trove-data-table ...>...</trove-data-table>
  </div>
  <trove-filter-panel #panel class="list-side" [(open)]="panelOpen" [activeCount]="activeCount()"
                      (apply)="apply()" (clearAll)="clear()">
    <trove-filter-category label="Status" [active]="!!status()" [expanded]="!!status()">
      <trove-select-field label="Status" [options]="statusOptions" [(value)]="status" />
    </trove-filter-category>
  </trove-filter-panel>
</div>
```

- The panel holds no values. The screen keeps the staged values as signals (or in the query parameters, as the repository and audit screens do), counts the **applied** filters for `activeCount`, and reacts to `apply` (the apply button submits the form) and `clearAll`.
- A category is a heading button with a chevron (`aria-expanded`); give it `[active]` and `[expanded]` of its filter, so that an applied filter is never hidden. A screen that can be reached with a filter in the URL (policies, audit, repositories) opens the panel itself.
- Focus moves to the panel heading on opening and back to the opener on closing; Escape closes. The form is a `search` landmark named by its heading.
- Controls inside a category need their own accessible name (`aria-label` for a native control, the label of `TextField` and `SelectField` otherwise); the category button is not their label.
- `.list-layout` puts the panel to the right of the table and below `52rem` of content width above it (container query on `.content`). Only add a filter that a request parameter of the API backs.

### Add a list screen

1. Declare the columns (`TableColumn[]` with `$localize` labels; `sortable` only where the API sorts, `help` where a column needs explaining) and bind the states of the list to `<trove-data-table>`.
2. If the API sorts, add `sort` to the filter type in `api/<family>-api.ts`, pass it through the HTTP client (it is part of `cleanQuery(filters)`), and make the fake honour it (`sorted()` in [testing/access-fakes.ts](../../web/src/app/api/testing/access-fakes.ts)).
3. If the screen has filters, add the panel, one category per filter, and the three signals (`panelOpen`, staged values, `activeCount`).
4. Specs: a table spec (sort flips `aria-sort` and the order of the rows; help buttons are named), a panel spec (the count, "Clear all", apply reaches the API or the router), `openFilters(fixture, root)` from `testing/screen.ts` before a spec types into a filter; the axe check with the panel open. The component specs [data-table.spec.ts](../../web/src/app/shared/ui/data-table.spec.ts) and [filter-panel.spec.ts](../../web/src/app/shared/ui/filter-panel.spec.ts) cover the components themselves.
5. Browser journeys: use `openFilter(page, '<category>')` from [web/e2e/support.ts](../../web/e2e/support.ts) before using a filter field.

<!-- >>> M1 ui-b -->
### The base URL rule

Every address the UI shows or copies as a client address, and the `curl` text of the repository forms, is built from the **server's configured base
URL**, never from `location` (Q-UI-69, Q-UI-75). [ServerAddress](../../web/src/app/core/server-address.ts) answers `baseUrl()` from the `baseUrl` member of
`GET /api/v1/formats` (`RepositoriesApi.baseUrl()`), which every caller that may use the UI can read: no permission, no readable repository and no second
request is needed. The server fills it from the configured base URL of the endpoint the page was served from, and never from a `Host` header;
`null` means that none is configured, and the screen shows no address (the review step of the repository wizard an en dash) instead of a wrong one. A
`null` or a failed read is not remembered.

`clientAddress(baseUrl, format, key)` and `apiAddress(baseUrl, path)` in [repository-model.ts](../../web/src/app/features/repositories/repository-model.ts)
build the strings. Lists and the consumer view show the absolute `addresses` of the server unchanged. A spec that proves the rule renders a screen under
jsdom's own origin and asserts that the shown address carries the fake's configured base URL and not `location.origin`
([server-address.spec.ts](../../web/src/app/core/server-address.spec.ts) also covers a caller with `repositories:write-config` and nothing else).

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
5. Add the navigation entry to `NAV_AREAS` in [core/navigation.ts](../../web/src/app/core/navigation.ts) with its icon and the capability that shows it, and `data: wrapper(<title>)` to the `loadChildren` route in `app.routes.ts` so that the breadcrumbs link the list from the detail screens.
6. Mark every text with `i18n`/`$localize`, run `npm run extract-i18n`, add the German `<target>`s to `messages.de.xlf`, extend the glossary.
7. Write specs next to the code with the requirement ID in the test name, add the route to `SCREENS` in `app.routes.spec.ts` (heading, title and the axe check come with it),
   run `npm run lint` and `npm test`.
8. If the area changes the route table of R-UI-003, update the spec in the same change.

## What is not done yet

- `GET /api/v1/system/banner` is not wired to `BannerStore`. The catalog services, `system/encryption`, `whoami`, `login-options`, `authz/me`, `authz/check` and `users/me/preferences` use the generated types (Q-UI-12).
- System status: the backups card and route are placeholders (R-UI-048).
- What the API lacks for the catalog screens, and the UI works around (Q-UI-15, Q-UI-18, Q-UI-19): the values of `ui.browser.page_size` and
  `ui.deploy.max_upload_bytes`; a creation time and a sort by creation on the repository summary view; a flag for
  formats with tags in the descriptor.
- The tree does not label the members of a virtual repository ("resolves from") or the cache of a remote one: M1 has neither class. Copy, move, properties and the trash can are P1.
- The repository settings forms cover the generic members and the format member rendered from the format's schema
  ([above](#the-repository-settings-form-rendered-from-the-formats-schema)). The wizard has no fields for the upstream of a remote repository (an npm remote
  is created with the format's default upstream) or for the members of a virtual one; NuGet's `deleteMode` view, upstream presets, "Test connection", the
  member editor and the other format panels arrive with those formats. A schema construct outside the rendered subset is shown, not edited.
- The session expiry warning of R-UI-002 (WCAG 2.2.1) is not built: it needs the session deadlines in an answer the UI can read (`whoami` shows `type: session` without an
  expiry; the timeouts are in `settings/auth`, which needs `settings:read`). The `reauthentication_required` dialog leads to `/login?...&reauth=1`; the identity, token,
  settings and policy editors and the catalog editors keep and restore their non-secret drafts (see above).
- The top bar has the repository search only. The global search over packages and files, the project filter, the help link and the keyboard-shortcut switch are P1 entries of R-UI-002; the packages list and details (R-UI-006 to R-UI-009) are built against the contract and reuse the data table and the filter panel. The sidebar shows no version or footer text, because no API the browser may read carries the version.
- The jobs list of the system area (`/admin/system/jobs`) and the older tables of the detail screens (role bindings, group members, exposure rules, job table) still use their own `table.data` markup; they move to the data table when their screens are next touched.
- Forms: the identity, token, settings and policy forms use plain signals with `TextField` and `SelectField` (Q-UI-20), the catalog forms plain signals with native controls (Q-UI-14); in both the server's field errors by JSON pointer carry the validation, and the client checks only what saves a round trip. Signal Forms with validators derived from the OpenAPI constraints are not used.
- Browser-mode component tests and contract mocks. Of the Playwright tier, reflow at 320 CSS px, focus order across navigations, Firefox and WebKit, the client-setup journey with real `npm` and `dotnet` commands and the claim tester arrive with the screens and formats they need; the journeys of the M1 subset exist ([e2e.md](e2e.md#browser-journeys)).
- Local runs on a Node.js other than `web/.nvmrc` work only while the version satisfies `engines`; `make versions-check` names a mismatch.
- npm 11 prints `allow-scripts` warnings for the install scripts of `esbuild`, `lmdb`,
  `msgpackr-extract` and `@parcel/watcher`. Build, test and lint pass without running them; if a
  later npm turns the warning into an error, approve the scripts in the workspace configuration.
- Packages and client setup: the NuGet clients (.NET CLI, NuGet CLI, Visual Studio, `nuget.config`) wait for the NuGet format; the Owners and Builds tabs of the package page (P1), the native-exchange GitLab variant (R-NPM-022, P1) and the Playwright journey with a real `npm install` (R-UI-016) are not built. The version combobox is a native input with a `datalist`, not an ARIA combobox. The wizard of a caller who holds neither `settings:read` nor any readable repository shows no client address (Q-UI-69).
