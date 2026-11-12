# UI audit of every screen (T-21)

The work list of the fix waves of the owner's task T-21, "A UI that looks right" ([promt.md](promt.md), section 10, owner task of
2026-11-12). Every screen was rendered with `make ui-renders` ([web-ui.md](../developer/web-ui.md#renders-of-every-screen)) on
2026-11-12, as the three kinds of user of [R-UI-067](../spec/api-and-ui.md#r-ui-067--three-kinds-of-user-the-plain-user-the-power-user-and-the-administrator)
(`plain` = `sam`, a reader; `power` = `priya`, Owner of `web-frontend`; `admin` = `ada`) and without a session (`anonymous`), light
and dark, at 1440, 1024 and 390 px, and judged by the development agent against the
[style guide](../developer/ui-style-guide.md) and the reference it describes.

The pictures are not in the repository: a path below is under `.out/ui-renders/` of a checkout and is made again with
`make ui-renders` (or `make ui-renders GREP=<screen>`). A path names the light picture at 1440 px unless it says otherwise; the
same screen's other pictures are beside it (`-dark-`, `-1024`, `-390`).

**State.** The repository list and the address fields were changed in the same work (Q-UI-300 to Q-UI-306) and are not in this
list; what they still lack is UI-A-29 and UI-A-30. Every other line is open. A line is removed when its fix is rendered and
looked at (rule 11 of the plan). The lines UI-A-31 to UI-A-40 belong to the planner of the creation flow on T-18's tree, not to
the fix waves of this list.

## Systemic causes

1. **No shared page header.** Each page builds its own: the primary action at the right of the header, or under the description on
   the left; views of the same thing as quiet text links beside the tabs; the object's name in a subtitle under a heading that names
   its type. Decided in [Q-UI-307](../spec/api-and-ui.md#q-ui-307--a-pages-own-actions-are-buttons-at-the-right-end-of-its-header-views-of-the-same-thing-are-tabs); no shared component holds it yet.
2. **Lists set their own columns, with no budget and no narrow layout.** The package list has nine columns, the repository page's
   package table, the project table, the browser's contents and the audit log each scroll sideways at 1440 px or cut their last
   column; at 390 px every list but the repository list runs off the right edge
   ([Q-UI-306](../spec/api-and-ui.md#q-ui-306--below-48-rem-of-its-width-the-repository-list-stacks-each-row-with-the-address-across-the-row)
   gives lists a stacked row to opt into).
3. **Every screen shows the administrator's data to every user.** No screen distinguishes the kinds of R-UI-067: the plain user
   gets read-only settings pages, capability names, maintenance facts and the administrator's warnings, and the start page
   offers no way to the common job.
4. **Two models for settings and facts.** One column with collapsible sections (project, general, user settings) beside tabbed
   editors with a key-value list above them (repository settings) and cards of key-value pairs (dashboard, system status).
5. **Shared pieces missing or bypassed.** Empty states come in three forms (a dashed box, a sentence, a table row) and one text
   ("No tokens match.") is used where nothing was filtered; tabs in three looks; a format spelled as its key, its display name
   or with a product name in brackets; the settings measure `56rem` written as a literal in five components.

## Work list

Ranked: the plain user's main path first, then consistency across screens, then the rest. "Rule" names what the line breaks.

### The plain user's main path

| ID | Screen | Kind | Picture | What is wrong | Rule |
|---|---|---|---|---|---|
| UI-A-01 | Start page `/` | plain | `plain/home-light-1440.png` | Only the project list: no search for a package or repository, no recently used repository with its address; the top bar has no search for the plain user (**Find a repository** needs the permission to read repository configuration) | R-UI-067, [Q-UI-311](../spec/api-and-ui.md#q-ui-311--the-start-page-leads-the-plain-user-to-a-repository-and-its-address-in-two-steps) |
| UI-A-02 | Repository page | all | `plain/repository-npm-local-light-1440.png` | The header shows no client address; the address is only in the tab **Set up client**. The reference shows the clone address in the page header | [Q-UI-313](../spec/api-and-ui.md#q-ui-313--the-repository-page-shows-the-address-and-the-setup-first-its-configuration-facts-are-details-and-a-reader-gets-no-settings-page), R-UI-066 |
| UI-A-03 | Repository page, packages | all | `plain/repository-npm-local-light-1440.png` | The packages table is cut at the right at 1440 px ("Downloa…"): its region scrolls sideways | No sideways scrolling from 1024 px (T-21) |
| UI-A-04 | Repository page, packages | all | `plain/repository-npm-local-dark-390.png` | At 390 px names and descriptions are cut at the right edge | Q-UI-306 (narrow layout) |
| UI-A-05 | Repository page | all | `plain/repository-npm-local-light-1440.png` | **Browse files** and **Vulnerabilities** are loose quiet links at the top right, beside the tabs | Q-UI-307 |
| UI-A-06 | Repository settings | plain | `plain/repository-settings-npm-local-light-1440.png` | The list's menu offers the reader **Settings**; it opens "You may use this repository but not read or change its configuration", the facts, and the whole guide of the class, several screens long | R-UI-067, Q-UI-313 |
| UI-A-07 | Repository settings, guides | all | `plain/repository-settings-npm-local-light-1440.png`, `plain/guides-repository-light-1440.png` | The contents list "On this page" runs its items together ("What a local repository is2. Point the client…") | Text: a numbered list is read as a list |
| UI-A-08 | Guides | plain | `plain/guides-repository-light-1440.png` | `/guides?repository=web-frontend/npm` opens Ansible Galaxy with the repositories of `platform`: the repository of the link is ignored (the client setup honours it) | R-UI-049, deep links of R-UI-003 |
| UI-A-09 | Package list | all | `plain/packages-light-1440.png` | Nine columns; the list scrolls 245 px sideways at 1440 px (updated, versions, repositories, downloads out of view); the description is cut at 24 characters with an ellipsis | [Q-UI-314](../spec/api-and-ui.md#q-ui-314--the-package-list-shows-name-format-latest-version-and-when-it-changed-counts-and-dates-of-creation-are-the-package-pages) |
| UI-A-10 | Package list | all | `plain/packages-light-390.png` | At 390 px only the type and the name are in view | Q-UI-306 |
| UI-A-11 | Package page | plain | `plain/package-npm-light-1440.png` | Five lines of facts (published, licences, downloads `0`, repositories as chips) between the name and the README; the version choice looks like a text field | [Q-UI-315](../spec/api-and-ui.md#q-ui-315--the-package-page-shows-the-install-command-first-the-facts-and-the-owners-are-details) |
| UI-A-12 | Package page | all | `plain/package-npm-light-390.png` | At 390 px the tab strip wraps into two rows | Controls: one tab strip |
| UI-A-13 | Client setup | plain | `plain/setup-repository-light-1440.png` | Opened for `web-frontend/npm`, the page still asks for the package type, the client and the repository in three numbered groups, and notes "You cannot publish" on every local repository to a reader | [Q-UI-316](../spec/api-and-ui.md#q-ui-316--the-client-setup-starts-from-where-the-user-came-and-its-advanced-choices-are-closed) |
| UI-A-14 | Start page, project list | all | `plain/home-light-1440.png`, `plain/home-light-390.png` | **Key** and **Display name** are two columns that repeat each other; at 390 px the role and the counts are out of view | Lists: the first column names the thing, with a second line |
| UI-A-15 | Project sidebar, members | plain | `plain/members-light-1440.png` | The reader's sidebar lists **Access › Members** and **Settings**; the members screen says "Read only: changing this needs the permission `projects:write`" | [Q-UI-317](../spec/api-and-ui.md#q-ui-317--the-plain-users-project-sidebar-holds-the-content-the-projects-members-and-settings-move-into-its-about-section), [Q-UI-318](../spec/api-and-ui.md#q-ui-318--the-plain-users-main-views-name-no-internal-terms-a-refusal-says-who-can-do-it) |
| UI-A-16 | Vulnerabilities of a project | plain | `plain/vulnerabilities-dark-1440.png` | Two warnings about the advisory imports (an administrator's task), eight counters of `0`, an empty chart and "0 internal names only, 4 waiting for advisory data, 2 not covered" | R-UI-067 (Details), Q-UI-318 |
| UI-A-17 | Empty repository | all | `plain/repository-empty-light-1440.png` | "This repository holds no files yet." in a dashed box, without the action that fills it (upload, set up the client); a fallback description "Holds what you publish here." | Lists: an empty list offers the action that fills it |
| UI-A-18 | Browse | all | `plain/browse-light-1440.png`, `plain/browse-folder-light-1440.png` | One screen, two names: "Browse" at its root, "File tree" in a folder; the tree repeats the project in every name (`web-frontend/assets`) inside the project | Page titles are nouns, one per screen |
| UI-A-19 | Browse, folder | all | `plain/browse-folder-light-1440.png` | File names in the contents table are underlined (tables underline on hover only); the column "Last…" is cut at the pane's right edge | Design language, links; no cut text |
| UI-A-20 | Personal tokens | plain | `plain/profile-tokens-light-1440.png` | "No tokens match." with no filter set; the profile's pages are pill links, not the tab strip; **Create token** sits under the description on the left | Lists (empty state), Controls (tabs), Q-UI-307 |
| UI-A-21 | Documentation | plain | `plain/help-light-1440.png` | The administrator's guide is shown to the plain user under the user guide | R-UI-067 |

### Consistency across screens

| ID | Screen | Kind | Picture | What is wrong | Rule |
|---|---|---|---|---|---|
| UI-A-22 | Page headers | all | `power/policies-light-1440.png`, `power/repositories-light-1440.png`, `power/repository-settings-npm-remote-light-1440.png`, `admin/admin-user-light-1440.png` | Four forms of page action (systemic cause 1); detail pages put the type in the `<h1>` and the object in a subtitle ("User" / "sam", "Endpoint" / "default", "Repository settings" / "web-frontend/npm-internal") | Anatomy of a page, Q-UI-307 |
| UI-A-23 | Every list but the repository list | all | `plain/packages-light-390.png`, `plain/home-light-390.png`, `admin/admin-users-light-390.png`, `admin/admin-audit-light-390.png` | At 390 px the table runs off the right edge | Q-UI-306 |
| UI-A-24 | Format names | all | `plain/repositories-light-1440.png`, `plain/repository-empty-light-1440.png`, `plain/packages-light-1440.png` | A format is `npm` (its key) in the repository list without its icon, "Generic" in the repository page's badge, "OCI (Docker)" in the package list | Lists: a format is its icon and its name |
| UI-A-25 | Empty states | all | `plain/profile-tokens-light-1440.png`, `power/policies-light-1440.png`, `admin/admin-bastions-dark-1440.png`, `admin/admin-endpoint-light-1440.png` | A dashed box, a box with a border, a bare sentence; "No tokens match." where nothing is filtered (also `admin/admin-tokens-light-1440.png`) | Lists: one empty state, with the action that fills it |
| UI-A-26 | Tabs | all | `plain/profile-tokens-light-1440.png`, `plain/package-npm-light-1440.png`, `power/repository-settings-npm-remote-light-1440.png` | Pill links, the tab strip of links and the tab group; the strip wraps at 390 px on one screen | Controls: tabs |
| UI-A-27 | Persons in lists | admin | `admin/admin-users-dark-1440.png`, `plain/members-light-1440.png`, `admin/admin-projects-light-1440.png` | A person is avatar, name and `@username` on the members screen, two text columns without avatar in the user list, and `user:priya` (a principal reference) as an Owner in the admin area's project list | Lists: the first column names the thing; Q-UI-318 |
| UI-A-28 | Settings | power, admin | `power/repository-settings-npm-remote-light-1440.png`, `power/project-settings-light-1440.png` | Repository settings are a tabbed editor below a key-value list; project, general and user settings are one column with sections | Settings pages; known in "What does not follow this page yet" |
| UI-A-29 | Names in other lists | all | `plain/packages-light-1440.png` | Names stay on one line in every list but the repository list (Q-UI-303); a long package name pushes the package list sideways | [Q-UI-303](../spec/api-and-ui.md#q-ui-303--the-repository-lists-key-breaks-after-a-hyphen-where-it-is-longer-than-its-column) |
| UI-A-30 | Admin area's repository list | admin | `admin/admin-repositories-dark-1440.png` (and at 1280 px with `UI_RENDERS_WIDTHS=1440,1280`) | With its project column the address field shows 47 characters at 1440 px and 28 at 1280 px: the host and little more | [Q-UI-300](../spec/api-and-ui.md#q-ui-300--in-a-list-every-address-field-has-one-width-the-lists-longest-address-up-to-what-the-other-columns-leave) (revisit) |

### Creating a repository (the owner's rules of 2026-11-12)

The wizard walked for OCI, npm and Ansible, local, remote and virtual, as the power user in `web-frontend` and as the administrator
in the admin area (`<kind>/[admin-]wizard-<format>-<class>-<step>-<scheme>-<width>.png`). Judged against
[CLAUDE.md](../../CLAUDE.md#product-simple-by-default-details-on-demand) and the style guide's
[Simple by default, details on demand](../developer/ui-style-guide.md#simple-by-default-details-on-demand). Not to be built in
the fix waves of this list: the creation flow and the repository settings are planned on T-18's tree (T-21, "Creating and
configuring a repository"); these lines are that planner's input.

| ID | Screen | Kind | Picture | What is wrong | Rule |
|---|---|---|---|---|---|
| UI-A-31 | Wizard, every format and class | power, admin | `power/wizard-oci-local-1-format-light-1440.png` | The title is **New repository** on every step, also after the format was chosen | A title names the thing (**New OCI repository**) |
| UI-A-32 | Wizard, access step | power, admin | `power/wizard-npm-local-4-access-and-npm-settings-light-1440.png`, `power/wizard-ansible-virtual-4-access-settings-light-1440.png` | The access step of the standard flow is include and exclude patterns, **Disabled**, **Require authentication** and **Hide existence**; members and the visibility level are not in it | Access is the members and the visibility level; the rest is a detail view |
| UI-A-33 | Wizard, remote | power, admin | `power/wizard-oci-remote-4-access-upstream-oci-docker-and-mirror-settings-dark-1440.png` | The step label names four topics: "Access, upstream, OCI (Docker) and mirror settings" (npm and Ansible the same with their names) | A step names one topic |
| UI-A-34 | Wizard, remote | power, admin | same | The upstream is a URL field with the well-known registry prefilled; Docker Hub, Quay, GitHub (`ghcr.io`) and GitLab are not one-click choices. **Test connection** sits at the end of the upstream section, after the cache periods | Well-known upstreams are one-click choices; the test where the upstream is entered |
| UI-A-35 | Wizard, remote | power, admin | same | **Metadata cache period in seconds** and **Negative cache period in seconds** are empty with "Default: 300" as a hint, and as wide as the upstream URL | A default is the field's value; a field fits its input |
| UI-A-36 | Wizard, basic settings | power, admin | `power/wizard-oci-remote-3-basic-settings-untouched-light-1440.png`, `admin/admin-wizard-oci-remote-3-basic-settings-untouched-light-1440.png` | **Administrator notes** are a field of the standard flow | Details on demand |
| UI-A-37 | Wizard, basic settings | power, admin | the owner's screenshot | The key field is red with "does not match the pattern" before anything was typed. The renders of the untouched step do not show it (the walk types the key without first leaving the field empty); the owner's screenshot does | An error only after the field was touched or on submit |
| UI-A-38 | Wizard, format settings | power, admin | `power/wizard-npm-local-4-access-and-npm-settings-light-1440.png`, `power/wizard-oci-remote-4-access-upstream-oci-docker-and-mirror-settings-dark-1440.png` | The format's settings are shown by their schema names (`latestGuard`, `audit`, `mode`, `mirror`, `tags`) with requirement and decision IDs in their help ("R-NPM-016", "R-OCI-026, Q-OCI-16"), defaults as "Default: on" beside the control, and a select "Not set" | Text: no internal terms; a default is the field's value; details on demand |
| UI-A-39 | Wizard, virtual | power, admin | `power/wizard-ansible-virtual-4-access-settings-light-1440.png`, `power/wizard-oci-virtual-5-review-light-1440.png` | A virtual repository is created without its members: no step asks for them, so it is created empty and its members are set in the settings afterwards | The standard flow asks what the repository needs to work |
| UI-A-40 | Wizard, review | power, admin | `power/wizard-oci-local-5-review-light-1440.png` | The review lists **Include patterns**, **Exclude patterns** and **Switches** as dashes | Details on demand |

### The rest

| ID | Screen | Kind | Picture | What is wrong | Rule |
|---|---|---|---|---|---|
| UI-A-41 | Service accounts of a project | power | `power/project-service-accounts-light-1440.png` | The Owner's screen calls `GET /api/v1/service-accounts?project=web-frontend`, which needs `identities:read`: 403 and "You do not have the permission for this." (the one failure of the renders; the audit log shows the denials) | R-UI-041 (an Owner manages the project's accounts); a product defect |
| UI-A-42 | New permission policy | admin | `admin/admin-permission-new-light-1440.png` | The empty name field is shown invalid before anything was typed, and the error is the raw pattern `^[a-z0-9][a-z0-9._-]{0,127}$`, repeated in the preview | Forms: an error appears after input and says what to do in words |
| UI-A-43 | Repository wizard | power | `power/repository-wizard-light-1440.png` | "Search by package type" stands beside its field without a gap, not above it | Forms: the label above the field |
| UI-A-44 | Dashboard | admin | `admin/admin-dashboard-light-1440.png` | One component card in a three-column grid leaves two thirds of the row empty | Layout: no near-empty regions |
| UI-A-45 | System status | admin | `admin/admin-system-light-1440.png` | One replica of role `all` is reported as "api live 0, packages live 0, worker live 0", with warnings that nothing serves; whether role `all` is counted is a question for the status, not only its look | R-UI-038 |
| UI-A-46 | Audit log | admin | `admin/admin-audit-light-1440.png` | Targets are cut with an ellipsis | Never cut a text that is read (R-UI-065's reason) |
| UI-A-47 | System roles | admin | `admin/admin-roles-light-1440.png` | Each role's capabilities are one run-on line of monospace names, several lines long | Text: a list is a list |
| UI-A-48 | Top bar | admin | `admin/admin-repositories-light-390.png` | At 390 px the search collapses to an empty pill beside the Admin button | Layout at 390 px |
| UI-A-49 | Sign-in in the UI development profile | all | (none: the dev server) | A sign-in through the provider returns to `/ui/en/`, which the dev server does not serve: "Page not found" with the session set. The login composes `return_to` from the locale, not from the base href | Development profile only; documented in web-ui.md |
