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
| Header | `<header class="page-header">`: the `<h1 tabindex="-1">` on the left, with one muted sentence below it that says what the page is for |
| Primary action | At the right end of the header, one `btn btn-confirm` (**Add member**, **Create token**). A second action of the page is a `btn btn-default` to its left |
| Tabs | Directly under the header, `tab-nav`, the count of a list as a neutral `badge` in the tab |
| Toolbar of a list | Under the tabs: the filter on the full width, then sort and direction |
| Content | A table, a form or sections, as below |

One `<h1>` per page. Page titles are nouns (**Members**, **Settings**), in sentence case. The description is one sentence;
what would need a second one belongs in the [manual](manual.md).

## Lists

[`project-members.ts`](../../web/src/app/features/projects/project-members.ts) is the worked example.

- A table without a frame, hairlines between rows, the head at weight 600. `<trove-data-table>` where the list is paged.
- **The first column names the thing.** It is a `<th scope="row">`. A person or group is an avatar of 48 px, the name at weight
  600 on the first line and the `@username` or its kind muted on the second.
- **A status is a badge** with an icon and a word, never colour alone. `ok` is green and means a positive status only.
- **Actions on a row go into a menu** behind an `btn btn-tertiary btn-icon` showing `more_vert`, with an `aria-label` that names the row
  (**Actions for user:bob**). A list that has one frequent action per row may show it as a quiet button instead.
- An inline control in a row (the role select) has no visible label and an `aria-label` that names the row. It shows the current
  value, and a change is saved at once, with the status message of the `Notifier`.
- Filter and order work on what is loaded, or are sent to the server where the list is paged. A filter that finds nothing says
  so in a row of the table; an empty list says so in place of the table and offers the action that fills it.

## Settings pages

The reference's settings page is one column of fields, not a card of key and value. New settings pages follow it, and the
existing ones are moved over ([What does not follow this page yet](#what-does-not-follow-this-page-yet)).

- **One column**, at most 56 rem wide, on the page and without a card around it.
- **The most used settings are open at the top**, without a section title: the name, the description, the avatar.
- **Further groups are collapsible sections** (`<trove-settings-section>`), closed by default: a chevron on the left, the title at `--trove-heading-2` weight
  600, one muted sentence under it, a hairline between sections. The title names the group (**Permissions**, **Advanced**).
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
| A collapsible section of a page | `<trove-settings-section heading description>` ([settings-section.ts](../../web/src/app/shared/ui/settings-section.ts)) |
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
- Field width follows the content: a name half the column, a description the whole column.
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

- [ ] Compared side by side with the reference's screen for the same job, in a real browser, not only in a unit test.
- [ ] Seen in the dark and the light scheme, and at a narrow window.
- [ ] Only shared controls, classes of `_controls.scss` and `--trove-*` tokens; no colour literal.
- [ ] One `<h1>`, one primary action per view.
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
| Icons | Material Icons, outlined | Owner, 2026-11-06 |

## What does not follow this page yet

The guide was written on 2026-11-10 from the owner's screenshots of the reference's member list and general settings. Screens
written earlier were not reviewed against it, so this list is what is known, not all there is.

- No other settings page has been moved to one column yet; the worked example is [project-settings.ts](../../web/src/app/features/projects/project-settings.ts).

## What this does not cover

- The settings rules and the 56 rem column are read from one screenshot of the reference, not measured from its rendered page; the
  values of Q-UI-128 come from its admin dashboard and user list.
- Nothing enforces this page except the lint rules of R-UI-046 and the review before a commit. Whether a screen matches the reference
  cannot be tested automatically.
