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
list. On 2026-11-12 the owner judged the admin area's repository list beside its filter panel ("a huge field for copying a path
that may not even be needed gets all the room, and every other important property squeezes itself somehow into the artifact's
row"): the waves had measured it by his earlier complaint, equal widths and the whole address, not by what the list is for. Since
[Q-UI-330](../spec/api-and-ui.md#q-ui-330--a-list-takes-its-columns-from-the-job-of-its-kind-of-user-the-important-properties-are-columns-and-the-address-takes-no-more-than-its-share) the repository lists and the search results take their columns from their user's job (systemic cause 6). Every other line
is open. A line is removed when its fix is rendered and
looked at (rule 11 of the plan). The lines UI-A-31 to UI-A-40 belong to the planner of the creation flow on T-18's tree, not to
the fix waves of this list. A line marked "→ T-22" belongs to the owner's task T-22 (one search with suggestions), one marked
"→ B4" to the builder who replaces the vulnerability displays, one marked "→ T-18's planner" to the plan of the creation flow and
the repository settings; none of them is fixed here.

## Systemic causes

1. **No shared page header.** Each page builds its own: the primary action at the right of the header, or under the description on
   the left; views of the same thing as quiet text links beside the tabs; the object's name in a subtitle under a heading that names
   its type. Decided in [Q-UI-307](../spec/api-and-ui.md#q-ui-307--a-pages-own-actions-are-buttons-at-the-right-end-of-its-header-views-of-the-same-thing-are-tabs); since fix wave 2 every screen but those of UI-A-22 has the shared header.
2. **Lists set their own columns, with no budget and no narrow layout.** The package list has nine columns, the repository page's
   package table, the project table, the browser's contents and the audit log each scroll sideways at 1440 px or cut their last
   column. Since fix wave 1 the data table holds the mechanism
   ([Q-UI-308](../spec/api-and-ui.md#q-ui-308--every-list-takes-its-layout-from-the-data-table-a-column-budget-a-least-width-for-its-name-one-alignment-stacked-rows)):
   every list stacks its rows below 48 rem and aligns its cells one way; the lists still have to declare their budgets.
3. **Every screen shows the administrator's data to every user.** No screen distinguishes the kinds of R-UI-067: the plain user
   gets read-only settings pages, capability names, maintenance facts and the administrator's warnings, and the start page
   offers no way to the common job.
4. **Two models for settings and facts.** One column with collapsible sections (project, general, user settings) beside tabbed
   editors with a key-value list above them (repository settings) and cards of key-value pairs (dashboard, system status).
5. **Shared pieces missing or bypassed.** Empty states come in three forms (a dashed box, a sentence, a table row) and one text
   ("No tokens match.") is used where nothing was filtered; tabs in three looks; a format spelled as its key, its display name
   or with a product name in brackets. Since fix wave 1 there is one empty-state component and one page header (Q-UI-307,
   Q-UI-309); since fix wave 2 the screens of this audit use them, the tabs are one strip, and a format is its icon and its
   display name.

6. **Lists were measured by a field, not by their user's job.** The repository lists gave the address field every pixel the
   other columns did not need and packed project, format and class under the key (Q-UI-300, Q-UI-325, Q-UI-328), because the
   renders were judged by widths and scrolling. Since [Q-UI-330](../spec/api-and-ui.md#q-ui-330--a-list-takes-its-columns-from-the-job-of-its-kind-of-user-the-important-properties-are-columns-and-the-address-takes-no-more-than-its-share) a list takes its columns from the job of its kind of user
   (R-UI-067): the properties its user decides on are columns, the name column gets the most room, nothing but the description
   stands under the name, and an address field takes what the properties leave, up to its whole length; a render is judged by
   that first. The admin area's
   repository list (repository, project, format, class, last activity, the address as a copy button), a project's list (repository,
   format, class, the address field) and the search results (the same and the project) follow it; the other lists are judged by
   it when they are next changed.

## Work list

Ranked: the plain user's main path first, then consistency across screens, then the rest. "Rule" names what the line breaks.

### The plain user's main path

| ID | Screen | Kind | Picture | What is wrong | Rule |
|---|---|---|---|---|---|
| UI-A-16 | Vulnerabilities of a project | plain | `plain/vulnerabilities-dark-1440.png` | → B4 (the vulnerability displays become a grade with a shield, a severity bar and a card). Two warnings about the advisory imports (an administrator's task), eight counters of `0`, an empty chart and "0 internal names only, 4 waiting for advisory data, 2 not covered" | R-UI-067 (Details), Q-UI-318 |
| UI-A-01 | Top bar | plain | `plain/home-light-1440.png` | → T-22. The top bar had no search for the plain user: **Find a repository** needed the permission to read repository configuration. Since Q-UI-390 it shows for every signed-in user and Enter opens the results page of the main area; its suggestions are T-22. The start page finds packages since fix wave 1 and stays as it is | R-UI-067, [Q-UI-311](../spec/api-and-ui.md#q-ui-311--the-start-page-leads-the-plain-user-to-a-repository-and-its-address-in-two-steps) |

### Consistency across screens

| ID | Screen | Kind | Picture | What is wrong | Rule |
|---|---|---|---|---|---|
| UI-A-22 | Page headers | all | `power/repository-settings-npm-remote-light-1440.png`, `plain/vulnerabilities-light-1440.png` | → B4 and T-18's planner. Every other screen has the shared header since fix wave 2; the repository settings and the wizard (T-18's tree) and the vulnerability pages (B4) still have the old one, with quiet links in the header | Anatomy of a page, Q-UI-307 |
| UI-A-28 | Settings | power, admin | `power/repository-settings-npm-remote-light-1440.png`, `power/project-settings-light-1440.png` | → T-18's planner (the repository settings, UI-A-31 to UI-A-40). Repository settings are a tabbed editor below a key-value list; project, general and user settings are one column with sections | Settings pages; known in "What does not follow this page yet" |

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
| UI-A-37 | Wizard, basic settings | power, admin | the owner's screenshot | The key field is red with "does not match the pattern" before anything was typed. The renders of the untouched step do not show it (the walk types the key without first leaving the field empty); the owner's screenshot does. The wizard's key field is an input of its own: the shared text field, which since fix wave 3 shows an error only after a touch or a submit (UI-A-42), is not used there | An error only after the field was touched or on submit |
| UI-A-38 | Wizard, format settings | power, admin | `power/wizard-npm-local-4-access-and-npm-settings-light-1440.png`, `power/wizard-oci-remote-4-access-upstream-oci-docker-and-mirror-settings-dark-1440.png` | The format's settings are shown by their schema names (`latestGuard`, `audit`, `mode`, `mirror`, `tags`) with requirement and decision IDs in their help ("R-NPM-016", "R-OCI-026, Q-OCI-16"), defaults as "Default: on" beside the control, and a select "Not set" | Text: no internal terms; a default is the field's value; details on demand |
| UI-A-39 | Wizard, virtual | power, admin | `power/wizard-ansible-virtual-4-access-settings-light-1440.png`, `power/wizard-oci-virtual-5-review-light-1440.png` | A virtual repository is created without its members: no step asks for them, so it is created empty and its members are set in the settings afterwards | The standard flow asks what the repository needs to work |
| UI-A-40 | Wizard, review | power, admin | `power/wizard-oci-local-5-review-light-1440.png` | The review lists **Include patterns**, **Exclude patterns** and **Switches** as dashes | Details on demand |

### The rest

| ID | Screen | Kind | Picture | What is wrong | Rule |
|---|---|---|---|---|---|
| UI-A-43 | Repository wizard | power | `power/repository-wizard-light-1440.png` | "Search by package type" stands beside its field without a gap, not above it | Forms: the label above the field |
