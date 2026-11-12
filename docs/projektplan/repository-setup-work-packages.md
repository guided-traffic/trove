# T-21b · Creating and configuring a repository, simple by default — the work-package cut

The plan of the part "Creating and configuring a repository, simple by default" of the owner's task T-21
([promt.md](promt.md#owner-task-of-2026-11-12)), written by the planning agent on 2026-11-12 against commit 5bcad1bc and against
T-18's tree (1f3a5474 and a46de4c7, not on main yet; T-18's plan is [visibility-work-packages.md](visibility-work-packages.md)).
**This plan assumes T-18 has landed**: `requireAuthentication` and the anonymous switch are gone (ADR-192), and the artifacts'
visibility level is in the wizard and in the repository's Access tab (R-UI-055).

The rules are those of [CLAUDE.md](../../CLAUDE.md#product-simple-by-default-details-on-demand) and rule 11 of
[promt.md](promt.md) (renders). The reference is the owner's GitLab ([ui-style-guide.md](../developer/ui-style-guide.md)): its
**New project** is a page of tiles, each opening one form on one page, with no review step.

IDs used: **R-REPO-062 to R-REPO-064** (065 to 069 unused), **Q-REPO-320 to Q-REPO-327**, **R-UI-080 to R-UI-088** (089 unused),
**Q-UI-340 to Q-UI-353**, **H-970**, **H-971**. Changed: R-UI-021, R-UI-022, R-UI-049, R-UI-055, R-REPO-003, R-REPO-015, R-REPO-024,
R-REPO-052, R-OCI-001, R-OCI-021, the registration rows of npm, NuGet, Debian and Ansible; revisited decisions Q-UI-114, Q-UI-115,
Q-UI-116, Q-UI-117, Q-UI-122, Q-UI-222 (its open point), Q-REPO-91, Q-REPO-280.

Seams with parallel work:

- **B3** writes the three kinds of user (R-UI-067), a style-guide section on the rules of CLAUDE.md and `make ui-renders`. This plan
  does not write those files; it uses "plain user", "power user" and "administrator" as CLAUDE.md states them. The field widths of
  section 2.1 (R-UI-085) go into the style guide's **Forms** section: if B3's section has landed with names or values of its own, the
  builder takes those and changes R-UI-085 to match, and does not keep two sets.
- **T-18** owns the level control (`shared/ui/visibility-level.ts`) and the Visibility block (`repository-visibility.ts`); this plan
  places them, it does not change them.

## 0 · In one paragraph

A new repository starts on a page of format tiles and continues on one form titled after the format ("New OCI (Docker) repository").
The form asks only what the repository needs to work: the class, the key (suggested), an optional description and the visibility;
a remote adds its upstream, with one button per well-known registry of the format and **Test connection** right there; a virtual
repository adds its members and its deployment target. Every other setting is in a closed **Advanced settings** section, and the
details of the upstream fold inside the upstream group. Every field shows its default as its value, is as wide as what it holds,
and shows an error only after it was left or the form was sent. The repository's settings page becomes one column of sections: the
same standard settings open at the top, the details closed below, each section with its own **Save changes**. The server serves the
presets (a fact of each format), the defaults of a new repository (one operation), and an upstream test that runs a registry's
login handshake and names a token service the repository may not reach.

## 1 · What is wrong today

Verified in this repository at 5bcad1bc, with T-18's changes read from a46de4c7. Marks: **standard** (the repository needs it to
work, or the plain user really decides it), **details** (sensible, for experts), **gone** (T-18 removes it, or it is redundant),
**missing** (standard, and not in today's flow).

### 1.1 The flow and the defects the owner saw

Today's wizard ([repository-wizard.ts](../../web/src/app/features/repositories/repository-wizard.ts),
[.html](../../web/src/app/features/repositories/repository-wizard.html)) has five steps: Format (a tile listbox and Next), Class,
Basic settings, a class-specific step, Review.

| # | What is wrong | Cause in the code |
|---|---|---|
| D1 | The title is "New repository" after the format was chosen | `repository-wizard.html:2`; the route titles in `repositories.routes.ts:17` and `:34` |
| D2 | A step label lists up to four topics ("Access, upstream, OCI (Docker) and mirror settings") | `specificStepLabel()`, `repository-wizard.ts:211-226` |
| D3 | The key is red with "The key does not match the pattern." before anything was typed | `showErrors` is set by the first **Next** on the basic step (`repository-wizard.ts:468`), **Next** is enabled there whatever the key (`.html:335`), and nothing resets it; `aria-invalid` and the message follow it (`.html:153-156`, `:182-187`). Not verified: which action produced the owner's screenshot |
| D4 | Administrator notes are in the basic step | `repository-fields.ts:37-56` (section `basic`) |
| D5 | The access step of a local repository is patterns, Disabled, Require authentication and Hide existence | `repository-fields.ts:59-180`; `repository-wizard.html:201-206` |
| D6 | A default is a hint beside an empty field ("Default: 3600") | the cache periods: `upstream-settings.ts:204-246` and `:381-392`; the mirror's numbers: `mirror-settings.ts:206-227` and `:351-378`; a format's text and number settings: `format-settings.ts:374-377` (`text()` is empty for an unset member) and the "Default: …" lines `:200-212` |
| D7 | Every text field is 36 rem wide, a period of seconds like a URL | `.form-grid` is one column `minmax(0, 36rem)` and its fields stretch (`web/src/styles/_catalog-screens.scss:51-61`) |
| D8 | A format has one well-known upstream, offered only as "Use the well-known upstream" after the URL was changed | `upstream-settings.ts:106-110`; one `defaultUpstreamUrl` per format (`internal/api/v1/repositories_api.go:374`) |
| D9 | **Test connection** sits after the cache periods and the offline switch, in the step with four topics | `upstream-settings.ts:269-316` |
| D10 | A virtual repository is created without members; they are added afterwards on the Members tab | no member control in the wizard; [manual](../manual/admin/repositories.md) line 45 says so |
| D11 | The review step repeats the form and lists "Switches" | `repository-wizard.html:237-320` |
| D12 | **Test connection** of an OCI remote cannot judge a credential, and a registry whose token service is on another host passes the test and fails at the first pull (`foreign_host_denied`), with no field for the host in the UI | Q-REPO-280 (`authenticated: null` for a Bearer challenge); R-OCI-021; R-UI-021 leaves `extraHosts` to the API |
| D13 | (T-18) The wizard presets the visibility of a project Owner without `settings:read` to private, where a create without `visibility` would store the installation's level | a46de4c7 `repository-wizard.ts`: the `defaultLevel` loader falls back to `private`; `getVisibilitySettings` needs `settings:read`, and the body always carries `visibility` |

### 1.2 The fields of every class and format

Formats and classes, from the registrations: generic is local only (`internal/formats/generic/generic.go:41`); OCI, npm, NuGet,
Debian and Ansible have all three classes; all five remote formats can mirror (each has a `mirror.go`; the UI reads `mirror` of
`GET /api/v1/formats`).

| Field | Today | Classes | Mark | Target |
|---|---|---|---|---|
| Project (admin area only) | basic step, `.html:129-142` | all | standard | top of the form, admin area only |
| Class | step 2 | all | standard | a radio group in the form |
| Key | basic step, `.html:145-197` | all | standard | the form, starting with a suggestion |
| Description | `repository-fields.ts:16-36` | all | standard (optional) | the form |
| Visibility of the artifacts | basic step (T-18) | all | standard | the form |
| Administrator notes | `repository-fields.ts:37-56` | all | details | Advanced settings; a closed section |
| Include and exclude patterns, pattern syntax | `repository-fields.ts:59-124` | all | details | Advanced settings, **Paths**; section **Access details** |
| Disabled | `repository-fields.ts:125-135` | all | details, not at creation | settings page, **Advanced** |
| Require authentication | `repository-fields.ts:136-146` | all | gone (T-18, ADR-192) | — |
| Hide existence | `repository-fields.ts:147-179` | all | details | Advanced settings, **Access details**; section **Access details** |
| Review step, "Switches" | `.html:237-320` | all | gone | **Show as API request** stays as a disclosure |
| Upstream URL | `upstream-settings.ts:84-114` | remote | standard | the form's **Upstream** group, with the preset buttons |
| "Use the well-known upstream" | `upstream-settings.ts:106-110` | remote | gone | replaced by the preset buttons |
| Authentication, username, password | `upstream-settings.ts:116-202` | remote | standard | the **Upstream** group |
| Remove the stored password | `upstream-settings.ts:187-197` | remote | standard, settings only | as today |
| Test connection | `upstream-settings.ts:269-316` | remote | standard | under the credentials |
| Metadata and negative cache period | `upstream-settings.ts:204-246` | remote | details | **Upstream details**, inside the group |
| Offline | `upstream-settings.ts:248-266` | remote | details, not at creation | **Upstream details** on the settings page |
| Extra hosts (`remote.extraHosts`) | none: "set through the API" (R-UI-021) | remote | details (new field) | **Upstream details**, **Further hosts** |
| Mirror mode and members | `mirror-settings.ts:111-274` | remote of a format with `mirror` | details | Advanced settings, **Mirror**; closed section **Mirror** |
| Members, deployment target | the settings page's Members tab only (`virtual-members.ts`) | virtual | **missing** at creation | the form's **Members** group |

### 1.3 The format settings (all details)

Rendered from each format's `settings.schema.json` under `internal/formats/<format>/`; every member is **details**. Defaults as the
schemas declare them.

| Format | Local | Remote | Virtual |
|---|---|---|---|
| generic | no schema | no remote class | no virtual class |
| oci | none | `mirror.tags` (`latest`), `mirror.excludeTags` (none), `mirror.platforms` (none: every platform) | none |
| npm | `latestGuard` (on); `audit.*` | `serveRemovedVersions` (off); `audit.*` | `latestStrategy` (`highest-member-latest`), `distTagStrategy` (`member-order`); `audit.*` |
| npm `audit` (every class) | `mode` (no static default: derived), `providerURL` (null: the upstream), `timeout` (`30s`), `maxBodySize` (`1MiB`) | | |
| nuget | `deleteMode` (`unlist`); `vulnerabilityInfo` (derived) | `forwardSearch` (on); `vulnerabilityInfo` | `vulnerabilityInfo` |
| deb | `architectures` (`amd64`, `arm64`), `compression` (none), `origin` (`trove`), `label` (derived: the key) | `upstreamKeys` (none), `mirror.suites` (none), `.components` (`main`), `.architectures` (`amd64`), `.translations` (`en`), `.sources` (off) | as local, plus `suites` (none), `components` (`main`) |
| ansible | none | `authUrl` (none), `clientId` (`cloud-services`), `fetchDocumentation` (on) | none |

`deb.upstreamKeys` stays a detail although a Debian remote without keys cannot be a mirror or a member of a virtual repository
(R-DEB-020): a pull-through remote works without them, and the server's warning `deb_upstream_unverified` says so (Q-REPO-326).

### 1.4 The settings page today

[repository-detail.html](../../web/src/app/features/repositories/repository-detail.html): a facts list (class, project, format,
layout, address, three dates, `:65-100`), then a tab group (`:112-262`) with one **Save changes**, **Discard changes** and
**Delete** for the whole document (`:264-291`) and a section **Move to another project** (`:292-309`). The style guide lists it as
"Not moved yet" ([ui-style-guide.md](../developer/ui-style-guide.md#what-does-not-follow-this-page-yet)).

| Tab or part | Holds | Target (R-UI-087) |
|---|---|---|
| Facts above the tabs | class, project, format, layout, client address, created, changed, last activity | closed section **Details** |
| General | description, administrator notes | description at the top; notes in a closed section |
| Members (virtual) | the member editor with its own save | open section **Members** |
| Access | T-18's Visibility block; patterns, Disabled, Hide existence | open section **Visibility**; closed **Access details**; Disabled to **Advanced** |
| Upstream (remote) | the upstream settings | open section **Upstream** with its nested details |
| <Format> settings | the schema form, Debian's upstream key fingerprints | closed section; a format's `mirror` member moves to **Mirror** |
| Mirror | status and settings | closed section **Mirror** |
| Import tasks, Tombstones (Ansible); Signing keys, Index (Debian) | operations panels | closed sections of the same names |
| How to use | the guide (R-UI-049) | closed section **How to use** |
| API | stored document, the change as a request | closed section **API** |
| Save, Discard, Delete; Move | one save for every tab | a save per section; **Advanced** holds Disable, Move and Delete |

## 2 · The target, as spec text ready to paste

The builder of WP0 (section 3) pastes 2.1 to 2.5 into the spec pages named, before any code (hard rule 4).

### 2.1 [api-and-ui.md](../spec/api-and-ui.md), Part C, after R-UI-067

```markdown
### R-UI-080 · A new repository: the format first, then one form that names it
Priority: **MVP** · Covers: UI-031

- **The format chooser.** `/projects/{project}/repositories/new` and `/admin/repositories/new` are a page titled **New
  repository**, with the sentence "Choose the package format. It decides what the repository can do." and the tiles of the
  installed formats from `GET /api/v1/formats` (the format's icon, Q-UI-225, and its display name) under the field **Search by
  package type** (Q-UI-115). A tile is a link to the form of its format; Enter in the search field opens the one format that is
  left. There is no Next button ([Q-UI-340](#q-ui-340--a-new-repository-is-a-format-chooser-and-one-form-as-the-references-new-project)).
- **The form.** `/projects/{project}/repositories/new/{format}` and `/admin/repositories/new/{format}` are one page titled
  **New <display name> repository** ("New OCI (Docker) repository", "New npm repository", Q-UI-353) with a link **Change format**
  back to the chooser. In this order: the project (admin area only; inside a project it is given and shown as text), **Class**,
  **Key**, **Description (optional)**, **Visibility**, the class's own group (**Upstream** for remote, **Members** for virtual),
  the closed section **Advanced settings** (R-UI-083), the disclosure **Show as API request**, then **Create repository** and
  **Cancel**. One column of at most 56 rem.
- **Class** is a radio group of the three classes, each with its sentence; a class the format does not implement is disabled with
  its reason (Q-UI-113). Preset: local. Another class keeps the project, an edited key, the description and the visibility, and
  starts its own group and the Advanced settings from the defaults of that class (R-REPO-063, [Q-UI-341](#q-ui-341--the-class-is-a-radio-group-of-the-form-preset-to-local)).
- **Change format** returns to the chooser; what was typed is not carried over, and the unsaved guard asks first when something was.
- An unknown format in the address shows the chooser with the notice "This installation has no format <name>."
- **Create repository** sends the document with the `Idempotency-Key` generated when the form opened (R-UI-042) and opens the new
  repository's content page (R-UI-052). **Show as API request** shows the body (a typed password as `<the password>`, R-UI-043)
  and the same request as `curl`. A form that asks for a recent sign-in keeps its draft without secrets, as before.

Acceptance criteria:

- **Create repository** on the repository list opens the chooser; typing `nu` leaves the NuGet tile; Enter opens a page whose
  `<h1>` is "New NuGet repository", with no step list and no Next button.
- With a test format whose classes lack `virtual`, the class Virtual is disabled with "<Format> does not implement the class
  Virtual." and Local is chosen.
- Double-clicking **Create repository** creates one repository.

### R-UI-081 · The standard fields of each class
Priority: **MVP** · Covers: UI-031, UI-032

- **Every class:** **Key** (R-UI-086; it starts with the `suggestedKey` of R-REPO-063), **Description (optional)**, **Visibility**
  (the artifacts group of R-UI-055, preset to the `visibility` of R-REPO-063; the wiki follows the artifacts at create).
- **Remote:** the group **Upstream** of R-UI-082.
- **Virtual:** the group **Members**: the picker of R-UI-022 over the local and remote repositories of the project and the format,
  the ordered members with Add, Remove, Move up, Move down and Move to top, and **Deployment target**, a select of **None** and the
  local members, set to the first local member when the first one is added (Q-UI-347).
- Nothing else is in the main form: no administrator notes, no patterns, no switches, no format settings ([Q-UI-343](#q-ui-343--the-standard-flow-holds-what-a-repository-needs-to-work-and-no-format-setting)).

Acceptance criteria:

- Pressing **Create repository** on the form of a local generic repository without typing anything creates the repository of
  the suggested key, and its document holds the defaults of R-REPO-004.
- A virtual npm repository created with `npm-local` and `npm-remote` as members reads back `virtual.members` in that order and
  `virtual.deployTarget: "npm-local"` without the deployment target being touched.

### R-UI-082 · Well-known upstreams and Test connection where the upstream is entered
Priority: **MVP** · Covers: UI-031, RMT-004

- Above **Upstream URL**, the group **Well-known registries** has one button per entry of the format's `upstreamPresets`
  (R-REPO-062), in the format's order, labelled with its display name (`translate="no"`). A button sets the URL to its preset's.
  The button whose URL equals the field, compared as the server normalizes it, is pressed (`aria-pressed="true"`); any other URL
  presses none. A format without presets has no group, and its URL field starts empty and is required ([Q-UI-345](#q-ui-345--the-presets-are-buttons-with-names-and-the-pressed-one-follows-the-url)).
- The URL field starts with the default preset's URL. **Authentication** (None, Username and password, Bearer token), **Username**
  and **Password or token** follow (write-only, Q-UI-123). Under the password one muted line names where the credential goes: the
  URL's host and every further host that receives it ("The credential is sent to registry.gitlab.com and gitlab.com.").
- **Test connection** follows the credentials. It sends the unsaved section (`testUpstream`, R-REPO-024, R-REPO-064) and shows the
  verdict in words (Q-UI-124); a result goes when a field of the section changes. When the answer is `foreign_host_denied` with a
  `tokenService`, the result names that host and offers **Allow <host>**, which adds it to the further hosts (receiving the credential
  when Authentication is not None) and runs the test again ([Q-UI-346](#q-ui-346--test-connection-names-a-token-service-that-is-not-allowed-and-offers-to-allow-it)).
- Inside the group, the region **Upstream details** (a toggle with `aria-expanded`, closed by default) holds **Further hosts** (one
  row per host pattern with a checkbox **Receives the credential**, **Add host**, **Remove**; it starts with the preset's hosts and
  follows the preset until it is edited), **Metadata cache period in seconds** and **Negative cache period in seconds**, and, on the
  settings page only, **Offline**.
- A create writes what Q-UI-122 says, and `extraHosts` only when the list differs from the hosts the server stores for that URL
  without it (R-REPO-062). The settings page writes the section as Q-UI-122 says, with `extraHosts` always present.

Acceptance criteria:

- **New OCI (Docker) repository**, class Remote: the buttons Docker Hub, Quay, GitHub (ghcr.io) and GitLab; Docker Hub is pressed and
  the URL is `https://registry-1.docker.io/`. Pressing GitLab sets `https://registry.gitlab.com/` and shows `gitlab.com` under
  Further hosts as receiving the credential; the create sends `remote: {url: "https://registry.gitlab.com/"}`, and the stored
  document holds `extraHosts: [{pattern: "gitlab.com", sendCredentials: true}]`.
- Against a test registry whose token service is on a host the repository does not list, **Test connection** names that host and
  offers **Allow**; after it, the test runs again and the token service accepts the typed credential.
- **New npm repository**, class Remote: one button, pressed; the cache periods read 7200 and 1800; a create that changes nothing
  sends no `remote` member.

### R-UI-083 · Details on demand
Priority: **MVP** · Covers: UI-031

- The form's section **Advanced settings** (`<trove-settings-section>`, closed) has the sentence "Notes, paths and the settings of
  the format. The defaults suit most repositories." and these groups, each under its own heading, where they apply:
  **Administrator notes**; **Paths** (include and exclude patterns, the pattern syntax); **Access details** (Hide existence);
  **Mirror** (a remote of a format with `mirror`: the mode, and in mode mirror the members of `remote.mirror` and the format's own
  `mirror` member, such as `oci.mirror` or `deb.mirror`); **<Display name> settings** (the format's other schema members for the class;
  absent when there are none) ([Q-UI-344](#q-ui-344--where-the-details-go)).
- **Disabled** and **Offline** are not offered at creation; the settings page has them.
- The details of the upstream fold inside its group (R-UI-082), not in Advanced settings.
- A field error inside a closed region opens the region: on **Create repository** and from the link of the error summary.

Acceptance criteria:

- The form of a local npm repository shows **Advanced settings** closed; opened, it shows Administrator notes, Paths, Access details
  and npm settings with **latestGuard**, and no Disabled.
- An OCI remote in mode mirror shows the tags and the platforms of `oci.mirror` in the Mirror group, and no group "OCI (Docker)
  settings".
- A create refused with a field error at `/includePatterns/0` opens Advanced settings and puts the error at the include patterns.

### R-UI-084 · A default is the value of its field
Priority: **MVP** · Covers: UI-031

- A field whose setting has a static default shows the default as its value, editable: the key (`suggestedKey`), the class
  (local), the visibility, the URL (the default preset), the further hosts (the preset's), the cache periods, the members of
  `remote.mirror`, the deployment target (the first local member) and every member of the format's schema with a `default`,
  text and number fields included. The values come from `getRepositoryDefaults` (R-REPO-063) for the document's members and from
  `settingsSchema` for the format's member ([Q-UI-348](#q-ui-348--a-default-is-the-value-of-its-field)).
- A value equal to its default is not written (Q-UI-80). A field the user empties shows its default again when it is left.
- A setting whose default is derived from other settings has no static value: a choice offers **Automatic** first (npm
  `audit.mode`, NuGet `vulnerabilityInfo`), a text stays empty with its description (Debian `label`, which is the key while unset).
- No "Default: …" hint line remains.

Acceptance criteria:

- An OCI remote shows 300 in both cache periods; in mode mirror, 3600 as the pass interval and 4 as the concurrent fetches; a
  local Debian repository shows the architectures `amd64` and `arm64` and the origin `trove`. A create that changes none of them
  writes none of them.

### R-UI-085 · A field is as wide as its expected input
Priority: **MVP** · Covers: UI-031

- Five widths, as tokens in `_tokens.scss` and classes in `_controls.scss`, and the input `width` of `<trove-text-field>`
  ([Q-UI-349](#q-ui-349--five-field-widths)):

  | Class | Token | Width | For |
  |---|---|---|---|
  | `field-w-number` | `--trove-field-width-number` | 6 rem | a count of up to six digits |
  | `field-w-duration` | `--trove-field-width-duration` | 8 rem | a period in seconds, up to eight digits |
  | `field-w-bytes` | `--trove-field-width-bytes` | 13 rem | a byte count, up to nineteen digits |
  | `field-w-name` | `--trove-field-width-name` | 28 rem, half the column | a key, a name, a host, a select |
  | `field-w-full` | — | the column | a URL, a description, a list, a key block |

- A field is never wider than its column; a narrower column shrinks it and its text scrolls inside it.
- Which field gets which: key, username, password, project, authentication, hide existence, discovery, deployment target, a host
  of Further hosts: `name`; upstream URL, description, administrator notes, patterns: `full`; the two cache periods, the pass and
  full-pass intervals: `duration`; concurrent fetches, versions per package: `number`; storage limit: `bytes`. A schema member:
  `integer` and `number` → `number`; a string with `format: uri` or `maxLength` above 128, and every list → `full`; any other
  string and every choice → `name`.

Acceptance criteria:

- At the wide width of `make ui-renders`, the metadata cache period field is 8 rem wide and the URL field spans the column; at the
  narrow width no part of the form scrolls sideways.

### R-UI-086 · An error shows when it helps
Priority: **MVP** · Covers: UI-031

- A field's own error (its shape, a required value) appears only after the field was left with a value, or after **Create
  repository** or a section's **Save changes** was pressed; never on a field that was not touched, empty or prefilled
  ([Q-UI-350](#q-ui-350--an-error-appears-after-the-field-was-left-or-the-form-was-sent)).
- **Create repository** is enabled while nothing is being sent. Pressed with errors, it sends nothing, shows the error summary
  with a link per field and moves the focus to the summary.
- The key's availability is asked 400 ms after the last keystroke; "A repository with this key already exists in the project." is
  shown when the answer comes, because it answers what was typed. The suggested key is free when the form opens (R-REPO-063).
- An error says what to do: "Use 2 to 64 lower-case letters and digits, with ., _, __ or - between them." Server field errors
  appear at their fields whatever was touched.

Acceptance criteria:

- The opened form shows no error. Typing `A` into the key and leaving it shows the message; clearing the key and pressing **Create
  repository** shows the summary and sends no request.

### R-UI-087 · The repository's settings page
Priority: **MVP** · Covers: UI-031

The settings page follows the style guide's settings pages: one column of at most 56 rem, no tab group
([Q-UI-351](#q-ui-351--the-repository-settings-page-is-one-column-of-sections)). In this order, where they apply:

1. At the top, without a title: **Key** (a read-only field) and **Description (optional)**, with **Save changes**.
2. **Visibility**, open: the block of R-UI-055 with its own save.
3. **Upstream** (remote), open: R-UI-082 with its **Upstream details** region, which holds **Offline** here.
4. **Members** (virtual), open: the editor of R-UI-022 with its own save.
5. Closed: **Mirror** (status above the settings, R-UI-021, and the format's `mirror` member), **<Display name> settings**, **Access
   details** (include and exclude patterns, Hide existence), **Administrator notes**, **Signing keys** and **Indexes** (Debian local
   and virtual), **Import tasks** and **Tombstones** (Ansible), **How to use** (R-UI-049), **Details** (class, format, layout, project,
   client address, created, last changed, last activity), **API** (the stored document; each section's unsaved change as a request),
   **Advanced** ([Q-UI-352](#q-ui-352--disable-move-and-delete-are-in-advanced)): **Disable repository** or **Enable repository**,
   **Move to another project**, **Delete repository**.

- Each section's **Save changes** sends only its members as a merge patch with `If-Match`; a `412` opens the conflict dialog of
  R-UI-042 for that section; the other sections keep their unsaved edits.
- A closed section that holds an unsaved change says "Unsaved changes" after its title; leaving the page with one asks first.
- A caller who may not change the repository sees the fields read-only and no save; the reduced view of a caller without
  `repositories:read-config` is unchanged.

Acceptance criteria:

- The settings of a remote OCI repository show Key, Description, Visibility and Upstream open, every other section closed, and no
  tab. Saving a changed description sends `{"description": …}` and nothing else.
- With an unsaved include pattern in the closed Access details, the section says "Unsaved changes", and leaving the page asks.

### R-UI-088 · A title and a heading name one thing
Priority: **MVP** · Covers: UI-031

- The form's title names the format (R-UI-080). Every section and group heading names one topic; no heading lists several.
- A label is the setting in sentence case, its unit included ("Metadata cache period in seconds"); a help text is one line. The
  key's help: "Part of the repository's address. It cannot be changed later."

Acceptance criteria:

- No heading of the form or of the settings page holds "and" between two topics.
```

**Changes to existing UI requirements** (the builder of WP0 makes them):

- **R-UI-021**: the bullet "Steps: format, class, basic settings, class-specific settings, review" and the format-step and
  class-step bullets point to R-UI-080 and R-UI-081; "The basic settings hold the artifacts' visibility level" reads "The form holds
  …"; the **Remote** bullet points to R-UI-082 and drops "the rest of the `remote` section … is set through the API" for
  `extraHosts`; the **Mirror** bullet places the section in **Advanced settings** and in the closed section **Mirror**; "Review:
  summary plus Show as API request" becomes "**Show as API request** at the end of the form"; "Edit form: the same sections as
  tabs" points to R-UI-087. Acceptance criteria: "empty periods (their defaults in the hints)" becomes "the periods with their
  defaults as values"; "The format step lists the installed formats as tiles … chosen with the keyboard alone" reads "… reached and
  opened with the keyboard alone".
- **R-UI-022**: the picker is also the **Members** group of the creation form (R-UI-081); the editor is the section **Members**.
- **R-UI-049**: the guide is the closed section **How to use** of the settings page.
- **R-UI-055**: the "Access tab" is the section **Visibility**; the form's preset is the `visibility` of R-REPO-063.

### 2.2 [repositories-and-proxy.md](../spec/repositories-and-proxy.md), after R-REPO-024 (062, 064) and after R-REPO-009 (063)

```markdown
### R-REPO-062 · Upstream presets of a format
Priority: **MVP** · Covers: RMT-004

- A format with the remote class registers its **upstream presets** in the catalog (`catalog.Facts.UpstreamPresets`): the
  well-known public registries of its ecosystem, each with a stable `id`, a `displayName` (a product name, not translated), its
  `url` (written as R-REPO-024 normalizes it) and the `extraHosts` a repository of that upstream needs (`pattern`,
  `sendCredentials`). Exactly one is the `default`: it is the format's default upstream of R-REPO-024, which an omitted `url` takes
  ([Q-REPO-320](#q-repo-320--the-presets-are-a-fact-of-the-format-and-the-api-serves-them)).
- Registration refuses presets on a format without the remote class, no default or two, two presets with the same `id` or `url`,
  a `url` that is not in its normalized form, and a `sendCredentials` host that is not among the preset's `extraHosts` patterns.
- `GET /api/v1/formats` serves them as `upstreamPresets`, in the format's order; the members `defaultUpstreamUrl`,
  `defaultExtraHosts` and `defaultCredentialHosts` are gone (Q-REPO-327).
- **Extra hosts.** A create, or a replace, whose `url` equals a preset's (normalized) and that omits `extraHosts` stores that
  preset's hosts; any other URL stores none ([Q-REPO-322](#q-repo-322--a-presets-hosts-come-with-its-url)). A merge patch that
  omits `extraHosts` leaves them as stored.
- **Mirror bound.** Every preset's URL is a well-known upstream for R-REPO-052 ([Q-REPO-323](#q-repo-323--every-preset-is-a-well-known-upstream-for-the-mirror-bound)).

| Format | `id` | Display name | `url` | `extraHosts` stored with it | Default |
|---|---|---|---|---|---|
| `oci` | `docker-hub` | Docker Hub | `https://registry-1.docker.io/` | `auth.docker.io`, receives the credential | yes |
| `oci` | `quay` | Quay | `https://quay.io/` | none (token service `https://quay.io/v2/auth`, the origin) | |
| `oci` | `ghcr` | GitHub (ghcr.io) | `https://ghcr.io/` | none (token service `https://ghcr.io/token`, the origin) | |
| `oci` | `gitlab` | GitLab | `https://registry.gitlab.com/` | `gitlab.com`, receives the credential (token service `https://gitlab.com/jwt/auth`) | |
| `npm` | `npmjs` | npm public registry | `https://registry.npmjs.org/` | none | yes |
| `nuget` | `nuget-org` | nuget.org | `https://api.nuget.org/v3/index.json` | `*.nuget.org`, no credential | yes |
| `deb` | `debian` | Debian | `https://deb.debian.org/debian/` | none | yes |
| `deb` | `debian-security` | Debian security updates | `https://deb.debian.org/debian-security/` | none | |
| `deb` | `ubuntu` | Ubuntu | `https://archive.ubuntu.com/ubuntu/` | none | |
| `deb` | `ubuntu-security` | Ubuntu security updates | `https://security.ubuntu.com/ubuntu/` | none | |
| `deb` | `ubuntu-ports` | Ubuntu ports | `https://ports.ubuntu.com/ubuntu-ports/` | none | |
| `ansible` | `galaxy` | Ansible Galaxy | `https://galaxy.ansible.com/api/` | `github.com`, `codeload.github.com`, no credential | yes |

Which registries are presets is [Q-REPO-321](#q-repo-321--which-registries-are-presets). Verified on 2026-11-12 with one request
each from a workstation: the four OCI registries answer `GET /v2/` with `401` and the token services above; the npm registry's
`-/ping`, the NuGet service index and Galaxy's `/api/` answer 200; `dists/trixie/InRelease`, `debian-security/dists/trixie-security/InRelease`
and `dists/noble/InRelease` (archive, security and ports of Ubuntu, over `https`) answer 200. Not verified: a pull or install through a
trove remote of each new preset; the builder does one each by hand and records it (CI is not assumed to reach these hosts).

Acceptance criteria:

- `GET /api/v1/formats` lists `oci` with the four presets above, `docker-hub` the default, and `generic` with none.
- A remote OCI repository created with `url: "https://registry.gitlab.com/"` and no `extraHosts` reads back
  `extraHosts: [{pattern: "gitlab.com", sendCredentials: true}]`; with `https://registry.example.com/` it reads back `[]`.
- A mirror of `https://quay.io/` without include patterns and without `maxBytes` answers 422 `mirror_unbounded`.
- A test registration with two default presets is refused by `formats.Register`.

### R-REPO-063 · The defaults of a new repository
Priority: **MVP** · Covers: UI-031, REPO-010

- `GET /api/v1/projects/{projectKey}/repository-defaults?format=<format>&class=<class>` (`getRepositoryDefaults`) answers
  `{"suggestedKey", "repository"}` ([Q-REPO-324](#q-repo-324--a-new-repositorys-defaults-come-from-one-operation)):
  - `repository` is the document that a create of only `key`, `class` and `format` in that project would store, without `key`:
    `visibility` (the lower of the installation's default and the project's level, R-REPO-004), the common members at their
    defaults, the class's section in its stored, canonical form (remote: the default preset's `url` and `extraHosts` and every member
    of R-REPO-024's table; virtual: `members: []`, `deployTarget: null`, `mergedMetadataTtlSeconds`; local: the format's
    `packageOwnership`), and no format member, whose defaults are the `default`s of `settingsSchema`.
  - `suggestedKey` is `<format>-<class>`, or the first of `<format>-<class>-2` to `<format>-<class>-9` that is free in the project;
    `null` when all are taken.
- Permission: that of a create (`repositories:write-config`, or Owner of the project); `404` for an unknown project; `422` for an
  unknown format or a class the format lacks (`not_applicable`). It writes nothing and records no audit event, and it answers during
  maintenance.

Acceptance criteria:

- An Owner without `settings:read`, in an `internal` project of an installation whose default is `internal`, receives
  `visibility: {artifacts: "internal", wiki: "internal"}`.
- `format=oci&class=remote` answers `remote.url: "https://registry-1.docker.io/"`, `extraHosts` with `auth.docker.io` receiving the
  credential, `cache.metadataTtlSeconds: 300` and `mirror.intervalSeconds: 3600`.
- With `npm-local` in the project, `format=npm&class=local` suggests `npm-local-2`. A reader of the project gets 403.

### R-REPO-064 · The upstream test follows a token handshake and names the token service
Priority: **MVP** · Covers: RMT-004, RMT-005

For a format whose probe resource answers with a Bearer challenge (`ProbeChallengeReachable`, OCI), both upstream tests read the
challenge's realm ([Q-REPO-325](#q-repo-325--the-upstream-test-runs-a-registrys-login-for-a-typed-credential)):

- A realm whose host is neither the upstream's origin nor an `extraHosts` entry: `error.code` `foreign_host_denied`, whose message
  names the host; `authenticated: null`; no request to the realm.
- An allowed realm, `authMode: basic` with a credential, and a host that receives it (the origin, or an entry with
  `sendCredentials: true`): one `GET` of the realm with the challenge's `service` and no `scope`, with the credential as Basic (the
  request of a client's login). `200` with a token is `authenticated: true`, and the token is dropped, not cached; `401` and `403` are
  `false`; another status is `request_failed`. The egress rules, the TLS trust and the 64 KiB read bound of every token request apply.
- An allowed realm without a credential, or with a host that receives none: no request to the realm; `authenticated: null`.
- The result gains `tokenService`: `null`, or `{"host", "allowed", "receivesCredential"}`.
- `authMode: bearer` runs no handshake, as before (R-OCI-021).

Verified on 2026-11-12: Docker Hub's token service answers a wrong credential without scope `401`; an anonymous request without
scope gets `200` at Docker Hub, `403` at ghcr.io and GitLab and `401` at Quay, which is why the test sends none. Not verified: that
ghcr.io, Quay and GitLab answer a valid credential without scope with `200` (it is the request `docker login` sends).

Acceptance criteria:

- A test registry whose realm is on a second test host: without `extraHosts`, `foreign_host_denied` naming that host, and the
  second host saw no request; with `{pattern: <host>, sendCredentials: true}` and the right password, `authenticated: true` and the
  host saw one request without `scope`; with a wrong password `false`; with `sendCredentials: false`, `null` and no request.
- The saved test of a stored repository answers the same with its stored credential.
```

**Changes to existing requirements** (the builder of WP0 makes them):

- **R-REPO-003**: the catalog's facts list `UpstreamPresets` in place of the default upstream URL, the default extra hosts and the
  default credential hosts.
- **R-REPO-015**: "upstream: the format's default upstream URL" reads "the format's default preset (R-REPO-062)".
- **R-REPO-024**: the `extraHosts` row: "a format's presets bring their hosts when `url` equals one and `extraHosts` is omitted
  (R-REPO-062)"; the test's paragraph points to R-REPO-064; the error codes gain `foreign_host_denied`; the result example gains
  `"tokenService": null`.
- **R-REPO-052**: "the format's well-known upstream (the catalog's default upstream: …)" reads "a preset's URL (R-REPO-062)".
- **R-OCI-001** ([oci.md](../spec/formats/oci.md) lines 64 and 103), the npm registration ([npm.md](../spec/formats/npm.md) line
  1307), **R-NUGET-001** ([nuget.md](../spec/formats/nuget.md) lines 63 and 127), **R-DEB-001** ([debian.md](../spec/formats/debian.md)
  lines 107 and 140), **R-ANS-001** ([ansible.md](../spec/formats/ansible.md) line 118): "Default upstream" becomes "Upstream presets"
  with the format's rows of R-REPO-062.
- **R-OCI-021**: the bullet "Default extra hosts proposed for the well-known upstream" names the presets' hosts (`auth.docker.io`,
  `gitlab.com`); the bullet on the upstream test points to R-REPO-064.

### 2.3 Decision blocks for [api-and-ui.md](../spec/api-and-ui.md), in `## Decisions` after Q-UI-274

Each block starts with "Status: **Decided by the development agent** — selected from the options below; the owner reviews it at
the walkthrough."; it is left out below to save space.

```markdown
### Q-UI-340 · A new repository is a format chooser and one form, as the reference's New project

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Five steps, as built | Format, class, basic, class-specific, review | Built | A step label lists four topics; the review repeats the form; a step hides what follows |
| (b) A chooser page of tiles, then one form per format (chosen) | The reference's **New project**: tiles that open one page | Everything the job needs is in view; the title can name the format; the details fold away in one section | The class is chosen on the form, not before it |
| (c) One page with the format as a select | No chooser | One page | The form changes under the reader when the format changes; the tiles go |

**Choice:** (b). A tile is a link (one tab stop each, Enter opens it), which replaces the listbox of Q-UI-114 and Q-UI-115; the
review step goes, and **Show as API request** stays at the end of the form.

**Rationale:** the owner's reference does it this way, and the format decides the form (Q-UI-112). The development agent would
revisit (c) when the number of formats is small enough that a chooser page costs more than it explains.

**Applied in the spec:** R-UI-080, R-UI-021. Revisits Q-UI-114 and Q-UI-115 (the tiles), supersedes Q-UI-116 (Change format starts afresh).

### Q-UI-341 · The class is a radio group of the form, preset to local

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Radio group, preset local (chosen) | Three options with a sentence each; a missing class disabled with its reason | Three exclusive choices are radios (style guide); local is the plainest | A user who wants a remote changes one choice |
| (b) No preset | The user must choose | No assumption | A field without a value contradicts "a default is a value in the field" |
| (c) Second row of tiles | Class tiles under the format | Visual | Two choosers before the form |

**Choice:** (a). Another class keeps the identity fields and restarts the class's own parts from that class's defaults.

**Rationale:** a local repository needs nothing beyond the key. The development agent would revisit the preset if the owner's
installation creates mostly remotes.

**Applied in the spec:** R-UI-080.

### Q-UI-342 · The key starts with a suggestion that is free

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Empty | As built | No guess | A required empty field; the owner's red key |
| (b) `<format>-<class>`, or the first free of `-2` to `-9`, from the server (chosen) | `suggestedKey` of R-REPO-063 | A value in the field; the manual's own naming (`npm-local`, `npm-remote`); never red on arrival | A suggestion the user may not want |
| (c) The same, computed in the browser | GETs per candidate | No server change | Several requests; the server already answers the defaults |

**Choice:** (b). The suggestion follows the class until the key is edited.

**Rationale:** the plain user gets a working key without thinking about the pattern. The development agent would revisit it when
installations name repositories by another rule (then the rule is a setting).

**Applied in the spec:** R-UI-081, R-UI-084, R-UI-086, R-REPO-063.

### Q-UI-343 · The standard flow holds what a repository needs to work, and no format setting

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Key, description, visibility; plus the upstream (remote) or the members (virtual) (chosen) | CLAUDE.md's rule read against the code: the remote needs an upstream to fetch, the virtual members to resolve | A local repository is three fields; nothing the plain user does not decide | A setting a user does want needs one click more |
| (b) As (a), plus a format's "important" settings in the main form | Per-format exceptions | Shortcuts | Every member of every schema is a sensible default (section 1.3); an exception list is a second rule |
| (c) As built | Every setting on a step | Complete | The owner's finding |

**Choice:** (a). The brief's list "local: key, description, visibility; remote: plus the upstream; virtual: plus the members" is
confirmed, corrected in two points: a virtual repository's **deployment target** is standard, because without it the virtual
address is read-only (R-REPO-021); and today's flow has no member step at all (D10), so the members are added, not moved.

**Rationale:** each format setting has a default that suits a plain repository (1.3). The development agent would revisit (b) for
a setting whose default is wrong for more than one installation in three.

**Applied in the spec:** R-UI-081.

### Q-UI-344 · Where the details go

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One closed section **Advanced settings** at the end of the form; the upstream's details folded inside its group; on the settings page, one closed section per topic (chosen) | Two folding places in the form | The upstream details stay next to the URL they qualify; the rest is one click away | Two kinds of fold in one form |
| (b) Everything in Advanced settings | One fold | Uniform | The cache periods and further hosts away from the upstream; Allow host would change a field in a closed section |
| (c) A step "Details" | A step after the form | Out of the way | Steps again |

**Choice:** (a). Disabled and Offline are not offered at creation (a new repository is not created switched off). The format's
`mirror` member (`oci.mirror`, `deb.mirror`) goes into the Mirror group, so that a mirror is one topic.

**Rationale:** the owner asked for a detail view, not for the details to go. The development agent would revisit the creation's
Disabled if an installation stages repositories before opening them.

**Applied in the spec:** R-UI-082, R-UI-083, R-UI-087. Revisits Q-UI-117 (where the Mirror section sits) and Q-UI-122 (where the
upstream sits; what a create writes stays).

### Q-UI-345 · The presets are buttons with names, and the pressed one follows the URL

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A button per preset, name only, `aria-pressed` from the URL (chosen) | The owner's "buttons" | The owner's words; a typed URL shows that no preset applies | Four or more buttons where the style guide would use a select |
| (b) A select | The style guide's rule for four or more | Compact | Not what the owner asked |
| (c) Buttons with the registries' logos | Recognisable | Looks good | Each logo's terms must be read first (Q-UI-225); a logo is no label |

**Choice:** (a), in the order of CLAUDE.md for OCI (Docker Hub, Quay, GitHub (ghcr.io), GitLab).

**Rationale:** an explicit decision of the owner outranks the style guide's rule (rule 2 over rule 3). The development agent would
revisit (c) once the logos' terms are read.

**Applied in the spec:** R-UI-082.

### Q-UI-346 · Test connection names a token service that is not allowed, and offers to allow it

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Name the host and offer **Allow <host>**, which adds a further host receiving the credential and tests again (chosen) | One click from the finding to the fix | The owner's own GitLab registry works from the form | The host comes from the upstream (H-610): the click is the user's consent, and the button names the host |
| (b) Name the host only | The user edits Further hosts | No action on the upstream's word | An extra trip into the details |
| (c) Nothing | As built | — | The test passes and the first pull fails (D12) |

**Choice:** (a). Under the password a line names every host the credential goes to.

**Rationale:** a registry whose token service is on its own second host (every self-managed GitLab) cannot be set up from the form
otherwise. The development agent would revisit the credential default of Allow if a registry is found whose token service must
not see the credential.

**Applied in the spec:** R-UI-082, R-REPO-064.

### Q-UI-347 · A new virtual repository's deployment target is its first local member

| Option | What it means | For | Against |
|---|---|---|---|
| (a) None, as the server's default | Read-only virtual | No assumption | Publishing through the virtual address fails (`E405`) until someone finds the setting |
| (b) The first local member, until the user chooses (chosen) | A value in the field | The recommended practice (clients use the virtual address) works for install and publish | A virtual that should be read-only needs **None** chosen |

**Choice:** (b). Writes through it still pass the target's own checks (R-REPO-021).

**Rationale:** one address for install and publish is what the plain user expects. The development agent would revisit it if
read-only virtual repositories turn out to be the common case.

**Applied in the spec:** R-UI-081.

### Q-UI-348 · A default is the value of its field

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The value in the field, written only when changed (chosen) | Q-UI-80 stays; the form shows what the repository will do | The owner's rule; no hint lines | A derived default has no value to show |
| (b) Write every shown value | The document states everything | — | Freezes today's defaults into every repository (Q-UI-80) |

**Choice:** (a). A choice whose default is derived offers **Automatic** first; a derived text stays empty with its description; an
emptied field shows its default again when it is left.

**Rationale:** the default is what the plain user sees and keeps. The development agent would revisit the emptied-field rule if a
setting needs "empty" as a value of its own.

**Applied in the spec:** R-UI-084.

### Q-UI-349 · Five field widths

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Five classes by kind of input: number, duration, bytes, name, full (chosen) | Tokens and classes; the schema maps to them | Few enough to stay consistent; a period no longer offers room for billions | A field of an unusual kind takes the nearest class |
| (b) `ch` per field from its maximum | Exact | Each field fits its maximum | Every field is a different width: the defect T-20 shipped |
| (c) Full column, as built | Simple | — | The owner's finding |

**Choice:** (a), widths of section R-UI-085 at 14 px Inter, judged on the renders.

**Rationale:** consistency across screens is the other half of T-21. The development agent would revisit a width that the renders
show to cut a common value.

**Applied in the spec:** R-UI-085, ui-style-guide.md (Forms).

### Q-UI-350 · An error appears after the field was left or the form was sent

| Option | What it means | For | Against |
|---|---|---|---|
| (a) On every keystroke | Immediate | — | Red while typing the first letter |
| (b) After leaving the field with a value, or on submit (chosen) | The common rule of forms | Never red on arrival; the submit still catches empty required fields | An error shows one step later |
| (c) On submit only | Quiet | — | A long form's errors all at the end |

**Choice:** (b). The submit button stays enabled; a press with errors shows the summary and sends nothing.

**Rationale:** the owner's red key (D3). The development agent would revisit it for a field whose wrong value is costly to type twice.

**Applied in the spec:** R-UI-086.

### Q-UI-351 · The repository settings page is one column of sections

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep the tabs | As built | Built | Tabs hide the standard settings among the details; one save for every tab |
| (b) One column: the standard settings open at the top, every detail a closed section, a save per section (chosen) | The style guide's settings page; the reference's general settings | The same split as the creation form; a section saves only its own group | A long page for a remote of a mirror format |

**Choice:** (b), with the order of R-UI-087. This settles the open point of Q-UI-222 ("what its tabs become needs the owner's
reference"): the owner's rule of 2026-11-12 is that reference.

**Rationale:** the plain path first, details on demand. The development agent would revisit the order once the owner has walked
through a remote's page.

**Applied in the spec:** R-UI-087; ui-style-guide.md, "What does not follow this page yet".

### Q-UI-352 · Disable, move and delete are in Advanced

| Option | What it means | For | Against |
|---|---|---|---|
| (a) **Disable repository** and **Enable repository** act at once after a confirmation that says what stops; Move and Delete beside them (chosen) | The reference's Advanced section (archive, transfer, delete) | A switch that stops every client is not a field next to the description | One more confirmation |
| (b) A checkbox saved with a section | As built | — | Saved by accident with another change |

**Choice:** (a). A disabled repository shows a badge "Disabled" beside its key at the top of the page.

**Rationale:** disabling is an operation on a running repository. The development agent would revisit it if Disabled becomes a
state with more than two values.

**Applied in the spec:** R-UI-087.

### Q-UI-353 · The title uses the format's display name

| Option | What it means | For | Against |
|---|---|---|---|
| (a) "New <displayName> repository" (chosen) | "New OCI (Docker) repository", "New Ansible Galaxy repository" | One name per format on the tiles, in the lists and in the title | Longer than the owner's "New OCI repository" |
| (b) A short title name per format in the catalog | "New OCI repository" | The owner's words | A second name of each format to keep |

**Choice:** (a); the owner's question 2 in [repository-setup-work-packages.md](../projektplan/repository-setup-work-packages.md) asks him.

**Rationale:** a format is called one thing everywhere. The development agent would revisit (b) on the owner's word.

**Applied in the spec:** R-UI-080.
```

### 2.4 Decision blocks for [repositories-and-proxy.md](../spec/repositories-and-proxy.md), after Q-REPO-307

The same status line opens each block.

```markdown
### Q-REPO-320 · The presets are a fact of the format, and the API serves them

| Option | What it means | For | Against |
|---|---|---|---|
| (a) In the catalog (`catalog.Facts.UpstreamPresets`), served by `GET /api/v1/formats` (chosen) | The format declares them next to its handshake | The format knows which host its registry's token service is on; the server applies a preset's hosts to a create by URL, so an API client gets the same repository as the UI; a new format brings its presets without a UI change (R-UI-021: the UI holds no list per format) | A contract change of the formats (ContractVersion 4) |
| (b) In the UI | A table in the web client | No server change | A second list per format; the UI would have to send `extraHosts`, and a `curl` create of `https://registry.gitlab.com/` would get none and fail at its first pull |

**Choice:** (a).

**Rationale:** a preset carries security facts (which host receives the credential) that belong where the handshake is checked.
The development agent would revisit it if presets ever need a translation, which a product name does not.

**Applied in the spec:** R-REPO-062, R-REPO-003.

### Q-REPO-321 · Which registries are presets

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The owner's four for OCI and the format's default elsewhere | Minimal | Nothing unverified | Ubuntu users type the archive's address |
| (b) A rule: a public registry of the format's ecosystem whose address is the same for every user, whose probe answers without a credential, and that the remote serves with a URL and hosts alone (chosen) | The table of R-REPO-062 | Each preset works for anyone who presses it | A registry the rule leaves out is typed |
| (c) Every registry that hosts the format | GitHub Packages, GitLab's package endpoints, Red Hat's hub | More buttons | Some need an owner in the URL or a token for every read, some failed their probe |

**Choice:** (b): OCI's four (the owner's), npm's public registry, nuget.org, Ansible Galaxy, and Debian, Debian security, Ubuntu,
Ubuntu security and Ubuntu ports (arm64 lives on ports, not on the archive). Left out by the rule: GitHub Packages for npm and NuGet
(a token for every read; NuGet's URL names the owner), GitLab's npm endpoint (its `-/ping` answered `302` on 2026-11-12), Red Hat's
Automation Hub (needs `ansible.authUrl` and was never reached, H-842), and other public container registries the owner did not name.

**Rationale:** a button that does not work is worse than a field to type in. The development agent would revisit the list when
a registry passes the rule and an installation asks for it.

**Applied in the spec:** R-REPO-062.

### Q-REPO-322 · A preset's hosts come with its URL

| Option | What it means | For | Against |
|---|---|---|---|
| (a) An omitted `extraHosts` takes the hosts of the preset whose URL the create names; another URL takes none (chosen) | Q-REPO-91 for every preset, not only the default | A GitLab remote works from its URL alone | — |
| (b) Only the default preset's hosts, as built | Q-REPO-91 | — | A GitLab remote created from its URL fails its handshake |

**Choice:** (a).

**Rationale:** Q-REPO-91's reason (no host is trusted for a private upstream that never asked for it) holds per preset. The
development agent would revisit it if two presets ever shared a URL.

**Applied in the spec:** R-REPO-062, R-REPO-024. Revisits Q-REPO-91.

### Q-REPO-323 · Every preset is a well-known upstream for the mirror bound

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Every preset's URL needs a bound (chosen) | R-REPO-052's refusal for each | Quay, ghcr.io and the Ubuntu archive are as unbounded as Docker Hub | — |
| (b) The default preset only, as built | — | — | An unbounded mirror of ghcr.io would be saved with a warning |

**Choice:** (a).

**Rationale:** the bound exists because a public registry is terabytes. The development agent would revisit it for a preset that
is small by nature.

**Applied in the spec:** R-REPO-052, R-REPO-062.

### Q-REPO-324 · A new repository's defaults come from one operation

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `getRepositoryDefaults`: the document a bare create would store, and a free key (chosen) | One read per format and class | One source for every default the form shows: the visibility an Owner without `settings:read` cannot read today (D13), the remote and mirror defaults the client copies today (`MIRROR_DEFAULTS`) | One more operation |
| (b) Facts spread over existing documents | `newRepositoryLevel` on the project, mirror defaults on the format | No new operation | A new field somewhere for each new default |
| (c) Client constants | As built | Nothing to build | Copies that drift; the Owner's visibility stays wrong |

**Choice:** (a). The format member is not part of it: its defaults are in `settingsSchema`, which the form already reads.

**Rationale:** the server already resolves every omitted member when it stores a create; the form asks for that result instead of
guessing it. The development agent would revisit it if the edit form ever needs it too.

**Applied in the spec:** R-REPO-063.

### Q-REPO-325 · The upstream test runs a registry's login for a typed credential

| Option | What it means | For | Against |
|---|---|---|---|
| (a) As built (Q-REPO-280 (a)): a Bearer challenge is reachable, `authenticated: null` | No second request | — | A wrong password and a token service the repository cannot reach both pass the test (D12) |
| (b) Check the realm's host always; with a typed credential, ask the token service once without scope, as a client's login does (chosen) | Q-REPO-280's option (b), without an anonymous request | The test judges what the owner asked to check before the save | A second request to a second host; a credential oracle from trove's address (H-971) |
| (c) Ask the token service also without a credential | One more verdict | — | Anonymous scope-less requests answer 200, 401 or 403 by registry (verified), so the answer means nothing |

**Choice:** (b). The owner's demand to check the connection settings before the save is the revisit condition of Q-REPO-280.

**Rationale:** for Docker, the owner's main case, the credential is judged by the token service, so the test must ask it. The
development agent would revisit the scope-less request for a registry whose token service refuses it with a valid credential.

**Applied in the spec:** R-REPO-064, R-REPO-024, R-OCI-021. Revisits Q-REPO-280.

### Q-REPO-326 · The Debian and Ubuntu presets carry no archive keys

| Option | What it means | For | Against |
|---|---|---|---|
| (a) URL only; `deb.upstreamKeys` stays the operator's (chosen) | Q-DEB-14 (a) stands | trove does not follow two archives' key changes; Q-DEB-14 rejected built-in keys | A Debian remote from a preset is unverified until keys are given |
| (b) Each preset brings its archive keyring | Built-in keys | A verified remote in one click | Q-DEB-14 (d), rejected: every key change of two archives becomes a trove release |

**Choice:** (a). The keys stay a detail in the Debian settings; the help of `upstreamKeys` names the keyring file a Debian or Ubuntu
host already has.

**Rationale:** the decision of Q-DEB-14 is not changed by presets. The development agent would revisit it with Q-DEB-14.

**Applied in the spec:** R-REPO-062.

### Q-REPO-327 · The format list's default upstream members are replaced by the presets

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `upstreamPresets` replaces `defaultUpstreamUrl`, `defaultExtraHosts` and `defaultCredentialHosts` (chosen) | One shape | One way to read a format's upstreams | Every reader changes in the same commit |
| (b) Keep the three beside the presets | Two shapes | — | A second way, for no client (pre-alpha, CLAUDE.md) |

**Choice:** (a). `defaultMetadataTtlSeconds` and `defaultNotFoundTtlSeconds` stay: they are periods, not upstreams.

**Rationale:** no backward compatibility before the first stable release. The development agent would revisit nothing here.

**Applied in the spec:** R-REPO-062, R-REPO-003.
```

### 2.5 Security gaps

Into [remote-repositories.md](../security/remote-repositories.md), section "What this does not cover", after H-613 and with a line in
the list at the top of that section; the section
"The token handshake and the credential hosts" gains a paragraph on presets, and H-610's last sentence reads "Each OCI preset
proposes at most one `sendCredentials` host, run by the registry's own operator: `auth.docker.io` (Docker Hub), `gitlab.com`
(GitLab)."

```markdown
### H-970 · A preset's credential host is fixed in the repository when it is created

Component: the presets of the catalog (`catalog.Facts.UpstreamPresets`) and the create of R-REPO-062, which copies a preset's
`extraHosts` into the stored `remote` section.

Subject: the upstream credential of a remote created from a preset with a `sendCredentials` host (GitLab: `gitlab.com`; Docker Hub:
`auth.docker.io`), and the host that receives it.

Live once R-REPO-062 is built. The hosts are the format's knowledge of the registry on the day the format was written. A registry
that moves its token service to another host makes the remote fail closed (`foreign_host_denied`), never send the credential to the
new host; but a preset corrected in a later release does not reach repositories created earlier, which keep the stored hosts, so a
wrong `sendCredentials` host stays until an administrator edits the repository. What makes it real: a preset whose credential host
is not run by the registry's operator, which the table of R-REPO-062 rules out for the presets listed.
```

Into [egress.md](../security/egress.md), section "What this does not cover", after H-282 and with a line in the list at the top
of that section:

```markdown
### H-971 · The upstream test tells its caller whether a credential is accepted, from trove's address

Stage: the upstream tests of R-REPO-024 and R-REPO-064 ([repository/upstream.go](../../internal/repository/upstream.go)).

Subject: the accounts of an upstream registry and of its token service. A holder of `repositories:write-config`, or the Owner of a
project, can send a typed credential to a registry, or since R-REPO-064 to its token service, and read `authenticated`; the registry
sees trove's egress address, not the caller's, so its per-address limits and blocks fall on trove. Live today for upstreams that
answer the probe with Basic; live for token services once R-REPO-064 is built. What bounds it: the API's rate limit per principal,
the audit record of the request, and that the caller must already be able to configure a repository. What makes it real: a caller
who guesses another account's password through trove.
```

## 3 · Work packages

### 3.1 The cut: two builders, after T-18 has landed

| Builder | Feature end to end | Owns | Stack | kind |
|---|---|---|---|---|
| **B1 · The upstream** (does WP0 first) | Presets, the defaults operation, the test's handshake, and the upstream section of the UI that uses them | `internal/formats/catalog`, the `format.go` and `format_test.go` of `oci`, `npm`, `nuget`, `deb`, `ansible`, `internal/formats/formats.go` (ContractVersion), `internal/formats/testdata/contract.txt`, `internal/formats/formattest`, `internal/repository` (`validate_remote.go`, `validate_mirror.go`, a new `defaults.go`), `internal/proxy/upstream/probe.go`, `internal/proxy/authenticator.go` and the OCI authenticator (a request without scope), `internal/api/v1` (`repositories_api.go`, a new `repository_defaults_api.go`, the upstream test handler), `api/openapi.yaml` and its generated code, `web/src/app/api/repositories-api.ts` and the fakes under `web/src/app/api/testing/`, a new `web/src/app/features/repositories/upstream-model.ts`, `upstream-settings.ts` and its spec, the width tokens and classes in `web/src/styles/_tokens.scss` and `_controls.scss`, `test/integration/{repository,api,ociremote,projects}`, `docs/spec/repositories-and-proxy.md`, `docs/spec/formats/*.md`, `docs/security/remote-repositories.md`, `docs/security/egress.md`, `docs/developer/{formats,proxy,repositories,oci,http-api}.md`, README's API table | yes | no |
| **B2 · The flow and the page** | The format chooser, the creation form, the settings page, the shared pieces they need, every browser journey that creates or configures a repository, the manual | `web/src/app/features/repositories/` except B1's two files, `repositories.routes.ts`, `web/src/app/shared/access/fields.ts` (the `width` input), the member picker extracted from `virtual-members.ts`, `web/e2e/tests/` (journeys below), `docs/spec/api-and-ui.md` after WP0, `docs/developer/{web-ui,ui-style-guide,e2e}.md` after WP0, `docs/manual/**`, README's feature lines 133 and 135 | journeys | **yes** (`access-long` runs the journeys) |

Shared, with a rule: `web/src/locale/messages.xlf` and `messages.de.xlf` (B1's WP4 lands first; B2 merges it before it extracts
its own strings), `docs/manual/admin/repositories.md` and `container-repositories.md` (B1's WP4 writes the upstream part first, B2
the rest after it), `README.md` (different lines), `docs/coverage/*` (regenerated by `make coverage` in each builder's last commit).

### 3.2 B1 · The upstream

| WP | Content | Requirements |
|---|---|---|
| **WP0 · Contracts and spec** (lands before B2 starts) | Section 2 pasted into the spec pages, with the changed requirements and the revisit lines in Q-UI-114, -115, -116, -117, -122, -222, Q-REPO-91, -280; `catalog.UpstreamPreset` and `Facts.UpstreamPresets` with the registration checks, the three old fields removed, `ContractVersion` 4, every format's preset list (data only), `contract.txt`; OpenAPI: `UpstreamPreset`, `FormatDescriptor.upstreamPresets`, `getRepositoryDefaults` with `RepositoryDefaults`, `UpstreamTestResult.tokenService` and `foreign_host_denied`; `make generate`; the web API's types, `RepositoriesApi.defaults(project, format, cls)` and its fakes; the move of `UpstreamForm`, `EMPTY_UPSTREAM`, `UpstreamDefaults`, `upstreamDefaultsOf`, `initialUpstream`, `normalizedUrl`, `effectiveUrl`, `upstreamOf` and `upstreamTestBody` from `repository-model.ts` to `upstream-model.ts` with no change of behaviour; the five width tokens and classes and their table in the style guide's **Forms** (the seam with B3 at the top of this page) | R-REPO-062, -063, -064 (contracts), R-UI-085 (tokens) |
| **WP1 · Presets** | The descriptor serves `upstreamPresets`; a create's `extraHosts` from the preset of its URL; the mirror bound for every preset; the guides' tests that read the default URL | R-REPO-062, R-REPO-052, R-REPO-024 |
| **WP2 · Defaults** | `repository.Service.Defaults` (the create's resolution without the insert) and `suggestedKey`; the handler, its permission and maintenance rule | R-REPO-063 |
| **WP3 · The test's handshake** | The probe reads the realm, checks its host, asks the token service once without scope for a typed Basic credential; `tokenService`; the saved test too | R-REPO-064 |
| **WP4 · The upstream section** | `upstream-settings.ts`: the preset buttons, the URL, the authentication, the credential's hosts line, Test connection with `tokenService` and **Allow <host>**, the region **Upstream details** with Further hosts, the periods as values and Offline on the edit form; inputs `form`, `format`, `project`, `fieldErrors`, `defaults` (the `remote` of R-REPO-063), `edit` (boolean); the manual's upstream part | R-UI-082, R-UI-084, R-UI-085 (its fields), R-UI-086 (its fields) |

**Tests by tier.**

- Unit: `internal/formats/catalog` (`TestR_REPO_062_…`: one default, unique ids and URLs, normalized URLs, credential hosts among the
  hosts, no presets without remote); each format's `format_test.go` (its rows of R-REPO-062); `internal/repository`
  (`TestR_REPO_062_APresetsHostsComeWithItsURL`, `TestR_REPO_052_EveryPresetNeedsABound`, `TestR_REPO_063_…`);
  `internal/proxy/upstream` (`TestR_REPO_064_…` with an `httptest` registry and token service: foreign host, valid and wrong
  password, `sendCredentials: false`, no credential, `bearer`); `internal/api/v1` (the descriptor's JSON, the defaults handler's
  403, 404, 422). Web: `upstream-settings.spec.ts` (`R-UI-082 …`: the pressed preset follows the URL, a preset fills the URL and the
  further hosts, the credential line, Allow adds the host and tests again, the periods show their defaults, a create writes nothing
  for untouched defaults, the width classes, `seriousViolations`).
- Integration, once each through the slot wrapper: `repository` (presets' hosts stored, the mirror bound of a preset, the defaults
  document), `api` (`getRepositoryDefaults` for an Owner without `settings:read`; the unsaved test against a stub registry with its
  realm on a second stub host), `ociremote` (the handshake test against the counting OCI stub), `projects` (an Owner's defaults and
  test, a reader's 403).
- By hand, recorded in the report: one pull through a remote of Quay, ghcr.io and GitLab, and one `apt-get update` through a remote of
  each Ubuntu preset and of Debian security, on the owner's installation or the dev profile with internet access.

**Gates.** The common brief's, plus `go vet -tags e2e ./test/e2e/...` (WP0 touches the generated types that e2e fixtures may read).

### 3.3 B2 · The flow and the page

| WP | Content | Requirements |
|---|---|---|
| **WP5 · The creation flow** | The chooser (`repository-format-chooser.ts`, tiles as links, filter, Enter) and the form (`repository-create.{ts,html,scss}` in place of `repository-wizard.*`): routes `new` and `new/:format` in both route tables, the title, Class, the key from `suggestedKey` and its check, Description, T-18's visibility control preset from R-REPO-063, B1's upstream section, the Members group with the member picker extracted from `virtual-members.ts` (`member-picker.ts`, used by both) and the deployment target, **Advanced settings** with its groups (`repository-fields.ts` split into notes, paths and access details; `format-settings.ts` with a filter that sends the `mirror` member to the Mirror group), **Show as API request**, the validation timing, the summary that opens closed regions, the draft and the unsaved guard | R-UI-080, -081, -083, -084, -085, -086, -088; R-UI-021, R-UI-022, R-UI-055 as changed |
| **WP6 · The settings page** | `repository-detail.*` as one column of `<trove-settings-section>`s in the order of R-UI-087, a merge patch per section with its own `If-Match` and conflict handling, the unsaved marker of a closed section, **Advanced** (disable and enable with a confirmation, move, delete), **How to use**, **Details**, **API**; `format-settings.ts` and `mirror-settings.ts` with their defaults as values, **Automatic**, the widths; the style guide's "What does not follow this page yet" without the repository's settings | R-UI-084, -085, -086, -087, -088; R-UI-049 as changed |
| **WP7 · Journeys and manual** | The journeys below; the manual pages below; `web-ui.md` (the screens, the routes, the member picker), `e2e.md` (journey 22), README lines 133 ("on the Mirror tab" → "in the Mirror section") and 135 (the wizard → the chooser and the form) | all of B2's |

**Tests by tier.**

- Unit (`make web-test`), each spec calls `seriousViolations`: the chooser (`R-UI-080`: links, filter, Enter, unknown format); the
  form (`R-UI-080` to `R-UI-086`: the title per format, no step list, the class preset and a disabled class with its reason, the
  suggested key and no error on arrival, the error after leaving, Create with errors opens Advanced settings and the summary, the
  body of a local, a remote and a virtual create equals the sparse rule of Q-UI-80, the deployment target preset, Show as API request
  masks the password); the settings page (`R-UI-087`: the sections and their order per class, each save sends only its group, the
  unsaved marker, a 412 per section, Disable's confirmation); `format-settings.spec.ts` (`R-UI-084`: text and number defaults as
  values, Automatic, an emptied field returns to its default); `mirror-settings.spec.ts` (values and widths); the member picker
  (keyboard only, the live region, as R-UI-022's spec does today).
- Playwright journeys (`web/e2e/tests/`, set `access-long`, run once on kind with `$S/kind-lock.sh`):
  - rewritten for the chooser and the form: `02-repository-and-files` (local generic), `07-german-dark` (the chooser's German
    heading, and one form's), `08-packages`, `11-nuget`, `14-mirror` (a remote with the npm stub's URL typed, Test connection, the
    Mirror group in Advanced settings), `17-deb`;
  - rewritten for the settings sections instead of tabs: `08-packages` (npm settings, Mirror), `09-guides` (How to use), `11-nuget`
    (NuGet settings), `14-mirror` (Upstream, Mirror), `17-deb` (Signing keys, Indexes), `18-virtual-members` (Members), `20-ansible`
    (Import tasks, Tombstones);
  - new `22-repository-setup.spec.ts` (`R-UI-080` to `R-UI-088`): the OCI form with its four preset buttons and GitLab's further host,
    created through the API's answer only (no upstream is contacted); a virtual npm repository with two members and the preset
    deployment target; the key without an error on arrival and with one after leaving; the settings page of the created remote with
    its open and closed sections and a description saved alone.
- Go e2e: none of B2's; `go vet -tags e2e ./test/e2e/...` once.

**Gates.** `make web-test`, `make web-lint`, the journeys above once, the coverage links, the white-label scan.

**Manual pages** (B2, after B1's WP4 for the two shared ones): `docs/manual/admin/repositories.md` (Create a repository, the
upstream's table, Mirror, Settings set through the API: what moved into the UI, Edit a repository), `container-repositories.md`,
`apt-repositories.md`, `ansible-repositories.md`, `limitations.md`, `runbook.md` (wherever a tab or a wizard step is named),
`docs/manual/user/files.md`.

### 3.4 Order

| Step | Who | Starts after | Lands |
|---|---|---|---|
| T-18 | its builders | — | the visibility levels on main |
| WP0 | B1 | T-18 on main | the spec, the contracts, the generated types and fakes, `upstream-model.ts`, the width tokens; the build and the unit tests green |
| WP1 to WP3 | B1 | WP0 | one commit each, its integration areas green |
| WP4 | B1 | WP0 (WP3 for Allow) | the section and its renders; the manual's upstream part |
| WP5, WP6 | B2 | WP0 | against the fakes; the journeys against B1's server once WP1 to WP3 have landed; WP5's strings extracted after WP4 has landed |
| WP7 | B2 | WP5, WP6 | the journeys run once, the manual |
| Closing | the development agent | all | `make coverage`, the evidence record from CI, promt.md |

### 3.5 Render gate (rule 11)

Each WP that changes a screen renders it with `make ui-renders` (B3's command: both schemes, its three widths, realistic data with
short and long names, several rows and an empty state) before its commit, opens the pictures and judges them against the style
guide's checklist; the report names what was found and fixed and the paths of the pictures. If `make ui-renders` is not on main when
a WP is ready, the builder starts the next WP and lands the finished one once it is rendered. States that need an upstream answer (the
four test results) are rendered by answering `testUpstream` with Playwright's `page.route`, on the real screen.

| WP | Screens and states | As |
|---|---|---|
| WP4 | The upstream group in the form and on the settings page: OCI with Docker Hub pressed; GitLab pressed with a username, a password and its credential line; a typed URL with no button pressed; Debian with five buttons (wrapping at the narrow width); npm with one; the test results ok, credential refused, unreachable, token service not allowed with **Allow**; **Upstream details** open with two further hosts and a 64-character host pattern | power user (project Owner), administrator |
| WP5 | The chooser (six tiles, a filter with no match); the form of local generic, local OCI, remote OCI, remote Debian, virtual npm with no member, with two, and with eight members of long keys; **Advanced settings** open for a remote npm in mode mirror; after **Create repository** with a taken key and a server error inside Advanced settings; the admin area's form with the project select and a long project key | power user, administrator |
| WP6 | The settings page of a local npm, a remote OCI, a virtual Debian and a remote npm in mode mirror, with the standard sections open and the rest closed; each closed section opened once; a closed section with "Unsaved changes"; **Advanced** open; a disabled repository's badge | power user, administrator; the plain user's reduced view (read-only) |

At the narrow width no form or section scrolls sideways, and every field keeps its width class or shrinks to the column.

### 3.6 Exit criteria

- Every acceptance criterion of R-REPO-062 to R-REPO-064 and R-UI-080 to R-UI-088 is a test whose name carries its ID, in the tier of
  3.2 and 3.3.
- The renders of 3.5 exist and were judged; the reports list them.
- `grep -rn "defaultUpstreamUrl\|DefaultUpstreamURL\|DefaultExtraHosts\|DefaultCredentialHosts\|MIRROR_DEFAULTS\|specificStepLabel" internal web/src api test`
  prints nothing but the decisions' history.
- The set `access-long` passes once on kind with the rewritten and the new journeys; the integration areas of 3.2 pass once each.
- `make coverage` regenerated; the development agent refreshes the evidence record from CI.

## 4 · Decisions and questions

### 4.1 Decisions of the planning agent

Proposals of the development agent, reviewed by the owner at the walkthrough.

- **Q-UI-340**: a page of format tiles, then one form per format, because that is the reference's **New project** and the format
  decides the form.
- **Q-UI-341**: the class is a radio group preset to local, because three exclusive choices are radios and local needs nothing else.
- **Q-UI-342**: the key starts as a free `<format>-<class>` from the server, so that the field has a value and is never red on arrival.
- **Q-UI-343**: the standard flow is key, description and visibility, plus the upstream or the members and deployment target, because
  that is what each class needs to work; every format setting has a fitting default and is a detail.
- **Q-UI-344**: the details are one closed **Advanced settings** section, with the upstream's details folded inside the upstream,
  because a period belongs next to the URL it qualifies; Disabled and Offline are not offered at creation.
- **Q-UI-345**: the presets are named buttons whose pressed state follows the URL, because the owner asked for buttons.
- **Q-UI-346**: the test names a token service the repository may not reach and offers **Allow**, because a self-managed GitLab
  registry cannot be set up from the form otherwise.
- **Q-UI-347**: a new virtual repository's deployment target is its first local member, so that one address installs and publishes.
- **Q-UI-348**: a default is the field's value and is written only when changed, so that the form shows what the repository will do
  without freezing defaults.
- **Q-UI-349**: five width classes by kind of input, few enough to stay consistent across screens.
- **Q-UI-350**: an error appears after the field was left or the form was sent, never on arrival.
- **Q-UI-351**: the settings page is one column of sections, standard open and details closed, each saving its own group, which
  settles the open point of Q-UI-222.
- **Q-UI-352**: Disable, Move and Delete are actions in **Advanced**, because they act on a running repository.
- **Q-UI-353**: the title uses the format's display name, one name per format everywhere.
- **Q-REPO-320**: the presets are a catalog fact served by the API, because their credential hosts are security facts and an API
  client must get the same repository as the UI.
- **Q-REPO-321**: a preset is a public registry with one address for everyone, an anonymous probe and no format setting, which gives
  OCI's four, one each for npm, NuGet and Ansible, and five for Debian and Ubuntu.
- **Q-REPO-322**: a create naming a preset's URL gets that preset's hosts, so that a GitLab remote works from its URL alone.
- **Q-REPO-323**: every preset needs a mirror bound, because each is a public registry of unbounded size.
- **Q-REPO-324**: the form's defaults come from one operation, which also fixes the visibility preset of an Owner without
  `settings:read`.
- **Q-REPO-325**: the test runs the registry's login for a typed credential and checks the token service's host, because that is
  what "check the connection before saving" means for a registry.
- **Q-REPO-326**: the Debian and Ubuntu presets carry no keys, because Q-DEB-14 rejected built-in archive keys.
- **Q-REPO-327**: `upstreamPresets` replaces the three default-upstream members, with no alias (pre-alpha).

### 4.2 Questions for the owner

1. **Presets of the installation's own registries.** Should an administrator be able to add the installation's own registries (for
   example the registry of your own GitLab) as preset buttons?
   - (a) No: the formats' public presets only; your own registry is typed once per repository, and **Allow** fixes its token
     service in one click.
   - (b) Yes: a list per format in the general settings, served with the format's presets.
   - **Recommendation: (a) now, (b) as its own task if you want it.** Presets are already served by the server, so (b) is one
     settings list and a merge into `upstreamPresets`, about one work package; it is not needed for anything in this plan.
2. **The title's name of a format.** "New OCI (Docker) repository" and "New Ansible Galaxy repository" (the display name used on the
   tiles and in the lists), or "New OCI repository" and "New Ansible repository" as you wrote?
   - (a) The display name, one name per format everywhere.
   - (b) A short title name per format, as a second catalog fact.
   - **Recommendation: (a)**, because a format then has one name on every screen; (b) costs one fact per format and a second name
     to keep in step.
