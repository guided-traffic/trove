# The search field: what it shows across projects

The search field of the UI (R-UI-095 to R-UI-099 in [api-and-ui.md](../spec/api-and-ui.md)) and its operation
`GET /api/v1/search/suggestions` (R-API-060 to R-API-064, [search_api.go](../../internal/api/v1/search_api.go)) read across every
project. This page says what a suggestion may show, what a request can make the database do, how the admin-area rule works, and
what the browser keeps.

## What a suggestion shows

- Each kind follows its list, on the one index of the request ([search_api.go](../../internal/api/v1/search_api.go) `suggest`):
  - a repository is suggested when the caller may read it, or when the request's endpoint exposes it to a holder of
    `repositories:read-config` or to an Owner of its project, as `GET /api/v1/repositories` decides its rows
    (`suggestRepositories`, `visibleRepositories`); a repository being deleted is not in the index;
  - a package when the read scope of the package list admits it: a repository read whole admits every name, one read in part
    (path-scoped grants, a narrowing token scope, exposure rules) admits a name when the caller may read the package's document
    and the repository's include and exclude patterns admit it ([suggest.go](../../internal/packages/view/suggest.go), Q-API-275);
  - a project by the rule of the project list: a member, a holder of `projects:read`, or a level that admits the caller
    (`projectListed` in [projects_api.go](../../internal/api/v1/projects_api.go));
  - users and groups only to a holder of `identities:read`.
- T-18's rules hold as on the lists: `internal` admits an internal subject only, so a user flagged external, a service account, a
  CI token and a request through a Bastion get what grants, roles and `public` give them; a token's scope narrows grants and
  `internal`, not `public`; an endpoint's exposure rules narrow every caller on it.
- The suggestions are filtered before they are ranked and cut ([search_match.go](../../internal/api/v1/search_match.go)
  `cutRanked` receives readable matches only), and `more` reports only a readable match the answer left out. A repository the
  caller does not read at all contributes no row to any statement, so a name added to it or removed from it changes neither the
  entries nor `more` (`TestR_API_061_ASuggestionNamesOnlyWhatTheCallerMayRead`). Within a repository read in part, the names it
  holds share the bound of the package statements before they are decided (H-985).
- `hideExistence: false` changes the status of a refused read, never a listing: a repository the caller cannot read is not
  suggested, whatever its setting.
- A repository's or a package's project is named by its key, which is part of the qualified name the caller reads; the display
  name appears only in a project entry the project rule admits (Q-API-277).
- The parameters are checked first (400), then the anonymous gate (401 while nothing is `public`), then `identities:read` for
  `kind` `user` or `group` (403); each answer is the same for every text, and the 403 comes before any lookup.

## What a request can make the database do

- One request per settled text, in the `read` class of its principal or the `anonymous` class of its client address (R-API-016,
  Q-API-281); `q` holds 2 to 100 characters.
- At most a fixed number of statements per request, whatever the number of matches: the projects, the users and the groups one
  statement each, and for packages the withheld query of each format the caller reads, a plan setting, statement A and, while A
  leaves room, statement B ([search.sql](../../internal/meta/postgres/queries/search.sql);
  `TestR_API_064_ASuggestionCostsAFixedNumberOfStatements` counts them with 10 and with 10,000 matches). Each package statement
  reads at most 200 rows, and each row one served-version check. The repositories are matched over the grant index in memory.
- No character of the text is an operator: the name prefix is two bound values (`low <= name < high`), not a pattern, and the
  tsquery is built from runs of `a` to `z` and `0` to `9` of the text only (`suggestWords`, `suggestTSQuery`).
- The package statements are planned for their text (`SET LOCAL plan_cache_mode = force_custom_plan` in their transaction): a
  generic plan of a prepared statement walked every package for a rare word.

## The admin area

The owner's rule (R-UI-099) is a rule of navigation: the field opens a route under `/admin` only for a suggestion marked **Admin
area**, shown to a holder of the system role `admin` and picked explicitly, through one gate function of the UI. It is not access
control: the admin area's screens are decided by the API as before, and the operation answers users and groups to every holder of
`identities:read`, whatever the UI shows.

## What the browser keeps

The frequently visited repositories and projects are kept in `localStorage` under `trove.frequent.<principal ID>` (R-UI-096):
qualified names, keys, visit counts and times, no secret (R-UI-043). A stored repository is shown only after the repository list has
confirmed that the caller still sees it (`GET /api/v1/repositories?name=…`, R-API-063), which answers a name that does not exist and
one the caller may not see alike.

## What this does not cover

### H-985 · The time of a suggestion, and which names fit its bound, depend on names the caller cannot read

Component: the package statements of R-API-064 ([search.sql](../../internal/meta/postgres/queries/search.sql)) and the in-process
match over the grant index ([search_api.go](../../internal/api/v1/search_api.go)).

Subject: a caller who times many suggestion requests, and a caller who reads a repository in part. The B-tree and GIN scans meet the
matching names of every repository before the filter on the repositories the caller reads, and the in-process match runs over every
repository of the index before the decision, so the time of an answer grows with matching names the caller cannot read; the answer
of a caller who reads its repositories whole does not. In a repository the caller reads in part, the 200 candidate rows of a
statement are cut before each is decided, so many unreadable names that sort before a readable one can keep it out of the answer
and out of `more`. Live since the suggestions are built; the same kind of gap as
[H-299](permissions.md#h-299--the-cost-of-a-package-list-for-a-caller-with-row-level-grants-depends-on-versions-the-caller-cannot-read).
What bounds it: the rate classes, the noise of the network against a difference of milliseconds, and the results page, whose
package list decides every candidate version. What makes it real: a very large repository the caller cannot read and a caller that
can measure many requests from close by, or a repository with thousands of names that sort before the one a caller with a
path-scoped grant looks for.

### H-986 · Search texts are in the request log with the principal

Component: the request log of R-OPS-010 in [operations-and-observability.md](../spec/operations-and-observability.md) (`path` with
its query string, `log.request.redact_query_params`).

Subject: every user of the field. Each settled text is one request, so the log holds a trail of what a person typed, with its
principal ID. Live since the suggestions are built. An operator can add `q` to `log.request.redact_query_params`, which redacts the
`q` of every list as well. What makes it real: a reader of the logs who should not learn what users search, such as the name of an
unreleased product.

### H-987 · The frequently visited names stay in the browser profile

Component: the store `trove.frequent.<principal ID>` of R-UI-096.

Subject: a user on a shared operating-system account or browser profile. Sign-out does not clear the store, so whoever can read the
profile's storage reads the qualified names of the repositories and the keys of the projects the user opened, and how often. Live
once the UI keeps the store; today's `trove.recent-repositories` holds the same kind of names under one key for every user of a
browser. What makes it real: a shared workstation.
