# The manual and the help area

The user and administrator manual lives in [docs/manual/](../manual/README.md), is embedded into the binary and is shown
in the UI behind the Documentation link of the top bar. Requirements [R-API-054](../spec/api-and-ui.md) (the operations) and
[R-UI-050](../spec/api-and-ui.md) (the help area); decisions
[Q-UI-89](../spec/api-and-ui.md#q-ui-89--how-the-manual-reaches-the-ui) (delivery) and
[Q-UI-90](../spec/api-and-ui.md#q-ui-90--what-the-help-button-opens) (the help button).

```text
docs/manual/{user,admin}/*.md ──go:embed (docs/manual/embed.go)──> manual.Load ──> v1.Options.Manual
                                                                     │  readme.RenderLinked per page, twice:
                                                                     │  anchors first, then the links against them
GET /api/v1/manual, /api/v1/manual/{section}/{page} <────────────────┘
        │
        ▼
/ui/<locale>/help (overview), /ui/<locale>/help/<section>/<page>   ← "Documentation" (core/help-topics.ts)
```

## Files

| File | Responsibility |
|---|---|
| [docs/manual/README.md](../manual/README.md) | The contents for GitHub readers and the writing rules of a page |
| [docs/manual/embed.go](../manual/embed.go) | `manual.Files`: `user/*.md` and `admin/*.md`; a package of its own because `go:embed` cannot reach a parent directory |
| [internal/manual/manual.go](../../internal/manual/manual.go) | `contents` (the order), `Load` (parse, render, check links, ETag), `Page`, `Section`, `Heading` |
| [internal/packages/readme/render.go](../../internal/packages/readme/render.go) | `RenderLinked`: the README pipeline with a `Links` resolver for every target that is not an absolute address |
| [internal/api/v1/manual_api.go](../../internal/api/v1/manual_api.go) | `GetManualContents`, `GetManualPage`: the anonymous gate, `304` for the ETag, `404` |
| [internal/app/app.go](../../internal/app/app.go) | `startClient` loads the manual once; a failing load stops start-up |
| [web/src/app/api/manual-api.ts](../../web/src/app/api/manual-api.ts) | `ManualApi`, `HttpManualApi` (keeps every read for the tab: the route title and the screen share one request) |
| [web/src/app/features/help/](../../web/src/app/features/help/) | `help.routes.ts` (the page title from the page), `help-overview.ts`, `help-page.ts` and `.html`, `help.scss`, `help.spec.ts` |
| [web/src/app/core/help-topics.ts](../../web/src/app/core/help-topics.ts) | `HELP_TOPICS` (route prefix to page) and `helpFor`, which the top bar's Documentation link uses |
| [web/src/app/features/packages/package-readme.ts](../../web/src/app/features/packages/package-readme.ts) | The binding component; `pageLinks="help/"` keeps the manual's page links and routes them in the application |
| [web/scripts/check-rules.mjs](../../web/scripts/check-rules.mjs) | Fails the lint when `HELP_TOPICS` names a page that is not a file of `docs/manual/` |

## How a page becomes HTML

1. `parse` takes the first line as the title (`# Title`) and the following paragraph as the summary; the body handed to
   the renderer is everything below the title line, the summary included.
2. The first pass renders every page with a resolver that keeps nothing and collects each page's heading anchors.
3. The second pass resolves the links: a relative `.md` link becomes `help/<section>/<page>`, a fragment becomes
   `#user-content-<slug>`; a link to a page or heading that does not exist, or to any other file, is a load error.
4. The headings of levels 2 and 3 become the page's table of contents; neighbours in reading order are linked.

The links are relative to the base href of the UI (`/ui/<locale>/`), so a link opened in a new tab lands on the help
route of the same language. In the application, `PackageReadmeView` keeps such a link (no `rel`, no removal) and turns a
plain click into `Router.navigateByUrl`; the help page scrolls to the fragment once the text is rendered.

## Layout of a page

[Q-UI-97](../spec/api-and-ui.md#q-ui-97--the-manuals-article-takes-the-width-of-its-column-only-the-prose-keeps-a-measure).
The article has no fixed width; the contents column keeps 15 rem and the article takes the rest. The help page binds its
README with the class `help-readme`, which caps the direct paragraphs, lists, quotes and headings at `110ch`
([package-readme.scss](../../web/src/app/features/packages/package-readme.scss)); tables and code use the full width.
`PackageReadmeView` wraps every table in `.readme-table` (full width, `overflow-x: auto`, `tabindex="0"`), so a table
wider than the column scrolls inside its container. Cells break at spaces only, code in a cell stays whole, the first
column is at least 9 rem wide. Below 52 rem of content the contents give way to the link back to the overview. The
rules are shared with package READMEs except the measure.

## Add or change a page

1. Write `docs/manual/<section>/<page>.md` by the rules of [docs/manual/README.md](../manual/README.md): title line,
   plain summary, `##`/`###` headings, relative `.md` links, only what the code of this version does.
2. Add the page id to `contents` in [internal/manual/manual.go](../../internal/manual/manual.go) and the same line to
   both lists of `docs/manual/README.md`, in the same position and with the page's title.
3. If a screen should open the page from the Documentation link, add the route prefix to `HELP_TOPICS`.
4. Run `go test ./internal/manual/ ./internal/api/v1/ -run R_API_054` and `npm run lint` in `web/`.
5. Run the white-label scan before the commit (the manual is the most likely place for a product comparison to slip in).

## Tests

| Test | What it pins |
|---|---|
| `TestR_API_054_EmbeddedManualLoads` | The embedded manual loads: every page exists, has a title, a summary and headings; every link resolves |
| `TestR_API_054_ReadmeListsTheContents` | `docs/manual/README.md` lists the sections and pages in the order and with the titles of `contents` |
| `TestR_API_054_LinksBecomeHelpRoutes`, `TestR_API_054_DefectsAreRefused` | Link rewriting, reading order, and every defect that stops a load |
| `TestR_API_054_LinkedRenderingResolvesRelativeLinks` | `RenderLinked` in the README renderer; `Render` still drops relative links |
| `TestR_API_054_TheManualOperations`, `TestR_API_054_AnonymousReadersFollowTheAnonymousAccessSetting` | The operations: order, page fields, ETag and `304`, `404`, the anonymous gate, `503` without a manual |
| [help.spec.ts](../../web/src/app/features/help/help.spec.ts), `app.spec.ts`, `app.routes.spec.ts` | The overview, a page, link navigation and scrolling, errors; the Documentation link's target per screen; both routes with title and axe |

## What is not done yet

- No browser journey visits the help area; the component tier and `app.routes.spec.ts` (axe on jsdom, without the colour
  check) cover it.
- The manual is English only (Q-UI-86 applies); a German manual would need a second set of files and a locale parameter.
- There is no search over the manual.
