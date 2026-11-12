# UI style guide

How to build and style a screen of trove. [design-language.md](design-language.md) says what the look is made of and which
values carry it; this page says what to do with them. Read it before you add a screen or change one, and check a screen
against the [checklist](#checklist) before it is committed.

## The rule behind every other rule

The reference is the owner's GitLab ([Q-UI-128](../spec/api-and-ui.md#q-ui-128--the-references-type-colours-and-measures)).
A screen looks like the reference's screen for the same job, and nothing is styled that the reference does not style.

When two rules meet, the higher one wins:

1. Accessibility: focus, contrast, labels, keyboard ([R-UI-044](../spec/api-and-ui.md)).
2. The owner's explicit decisions, listed under [Deliberate differences](#deliberate-differences).
3. The reference's screen for the same job.
4. An existing screen of trove that follows this page.

Where a screen needs data trove does not have, leave the element out. A column that would stay empty, or a value made up to
fill it, is worse than a list that is shorter than the reference's ([Q-UI-214](../spec/api-and-ui.md#q-ui-214--the-members-screen-takes-the-references-member-list)).

## Simple by default, details on demand

The owner's product rules of 2026-11-12 ([CLAUDE.md](../../CLAUDE.md#product-simple-by-default-details-on-demand), T-21 in
[promt.md](../projektplan/promt.md#owner-task-of-2026-11-12)) come before every other rule of this page that is about what a
screen shows. trove is for people who keep and fetch their artifacts, not for people who configure a repository manager; the
three kinds of user they serve are [R-UI-067](../spec/api-and-ui.md#r-ui-067--three-kinds-of-user-the-plain-user-the-power-user-and-the-administrator).

- **The plain path first.** The standard flow of a screen asks and shows only what the thing needs to work, for every format and
  every class. Access in a standard flow is the members and the visibility level, nothing else.
- **Details on demand.** A setting or a fact that only an expert needs (include and exclude patterns, disabling, hiding existence,
  administrator notes, mirror and protocol tuning, statistics, internal names) stays, in a detail view or behind a closed
  **Details** or advanced-settings section. It is never a step of a creation flow and never part of the main form.
- **A remote's connection is tested where its upstream is entered**, before the save, and the format's well-known public
  upstreams are one-click choices (for OCI: Docker Hub, Quay, GitHub `ghcr.io`, GitLab).
- **A default is a value in the field**, editable, never a hint beside an empty field.
- **A field is as wide as its expected input**: a number of seconds is a short field, a URL a long one.
- **A title names the thing**: **New OCI repository**, **New Ansible repository**, not **New repository**.
- **A field shows an error only after the user had a chance**: once it was touched and left, or on submit. An empty field the
  user has not touched is never red.
- **A step or section label names one topic.** "Access, upstream, OCI (Docker) and mirror settings" is four steps or four sections.

## How to style an element

Take the first of these that fits, in this order:

1. A shared control: `<trove-text-field>`, `<trove-select-field>`, `<trove-checkbox>`, `<trove-tab-group>`, `<trove-data-table>`,
   `<trove-avatar>`, `TroveDialog`, the `[troveTooltip]` directive ([web/src/app/shared/](../../web/src/app/shared/)).
2. A class of [_controls.scss](../../web/src/styles/_controls.scss): `btn`, `text-input`, `select-input`, `badge`, `tab-nav`,
   `menu`. The table in [design-language.md](design-language.md#controls) lists each with its markup.
3. A component style that uses only the `--trove-*` tokens of [_tokens.scss](../../web/src/styles/_tokens.scss).

Never write a colour, a font size, a shadow or a radius as a literal in a component. Spacing comes from `--trove-space-1` to
`-8` (4, 8, 12, 16, 20, 24 and 32 px). A value the tokens do not have is a new token, added with its light and dark value, and a
new text and background pair goes into [palette.spec.ts](../../web/src/styles/palette.spec.ts). No new font, shadow or gradient.

## Anatomy of a page

Every page inside the shell has this order:

| Part | How |
|---|---|
| Breadcrumb | Set by the route; a page does not draw its own |
| Header | `<trove-page-header heading description>` ([page-header.ts](../../web/src/app/shared/ui/page-header.ts), [Q-UI-307](../spec/api-and-ui.md#q-ui-307--a-pages-own-actions-are-buttons-at-the-right-end-of-its-header-views-of-the-same-thing-are-tabs)): the `<h1 tabindex="-1">` names the object on its page (`[name]="true"` for a key or a package name) or the list's noun; one muted sentence under it; the facts that name the object projected with `meta`; its client address where it has one (`[addresses]`). A detail page names its object in the `<h1>` and its kind as a badge projected with `meta` (the user `alice` with the badge **User**), and has no link up to its list: that is the breadcrumb. The repository settings, the wizard and the vulnerability pages still have `<header class="page-header">` |
| Primary action | At the right end of the header (projected with `actions`), one `btn btn-confirm` (**Add member**, **Create token**). A second action of the page is a `btn btn-default` to its left; more go into a `more_vert` menu |
| Tabs | Directly under the header, `tab-nav`, the count of a list as a neutral `badge` in the tab. Another view of the same object (its files, its vulnerabilities) is a tab or an entry of the header's menu, never a quiet link in the header. A job has one way to it: the client setup is the header's **Set up client**, which opens the setup page for the object, not also a tab ([Q-UI-321](../spec/api-and-ui.md#q-ui-321--a-repositorys-client-setup-is-reached-one-way-the-headers-set-up-client-opens-the-setup-for-the-repository)) |
| Toolbar of a list | Under the tabs: the filter on the full width, then sort and direction |
| Content | A table, a form or sections, as below |
| Details | What only a power user or an administrator needs, last, in one closed `<trove-disclosure>` **Details** (see [Opening and closing](#opening-and-closing)) |

One `<h1>` per page. Page titles are nouns (**Members**, **Settings**), in sentence case. The description is one sentence;
what would need a second one belongs in the [manual](manual.md).

## Lists

[`project-members.ts`](../../web/src/app/features/projects/project-members.ts) is the worked example.

- A table without a frame, hairlines between rows, the head at weight 600. `<trove-data-table>` where the list is paged.
- **The first column names the thing.** It is a `<th scope="row">`. A person or group is an avatar of 48 px, the name at weight
  600 on the first line and the `@username` or its kind muted on the second.
- **A status is a badge** with an icon and a word, never colour alone. `ok` is green and means a positive status only.
- **A package format is its icon and its name**, never the icon alone: `<trove-format-icon [format]>` beside the display name,
  sized by the place's `font-size` (20 px in a list, 16 px in a badge, 32 px on a tile). The icon is the format owner's logo
  where the owner's terms allow it, else the neutral `inventory_2`
  ([Q-UI-225](../spec/api-and-ui.md#q-ui-225--a-format-shows-its-owners-logo-where-the-owners-terms-allow-it-else-the-neutral-glyph)).
  A logo is never redrawn, recoloured or cropped; on the dark scheme it shows the owner's light variant or stands on the plate
  ([Q-UI-226](../spec/api-and-ui.md#q-ui-226--a-logo-is-an-image-of-the-owners-file-and-the-dark-scheme-shows-the-owners-light-variant-or-a-plate)).
  A new logo is the owner's file unchanged under `web/public/formats/<format>/`, a section in
  [NOTICE.txt](../../web/public/formats/NOTICE.txt) and a case in [format-icon.ts](../../web/src/app/shared/format-icon.ts),
  after its owner's terms were read.
- **Actions on a row go into a menu** behind an `btn btn-tertiary btn-icon` showing `more_vert`, with an `aria-label` that names the row
  (**Actions for user:bob**). A list that has one frequent action per row may show it as a quiet button instead. The repository
  list is the example of a thing with a description: the key, the description as the muted second line, the actions in the
  menu ([Q-UI-302](../spec/api-and-ui.md#q-ui-302--the-repository-list-names-a-repository-by-key-and-description-in-its-first-column-and-puts-the-rows-actions-in-a-menu)).
- **The columns come from the job of the list's kind of user** (R-UI-067, [Q-UI-330](../spec/api-and-ui.md#q-ui-330--a-list-takes-its-columns-from-the-job-of-its-kind-of-user-the-important-properties-are-columns-and-the-address-takes-no-more-than-its-share)). Ask first what the list is for: the
  properties its user decides on get columns of their own, the column that names the thing gets the most room, and nothing is
  packed under the name but its description. The repository lists are the example: in a project (find and copy) repository,
  format, class, client address; in the admin area (manage) repository, project, format, class, last activity, and the address
  as a copy button. Judge a render by that first, and only then by widths and scrolling.
- **The list's budget is its columns' widths** ([Q-UI-308](../spec/api-and-ui.md#q-ui-308--every-list-takes-its-layout-from-the-data-table-a-column-budget-a-least-width-for-its-name-one-alignment-stacked-rows)). A column of `<trove-data-table>` declares its least width
  (`width`, which must hold its header and its longest value on one line: a format with its icon 8 rem, a class 5 rem); the name
  column takes 12 to 16 rem and what the others leave. A list with a client address marks that column `fill`: it is the
  longest address up to its share of 46 characters (Q-UI-300). The least widths of a list sum to at most 48 rem, where its rows
  stack; a list that needs more sets `[stackWide]="true"` and stacks at 52 rem. A column of lower rank is `narrow`: below 64 rem
  of the list's width it is hidden and the others take its room (the list marks its cells `narrow-drop`; the admin area's Last
  activity). The list's width is the table's own (the container `table`),
  and the address's share is of its rows' width without the scrollbar beside them, so a list scrolls sideways neither when
  its rows scroll nor beside the filter panel, which sits beside a list only where the list keeps 71 rem, else between the
  Filters button and the list
  ([Q-UI-327](../spec/api-and-ui.md#q-ui-327--the-filter-panel-sits-beside-a-list-only-where-the-list-keeps-71-rem-and-a-tables-columns-share-its-rows-width-without-the-scrollbar)).
- **One alignment.** Every cell starts at the top of its row; a control in a cell (a button, a select, the address field) is
  centred on the first line of text. A list does not set its own vertical alignment.
- **On a narrow list the rows stack.** Below 48 rem of columns (49 rem of the list with the room of a scrollbar) every data table shows each row as a block: the name and
  its menu, then each cell on a line, named by its column (the table sets `data-label`; `stackLabel: false` where the value says
  it), a cell without a value ("–") left out, the address across the row
  ([Q-UI-306](../spec/api-and-ui.md#q-ui-306--below-48-rem-of-its-width-the-repository-list-stacks-each-row-with-the-address-across-the-row)).
  The head of a stacked list is one row of its sort buttons above the blocks; a header that sorts nothing is for assistive
  technology only ([Q-UI-391](../spec/api-and-ui.md#q-ui-391--a-stacked-list-keeps-its-sort-buttons-in-a-row-above-its-blocks)).
  `[stack]="false"` keeps the table for a list that cannot be read as blocks.
- **An empty list** is `<trove-empty-state text>` with the action that fills it as its content; the data table shows it with its
  `emptyText` and the content marked `emptyAction` ([Q-UI-309](../spec/api-and-ui.md#q-ui-309--an-empty-list-or-region-is-a-sentence-and-the-action-that-fills-it-without-a-frame)).
- **A line of facts** (a source line; never the properties of a list's row, which are columns) separates them with one middle dot, the same space
  on both sides, in the muted colour: `.dot-separated` around its parts, `.dot-before` on one part ([_controls.scss](../../web/src/styles/_controls.scss)).
  Where such a line wraps, no line starts with a dot.
- **Names stay on one line**, except the key of the repository list, which wraps after a hyphen where it is longer than its
  column (`th.wrap-name`, [Q-UI-303](../spec/api-and-ui.md#q-ui-303--the-repository-lists-key-breaks-after-a-hyphen-where-it-is-longer-than-its-column)).
- An inline control in a row (the role select) has no visible label and an `aria-label` that names the row. It shows the current
  value, and a change is saved at once, with the status message of the `Notifier`.
- Filter and order work on what is loaded, or are sent to the server where the list is paged. A filter that finds nothing says
  so in a row of the table; an empty list says so in place of the table and offers the action that fills it.

## Settings pages

The reference's settings page is one column of fields, not a card of key and value. New settings pages follow it, and the
existing ones are moved over ([What does not follow this page yet](#what-does-not-follow-this-page-yet)).

- **One column**, at most `--trove-settings-measure` (56 rem) wide, on the page and without a card around it.
- **The most used settings are open at the top**, without a section title: the name, the description, the avatar.
- **Further groups are collapsible sections** (`<trove-disclosure>`, the one collapsible section of every screen), closed by
  default: a chevron on the left, the title at the text's size, weight 600, one muted sentence under it, a hairline between
  sections. The title names the group (**Permissions**, **Advanced**).
- **Everything is edited in place.** A settings page has no read-only view with an **Edit** button that opens a dialog, and no
  key-value list of values that could be fields.
- **A value that cannot change is a read-only field** with the control's disabled fill and no border (the key of a project, an
  ID), so that it can be selected and copied.
- **Each section has its own save button**, `btn btn-confirm` with **Save changes**, at the end of the section and disabled until
  something changed.
- **Destructive actions come last**, in a section **Advanced**, with a `btn btn-danger` and a sentence that says what it destroys.
  A disabled destructive action says why it is disabled, next to the button.

## Opening and closing

**Everything that folds, folds with an animation** ([Q-UI-215](../spec/api-and-ui.md#q-ui-215--settings-are-fields-in-one-column-and-everything-that-folds-is-animated)), with one duration and easing (`--trove-motion-duration`, 200 ms, and `--trove-motion-easing`). Never write another one, and never hide a region with `[hidden]` or `@if` alone. Pick by the kind of element; the classes are in [_controls.scss](../../web/src/styles/_controls.scss#L1):

| Element | How |
|---|---|
| A region that stays in the page | `<div class="collapse-region" [class.open]="open()"><div>…</div></div>`; the second element is required |
| A collapsible section of any screen: the details of a content page, the groups of a settings page or an editor | `<trove-disclosure heading hint>` ([disclosure.ts](../../web/src/app/shared/ui/disclosure.ts), [Q-UI-322](../spec/api-and-ui.md#q-ui-322--a-closed-section-of-details-is-one-quiet-disclosure-a-small-heading-with-a-chevron-and-one-hint-line)): the heading at the text's size, weight 600, a chevron before it, one muted hint line, closed at first; `[(expanded)]` binds its state. Never a settings section below a list or a README |
| Something added and removed with `@if` | A wrapper with `class="collapse-animated" animate.enter="collapse-in" animate.leave="collapse-out"` around one child |
| A side panel, a drop-down panel, the sidebar | `panel-in`, `panel-out`; `dropdown-in`, `dropdown-out`; `sidebar-in`, `sidebar-out` |
| A native `<details>` | Nothing: it animates by itself where the browser can |
| The chevron of the toggle | `class="expand-chevron"` on `expand_more` inside the button with `aria-expanded`; it turns with the same timing |

The button has `aria-expanded` and `aria-controls`. A closed region is `visibility: hidden`, so nothing in it can be focused. `prefers-reduced-motion` sets the duration to 0 in one place. Dialogs and CDK menus are not folding elements.

## Forms

- The label is above the field, at weight 600, always visible. A placeholder is an example, never the label.
- An optional field says **(optional)** in its label. Required fields are not marked, because they are the normal case.
- Help text is one muted line under the field. An error replaces nothing: it appears below the field, in the error colour, tied
  to it by `aria-describedby`, and the field has `aria-invalid`.
- An error appears only after the field was touched and left, or after a submit of its form was attempted (the form's `submit`,
  or a click on its submit or confirm button). `<trove-text-field>` and `<trove-select-field>` hold this rule
  (`revealRule` in [fields.ts](../../web/src/app/shared/access/fields.ts)), so a form passes its errors in and keeps no state of
  its own; a field outside a form shows its error at once. An error says in words what to type, never the pattern (UI-A-42).
- Field width follows the content: a name half the column, a description the whole column.
- The selects of a panel (the install panel's client and repository) stack and take the panel's width, so they line up whatever
  their options say ([Q-UI-320](../spec/api-and-ui.md#q-ui-320--every-value-to-copy-of-one-line-is-a-field-with-its-joined-copy-button-a-page-shows-one-client-address-and-a-panels-selects-share-one-width)).
- A choice among the values of a list (the version of a package) is a select, so it looks like the choice it is.
- A choice among three or fewer exclusive things, where the reader should see them all, is a radio group, each option with a
  muted sentence under its label. Four or more is a select. A disabled option stays visible, muted, and says why.
- Only valid combinations can be chosen: a control that can reach a state that means nothing is the wrong control
  ([Q-UI-213](../spec/api-and-ui.md#q-ui-213--the-token-form-names-what-a-token-can-be-in-three-choices-and-only-an-administrator-is-offered-the-first)).

## Buttons and dialogs

| Kind | Class | Use |
|---|---|---|
| Primary | `btn btn-confirm` | The one main action of a view. At most one per page header and one per form or dialog |
| Secondary | `btn btn-default` | Other actions with weight |
| Quiet | `btn btn-tertiary` | **Cancel**, row actions, anything repeated down a list |
| Destructive | `btn btn-danger` | Only for the action that destroys, after a confirmation that names the object |
| Icon | `btn btn-icon` with `aria-label` | Toolbar and row actions |

Labels are a verb and its object in sentence case (**Add member**, **Save changes**). All controls are 32 px high; `btn-sm` is not
for general use. In a dialog, the buttons are at the end, **Cancel** first and the action last.

Use a dialog for a short, self-contained step that leaves the page behind it in place (adding a member, confirming a deletion). A
long or repeated form is a page. Open it with `TroveDialog`: focus, Escape and the return of focus come with it.

## Address field

`<trove-address-field>` (`web/src/app/shared/address-field.ts`, [R-UI-065](../spec/api-and-ui.md#r-ui-065--one-read-only-address-field-with-a-joined-copy-button-for-every-client-address)) shows
one line to copy the way the reference shows **Clone with SSH**: a read-only input group. It is the one pattern for a value to
copy of one line, an address or a command
([Q-UI-320](../spec/api-and-ui.md#q-ui-320--every-value-to-copy-of-one-line-is-a-field-with-its-joined-copy-button-a-page-shows-one-client-address-and-a-panels-selects-share-one-width)).

```html
<trove-address-field [value]="address" label="Client address" copyLabel="Copy client address" />
```

| Input | Meaning |
|---|---|
| `value` (required) | The address, one line. An empty value renders nothing |
| `label` | The accessible name of the field; in a list it names the row (**Client address of npm-internal**) |
| `copyLabel` | The accessible name and title of the copy button (**Copy client address of npm-internal**) |

In a list the cell is `<td class="address-cell">` of the column marked `fill`; the list sets on the data table the length of its
longest address, `[style.--trove-address-chars]="addressChars()"`, and the data table sums the room of the other columns from
their `width`s into `--trove-list-others` ([repository-list.html](../../web/src/app/features/repositories/repository-list.html)
is the worked example).

- **When.** The address a client puts into its configuration: a repository's base URL or registry host with a path, a Bastion's
  repository address, an exposure rule's address; and a command of one line (the install command of a package, labelled
  **Install command**). A configuration file or a snippet of several lines keeps the code block with its copy button
  ([Q-UI-272](../spec/api-and-ui.md#q-ui-272--an-address-field-holds-one-address-multi-line-snippets-stay-code-blocks-and-the-bastion-address-becomes-a-field), Q-UI-320).
  A hash (digest, fingerprint) is not an address either: `<code>` with its short form and the full text as `title`.
- **One address in the header.** A page header and a line of the start page show the object's client address, the first of its
  addresses. Its further addresses are in **Details**, each with a visible label that says what it is for (**Address for downloads
  by path (curl, wget)**); two fields without a label make the user guess (Q-UI-320).
- **Measures.** Height `--trove-control-height` (32 px); text `--trove-font-mono` at 0.875rem; padding `--trove-space-1`
  `--trove-space-3`; border 1 px `--trove-control-border` (hover `--trove-control-border-hover`, focus the ring of the other
  controls); fill `--trove-control-bg`, not the grey of `.text-input[readonly]`
  ([Q-UI-274](../spec/api-and-ui.md#q-ui-274--the-field-keeps-the-fill-and-border-of-an-editable-control-not-the-grey-of-a-read-only-value)).
  The button is the 32 px `btn btn-icon` copy button, attached to the input's right edge without a gap, its left border shared
  with the input's, inner corners square, outer corners `--trove-control-radius`.
- **Width: one per place, never per address, never more than its share.** The field fills its line and never takes its
  address's width.
  - In a list every field has one width: the column `td.address-cell` is as wide as the list's longest address in the field's
    face, but at most its share of 46 characters (`--trove-address-share`) and at most what the other columns leave at their
    least widths (`--trove-list-others`), never under 10 rem. The other columns keep their least widths and the name column
    takes the rest. The field fills the cell and shows the address from its start, never its tail; a longer address scrolls
    inside it ([Q-UI-300](../spec/api-and-ui.md#q-ui-300--in-a-list-every-address-field-has-one-width-the-lists-longest-address-up-to-what-the-other-columns-leave), [Q-UI-330](../spec/api-and-ui.md#q-ui-330--a-list-takes-its-columns-from-the-job-of-its-kind-of-user-the-important-properties-are-columns-and-the-address-takes-no-more-than-its-share)).
  - A list whose user does not copy addresses as its job has no field: the admin area's repository list has a quiet
    `<trove-copy-button [quiet]="true">` beside the row's menu, its `label` **Copy client address** with the address, which is
    also the button's tooltip, and the menu offers **Copy client address** ([Q-UI-330](../spec/api-and-ui.md#q-ui-330--a-list-takes-its-columns-from-the-job-of-its-kind-of-user-the-important-properties-are-columns-and-the-address-takes-no-more-than-its-share)).
  - A field alone on its line, or a stack of the addresses of one thing, fills the line up to `--trove-address-measure`
    (40 rem, 70 characters), so a stack is one width ([Q-UI-301](../spec/api-and-ui.md#q-ui-301--a-field-alone-on-its-line-fills-the-line-up-to-a-measure-of-70-characters)).
  - Where the field is shorter than its address, the address scrolls inside it (the input at least 8 rem), the button stays
    attached. Never an ellipsis.
- **Behaviour.** Focus or click selects the whole address; copy uses `trove-copy-button` (clipboard, polite status, alert on failure).
  The address is the input's value, so a test reads `.value`, not the text content.

## Vulnerability grade

A version's vulnerabilities are shown as its grade, the way the owner's cogpit grades an image
([R-VULN-038](../spec/vulnerability-scanning.md#r-vuln-038--the-grade-of-a-version),
[R-UI-090 to R-UI-094](../spec/api-and-ui.md#r-ui-090--the-grade-shield)): a shield with the letter S, A, B, C, D, E or N/A, the
count that decides it ("E · 13 Critical"), the rule it stands for ("More than 5 Critical"), the counts as a severity bar and a
line of facts. Four shared pieces in [web/src/app/shared/vulnerability/](../../web/src/app/shared/vulnerability/) draw it; no
screen draws its own, and no screen shows a version's highest severity instead.

```html
<trove-grade [grade]="item.vulnerabilityGrade" />                       <!-- a list cell, a header, a tab -->
<trove-grade-card [grade]="g" [packageName]="name" [findingVersions]="[version]" [image]="false" />
<trove-severity-bar [counts]="summary.bySeverity" [queries]="filterPerSeverity" />
<trove-grade-shield grade="E" size="card" label="Grade E" />           <!-- alone only; beside its text it is decorative -->
```

| Piece | What it is | Inputs |
|---|---|---|
| `<trove-grade>` | The shield of 20 px (a format icon's size in a list) and "E · 13 Critical"; its tooltip names every count | `grade` (`VulnerabilityGrade` of the API) |
| `<trove-grade-card>` | The shield of 32 px, the text, the rule, the bar and the facts: an image's OS, its end of life (warning colour) and "Scanned 1 h ago (…)", a package's "Checked 3 min ago (…)", and **Findings in** the repository, a link to the report filtered by repository, package and version | `grade`, `packageName`, `findingVersions` (the version, or an image's manifest digests), `image`; `barQueries` and `activeSeverity` make the bar the filter of the page's findings (the package page) |
| `<trove-severity-bar>` | Critical, High, Medium, Low (with None) and Unknown as five segments, each as wide as its share (at least 7 %) plus the room of its count; Unknown hatched; a severity without findings faint, its 0 readable | `counts`; `queries` turns the segments into filter links (the report's header replaces the query with them, `merge` keeps the page's other parameters); `active` marks the segment of the filter in force |
| `<trove-grade-shield>` | The crest as inline SVG with its letter | `grade`, `size` (`list` 20 px, `card` 32 px), `label` |
| `<trove-time-ago>` ([time-ago.ts](../../web/src/app/shared/ui/time-ago.ts)) | One relative time and the absolute time in brackets: "3 min ago (Nov 12, 2026, 2:22:37 PM)" ([Q-UI-378](../spec/api-and-ui.md#q-ui-378--a-time-on-the-grade-card-is-one-relative-time-with-the-absolute-time-in-brackets-from-one-component)) | `at` (RFC 3339) |

- **When.** A list shows `<trove-grade>`; the bar and the facts are the card's, on the detail page (simple by default, details
  on demand, [Q-UI-374](../spec/api-and-ui.md#q-ui-374--a-list-shows-the-shield-and-the-text-the-bar-and-the-facts-are-the-cards)).
  A finding keeps its severity badge (`<trove-severity>`): a grade belongs to a version, a severity to a finding.
- **The grade is the server's.** The API answers `VulnerabilityGrade` ([R-VULN-039](../spec/vulnerability-scanning.md#r-vuln-039--the-grade-in-the-api));
  [grade-words.ts](../../web/src/app/shared/vulnerability/grade-words.ts) words it (text, rule, tooltip) and orders it
  (`gradeRank`, `worstGrade`) and never decides it again. A caller who gets no grade sees no grade column and no card, not N/A
  ([Q-UI-377](../spec/api-and-ui.md#q-ui-377--a-list-without-grades-has-no-grade-column)).
- **Colours.** `--trove-grade-s` to `--trove-grade-e` and `--trove-grade-na`, the inks of the letters, `--trove-sev-*` for the
  bar ([design-language.md](design-language.md#vulnerability-grades)). S, A and B are the only greens beside the positive
  status ([Q-UI-370](../spec/api-and-ui.md#q-ui-370--the-grades-take-cogpits-colours-and-s-a-and-b-are-the-greens-of-a-good-grade)).
  The letter and the text carry the grade; the colour never does alone.
- **Sorting.** A list column of grades sorts by `gradeRank` (the grade, then the count that decides it, N/A lowest); a paged list
  loads up to 1,000 entries first ([Q-UI-373](../spec/api-and-ui.md#q-ui-373--the-list-sorts-by-grade-in-the-browser-over-the-whole-list-up-to-1000-entries)).

## Text

- Sentence case everywhere; upper-case is not used for labels.
- 14 px on 21 px for text and controls, 12 px for small text. Headings use `--trove-heading-1` to `-3`.
- Say what happens, in plain words: **Filter members**, not **Search criteria**. Do not name internal terms where the reader
  needs none: **All repository permissions of the owner, without administration**, not `inherit`.
- A label promises exactly what the thing does. If it does less than it says, the label is a bug.
- Every string has an `i18n` ID and a German translation in `messages.de.xlf`. Keys, names and paths carry `translate="no"`.

## Accessibility

Each of these fails the screen, whatever it looks like: a control without a name, an icon that carries meaning without text, a
state told by colour alone, a focus that is not visible, an element that the keyboard cannot reach. Both schemes must meet the
contrast of WCAG AA. The unit tier can run axe on a rendered screen (`seriousViolations` in [axe.ts](../../web/src/app/testing/axe.ts)); a new screen's spec calls it.

## Checklist

Before a screen is committed:

- [ ] Follows [Simple by default, details on demand](#simple-by-default-details-on-demand): the standard flow holds only what the
      thing needs to work, expert settings and facts are behind a disclosure, defaults are field values, fields fit their input,
      the title names the thing, no field is red before the user touched it, every step or section names one topic.
- [ ] Rendered with `make ui-renders` ([web-ui.md](web-ui.md#renders-of-every-screen)) and every changed screen looked at in
      both schemes and at 390 px, as each kind of user that reaches it; the contact sheet lists no failure, no page that scrolls
      sideways at any width and no region that does at 1024 px and wider, that the change brought, and no list whose address fields differ in width.
- [ ] Compared side by side with the reference's screen for the same job, in a real browser, not only in a unit test.
- [ ] Seen in the dark and the light scheme, and at a narrow window.
- [ ] Only shared controls, classes of `_controls.scss` and `--trove-*` tokens; no colour literal.
- [ ] One `<h1>`, one primary action per view, one way to each job (no button and tab for the same thing).
- [ ] Every value to copy of one line is an address field; a page shows one client address, further ones labelled in Details.
- [ ] Every control named; the keyboard reaches everything; a spec of the screen calls `seriousViolations` and it passes.
- [ ] Every string has an `i18n` ID and a German translation.
- [ ] The spec names the decision, and the manual describes the screen where it did.

## Deliberate differences

These differ from the reference on the owner's decision. Everything else that differs is a defect.

| What | trove | Why |
|---|---|---|
| Green | A positive status only, so the badge **It's you** is blue | [Q-UI-96](../spec/api-and-ui.md#q-ui-96--cogpit-blue-as-the-primary-colour-green-only-for-positive-status) |
| Content width | The panel's full width, the reference centres 1280 px | Owner, 2026-11-06 ([design-language.md](design-language.md#what-this-does-not-cover)) |
| Links | Underlined in running text; in tables only under the pointer | [Q-UI-128](../spec/api-and-ui.md#q-ui-128--the-references-type-colours-and-measures), R-UI-044 |
| Icons | Material Icons, outlined; a package format's icon is its owner's logo where allowed ([Lists](#lists)) | Owner, 2026-11-06 and 2026-11-11 |

## What does not follow this page yet

The guide was written on 2026-11-10 from the owner's screenshots of the reference's member list and general settings. Screens
written earlier were not reviewed against it, so this list is what is known, not all there is.

- Moved to one column ([Q-UI-222](../spec/api-and-ui.md#q-ui-222--which-settings-pages-are-one-column-now-and-what-a-sections-save-button-is-called)): the project's settings ([project-settings.ts](../../web/src/app/features/projects/project-settings.ts), the worked example), the general and the authentication settings, the service account and the group. Where a page has more than one save button, each is **Save changes** with the group in a visually hidden suffix, and sends only its group.
- Moved with [Q-UI-223](../spec/api-and-ui.md#q-ui-223--the-user-the-endpoint-and-the-service-account-and-group-lists-follow-the-guide) and [Q-UI-224](../spec/api-and-ui.md#q-ui-224--the-policy-editor-and-the-issuer-page-keep-their-structure-and-lose-the-cards-and-the-delete-in-the-header): the user, the endpoint, the policy editor and the issuer pages, and the lists of the group and service-account pages. The policy editor keeps its table of contents, its preview and its review before the save; the issuer page keeps its tabs of different objects.
- Not moved yet: the repository's settings ([repository-detail.html](../../web/src/app/features/repositories/repository-detail.html)), a tabbed editor whose tabs are the groups of one document, with a stored-document tab and a move dialog. What its tabs become needs the owner's reference ([Q-UI-222](../spec/api-and-ui.md#q-ui-222--which-settings-pages-are-one-column-now-and-what-a-sections-save-button-is-called)). The exposure rule editor, the mapping editor and the other editors of one object are not settings pages and are not part of this list.

## What this does not cover

- The settings rules and the 56 rem column are read from one screenshot of the reference, not measured from its rendered page; the
  values of Q-UI-128 come from its admin dashboard and user list.
- Nothing enforces this page except the lint rules of R-UI-046 and the review before a commit. Whether a screen matches the reference
  cannot be tested automatically.
