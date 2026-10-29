# Web UI: workspace, build and serving

The UI is an Angular 22 workspace in [web/](../../web/) and a small Go package,
[internal/webui/](../../internal/webui/), that embeds the build and serves it under `/ui/{locale}/`.
Requirements: [R-ARCH-004](../spec/architecture.md), [R-UI-001](../spec/api-and-ui.md),
[R-UI-045](../spec/api-and-ui.md), [R-UI-046](../spec/api-and-ui.md),
[R-UI-047](../spec/api-and-ui.md).

M0 ships the shell only: skip link, header, side bar (pinned from 1024 CSS px, drawer below), banner
area, router outlet, an empty status page, a not-found page, light and dark tokens, and the
locales `en` and `de`. There is no login and no API client yet.

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
| [web/src/app/app.ts](../../web/src/app/app.ts) | Root standalone component: shell, navigation, banner, outlet |
| [web/src/app/app.config.ts](../../web/src/app/app.config.ts) | Router (component input binding), `HttpClient` on fetch, interceptor list (empty in M0), global error listeners |
| [web/src/app/app.routes.ts](../../web/src/app/app.routes.ts) | One lazy route per area |
| `web/src/app/api/` | Thin hand-written clients per resource family |
| `web/src/app/api/generated/` | `schema.d.ts` from openapi-typescript, committed, never edited |
| [web/src/app/core/](../../web/src/app/core/) | Shell services: `Layout` (pinned side bar signal), `BannerStore`. Later: `SessionStore`, interceptors, `storage.ts` |
| [web/src/app/shared/](../../web/src/app/shared/) | Presentational components (`Banner`) |
| [web/src/app/features/](../../web/src/app/features/) | One folder per area (`home`, `not-found` in M0) |
| [web/scripts/](../../web/scripts/) | `build-embed.mjs`, `check-budget.mjs`, `check-rules.mjs` |
| [web/eslint.config.js](../../web/eslint.config.js) | angular-eslint plus the R-UI-046 rules |

## Commands

Run in `web/` after `nvm use` (or any Node matching `.nvmrc`).

| Command | What it does |
|---|---|
| `npm ci` | Install exactly the lockfile |
| `npm start` | Dev server, English only (the `development` configuration builds one locale) |
| `npm run lint` | ESLint (including the four R-UI-046 rules) and `scripts/check-rules.mjs` |
| `npm test` | Vitest through `@angular/build:unit-test`, jsdom, single run, headless |
| `npm run test:coverage` | The same with a V8 coverage report (`src/app/**`, generated code excluded) |
| `npm run extract-i18n` | Rewrites `src/locale/messages.xlf`; then add the `<target>` units to `messages.de.xlf` |
| `npm run generate:api` | `openapi-typescript ../api/openapi.yaml` into `src/app/api/generated/schema.d.ts`. Needs `api/openapi.yaml`, which the API work package writes |
| `npm run build` | Localized production build into `web/dist/web/browser/<locale>/` |
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

| Tier | State in M0 |
|---|---|
| Unit | In place: Vitest through the Angular builder, specs next to code (`*.spec.ts`), requirement IDs in test names |
| Component | In place with `TestBed` on jsdom (`app.spec.ts`). Vitest browser mode (real Chromium) is **not** set up: it needs a browser provider package and browser binaries in CI |
| Contract | Not yet: needs `api/openapi.yaml`. `generate:api` is ready; the tier is the compile of mocks against `schema.d.ts` |
| End-to-end, accessibility, security | Not set up (Playwright, axe-core). They belong to the e2e work with the chart-installed build |
| Internationalization | In place as the production build itself; verified by removing one German unit, which fails the build |

Go side: [webui_test.go](../../internal/webui/webui_test.go) covers nonces, headers, deep links,
caching, 404s, locale redirects, placeholder mode and start-up validation. Subtest names carry the
requirement ID (`R-ARCH-004`, `R-UI-001`, ...) for the coverage generator.

## How to add a feature area

1. Create `web/src/app/features/<area>/` with a standalone component per screen (`Component` with
   `selector: 'trove-…'`, no NgModule).
2. Add a lazy route in [app.routes.ts](../../web/src/app/app.routes.ts) with `loadComponent` (or
   `loadChildren` for several screens) and a `$localize` title with an `@@route.<area>.title` id.
3. Read server data with `httpResource()` through a thin client in `web/src/app/api/<family>.ts`
   that uses the types of `src/app/api/generated/schema.d.ts`; shared state goes in an injectable
   store in `core/` (or the area folder) that exposes read-only signals.
4. Put presentational parts in `shared/` only when a second area needs them.
5. Add navigation entries in [app.html](../../web/src/app/app.html) (side bar) behind the
   capability check once `authz/me` exists.
6. Mark every text with `i18n`, run `npm run extract-i18n`, add the German `<target>`s to
   `messages.de.xlf`, and extend the glossary for new terms.
7. Write specs next to the code with the requirement ID in the test name; run `npm run lint` and
   `npm test`.
8. If the area changes the route table of R-UI-003, update the spec in the same change.

## What is not done yet

- No `SessionStore`, interceptors (CSRF, problem mapping, 401) or `storage.ts`.
- The banner area reads `BannerStore`, which nothing fills; `GET /api/v1/system/banner` is wired with
  the system status work.
- No user theme preference: `data-theme="light|dark"` on `<html>` is honored by the tokens, but
  nothing sets it.
- Browser-mode component tests, contract tier, Playwright and axe-core.
- npm 11 prints `allow-scripts` warnings for the install scripts of `esbuild`, `lmdb`,
  `msgpackr-extract` and `@parcel/watcher`. Build, test and lint pass without running them; if a
  later npm turns the warning into an error, approve the scripts in the workspace configuration.
