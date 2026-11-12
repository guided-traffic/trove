# T-22 · One search with suggestions — the work-package cut

The plan of the owner's task T-22 of 2026-11-12 ([promt.md](promt.md#owner-task-of-2026-11-12-for-later-a-search-with-suggestions)),
written by the planning agent on 2026-11-12 against main 73d17f09 and against T-18's branch (8c58e121..5e56385e, not on main yet;
T-18's plan is [visibility-work-packages.md](visibility-work-packages.md)). **This plan assumes T-18 has landed**: the levels
`private`, `internal` and `public` and the external flag decide reads (R-AUTHZ-033 to R-AUTHZ-035, R-AUTH-044), `GET
/api/v1/projects` and `GET /api/v1/projects/{projectKey}` are open to every caller and filter in the handler (`projectListed`,
`levelAdmits` in T-18's `projects_api.go`), and `login-options` answers `publicContent`.

The owner's words, both of 2026-11-12:

- Showing his GitLab's search dropdown: "A sensible search, with suggestions and everything. This is how I picture it for us later."
- After typing "test" into trove's top bar and landing on the admin area's repository list with Enter: "Absolutely wrong. A search
  on the main page must NEVER end in the admin area, unless the user is shown live suggestions while searching, some of them are
  pages explicitly marked as pages of the admin area, and the user is an instance admin. Then and only then may a search result
  lead into the admin area at all." Then he showed the dropdown again, with live results.

The dropdown he showed, in words: a field with a filter icon at its right end; before typing **Places** (Your work, Explore,
Profile, Preferences), **Frequently visited projects** (a letter avatar, the name, "· group / subgroup / project" muted), **View
all my projects**, **Frequently visited groups**; while typing, live results grouped by kind.

The rules are [CLAUDE.md](../../CLAUDE.md#product-simple-by-default-details-on-demand) ("A search never leads into the admin area")
and rule 11 of [promt.md](promt.md) (every changed screen rendered and looked at). The reference is the owner's GitLab
([ui-style-guide.md](../developer/ui-style-guide.md)).

IDs used: **R-API-060 to R-API-064**, **R-UI-095 to R-UI-099**, **Q-API-270 to Q-API-281** (282 to 289 unused), **Q-UI-400 to
Q-UI-415** (416 to 419 unused), **H-985 to H-987** (988 to 994 unused). Changed: R-UI-002, R-UI-015, R-UI-044, R-API-010, R-API-020,
R-API-021, R-REPO-012; revisited: Q-UI-311 and the stopgap's Q-UI-390 to Q-UI-399. One migration, numbered by the development agent
when B1 starts (T-18 takes 00073 to 00075 on its branch).

Seams with parallel work:

- **The stopgap (builder B4, Q-UI-390 to Q-UI-399)**: Enter in today's field opens a results page of the main area instead of
  `/admin/repositories?q=`. It was not on main at 73d17f09, so this plan has not seen it. R-UI-098 is that page's target; B2 extends
  what B4 landed (Q-UI-415 in section 2.4) and writes "Revisited by Q-UI-4xx" into each of B4's decision blocks it changes.
- **T-21b's B2** owns `web/src/app/features/repositories/` ([repository-setup-work-packages.md](repository-setup-work-packages.md)).
  This plan changes one line in each of `repository-detail.ts`, `repository-content.ts` and `repository-list.ts` (the visit record)
  and deletes `recent-repositories.ts`; whichever of the two lands second merges.
- **T-21b's B1** also owns `web/src/app/api/repositories-api.ts` and the fakes under `web/src/app/api/testing/`; this plan's B1 adds
  `name` there in WP0. Whichever lands second merges.
- **T-18's B2** (the levels in the UI) lands before this plan starts.

## 0 · In one paragraph

Every caller gets one field in the top bar: a signed-in user always, an anonymous visitor while something is public. `/` (on by
default, off in the profile) and Ctrl+K focus it; a filter at its right end narrows it to repositories, packages or projects, and for
an instance administrator to users or groups. Opened empty, it offers the reference's parts with trove's objects: places, the
frequently visited repositories, **View all my projects**, the frequently visited projects, and for an instance administrator an
**Admin area** group last. While typing, one request per settled text asks a new operation, `GET /api/v1/search/suggestions`, for the
best five matches of each kind the caller may read, under T-18's levels, the external flag, path-scoped grants and hidden existence;
the answer is filtered before it is cut and names a repository's project by its key only. Enter without a picked suggestion opens
`/search` in the main area, whose tabs are the three existing lists with `q`; it never opens the admin area. Only a suggestion marked
**Admin area**, shown to the holder of the system role `admin` and picked by click or by Enter on it, leads there. "Frequently
visited" stays in the browser, per principal. Packages are matched by name prefix and by word prefix on two new indexes; repositories
over the request's grant index, without a statement.

## 1 · What exists and what is missing

### 1.1 The top bar

- [app.html](../../web/src/app/app.html) holds a `<form role="search">` with one input, **Find a repository**, shown only while
  `canSearch()` is true, that is while the caller holds `repositories:read-config` ([app.ts](../../web/src/app/app.ts), `canSearch`).
- `submitSearch` navigates to `/admin/repositories?q=<text>`: the admin area's list. This is what the owner called "absolutely wrong".
- No suggestions, no keyboard shortcut, no filter. [_shell.scss](../../web/src/styles/_shell.scss) hides the field below 36 rem
  (UI-A-48, as R-UI-002 and the stylesheet call it; [ui-audit.md](ui-audit.md) ends at UI-A-43 and has no such row).
- R-UI-002 describes exactly this and has the acceptance criterion "A user whose `authz/me` lacks `repositories:read-config` sees no
  search field in the top bar"; UI-A-01 of the audit hands the plain user's missing search to T-22.

### 1.2 The start page and the project switcher

- [projects-home.ts](../../web/src/app/features/projects/projects-home.ts), **Find a package**: `GET /api/v1/packages?q=` from two
  characters, 300 ms after the last key, ten results, a list of links (no combobox).
- **Recently opened** reads `recent-repositories.ts` (deleted by WP7): one
  `localStorage` key, `trove.recent-repositories`, for every user of the browser, 20 entries by recency. The start page reads each of
  the five it shows with its own `GET` of the repository (five requests per visit).
- [project-context.ts](../../web/src/app/core/project-context.ts) keeps the recent projects per principal name
  (`trove.recent-projects.<name>`) and reads every project the caller may see once (`collectAll` of `GET /api/v1/projects`); the
  switcher filters that list in the browser.
- [Q-UI-311](../spec/api-and-ui.md#q-ui-311--the-start-page-leads-the-plain-user-to-a-repository-and-its-address-in-two-steps) says
  a repository search across projects "needs a list the plain user may read across projects". That list exists (1.3); what is
  missing is the field and a page of the main area.

### 1.3 The API a search can use

| Operation | Who may call | `q` matches | How it runs | Fit for suggestions |
|---|---|---|---|---|
| `GET /api/v1/repositories` | any caller; rows filtered by `read` ([repositories_api.go](../../internal/api/v1/repositories_api.go) `visibleRepositories`: `acc.Readable`, or `acc.Exposed` for `repositories:read-config`) | key and description, substring | `repository.Service.List` reads the whole table and the virtual members and parses every document, then filters in Go ([service.go](../../internal/repository/service.go)) | Readers may call it across projects; a full read and parse per keystroke; no match on `<project>/<key>` |
| `GET /api/v1/packages` | any caller; versions decided per repository's read shape | name substring, and the description through each format's find | one aggregating statement, `PackageViewList` ([package_view.sql](../../internal/meta/postgres/queries/package_view.sql)); a repository read in part enumerates every candidate version, up to `DefaultMaxRows` 200,000, and refuses above ([view/service.go](../../internal/packages/view/service.go)) | Right answer, heavy: an aggregation, and up to 200,000 decisions per keystroke for a caller with path-scoped grants |
| `GET /api/v1/projects` (T-18) | any caller; member, `projects:read`, or the level admits | key and display name, substring | reads every project, filters in Go | Fine |
| `GET /api/v1/users`, `GET /api/v1/groups` | `identities:read` | users: name or e-mail; groups: name | the principal manager | Administrators only |
| `/api/v1/search/…` | the family of R-API-028 (items search, P1) | — | no route is built (no handler in `internal/`) | A free family |

### 1.4 The database and the index

- `packages` has `UNIQUE (repo_id, name)` and `packages_name (name)` (migration 00036). No index serves a case-insensitive prefix
  or a word inside a name.
- No migration creates `pg_trgm`, on purpose: 00017 records that a managed PostgreSQL may refuse the extension to the application's
  role. NuGet's search uses a GIN index over a `tsvector` with prefix queries (`nuget_version_search`), the precedent this plan follows.
- The grant index ([index.go](../../internal/authz/index.go)) holds every active repository with its ID, qualified name, class and
  format (`repoView`), and every project with its members.
- `user_preferences.keyboard_shortcuts` is `false` by default; nothing in `web/src` reads it but the profile, and no single-key
  shortcut exists.
- `@angular/aria` 22.2.5 is a dependency and ships `ngCombobox`, `ngComboboxPopup` and `ngComboboxWidget`
  (`types/combobox.d.ts`); its listbox has no group directive.

### 1.5 The spec as it stands

- R-UI-015 (P1, not built): a header search with a type selector, package suggestions from two characters (`namePrefix`, five,
  250 ms), Enter opening `/search?type=…&q=…`, Ctrl+K, and `/` only after the user turned shortcuts on.
- R-UI-044: WCAG 2.1.4, "Single-key shortcuts are off by default".
- R-AUTHZ-016: "Listings, search results and their counts contain only items the subject may read"; R-AUTHZ-017: `hideExistence`
  changes only the status of a refused read.
- R-API-016: the class `read` admits 200 requests per 4 s per principal (50/s), `anonymous` 20 per 4 s per client address.
- R-API-052: a configuration read comes from PostgreSQL; enforcement follows within the propagation bound.

### 1.6 What is missing

- A field for every caller, a keyboard shortcut, a filter, a usable narrow layout.
- Suggestions before and while typing, grouped by kind, by keyboard, with ARIA semantics.
- One API answer for all kinds within the request budget, with the visibility rules of T-18.
- A results page of the main area; the end of every way from the search into the admin area, except the owner's one.
- Indexes for package names; a per-principal store of frequently visited places.

## 2 · The target, as spec text ready to paste

The builder of WP0 (section 3) pastes 2.1 to 2.6 into the pages named, before any code (hard rule 4). Links inside the blocks are
written for the page they go to.

### 2.1 [api-and-ui.md](../spec/api-and-ui.md), Part B, after R-API-058

````markdown
### R-API-060 · One operation answers the suggestions of the search field
Priority: **MVP** · Covers: trove-only

The owner's task T-22 (2026-11-12): one search field with live suggestions for every caller (R-UI-095 to R-UI-099).
`GET /api/v1/search/suggestions` answers, in one request, the best matches of a text for each kind the field shows
([Q-API-270](#q-api-270--one-operation-answers-the-suggestions-and-the-results-page-reads-the-three-lists)):

| Parameter | Meaning |
|---|---|
| `q` | Required. The text, trimmed: 2 to 100 characters, else `400 malformed_request` with the pointer `q` |
| `kind` | `repository`, `package`, `project`, `user`, `group`; repeated or comma-separated; default `repository,package,project`. An unknown kind answers `400 malformed_request` |
| `limit` | Suggestions per kind, 1 to 10, default 5 |

```json
{
  "repositories": {"items": [{"project": "web", "key": "npm-local", "format": "npm", "class": "local"}], "more": false},
  "packages": {"items": [{"id": "pkg_01JBQ6V7S3K9", "format": "npm", "name": "@web/ui-kit", "project": "web", "repository": "npm-local"}], "more": true},
  "projects": {"items": [{"key": "web", "displayName": "Web platform"}], "more": false}
}
```

- One member per kind asked (`repositories`, `packages`, `projects`, `users`, `groups`), absent for a kind not asked; `items` in the
  order of R-API-062, `more` by R-API-061. A user is `{"id", "username", "displayName", "disabled"}`, a group `{"name",
  "description"}`; a package's `name` is its display name and `id` the identifier of `GET /api/v1/packages/{packageId}`
  ([Q-API-271](#q-api-271--the-answer-holds-one-typed-member-per-kind-and-no-cursor)).
- Not a list of R-API-010: no `cursor`, `nextCursor` or `Link`. The results page reads the lists (R-UI-098).
- Permission: any caller (`x-trove-permission: public: true`), decided in the handler by R-API-061. Without a credential `401`
  while no repository's artifacts are `public`, as the lists answer. `kind` `user` or `group` needs `identities:read`; without it
  the answer is `403`, before any lookup and the same for every `q`
  ([Q-API-272](#q-api-272--users-and-groups-need-identitiesread-checked-before-any-lookup)).
- Rate limit: the class `read` with a credential, `anonymous` without (R-API-016); no class of its own
  ([Q-API-281](#q-api-281--the-suggestions-use-the-read-and-anonymous-classes-and-q-is-2-to-100-characters)).
- Freshness: the repositories are matched over the grant index of the request (R-AUTHZ-028), on which they are decided anyway, so a
  repository created through another replica is suggested within the propagation bound of R-API-052; projects, packages, users and
  groups are read from PostgreSQL. `Trove-Authz-Min-Epoch` (R-AUTHZ-032) applies as on every operation.
- `Cache-Control: no-store` (R-API-017).

Acceptance criteria:

- `GET /api/v1/search/suggestions?q=n` and a `q` of 101 characters answer `400 malformed_request`; `kind=file` `400`; `limit=11`
  `400`.
- `GET /api/v1/search/suggestions?q=npm&kind=repository` answers only the member `repositories`.
- The answer carries `RateLimit-Policy: "read";q=200;w=4` with the defaults.

### R-API-061 · A suggestion names only what the caller may read
Priority: **MVP** · Covers: trove-only

The suggestions are a listing in the sense of [authorization.md](authorization.md) R-AUTHZ-016: they hold only what the caller may
read, and nothing in the answer depends on an object it may not read. Per kind the rule is the one of that kind's list, on the same
decision point and the same index of the request:

| Kind | Suggested when | Decided by |
|---|---|---|
| repository | the caller may read it, by a grant, a role or its artifacts level (R-AUTHZ-035), or, holding `repositories:read-config`, the endpoint of the request exposes it; a repository being deleted is never in the index | `acc.Readable`, `acc.Exposed` for `repositories:read-config`, as `GET /api/v1/repositories` |
| package | in a physical repository the caller reads whole (read shape `ReadAll`), one version is live and served; in one it reads in part (`ReadRows`), the caller may read the package's document and the repository's include and exclude patterns admit the name ([Q-API-275](#q-api-275--a-package-in-a-repository-read-in-part-is-decided-on-its-document)) | the read scope of `GET /api/v1/packages` (`authz.ReadScope`) |
| project | the caller is a member, holds `projects:read`, or the project's level admits it (R-AUTHZ-035 rule 8) | the rule of `GET /api/v1/projects` |
| user, group | the caller holds `identities:read` | the capability, checked before any lookup |

- **Hidden existence holds.** `hideExistence` (R-AUTHZ-017) changes only the status of a refused read. A repository with
  `hideExistence: false` that the caller cannot read is suggested no more than any other. A virtual repository is suggested by its own
  gate (R-AUTHZ-009 rule 1); its members are not suggested through it.
- **Levels, users and tokens.** The level `internal` admits an internal subject only (R-AUTHZ-030): a user flagged external, a
  service account, a CI token and a request through a Bastion get what grants, roles and `public` give them. A token's scope narrows
  what grants and `internal` give, not what `public` gives (R-AUTHZ-035 rule 3).
- **A project is named by its key** in a repository's or a package's entry
  ([Q-API-277](#q-api-277--an-entry-names-the-project-of-a-repository-or-a-package-by-its-key)). Its display name appears only in a
  project entry, which the project rule admits.
- **Filtered before it is cut** ([Q-API-276](#q-api-276--more-reports-only-readable-matches-the-answer-left-out)). Each kind is ranked
  among the readable matches (R-API-062) and then cut at `limit`. `more` is `true` when the kind left out a readable match it found.
  The work per request is bounded (R-API-064), so `more` may be `false` while the results page holds more; it is never `true` because
  of an object the caller cannot read.
- **The same for every text.** `400`, `401` and `403` do not depend on `q`.

Acceptance criteria, each a test of the integration area `search` on the worked rows of R-AUTHZ-035 (project `web`, `public`;
`npm-public` with artifacts `public` and wiki `internal`; `npm-internal`, both `internal`; `npm-private`, both `private`, with
`hideExistence: false`; the virtual `npm-all`, `internal`, over the three; each local repository holds `@web/button`; Erin flagged
external with `read` on `npm-private`; Frank internal without grants):

- Without a credential, `q=npm` answers the repository `web/npm-public` alone and `@web/button` with `repository: npm-public`.
- Frank: `npm-all`, `npm-internal` and `npm-public`, never `npm-private`. Erin: `npm-private` and `npm-public`, never `npm-internal`
  or `npm-all`.
- Frank with a token scoped to `mobile/*`: `npm-public` alone.
- Frank's `kind=user` answers `403` for `q=fr` and for `q=zz`, with the same body.
- Twenty packages `button-<n>` added to `npm-private` change neither Frank's answer to `q=button` nor its `more`.

### R-API-062 · How a suggestion matches and is ranked
Priority: **MVP** · Covers: trove-only

`q` is trimmed and lower-cased (Unicode simple case folding); its **terms** are its parts between white space.

| Kind | Matches when | Rank, best first | Then by |
|---|---|---|---|
| repository | every term is part of `<project>/<key>` | 0 the key or `<project>/<key>` equals `q`; 1 the key starts with `q`; 2 `<project>/<key>` starts with `q`; 3 any other | `<project>/<key>`, byte order |
| project | every term is part of the key or of the display name | 0 the key equals `q`; 1 the key starts with `q`; 2 the display name or one of its words starts with `q`; 3 any other | key |
| package | the name starts with `q`, or every **word** of `q` begins a word of the name; a word is a run of `a` to `z` and `0` to `9` ([Q-API-274](#q-api-274--package-names-a-whole-name-prefix-on-a-c-collation-b-tree-and-word-prefixes-on-a-gin-tsvector-without-pg_trgm)) | 0 the name equals `q`; 1 the name starts with `q`; 2 by words | the name, byte order; then the format |
| user | every term is part of the username or of the display name | 0 the username equals `q`; 1 the username starts with `q`; 2 the display name or one of its words starts with `q`; 3 any other | username |
| group | every term is part of the name | 0 equal; 1 starts with `q`; 3 any other | name |

- A package matches on its stored name (`packages.name`, the format's normalized name) and shows its display name. A package held by
  several repositories is one entry, placed in the first of them by qualified name that the caller reads.
- Descriptions are not matched by the suggestions. The lists that the results page reads match them (R-UI-098), and they match
  every suggestion of their kind: `GET /api/v1/repositories` matches `<project>/<key>` besides the key and the description
  ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-012,
  [Q-API-279](#q-api-279--the-repository-lists-q-matches-the-qualified-name)).

Acceptance criteria:

- `q=npm` orders the repositories `web/npm`, then `mobile/npm-local` and `web/npm-local`, then `web/my-npm`.
- `q=@acme/ui` suggests `@acme/ui-kit` (rank 1) before `@acme/kit-ui` (rank 2, by words); `q=kit` suggests both; `q=it` neither.
- `GET /api/v1/repositories?q=web/npm` lists `web/npm-local`.

### R-API-063 · The repository list answers named repositories
Priority: **MVP** · Covers: trove-only

`GET /api/v1/repositories` takes `name`, a qualified name `<project>/<key>`, repeated or comma-separated, at most 50
([Q-API-278](#q-api-278--the-repository-list-takes-name)). The answer holds the named repositories the caller may see, in the list's
order, by the rules of [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-012. A name that does not exist and a name of a
repository the caller may not see are both absent, so the answer tells them apart for nobody. A malformed name, or more than 50,
answers `400 malformed_request`. The UI resolves the frequently visited repositories of R-UI-096 and the start page's **Recently
opened** with one such request.

Acceptance criteria:

- `name=web/npm-public&name=web/npm-private&name=web/nope` as Frank of R-API-061 lists `web/npm-public` alone.
- 51 names answer `400`.

### R-API-064 · The indexes and the bounds of the suggestions
Priority: **MVP** · Covers: trove-only

Data model ([Q-API-274](#q-api-274--package-names-a-whole-name-prefix-on-a-c-collation-b-tree-and-word-prefixes-on-a-gin-tsvector-without-pg_trgm)),
in the migration of T-22:

```sql
ALTER TABLE packages
    ADD COLUMN name_words tsvector GENERATED ALWAYS AS
        (to_tsvector('simple'::regconfig, regexp_replace(lower(name), '[^a-z0-9]+', ' ', 'g'))) STORED;
CREATE INDEX packages_name_words ON packages USING gin (name_words);
CREATE INDEX packages_name_lower ON packages ((lower(name) COLLATE "C"));
```

How each kind is read ([Q-API-273](#q-api-273--repositories-are-matched-over-the-requests-grant-index-and-projects-over-their-rows-without-an-index)):

| Kind | Read | Index | Bound |
|---|---|---|---|
| repository | the repositories of the request's grant index, in memory; no statement | — | every active repository, 10,000 in the reference environment ([architecture.md](architecture.md) R-ARCH-027 P-15) |
| project | one statement over `project`, as the project list | none | every project |
| package | statement A: names that start with `q` (`lower(p.name) COLLATE "C" LIKE <escaped q>%`) in the repositories the caller reads whole or in part, ordered by the same expression, at most 200 rows; statement B, only when A found fewer than `limit + 1` packages: names whose words begin with the words of `q` (`p.name_words @@ to_tsquery('simple', 'w1:* & w2:*')`), without A's rows, at most 200 rows. Every row needs a live, served version: `EXISTS` over `package_version_listing` (`package_versions_live`), the withheld versions of the formats as `GET /api/v1/packages` | A: `packages_name_lower`; B: `packages_name_words` | 400 candidate rows, one served-version check each |
| user, group | one statement each over `principal` with `app_user` or `user_group`, `kind` fixed, `strpos` on the lower-cased columns | none (`principal` is unique on `kind, name`) | every user or group |

- The words of a tsquery are runs of `a` to `z` and `0` to `9` taken from `q`, so no operator of the tsquery syntax reaches the
  database; `%`, `_` and `\` of `q` are escaped in the `LIKE` pattern.
- A request costs a fixed number of statements, independent of the number of matches and of the rows read.
- trove does not create `pg_trgm` (migration 00017), so an infix inside a word of a name (`it` in `ui-kit`) is not suggested; the
  results page's `GET /api/v1/packages?q=` finds it.

Acceptance criteria:

- With `SET enable_seqscan = off`, `EXPLAIN` of statement A shows `packages_name_lower` and of statement B `packages_name_words`.
- A request with the five kinds costs the same number of statements with 10 and with 10,000 matching packages (integration area
  `querybudget`).
````

### 2.2 [api-and-ui.md](../spec/api-and-ui.md), Part C, after R-UI-094

````markdown
### R-UI-095 · One search field in the top bar for every caller
Priority: **MVP** · Covers: trove-only

The owner's task T-22 (2026-11-12): "A sensible search, with suggestions and everything." The top bar's field (R-UI-002) is the one
search of the UI.

- **Who gets it.** Every signed-in caller, whatever its roles, and an anonymous caller while the UI runs without a session, which it
  does only while some repository's artifacts are `public` (`publicContent`, R-UI-004,
  [Q-UI-400](#q-ui-400--every-signed-in-caller-gets-the-field-and-an-anonymous-one-while-something-is-public)).
- **The field.** The placeholder "Search or go to…", with " (/)" while `/` is on; a `search` icon at its start; at its end the filter
  button (`filter_list`, accessible name "Filter: <kind>"); while a kind is chosen, a chip with the kind's name and a remove button at
  the field's start. The field fills the top bar's middle column up to 40 rem.
- **The filter** ([Q-UI-402](#q-ui-402--the-filter-is-a-menu-button-at-the-fields-end-and-the-chosen-kind-a-chip-at-its-start)). A
  menu of radio items: **All** (the default), **Repositories**, **Packages**, **Projects**, and for an instance administrator
  (R-UI-099), after a divider, **Users** and **Groups**, each marked **Admin area**. Backspace in an empty field, or the chip's remove
  button, returns to **All**. The kind lasts until the page is reloaded and is part of the results page's address (R-UI-098).
- **Keyboard** ([Q-UI-401](#q-ui-401--slash-focuses-the-field-and-is-on-by-default-the-preference-turns-it-off)). `/` moves the
  focus into the field while the preference `keyboardShortcuts` is on (R-API-021, on by default) and the focus is not in a text
  field, a text area, a select or an editable element; the key is not typed. Ctrl+K, and ⌘K on macOS, does the same whatever the
  preference. An anonymous caller has no preferences and gets Ctrl+K and ⌘K only.
- **Below 36 rem** ([Q-UI-403](#q-ui-403--below-36-rem-a-search-button-opens-the-field-as-a-sheet)). The field gives way to an icon
  button (`search`, accessible name "Search") among the top bar's actions. The button, `/` and Ctrl+K open the search as a sheet: a
  panel over the top bar's full width, `role="dialog"` named "Search", with the field and the filter at its top, a **Close** button,
  and the suggestions below it down to the bottom of the window. The focus stays in the sheet; **Close**, or Escape while no list is
  open, closes it and returns the focus to the button.
- The field asks the server for nothing before it has the focus (R-UI-096) and a text (R-UI-097).

Acceptance criteria:

- The plain user `sam` (`reader` of two projects, no system role) sees the field at 1440 px and the button at 390 px; an anonymous
  visitor of an installation with one public repository sees them too; with nothing public, an anonymous visitor sees the sign-in
  page and no field.
- With `keyboardShortcuts` on, `/` typed on the page body focuses the field and is not typed into it; typed into another text field
  it is typed there; with the preference off, `/` does nothing; Ctrl+K focuses the field either way.
- At 390 px the button opens the sheet with the focus in its field; Escape closes it and the focus is on the button again.

### R-UI-096 · What the opened field offers before anything is typed
Priority: **MVP** · Covers: trove-only

When the field gets the focus without a text, a list opens under it, in this order
([Q-UI-404](#q-ui-404--the-empty-field-offers-the-references-parts-in-its-order-and-the-admin-pages-last)):

1. **Places**: Projects (`/projects`), Guides (`/guides`), Profile (`/profile`), Personal tokens (`/profile/tokens`), each with its
   side-bar icon; an anonymous caller gets Projects and Guides.
2. **Frequently visited repositories**: up to five, each with its format's icon, the key, and "· <project key>" muted.
3. **View all my projects** (`/projects`).
4. **Frequently visited projects**: up to five, each with the project's letter mark (as the project switcher's), the display name,
   and "· <key>" muted.
5. For an instance administrator only (R-UI-099), after a divider: **Admin area**, with Dashboard, Repositories, Users, Groups and
   Settings, each the admin side bar's entry (icon and label, R-UI-002) where the administrator may use it, and each marked
   **Admin area**.

Groups 2 to 4 are not shown to an anonymous caller.

- **Where "frequently visited" is kept**
  ([Q-UI-405](#q-ui-405--frequently-visited-is-kept-in-the-browser-per-principal-by-visits-and-recency)). In browser storage
  (non-secret, R-UI-043), one key per signed-in principal ID, `trove.frequent.<principal ID>`: per repository its qualified name,
  per project its key, each with the number of visits and the time of the last one, at most 20 per kind. A visit is the opening of a
  repository's page or content page, or of a project's screen, and counts once per entry in 15 minutes. The order is the number of
  visits, halved for every 14 days since the last visit, then the last visit. The same store, ordered by the last visit, is the start
  page's **Recently opened** and the project switcher's recent list; it replaces `trove.recent-repositories` and
  `trove.recent-projects.<name>`, which are removed when first read. A route under `/admin` is never recorded. With storage blocked,
  groups 2 and 4 are left out.
- **Only what the caller may still read**
  ([Q-UI-406](#q-ui-406--a-stored-entry-is-shown-after-the-server-confirms-that-the-caller-still-reads-it)). The stored repositories
  are shown after one `GET /api/v1/repositories?name=…` (R-API-063) has returned them, and only those it returned; the stored projects
  when the project list the UI reads once (Q-UI-129) holds them. An entry that is not returned is left out and stays stored. The
  request runs when the list first opens after sign-in, and again after the stored repositories or the caller's authorization epoch
  changed.

Acceptance criteria:

- `sam`, who opened `web-frontend/npm-vendor` three times and `platform/collections` once, sees them in that order; after he loses
  `read` on `platform`, `platform/collections` is not shown and its entry stays stored.
- `ada` (system role `admin`) sees the **Admin area** group last, each entry marked; `sam` and the power user `priya` see no such
  group.
- A second user signed in in the same browser sees none of `sam`'s entries; with storage blocked the list holds Places and **View
  all my projects** only.
- Opening the empty list sends at most the one request of the repositories.

### R-UI-097 · Live suggestions while typing
Priority: **MVP** · Covers: trove-only

- **When** ([Q-UI-408](#q-ui-408--one-request-per-settled-text-250-ms-after-the-last-key-an-anonymous-caller-waits-longer)). A
  signed-in caller's text of two or more characters is sent 250 ms after the last key; an anonymous caller's of three or more after
  500 ms, and at most four times in 4 s. One request per settled text, `GET /api/v1/search/suggestions` (R-API-060) with the kinds of
  the filter; a request a newer text supersedes is cancelled; the answers of the last ten texts are reused for 30 s while the list is
  open. Below the minimum the list shows R-UI-096.
- **Groups and order** ([Q-UI-407](#q-ui-407--the-main-areas-groups-come-first-and-the-admin-areas-last-five-in-each)).
  Repositories, Packages, Projects, Pages; then, for an instance administrator only and after a divider, Admin pages, Users and
  Groups, each group heading and each entry marked **Admin area** (R-UI-099). Five per group, three for Pages and Admin pages; with a
  kind chosen, that group alone with ten. A group without a match is left out. A group of the main area whose answer says `more` ends
  with **All repositories for "<text>"** (packages, projects), which opens the results page on that tab (R-UI-098).
- **An entry** shows: a repository the format's icon, the key and "· <project key>" muted; a package the format's icon, the name
  and "· <project key> / <repository key>" muted; a project the letter mark, the display name and "· <key>" muted; a page its
  side-bar icon and label; a user the avatar (R-UI-002), the display name or the username, "· <username>" muted and **Disabled**
  where it is; a group the `group` icon and the name. Keys and names carry `translate="no"`.
- **Where it leads**: a repository `/projects/<project>/repositories/<key>`; a package its page in the project of the answer's
  repository (`/projects/<project>/packages/<format>/<name>`); a project `/projects/<key>`; a page its route; a user
  `/admin/users/<username>`; a group `/admin/groups/<name>`.
- **Pages** are matched in the browser, in the UI's language, by the project rule of R-API-062 over the labels of the Places of
  R-UI-096 and of Client setup (`/setup`), Sessions (`/profile/sessions`) and Documentation (`/help`); **Admin pages** over the labels
  of the admin side bar's entries the administrator may use.
- **Highlighting** ([Q-UI-409](#q-ui-409--the-matched-text-is-bold-marked-by-the-client)). Every occurrence of each term of the text
  in the shown name is bold (weight 600), case-insensitively; overlapping marks merge. Nothing else is coloured.
- **States.** While a text's first answer is pending, the previous list stays with a quiet "Searching…" line. No match in any group:
  "Nothing you may read matches "<text>". Press Enter to search everything." A `429`: "Too many searches. Suggestions return in
  <n> s.", and no request is sent before its `Retry-After` has passed. Another error: "Suggestions are not available."; Enter still
  works.
- **Keyboard and ARIA** ([Q-UI-410](#q-ui-410--the-field-is-an-aria-combobox-with-a-grouped-listbox)), after the WAI-ARIA Authoring
  Practices' combobox with a listbox popup: the field is `role="combobox"` with `aria-expanded`, `aria-controls`,
  `aria-autocomplete="list"` and `aria-activedescendant`; the popup is a `listbox` named "Suggestions" whose groups are `role="group"`
  named by their headings. The focus stays in the field. Down and Up move the active entry: Down from the field enters the list, Up
  from the first entry returns to the text, nothing wraps. Enter opens the active entry; Escape closes the list and, with the list
  closed, empties the field; Tab leaves the field and closes the list; Alt+Down opens the list without moving. After each answer a
  polite live region says how many suggestions are shown, or that none matches.

Acceptance criteria:

- `sam` types `vend`: after 250 ms one request `GET /api/v1/search/suggestions?q=vend&kind=repository,package,project&limit=5`;
  the list shows `npm-vendor · web-frontend` under Repositories with `vend` in bold.
- `ven`, `vend` and `vendo` typed within 200 ms send one request, for `vendo`.
- With the keyboard only: Down, Down, Enter opens the second entry; Escape closes the list and a second Escape empties the field.
- A `429` with `Retry-After: 3` shows the notice, and no request is sent for 3 s.
- axe reports no serious or critical violation with the list open, in both schemes.

### R-UI-098 · Enter opens the results page of the main area
Priority: **MVP** · Covers: trove-only

Enter in the field with no active entry opens `/search?q=<text>&kind=<kind>`
([Q-UI-411](#q-ui-411--enter-without-a-picked-suggestion-opens-search-whose-tabs-read-the-three-lists)), a page of the main area with
the start side bar (R-UI-002); it never opens a route under `/admin` (R-UI-099).

- **Header**: the `<h1>` **Search** and the description "What you may read that matches "<text>"." The top bar's field keeps the text
  while the page is open; the page has no field of its own.
- **Tabs**: **Repositories**, **Packages**, **Projects**, without counts. `kind` chooses the tab; without it, or with `user` or
  `group`, the tab is the first group that had a suggestion when Enter was pressed, else **Repositories**. The tab is part of the
  address.
- **A tab is the list of its kind with `q`**, 25 rows at a time with **Load more** (R-API-010): Repositories, `GET
  /api/v1/repositories?q=`, with the plain user's columns of the repository list (Q-UI-312: the key with the project key as its
  second line, format and class, the client address in the address field of R-UI-065); Packages, `GET /api/v1/packages?q=`, with
  the columns of Q-UI-314 and the project and repository of each; Projects, `GET /api/v1/projects?q=`, with the start page's columns
  (Q-UI-311). A row leads to its object's page in the main area.
- **Empty**: "Nothing you may read matches "<text>"." on the tab. A text of fewer than two characters shows "Type at least two
  characters." and asks nothing.
- The page has no users, groups or admin pages and no link into the admin area. It takes over the page of the stopgap that replaced
  `/admin/repositories?q=` ([Q-UI-415](#q-ui-415--the-stopgaps-results-page-becomes-r-ui-098s)).

Acceptance criteria:

- As `ada` (system role `admin`), `test` and Enter open `/search?q=test`; the page holds no link whose path starts with `/admin`.
- `/search?q=vend&kind=package` opened directly shows the Packages tab, and **Load more** after 25 rows.
- As `sam`, the Repositories tab lists only what `sam` may read; a repository of `platform` with `hideExistence: false` on which
  `sam` holds nothing is not listed.

### R-UI-099 · The search leads into the admin area only by an administrator's choice of a marked suggestion
Priority: **MVP** · Covers: trove-only

The owner (2026-11-12, after typing "test" in the top bar and landing on the admin area's repository list with Enter): a search on
the main page must never end in the admin area, unless live suggestions are shown, some of them are explicitly marked as pages of the
admin area, and the user is an instance admin; then and only then may a search result lead there
([CLAUDE.md](../../CLAUDE.md#product-simple-by-default-details-on-demand)).

- **Instance administrator** ([Q-UI-412](#q-ui-412--an-instance-administrator-is-the-holder-of-the-system-role-admin)): a caller
  whose `GET /api/v1/authz/me` lists the system role `admin` in `roles`, directly or through a group. The other system roles, which
  open the admin area too (R-UI-067), get no admin suggestions and reach their screens through the Admin button.
- **Admin suggestions** are the admin pages, users and groups of R-UI-096 and R-UI-097, shown only to an instance administrator.
  Each shows the mark **Admin area**, the Admin button's icon `build` and the words, which are part of its accessible name ("Users,
  Admin area"); so does each of their group headings. They come after every group of the main area, behind a divider.
- **Only by an explicit choice**: a click on such a suggestion, or Enter while it is the active entry. Enter without an active
  entry, the results page, the filter, the frequently visited entries and every other way out of the field open a route of the main
  area. With the filter on Users or Groups, Enter without an active entry opens nothing and the live region says "Choose a user from
  the list." ("a group") ([Q-UI-413](#q-ui-413--under-a-users-or-groups-filter-enter-without-an-active-entry-opens-nothing)).
- **One gate.** The field opens every target through one function, which refuses a route under `/admin` unless the target is an
  admin suggestion and the caller is an instance administrator at that moment.
- The rule is navigation, not access control: what the admin area shows is decided by the API as before (R-UI-005).

Acceptance criteria:

- As `ada`: `users` shows **Users · Admin area** under Admin pages; Enter without an active entry opens `/search?q=users`; moving to
  that entry and Enter opens `/admin/users`.
- As the holder of `security-admin` alone: `users` shows no admin group, and Enter opens `/search?q=users`.
- A unit test drives every way out of the field (Enter with and without an active entry of each kind, a click on each kind, the
  **All … for** lines, the empty list's entries, the filter) as `ada`, `priya` and `sam` and asserts that only `ada`'s choice of a
  marked entry reaches `/admin`.
````

### 2.3 Changed requirements

| Page, requirement | Change |
|---|---|
| [api-and-ui.md](../spec/api-and-ui.md) R-UI-002 | Top bar: "a repository search field in the middle … has no room for a word (UI-A-48; one search for every reader is the owner's task T-22)" becomes "the search field of R-UI-095 in the middle (below 36 rem a search button, Q-UI-403)". "The global search over packages and files (P1, R-UI-015) takes its place in the same bar when its API exists." becomes "Files join the field's kinds when the items search of R-API-028 exists (R-UI-015)." The acceptance criterion "A user whose `authz/me` lacks `repositories:read-config` sees no search field in the top bar." becomes "Every signed-in user sees the search field (R-UI-095)." |
| [api-and-ui.md](../spec/api-and-ui.md) R-UI-015 | The first two bullets and the last one are replaced by: "The header search is R-UI-095 to R-UI-099. What stays here is the file search: when R-API-028 is built, **Files** becomes a kind of the field's filter, and Enter under it opens the file search form below." The other bullets and both acceptance criteria stay; the first criterion is reached through the kind **Files**. |
| [api-and-ui.md](../spec/api-and-ui.md) R-UI-044 | The row 2.1.4 reads "Single-key shortcuts (`/`, R-UI-095) are on by default and turned off in the profile (`keyboardShortcuts`, R-API-021); they never act while the focus is in a text field (Q-UI-401)". |
| [api-and-ui.md](../spec/api-and-ui.md) R-API-010 | "SCIM is the only exception." becomes "SCIM and the suggestions of R-API-060, which are no list, are the exceptions." |
| [api-and-ui.md](../spec/api-and-ui.md) R-API-020 | The row `/api/v1/search/…` reads "Search: the suggestions of the search field (R-API-060 to R-API-064) and the items search (R-API-028)". |
| [api-and-ui.md](../spec/api-and-ui.md) R-API-021 | The JSON example has `"keyboardShortcuts": true`; the data model `keyboard_shortcuts boolean NOT NULL DEFAULT true`; a sentence after the example: "`keyboardShortcuts` turns the UI's single-key shortcuts on and off; on by default (R-UI-095, Q-API-280)." |
| [repositories-and-proxy.md](../spec/repositories-and-proxy.md) R-REPO-012 | The request line takes `name=`; the filters bullet: "`q` matches the key, `<project>/<key>` and the description case-insensitively; `name` keeps the named repositories (R-API-063 of `api-and-ui.md`, linked as the page's other references are)". |
| [api-and-ui.md](../spec/api-and-ui.md) Q-UI-311 | A line under **Applied in the spec**: "Revisited by T-22 (Q-UI-414): the start page's package search goes, the top bar's field is the one search; **Recently opened** reads the store of Q-UI-405 with one request (R-API-063). The note that a repository search across projects needs a list readers may read was wrong: `GET /api/v1/repositories` is open to every caller and filters by read." |

The OpenAPI document follows: `getSearchSuggestions` under the tag `search` with the schemas `SearchSuggestions`,
`RepositorySuggestions`, `PackageSuggestions`, `ProjectSuggestions`, `UserSuggestions`, `GroupSuggestions` and their item schemas
(`additionalProperties: false`, `required` complete); `listRepositories` gains `name` and the new `q` text; the preferences example
and description change.

### 2.4 Decision blocks for [api-and-ui.md](../spec/api-and-ui.md), at the end of `## Decisions`, Q-UI first

Every block opens with the status line "Decided by the development agent — selected from the options below; the owner reviews it at
the walkthrough." The blocks here leave it out to stay short; the builder adds it.

````markdown
### Q-UI-400 · Every signed-in caller gets the field, and an anonymous one while something is public

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Every signed-in caller, and the anonymous UI (chosen) | The field wherever the shell runs | The owner asked for one search for every user; the anonymous UI exists only while `publicContent`, and the API answers what `public` admits | Anonymous typing spends the `anonymous` class of a client address (bounded by Q-UI-408) |
| (b) Signed-in callers only | No anonymous search | No anonymous load | A visitor of public content finds it only through the project list |
| (c) Only callers that can read something | A probe before the field shows | No empty answers | A request on every screen; an empty answer is a valid answer |

**Choice:** (a). **Rationale:** the field is how a reader finds content, and the anonymous visitor of public content is a reader.
The development agent revisits it if the field shows up in `trove_api_rate_limited_total{class="anonymous"}`.

**Applied in the spec:** R-UI-095.

### Q-UI-401 · Slash focuses the field and is on by default; the preference turns it off

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `/` on by default, off in the profile; Ctrl+K and ⌘K always; an anonymous caller Ctrl+K and ⌘K only (chosen) | The reference's key works at once | The owner asked for the reference's search; WCAG 2.1.4 is met by a way to turn the shortcut off, and the key never acts in a text field | A default that R-UI-044 had the other way |
| (b) `/` only after the user turned shortcuts on (R-UI-015 as written) | Off by default | No surprise for a speech-input user | Hardly anyone finds the preference; the key the owner knows does nothing |
| (c) `/` always | No switch | Simple | Fails WCAG 2.1.4 |

**Choice:** (a). An anonymous caller has no preference to turn `/` off, so it gets the chorded keys only. **Rationale:** 2.1.4
accepts a mechanism that turns a single-key shortcut off, and the preference exists already. The development agent revisits it if a
speech-input or screen-reader user reports the key.

**Applied in the spec:** R-UI-095, R-UI-044, R-API-021 (Q-API-280).

### Q-UI-402 · The filter is a menu button at the field's end, and the chosen kind a chip at its start

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A `filter_list` button at the field's end with radio items; the chosen kind as a removable chip at the field's start (chosen) | The reference's filter icon at the right | The default **All** needs no click; the chip shows the scope where the text is | One more control in the field |
| (b) A select before the field (R-UI-015's type selector) | Always visible | Explicit | Takes the room a 390 px bar does not have, for a choice rarely changed |
| (c) A prefix in the text (`repo:npm`) | Typed | No control | Not discoverable; hard to translate |

**Choice:** (a). **Rationale:** it is the reference's control, and it costs nothing while unused. The development agent revisits it
if the owner's reference shows the filter otherwise.

**Applied in the spec:** R-UI-095.

### Q-UI-403 · Below 36 rem a search button opens the field as a sheet

| Option | What it means | For | Against |
|---|---|---|---|
| (a) An icon button that opens a sheet over the top bar (chosen) | Search at every width | No room taken while unused | One more overlay |
| (b) No search below 36 rem (UI-A-48 today) | Hidden | Nothing to build | No search on a phone |
| (c) A second row under the top bar | Always shown | One tap less | 40 px of every narrow screen |

**Choice:** (a). **Rationale:** the field is for every user, at every width. The development agent revisits it if the sheet hides
something the search needs.

**Applied in the spec:** R-UI-095, R-UI-002.

### Q-UI-404 · The empty field offers the reference's parts in its order, and the admin pages last

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Places, frequently visited repositories, **View all my projects**, frequently visited projects; for an instance administrator an **Admin area** group last (chosen) | The reference's order with trove's objects: a repository where the reference has a project, a project where it has a group | Familiar; the user's own places first; the admin entries apart | Five groups in a small list |
| (b) Only the frequently visited | Short | Less to read | No way to the places by keyboard |
| (c) The admin area as one of the Places, as the reference lists it | Mixed | One group fewer | The plain group then holds an admin entry, which R-UI-099 keeps apart |

**Choice:** (a). **Rationale:** the owner showed this list as his picture of the search. The development agent revisits the parts
after the walkthrough shows what is used.

**Applied in the spec:** R-UI-096.

### Q-UI-405 · "Frequently visited" is kept in the browser, per principal, by visits and recency

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Browser storage per principal ID, ordered by visits halved every 14 days; one store for the recent lists too (chosen) | Per browser | Privacy: the server keeps no record of which repositories a person opens, and nothing to back up, restore or delete when a user leaves; no write per page view against the `write` class (10/s) and the database; no request to show it | A second device starts empty; whoever can read the browser profile reads the names (H-987) |
| (b) A server table of visits per user | Across devices | Follows the user | A navigation history of every user on the server; a write per page view; filtered by read on every use anyway |
| (c) The browser, by recency only (today's recent lists) | Last opened | Nothing new | "Frequently" means more than "last" |

**Choice:** (a). The store replaces `trove.recent-repositories`, which is one key for every user of a browser, and
`trove.recent-projects.<name>`. **Rationale:** the list is a convenience, and a convenience does not justify a record of what people
open. As the owner's brief describes the reference, it keeps the list in the browser too (not checked against its current version).
The development agent revisits it if the owner wants the list on every device.

**Applied in the spec:** R-UI-096.

### Q-UI-406 · A stored entry is shown after the server confirms that the caller still reads it

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One `GET /api/v1/repositories?name=…` for the stored repositories; the stored projects checked against the project list the UI holds (chosen) | One request | A suggestion never names a repository the caller lost | The repositories group appears one round trip later |
| (b) Shown as stored | No request | Instant | Names a repository the caller may no longer read; a click ends in 404 |
| (c) One `GET` per entry (the start page today) | Five requests | Nothing new | Five times the budget |

**Choice:** (a). **Rationale:** R-API-061's rule holds for every entry of the list, not only for the server's answers. The
development agent revisits it if the round trip is visible in the renders.

**Applied in the spec:** R-UI-096, R-API-063.

### Q-UI-407 · The main area's groups come first and the admin area's last, five in each

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Repositories, Packages, Projects, Pages; then Admin pages, Users, Groups for an instance administrator; five each, three pages, ten under a kind (chosen) | Grouped by kind | The plain user's job is a repository or a package (R-UI-067); the admin kinds apart (R-UI-099) | Up to 25 entries for an administrator |
| (b) One list ranked across kinds | Mixed | Short | Ranks of different kinds do not compare; the owner showed groups |
| (c) Projects first, in the reference's order | Containers first | Like the reference | In trove a project is the container and rarely the target |

**Choice:** (a). **Rationale:** the order follows what a reader looks for most. The development agent revisits it if the
walkthrough shows projects are looked for more.

**Applied in the spec:** R-UI-097.

### Q-UI-408 · One request per settled text, 250 ms after the last key; an anonymous caller waits longer

| Option | What it means | For | Against |
|---|---|---|---|
| (a) 250 ms and two characters signed in; 500 ms, three characters and at most four requests per 4 s anonymous; superseded requests cancelled; a `429` waits for `Retry-After` (chosen) | One request per pause | Signed in at most 16 requests per 4 s, 8 % of the `read` class's 200; anonymous at most 4, 20 % of the `anonymous` class's 20 per client address | An anonymous caller sees suggestions later |
| (b) 150 ms for everyone | Faster | Feels instant | Up to 27 requests per 4 s, more than the anonymous class admits |
| (c) One request per kind | Three to five lists per text | No new operation | Three to five times the budget; answers that arrive apart |

**Choice:** (a). **Rationale:** the field must never starve the screens of their budget, and the anonymous budget is shared by a
client address. The development agent revisits the numbers if the field shows up in `trove_api_rate_limited_total`.

**Applied in the spec:** R-UI-097.

### Q-UI-409 · The matched text is bold, marked by the client

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The client bolds every occurrence of each term in the shown name (chosen) | Computed from the text sent | No contract member; one rule for every kind | A package found by word prefix shows the prefix bold, not a range the server chose |
| (b) The server returns match ranges | Exact | The server's own rule | A member of the contract for presentation |
| (c) No highlighting | Plain | Nothing to build | The reader does not see why an entry matched |

**Choice:** (a). **Rationale:** the match rules are simple enough to show in the browser. The development agent revisits it if a
match is shown that the server did not make.

**Applied in the spec:** R-UI-097.

### Q-UI-410 · The field is an ARIA combobox with a grouped listbox

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `@angular/aria`'s combobox with a listbox popup, groups as `role="group"` (chosen) | The library R-UI-044 names | Roles, active descendant and keys from the library | Its listbox has no group directive (types of 22.2.5): if it does not find options inside group elements, (b) is used |
| (b) A combobox written on the CDK's `ActiveDescendantKeyManager` | Hand-made | Groups under full control | Keys and roles written and tested by trove |
| (c) A menu | `role="menu"` | Simple | A menu has no text field; screen readers announce it as a menu of commands |

**Choice:** (a), with (b) when a spec test shows that the arrow keys do not cross groups. **Rationale:** the APG's combobox is the
pattern for a text field with suggestions. The development agent revisits it when `@angular/aria` gains groups.

**Applied in the spec:** R-UI-097.

### Q-UI-411 · Enter without a picked suggestion opens `/search`, whose tabs read the three lists

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `/search?q=&kind=` in the main area, tabs Repositories, Packages, Projects, each the list of its kind with `q` (chosen) | The lists page and decide reads already | The columns and the address field of the lists; no second paging contract | No counts on the tabs |
| (b) A paged search operation for the results | One source | Ranked results | A second cursor over rows the lists already page |
| (c) Enter opens the first suggestion | No page | Fast | Enter would choose for the user, and could choose an admin page |

**Choice:** (a). **Rationale:** the page's job is everything that matches, and the lists are that. The development agent revisits
the counts if the walkthrough asks for them.

**Applied in the spec:** R-UI-098.

### Q-UI-412 · An instance administrator is the holder of the system role `admin`

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The role `admin`, directly or through a group (chosen) | The narrowest reading | The owner's words, "instance admin"; one role | A security administrator or an auditor reaches its few screens through the Admin button only |
| (b) Any system role, each with its own pages (R-UI-067's administrator) | Wider | Every administrator finds its screens | Wider than the owner's words |
| (c) Anyone with the admin area (any capability) | The Admin button's rule | Consistent with the button | Wider still |

**Choice:** (a). **Rationale:** the owner said "then and only then"; the narrow reading is the one that cannot be wrong in the
direction he warned about. The owner decides at the walkthrough (question 1 of the plan).

**Applied in the spec:** R-UI-099.

### Q-UI-413 · Under a Users or Groups filter, Enter without an active entry opens nothing

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Nothing opens; the live region says to choose (chosen) | Explicit only | R-UI-099 holds without an exception | Enter does nothing visible but the message |
| (b) Enter opens the first user | Implicit | Fast | An implicit way into the admin area |
| (c) Enter opens `/search` | Main area | Consistent | A page that has no users |

**Choice:** (a). **Rationale:** the owner allows the admin area only by an explicit choice. The development agent revisits it if
administrators ask for a user list in the main area.

**Applied in the spec:** R-UI-099.

### Q-UI-414 · The start page's package search goes; the top bar's field is the one search

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Remove it (chosen) | One field | One way to each job (the style guide's checklist) | The start page loses the section Q-UI-311 put first |
| (b) Keep it beside the field | Two searches | Nothing removed | Two searches with different rules on one screen |
| (c) The same component, large, on the start page | Two instances | Visible | Two copies of one field on one screen |

**Choice:** (a). The start page keeps **Recently opened** and the projects. **Rationale:** the field is on every screen, the start
page included. The development agent revisits it if plain users do not find the field.

**Applied in the spec:** R-UI-098, Q-UI-311 (revisited).

### Q-UI-415 · The stopgap's results page becomes R-UI-098's

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Extend what landed under Q-UI-390 to Q-UI-399 to R-UI-098 (chosen) | No throwaway | Builds on a tested page | Its decisions are read again |
| (b) Rebuild from scratch | Clean | No inherited shape | Throws away a working page |

**Choice:** (a). If the stopgap chose another route than `/search`, it moves there without an alias (no backward compatibility before
the first stable release). **Rationale:** both pages answer the same Enter. The development agent revisits nothing here.

**Applied in the spec:** R-UI-098; a revisit line in each of Q-UI-390 to Q-UI-399 that R-UI-098 changes.
````

### 2.5 Decision blocks for [api-and-ui.md](../spec/api-and-ui.md), after the Q-UI blocks of 2.4

````markdown
### Q-API-270 · One operation answers the suggestions, and the results page reads the three lists

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `GET /api/v1/search/suggestions` for the field; the lists with `q` for the results page (chosen) | One request per text | One request per settled text; one index snapshot for every kind; cheaper reads than the lists (no aggregation, no enumeration of candidate versions); the results page keeps the lists' paging, columns and filters | A kind's matching is stated twice; R-API-062 keeps the lists a superset |
| (b) The lists alone, called in parallel per text | Nothing new | No contract change | Three to five requests per text; the package list aggregates every version and may decide up to 200,000 candidate versions per keystroke; no users or groups |
| (c) One search operation for suggestions and paged results | One source | Ranked results pages | A second cursor (over rank and name) for rows the lists page already |

**Choice:** (a). **Rationale:** the field's budget and the lists' paging are different jobs. The development agent revisits (c) if the
results page needs a ranked order.

**Applied in the spec:** R-API-060, R-UI-098.

### Q-API-271 · The answer holds one typed member per kind and no cursor

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `repositories`, `packages`, `projects`, `users`, `groups`, each `{items, more}` (chosen) | Typed | A generated type per kind; the UI groups without sorting | Five schemas |
| (b) One array of tagged items | Mixed | One schema | A union to discriminate in every client |
| (c) The list envelope of R-API-010 per kind | Paged | Familiar | A cursor the field never follows |

**Choice:** (a). **Rationale:** a group of the dropdown is a member of the answer. The development agent revisits it if a kind needs
paging.

**Applied in the spec:** R-API-060, R-API-010.

### Q-API-272 · Users and groups need `identities:read`, checked before any lookup

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `403` without the capability, the same for every `q`; never in the default kinds (chosen) | Explicit | A client tells "none" from "not allowed"; no answer depends on a lookup the caller may not make (R-API-008) | The UI must not ask for them without the role |
| (b) Every caller, as a member picker | Wide | Reuse | The owner made them admin suggestions; the project's principal picker has its own operation |
| (c) Left out silently | Quiet | No `403` | An answer that differs by capability without saying so |

**Choice:** (a). **Rationale:** users and groups are administration objects. The development agent revisits it if a non-admin
screen needs them.

**Applied in the spec:** R-API-060, R-API-061.

### Q-API-273 · Repositories are matched over the request's grant index, and projects over their rows, without an index

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The grant index in memory for repositories; one read of `project` for projects (chosen) | No statement for repositories | The candidates and the decision share one index; every active repository is in it already | A repository created through another replica is suggested after the propagation bound (R-API-052), as it becomes readable anyway |
| (b) SQL with `ILIKE` over `repositories` and `project` | A statement each | Fresh at once | A statement for rows the index holds; still decided on the index |
| (c) The repository list's read and parse | As the list | Reuse | Parses every document on every keystroke |

**Choice:** (a). **Rationale:** a repository the index does not know cannot be decided readable, so a read of the table adds nothing.
The development agent revisits it if an installation holds far more than the 10,000 repositories of R-ARCH-027.

**Applied in the spec:** R-API-060, R-API-064.

### Q-API-274 · Package names: a whole-name prefix on a C-collation B-tree and word prefixes on a GIN tsvector, without pg_trgm

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `packages_name_lower` on `lower(name) COLLATE "C"` and `packages_name_words` on a generated `tsvector` (chosen) | Built-in indexes | No extension; `ui` finds `@acme/ui-kit`, `logging` finds `Microsoft.Extensions.Logging`; the B-tree also orders and stops at the limit | An infix inside a word is not suggested |
| (b) A trigram GIN index (`pg_trgm`) | Any infix | Finds everything | The extension may be refused on a managed PostgreSQL (migration 00017) |
| (c) The name prefix only | One B-tree | Smallest | Misses every scoped npm name and every dotted NuGet ID by its last part |

**Choice:** (a). **Rationale:** it follows NuGet's search (Q-NUGET-12) and trove's rule against the extension. The development agent
revisits (b) if trove ever requires `pg_trgm` for another reason.

**Applied in the spec:** R-API-062, R-API-064.

### Q-API-275 · A package in a repository read in part is decided on its document

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `ReadScope.MayRead(repo, name, "")` and the repository's patterns, once per candidate (chosen) | Package level | One decision per candidate; never more than the caller may read | A package whose document is readable and none of whose versions is gets suggested, and its page shows no version; one readable only through its versions is not suggested (the results page lists it) |
| (b) Its versions, one by one, until one is readable (the list's rule) | Version level | The list's answer | Unbounded per candidate, or the enumeration of up to 200,000 versions on every keystroke |
| (c) No suggestions from repositories read in part | Whole repositories only | Cheapest | A reader with a path-scoped grant never gets a package suggested |

**Choice:** (a). **Rationale:** a suggestion is a name, and the document is what names a package. The development agent revisits (b)
if path-scoped grants on versions turn out common.

**Applied in the spec:** R-API-061.

### Q-API-276 · `more` reports only readable matches the answer left out

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `true` when a readable match was left out (chosen) | Readable only | Never an oracle | May be `false` while the bounded read stopped before more |
| (b) A count of every match | Total | Informative | Counts what the caller may not read |
| (c) No `more` | Nothing | Simplest | The UI cannot offer **All … for** where it helps |

**Choice:** (a). **Rationale:** R-AUTHZ-016 allows counts of readable items only. The development agent revisits nothing here.

**Applied in the spec:** R-API-061.

### Q-API-277 · An entry names the project of a repository or a package by its key

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The key only (chosen) | `web` | The key is part of the qualified name the caller reads | Less friendly than the display name |
| (b) With the display name | `Web platform` | Friendlier | A caller who reads a repository by a grant may not read its project (R-AUTHZ-035 rule 8); the name would leak |

**Choice:** (a). **Rationale:** a suggestion shows nothing its reader could not read on its own. The development agent revisits it if
every reader of a repository becomes a reader of its project.

**Applied in the spec:** R-API-061.

### Q-API-278 · The repository list takes `name`

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `name` repeated or comma-separated, at most 50, absent when not visible (chosen) | A filter | One request for the stored entries; the start page's five reads become one | A filter more |
| (b) A `GET` per name | As today | Nothing new | Five requests per opening |
| (c) A batch `POST` | A body | Many names | A `POST` for a read |

**Choice:** (a). **Rationale:** a filter is how R-API-011 narrows a list. The development agent revisits the 50 if a store grows past
20 per kind.

**Applied in the spec:** R-API-063, R-REPO-012.

### Q-API-279 · The repository list's `q` matches the qualified name

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `q` matches the key, `<project>/<key>` and the description (chosen) | Wider | Every repository suggestion for `web/npm` is on the results page | A breaking change of `q` (none before the first stable release) |
| (b) As today | Key and description | Nothing changes | The results page misses suggestions |

**Choice:** (a). **Rationale:** the results page must hold every suggestion of its kind. The development agent revisits nothing here.

**Applied in the spec:** R-API-062, R-REPO-012.

### Q-API-280 · `keyboardShortcuts` is true by default, and the migration sets it on every stored row

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Default `true`; existing rows set to `true` (chosen) | On everywhere | No single-key shortcut existed before T-22 (nothing in `web/src` read the preference but the profile), so a stored `false` turned nothing off and is no choice | A box someone unticked becomes ticked |
| (b) Default `true` for new rows only | Partial | Keeps stored values | Everyone who ever saved a theme keeps `false` and finds `/` dead |
| (c) Default `false` | As today | Nothing changes | The owner's key does nothing until found in the profile |

**Choice:** (a). **Rationale:** a value that never had an effect carries no intent. The development agent revisits nothing here.

**Applied in the spec:** R-API-021, R-UI-044.

### Q-API-281 · The suggestions use the `read` and `anonymous` classes, and `q` is 2 to 100 characters

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The existing classes; `q` of 2 to 100 characters (chosen) | No new budget | The field keeps itself at 8 % of `read` (Q-UI-408); 100 is more than the longest qualified name (32 + 1 + 64) | — |
| (b) A class `search` of its own | A second budget | Isolated | A second budget per principal that a client can spend in parallel |
| (c) No length rule | Any text | Flexible | One character matches almost everything; very long texts cost without use |

**Choice:** (a). **Rationale:** the classes exist to bound every read the same way. The development agent revisits (b) if searches
starve the screens.

**Applied in the spec:** R-API-060.
````

### 2.6 Security: a new page `docs/security/search.md`

The search reads across every project, which no other mechanism of the UI does, so it gets a page of its own; the security README
gains a row in its table ("search.md · What the search field shows across projects · the suggestions operation, its per-kind rules,
the admin-area gate, the frequently visited store"), and [permissions.md](../security/permissions.md) gains one sentence in "What a
refusal reveals": "The search field's suggestions follow the listing rule; `search.md` says what they show", with the file linked.

````markdown
# The search field: what it shows across projects

The search field of the UI (R-UI-095 to R-UI-099 in [api-and-ui.md](../spec/api-and-ui.md)) and its operation
`GET /api/v1/search/suggestions` (R-API-060 to R-API-064, [search_api.go](../../internal/api/v1/search_api.go)) read across every
project. This page says what a suggestion may show, what a request can make the database do, how the admin-area rule works, and
what the browser keeps.

## What a suggestion shows

- Each kind follows its list: a repository is suggested when the caller may read it on the request's index, a package when the read
  scope of the package list admits it (in a repository read in part, by its document), a project by the rule of the project list,
  users and groups only to a holder of `identities:read`.
- The suggestions are filtered before they are cut, and `more` counts only readable matches, so neither the entries nor `more` change
  when a caller adds or removes what another caller cannot read.
- `hideExistence: false` changes the status of a refused read, never a listing: a repository the caller cannot read is not
  suggested, whatever its setting.
- A repository's or a package's project is named by its key, which is part of the qualified name the caller reads; the display name
  appears only in a project entry the project rule admits.
- `kind` `user` or `group` without `identities:read` answers `403` before any lookup, with the same body for every text.

## What a request can make the database do

- One request per settled text, in the `read` class of its principal or the `anonymous` class of its client address.
- A fixed number of statements per request; the package statements read at most 400 candidate rows.
- The tsquery is built from runs of letters and digits of the text only, and the `LIKE` pattern escapes `%`, `_` and `\`, so no
  operator of either syntax reaches PostgreSQL from a request.

## The admin area

The owner's rule (R-UI-099) is a rule of navigation: the field opens a route under `/admin` only for a suggestion marked **Admin
area**, shown to a holder of the system role `admin` and picked explicitly, through one gate function. It is not access control:
the admin area's screens are decided by the API as before, and the operation answers users and groups to every holder of
`identities:read`, whatever the UI shows.

## What the browser keeps

The frequently visited repositories and projects are kept in `localStorage` under `trove.frequent.<principal ID>`: qualified names,
keys, visit counts and times, no secret (R-UI-043). A stored repository is shown only after the repository list has confirmed that the
caller still sees it.

## What this does not cover

### H-985 · The time of a suggestion depends on names the caller cannot read

Component: the package statements of R-API-064 and the in-process match over the grant index.

Subject: a caller who times many suggestion requests. The B-tree and GIN scans meet the matching names of every repository before
the filter on the repositories the caller reads, and the in-process match runs over every repository of the index before the
decision, so the time of an answer grows with matching names the caller cannot read; the answer itself does not. Live once T-22 is
built; the same kind of gap as [H-299](permissions.md#h-299--the-cost-of-a-package-list-for-a-caller-with-row-level-grants-depends-on-versions-the-caller-cannot-read).
What bounds it: the rate classes, and the noise of the network against a difference of milliseconds. What makes it real: a very
large repository the caller cannot read and a caller that can measure many requests from close by.

### H-986 · Search texts are in the request log with the principal

Component: the request log of R-OPS-010 in [operations-and-observability.md](../spec/operations-and-observability.md) (`path` with
its query string, `log.request.redact_query_params`).

Subject: every user of the field. Each settled text is one request, so the log holds a trail of what a person typed, with its
principal ID. Live once T-22 is built. An operator can add `q` to `log.request.redact_query_params`, which redacts the `q` of every
list as well. What makes it real: a reader of the logs who should not learn what users search, such as the name of an unreleased
product.

### H-987 · The frequently visited names stay in the browser profile

Component: the store `trove.frequent.<principal ID>` of R-UI-096.

Subject: a user on a shared operating-system account or browser profile. Sign-out does not clear the store, so whoever can read the
profile's storage reads the qualified names of the repositories and the keys of the projects the user opened, and how often. Live
once T-22 is built; today's `trove.recent-repositories` holds the same kind of names under one key for every user of a browser. What
makes it real: a shared workstation.
````

## 3 · Work packages

### 3.1 The cut: two builders, after T-18 has landed

| Builder | Feature end to end | Owns | Stack | kind |
|---|---|---|---|---|
| **B1 · Backend** (does WP0 first) | The operation, the SQL and the indexes, the authorization of every kind, the repository list's `name` and `q`, the preference default, every Go unit and integration test, the backend documentation, the security page | `api/openapi.yaml` and its generated code (`internal/api/v1/gen/`, `web/src/app/api/generated/schema.d.ts`), `internal/api/v1/search_api.go`, `search_match.go` and their tests (new), `internal/api/v1/repositories_api.go`, `internal/repository/service.go` (`ListFilter.Names`, the `q` match), `internal/packages/view/suggest.go` (new), `internal/authz/index.go` (an exported read of a repository's class and format), the principal manager's suggestion read, `internal/meta/postgres/queries/search.sql` (new) and the generated `q` package, the migration, the preference default in Go, `internal/archtest` (a layer entry if a package is added), `web/src/app/api/search-api.ts` (new), `web/src/app/api/repositories-api.ts` (`name`), the fakes under `web/src/app/api/testing/`, `test/integration/search/` (new), `test/integration/querybudget/search_test.go`, `test/integration/repository/`, `test/integration/identity/` (the default), the spec pages of section 2 (WP0), `docs/security/search.md` (new) and its README row, `docs/security/permissions.md` (one sentence), `docs/developer/{http-api,packages,database,authorization}.md`, README's API table | yes | no |
| **B2 · UI and end to end** (starts when WP0 has landed) | The field, the dropdown, the sheet, the shortcut, the store, the results page, the start page and the switcher, the renders, the journey, the manual | `web/src/app/features/search/` (new), `web/src/app/core/frequent-places.ts` (new), `web/src/app/core/session-store.ts` (`isInstanceAdmin`), `web/src/app/core/project-context.ts` (its recent list), `web/src/app/core/help-topics.ts`, `web/src/app/app.{html,ts,routes.ts}`, `web/src/styles/_shell.scss`, `web/src/app/features/projects/{projects-home,project-switcher}.ts` and specs, the one-line visit records in `features/repositories/` (seam above), `features/repositories/recent-repositories.ts` (deleted), `features/profile/profile.html` (the label), the stopgap's files, the locale files, `web/e2e/tests/23-search.spec.ts` (new), `web/e2e/renders/screens.ts`, `docs/manual/**`, `docs/developer/{web-ui,ui-style-guide,e2e}.md`, README's feature list | journey only | **yes** (`access-long`) |

Shared, with a rule: `docs/decisions.md` (no ADR is needed; a builder that decides a new question appends its row), `docs/coverage/*`
(regenerated by `make coverage` in each builder's last commit), `promt.md`, `BETA.md`, `FULL.md` (the development agent).

### 3.2 B1 · Backend

| WP | Content | Requirements |
|---|---|---|
| **WP0 · Contracts and spec** (lands before B2 starts) | Section 2 pasted (requirements, changed rows, decision blocks with their status lines, the security page and its links, the revisit lines in Q-UI-311 and the stopgap's blocks); OpenAPI: `getSearchSuggestions` and its schemas, `listRepositories` `name` and the `q` text, the preference default; `make generate`; the handler registered with its parameter checks, the `403` of the admin kinds and empty members, so B2 runs against a real server from the start; `SearchApi.suggest({q, kinds, limit})` with an `AbortSignal`, `RepositoriesApi.list({name})`, their fakes | R-API-060 (contract), R-API-063 (contract) |
| **WP1 · Schema** | The migration: `packages.name_words`, `packages_name_words`, `packages_name_lower`; `user_preferences.keyboard_shortcuts` default `true` and `UPDATE … SET keyboard_shortcuts = true`; the Go default of a user without a row; `search.sql` with `PackageSuggestionsByPrefix`, `PackageSuggestionsByWords`, `UserSuggestions`, `GroupSuggestions`; `make generate` | R-API-064, R-API-021 |
| **WP2 · The suggestions** | `search_match.go`: the normalization, the terms, the words, the `LIKE` escape, the tsquery text, the ranks of R-API-062; repositories over `acc.Index()` decided with `acc.Readable` or `acc.Exposed`; projects with T-18's `projectListed`; packages through `view.Service.Suggest(ctx, scope, topology, q, limit)` that builds the shapes of `ReadScopeOn` without enumerating candidates, runs statement A and, when needed, B, decides `ReadRows` candidates with `MayRead(repo, name, "")` and `Topology.Admits`, collapses a package held by several repositories to its first readable one; users and groups through the principal manager; `more` per kind | R-API-060, R-API-061, R-API-062, R-API-064 |
| **WP3 · The repository list** | `name` (at most 50, malformed `400`), `q` over `<project>/<key>` | R-API-063, R-REPO-012 |
| **WP4 · Tests and documentation** | The integration areas below; `http-api.md` (the operation and why it is no list), `packages.md` (the suggestion read), `database.md` (the migration and the two indexes), `authorization.md` (the index read the suggestions use); README's API table | all of B1's |

**Tests by tier.**

- Unit: `internal/api/v1/search_match_test.go` (`TestR_API_062_…`: the ranks of each kind with the acceptance rows, terms and words,
  the `LIKE` escape of `%`, `_` and `\`, a tsquery built from `@acme/ui-kit` and from `a:*|!b`); `search_api_test.go`
  (`TestR_API_060_…`: the parameter errors, the members per `kind`, the `403` of `kind=user` before any lookup and equal for two
  texts; `TestR_API_061_…` on a fake decider: the cut after the filter, `more` from readable matches only).
- Integration, once per area through the slot wrapper:
  - `search` (new; the fixture of R-API-061's acceptance criteria on T-18's levels): every row of R-API-061 and R-API-062; a
    path-scoped grant on `@web/*` in a repository read in part; `hideExistence: false`; the endpoint `customers` with exposure rules;
    the break-glass administrator with `kind=user,group`.
  - `querybudget`: `TestR_API_064_ASuggestionCostsAFixedNumberOfStatements` with 10 and with 10,000 matching packages.
  - `repository`: `TestR_API_063_…` (`name`), `TestR_REPO_012_…` (`q` over the qualified name).
  - `identity`: `TestR_API_021_…` (a new user's `keyboardShortcuts` is `true`).
  - The migration's `EXPLAIN` test of R-API-064 runs in `search`.
  The area `search` falls into shard b of `make test-integration` by itself (the Makefile's shards a and c list their areas).
- Go e2e: none; `go vet -tags e2e ./test/e2e/...` once (WP0 changes generated types).

**Gates.** The common brief's, with `make generate-check` on the committed tree after WP0 and WP1.

### 3.3 B2 · UI and end to end

| WP | Content | Requirements |
|---|---|---|
| **WP5 · The field** | `features/search/search-field.{ts,html,scss}`: the combobox (Q-UI-410), the filter menu and the chip, the sheet below 36 rem, the states, the live region; `search-model.ts`: the suggestion type with its area (`main` or `admin`), the groups and their order, the places, the admin pages from `Navigation` filtered by capability, and the one gate function of R-UI-099; `highlight.ts`; the shortcut (`/` with the preference, Ctrl+K and ⌘K) as a host listener of `App`; `SessionStore.isInstanceAdmin`; `core/frequent-places.ts` (the store of Q-UI-405, its order, the 15-minute rule, the removal of the two old keys) and the one `name=` request; the field in `app.html` in place of the form; `_shell.scss`; the profile's label "Keyboard shortcuts with a single key, such as / for the search" | R-UI-095, R-UI-096, R-UI-097, R-UI-099 |
| **WP6 · The results page** | `features/search/search-results.ts` and `search.routes.ts`, the route `search` in `app.routes.ts`, the tabs and their lists with **Load more**, the help topic of `/search`; the stopgap's page and its specs absorbed (Q-UI-415) | R-UI-098 |
| **WP7 · The start page and the recent lists** | `projects-home.ts` without **Find a package**, **Recently opened** from the store with one `name=` request; `project-context.ts` and `project-switcher.ts` reading the store; the visit records in `repository-detail.ts`, `repository-content.ts`, `repository-list.ts` and `ProjectContext`; `recent-repositories.ts` deleted | R-UI-096, Q-UI-414 |
| **WP8 · Journey, renders, manual** | `23-search.spec.ts`; the render states of 3.5; the manual: a new page `docs/manual/user/search.md` in the user guide's list of the manual's README, `getting-started.md` (the top bar), `client-setup.md` and `packages.md` where they say how to find a repository or a package, `admin/overview.md` (the admin suggestions); `web-ui.md` (the components, the store, the route), `ui-style-guide.md` (the search dropdown under **Opening and closing**: `dropdown-in` and `dropdown-out`), `e2e.md` (journey 23); README: one feature line | all of B2's |

**Tests by tier.**

- Unit (`make web-test`), each spec calls `seriousViolations` with the list open:
  - `search-field.spec.ts` (`R-UI-095 …` to `R-UI-097 …`): the field for every session state; `/` with the preference on and off and
    inside a text field; Ctrl+K; the filter and the chip, Backspace; the sheet at 390 px with Escape and the focus back; the empty
    list per kind of user and with storage blocked; one request after 250 ms and the cancelled one; the anonymous timing and the
    window of four; the groups and their order; `more` and **All … for**; the states, the `429` with `Retry-After`; the keyboard of
    R-UI-097 and the ARIA attributes, Down across a group boundary.
  - `search-gate.spec.ts` (`R-UI-099 …`): the walk over every way out of the field as `ada`, `priya`, `sam` and a holder of
    `security-admin`.
  - `frequent-places.spec.ts` (`R-UI-096 …`): the key per principal, the order, the 15 minutes, the cap of 20, the removal of the old
    keys, no admin route recorded, blocked storage.
  - `highlight.spec.ts` (`R-UI-097 …`): overlaps, case, a term twice, keys with `.`, `-`, `@` and `/`.
  - `search-results.spec.ts` (`R-UI-098 …`): the tab from `kind` and from the first group, each tab's list call with `q`, **Load more**,
    the empty text, no `href` under `/admin` in the page.
  - Changed: `projects-home.spec.ts`, `app.spec.ts` (the top bar for `sam`), the project switcher's spec, `profile.spec.ts`.
- Playwright (`web/e2e/tests/23-search.spec.ts`, set `access-long`, run once on kind with `$S/kind-lock.sh`): `beforeAll` creates a
  project with an npm local repository, publishes a package, and makes bob a reader, through the API with the administrator token.
  Journey 1, bob: a part of the key shows the repository and the package with the text bold; Down and Enter open the repository's
  page; Enter without a pick opens `/search` with the repository on its tab and no link under `/admin`; axe in both schemes with the
  list open. Journey 2, the break-glass administrator (role `admin`): the empty list ends with the **Admin area** group; `users`
  shows **Users · Admin area**; Enter without a pick opens `/search?q=users`; the pick opens `/admin/users`. Journey 3: `/` focuses
  the field, the profile turns shortcuts off and `/` no longer does, Ctrl+K still does; the profile turns them on again. Journey 4,
  390 px: the button opens the sheet, Escape closes it, the focus is on the button.
- Go e2e: none.

**Gates.** `make web-test`, `make web-lint`, the journey once, the coverage links, the white-label scan.

### 3.4 Order

| Step | Who | Starts after | Lands |
|---|---|---|---|
| T-18, the stopgap | their builders | — | on main |
| WP0 | B1 | T-18 on main | the spec, the contract, the generated types, the handler's skeleton, the web clients and fakes |
| WP1 to WP3 | B1 | WP0 | one commit each, its integration areas green |
| WP4 | B1 | WP1 to WP3 | the remaining tests and the documentation |
| WP5, WP6 | B2 | WP0 | against the fakes and WP0's skeleton; rendered against B1's server once WP2 has landed |
| WP7 | B2 | WP5; WP3 for `name=` | the start page and the recent lists |
| WP8 | B2 | WP5 to WP7, B1's WP2 and WP3 | the journey run once, the manual |
| Closing | the development agent | all | `make coverage`, the evidence record from CI, promt.md |

### 3.5 Render gate (rule 11)

Each WP that changes a screen renders it with `make ui-renders` (both schemes; 1440, 1024 and 390 px; the seed's short and long
keys, several rows and an empty state) before its commit, opens the pictures and judges them against the style guide's checklist;
the report names what was found and fixed and the paths of the pictures. The open dropdown is a state of a screen, so the states are
`walk`s in [screens.ts](../../web/e2e/renders/screens.ts): focus the field, type, press keys. Frequently visited entries come from the
walk opening repositories and projects first. A `429` is answered with Playwright's `page.route` on the real screen.

| WP | Screens and states | As |
|---|---|---|
| WP5 | The top bar with the field at 1440 and 1024 px and the button at 390 px on the start page, a project's repository list and the admin dashboard; the empty list with frequently visited entries, with an empty store, and with the **Admin area** group; typing `web` (every group), the seed's longest keys (the addresses of 89 and 96 characters), a text with no match, `us` as the administrator (Users and Admin pages); the filter menu open, the chip **Packages**; the `429` notice; the sheet at 390 px with suggestions | plain, power, admin; anonymous where the seed makes something public |
| WP6 | `/search?q=web` on each tab; a tab with no row; the Packages tab with **Load more**; `kind=package` | plain, admin |
| WP7 | The start page without the package search and with **Recently opened**; the project switcher's recent list | plain, power |

At 390 px no list, tab or sheet scrolls sideways, and a long key is cut with an ellipsis inside its entry, never past the list's edge.

### 3.6 Exit criteria

- Every acceptance criterion of R-API-060 to R-API-064 and R-UI-095 to R-UI-099 is a test whose name carries its ID, in the tier of
  3.2 and 3.3.
- `grep -rn "canSearch\|submitSearch\|recent-repositories\|trove.recent-projects" web/src` prints nothing.
- The renders of 3.5 exist and were judged; the reports list them.
- The set `access-long` passes once on kind with journey 23; the integration areas of 3.2 pass once each.
- `make coverage` regenerated; the development agent refreshes the evidence record from CI.

## 4 · Decisions and questions

### 4.1 Decisions of the planning agent

Proposals of the development agent, reviewed by the owner at the walkthrough.

- **Q-UI-400**: every signed-in caller gets the field, and an anonymous one while something is public, because the field is how a
  reader finds content.
- **Q-UI-401**: `/` is on by default and turned off in the profile, Ctrl+K always, because WCAG 2.1.4 is met by the off switch and the
  owner asked for the reference's key.
- **Q-UI-402**: the filter is a menu button at the field's end with a chip for the chosen kind, as the reference's filter icon.
- **Q-UI-403**: below 36 rem a button opens the search as a sheet, so that a phone has a search too.
- **Q-UI-404**: the empty list takes the reference's parts in its order with trove's objects, and the admin pages come last and apart.
- **Q-UI-405**: "frequently visited" stays in the browser per principal, so that the server keeps no record of what people open and no
  page view costs a write.
- **Q-UI-406**: a stored repository is shown only after one request confirms it, so that no entry names what the caller lost.
- **Q-UI-407**: repositories, packages, projects and pages first, the admin kinds last, five each, because that is the plain user's
  order of need.
- **Q-UI-408**: one request per settled text, 250 ms signed in and 500 ms anonymous, which keeps the field at 8 % and 20 % of the two
  classes.
- **Q-UI-409**: the client bolds the matched text, because the rules are simple enough to show without a contract member.
- **Q-UI-410**: an APG combobox with a grouped listbox on `@angular/aria`, with the CDK's key manager if the library's listbox does not
  cross groups.
- **Q-UI-411**: Enter opens `/search`, whose tabs are the three lists with `q`, because they already page and decide reads.
- **Q-UI-412**: an instance administrator is the holder of the role `admin`, the narrowest reading of the owner's words.
- **Q-UI-413**: under a Users or Groups filter, Enter without a pick opens nothing, so that no implicit way into the admin area remains.
- **Q-UI-414**: the start page's package search goes, because one job has one way.
- **Q-UI-415**: the stopgap's page is extended to R-UI-098, not rebuilt.
- **Q-API-270**: one operation for the suggestions and the lists for the results page, because the field's budget and the lists'
  paging are different jobs.
- **Q-API-271**: one typed member per kind and no cursor, because a dropdown group is a member of the answer.
- **Q-API-272**: users and groups need `identities:read`, refused before any lookup, because they are administration objects.
- **Q-API-273**: repositories are matched over the request's grant index, because a repository the index does not know cannot be
  decided readable anyway.
- **Q-API-274**: package names by a C-collation B-tree prefix and GIN word prefixes, because `pg_trgm` stays out of trove.
- **Q-API-275**: a package in a repository read in part is decided on its document, which bounds the work to one decision per
  candidate.
- **Q-API-276**: `more` counts only readable matches, so that it is never an oracle.
- **Q-API-277**: an entry names a project by its key, because a reader of a repository need not read its project.
- **Q-API-278**: the repository list takes `name`, so that stored entries are confirmed in one request.
- **Q-API-279**: the repository list's `q` also matches `<project>/<key>`, so that the results page holds every suggestion.
- **Q-API-280**: `keyboardShortcuts` becomes `true` everywhere, because a stored `false` never had an effect.
- **Q-API-281**: no rate-limit class of its own and `q` of 2 to 100 characters, because the existing classes bound every read alike.

### 4.2 Questions for the owner

One at a time; each changes what is built.

**1. Who is an "instance admin" for the admin suggestions?** trove has five system roles; each opens the admin area with its own
screens.

- (a) Only the role `admin`. The others reach their screens through the Admin button.
- (b) Every system role, each offered the admin pages it may use (the administrator of R-UI-067).
- (c) Everyone who has the admin area, a project's Owner included where it has one.
- **Recommendation: (a).** It is the literal reading of "instance admin" and cannot lead anyone into the admin area you did not mean;
  (b) is a filter on the admin pages that exists already (the side bar's), so widening later costs one work package's tests, not a
  redesign.

**2. Should "frequently visited" follow a user to another browser?**

- (a) No: it stays in the browser, per user, as planned. A new device starts empty.
- (b) Yes: the server keeps each user's visits (a table, a write per visited page, a retention of 90 days).
- **Recommendation: (a).** The server would hold a record of which repositories each person opens, for a convenience; (b) is one work
  package if you want it, and the UI's store would then fill from the server.

**3. Should the field, opened inside a project, offer to search only that project first?**

- (a) No: the field always searches everything the caller may read, and the project is shown on every entry.
- (b) Yes: inside a project the first entry is "Search in <project>", and the results page takes `project=`; every list takes
  `project` already.
- **Recommendation: (a) for T-22, (b) after the walkthrough** if you miss it; it is a small step on top of this plan, because the lists
  and the operation's matching need no change beyond a `project` parameter.
