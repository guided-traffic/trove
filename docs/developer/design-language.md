# Design language

The look of the UI: what it is made of, which values carry it in the light and the dark scheme, and where those
values live. Decision [Q-UI-88](../spec/api-and-ui.md#q-ui-88--the-design-language-and-its-two-colour-schemes);
since [Q-UI-128](../spec/api-and-ui.md#q-ui-128--the-references-type-colours-and-measures) the colours, the type and the
measures are the reference's, the owner's GitLab, where [Q-UI-96](../spec/api-and-ui.md#q-ui-96--cogpit-blue-as-the-primary-colour-green-only-for-positive-status)
had cogpit's blue; the shell and list layout it dresses is [Q-UI-68](../spec/api-and-ui.md#q-ui-68--layout-of-the-application-shell-and-the-list-screens)
and [Q-UI-127](../spec/api-and-ui.md#q-ui-127--the-admin-area-has-a-sidebar-of-its-own-behind-an-admin-button-and-the-shell-takes-the-references-frame).
The tokens and how components use them are in [web-ui.md](web-ui.md#design-tokens). How to build a screen with them is in [ui-style-guide.md](ui-style-guide.md).

## Where it comes from

On 2026-11-01 the owner sent two example screens, a dashboard in a dark scheme and a list screen in a light one, and asked
that trove take their design language and colours, with both schemes. The screens themselves are not in the repository.
Every colour below was measured on them (the most frequent pixel value of a region), not estimated; where a measured
value misses WCAG AA, the table says so and names the value trove uses instead.

On 2026-11-02 the owner replaced the screens' green accent with the blue of his other product, cogpit (the dark and
light palettes of `internal/ui/fyneui/theme.go` in guided-traffic/cogpit), and kept green for a positive status alone
(Q-UI-96). The surfaces, borders, chrome and type of the screens stay as measured.

On 2026-11-06 the owner made his GitLab the reference for colours, type and measures (Q-UI-128): "Ich will dass du das
Design (auch Schriftgröße) von hier übernimmst", in its admin area. The values were read from the rendered page of his
instance (GitLab 19.0.1, neutral navigation theme) in the dark scheme: the computed styles of the frame, the sidebar, the
breadcrumb bar, the cards, buttons, headings and lists, and the instance's 1068 `--gl-*` custom properties. The light
scheme's semantic colours come from the MIT-licensed token set of `@gitlab/ui` 134.1.3, whose dark tokens agree with the
instance's for every colour (1044 of 1064 properties; the 20 others are font stacks and empty letter spacings); the light
frame's values (`--body-background-color`, the sidebar's current-entry tint) are the instance stylesheet's own rules for
the light scheme, which it ships beside the dark ones. Nothing of GitLab's code, name or logo is in trove: the values are
written into trove's own tokens.

## What the language is made of

- **A frame around the work, the work as a panel.** The top bar and the full-height side bar share one fill; the content
  is a panel set into them with 16 px corners and an 8 px gap to the window's right and bottom edge, without a border
  (Q-UI-127, Q-UI-128). The frame is `#ececef` around white panels in the light scheme and `#28272d` around `#18171d`
  panels in the dark one.
- **One accent, blue.** The reference's blue marks what is active or primary: the confirm button (`#1f75cb`, `#63a6e9`),
  links (`#2f5ca0`, `#9dc7f1`), the focus ring, the current navigation entry (a blue tint, its text in a deep or pale step
  of the blue), the selected tab's underline, and as a pale or deep fill a selected row. The accent is not a status.
- **Green is a positive status, and nothing else.** "Active", "ok", "success", "allowed", the added lines of a diff:
  `--trove-status-ok` beside its icon. No other token is green (the unit tier checks it), so green always means "fine".
- **Flat surfaces.** A card is a strong fill (`#ececef`, `#3a383f`) 12 px round with 4 px inside, its title on that fill
  and its content on the page's fill 8 px round; tables and lists are separated by hairlines (`#dcdcde`, `#4c4b51`), not
  by shadows. Controls and menus are 8 px round, dialogs and panels 16 px.
- **Dense, quiet type.** Inter with the reference's lower-case l (`cv05`), 14 px on 21 px at weight 400 for text and
  controls, 12 px for small text (breadcrumbs, counts, dates); sidebar entries at weight 425, the current one at 500;
  headings at 600 with a hair less tracking, page titles 24 to 30 px and section titles 21 to 25 px by the window's width.
  Muted grey (`#626168`, `#bfbfc3`) for secondary text. Upper-case is not used for labels.
- **Data colours beside the accent.** The screens' charts use green, a muted teal and a violet. Green marks a positive
  status only (Q-UI-96), so a chart in trove does not take it for a series; the teal and violet never mark state.
- **Status as a badge.** A status is an icon and a word ("Active" with a check in a circle) on a pill in the status's
  colour, the reference's badge (Q-UI-128); a line of text that reports a state (a note, a hint) keeps the status colour
  as text.
- **Links.** The link colour, underlined in running text so a link is not told apart by colour alone; in tables and
  key-value lists underlined only under the pointer and on focus (Q-UI-128). There a link is an inline-block box
  (`.content :is(table, dl) a` in `_shell.scss`), so axe's `link-in-text-block` does not count a cell with a link and
  a badge or a second line as running text (Q-UI-148); text that is real running text inside a cell needs the underline.

## The two schemes

| Role | Reference light | Reference dark | trove light | trove dark | Notes |
|---|---|---|---|---|---|
| Page, panel | `#ffffff` | `#18171d` | `#ffffff` | `#18171d` | `--trove-surface` |
| Frame (top bar, sidebar) | `#ececef` | `#28272d` | `#ececef` | `#28272d` | `--trove-nav-bg`; the reference's `--body-background-color` of the neutral theme |
| Card, licence bar | `#ececef` | `#3a383f` | `#ececef` | `#3a383f` | `--trove-card-bg`; the content inside on the page's fill |
| Table head | `#fbfafd` | `#28272d` | `#fbfafd` | `#28272d` | `--trove-table-head-bg`, the subtle fill |
| Row under the pointer | `#e9f3fc` | `#1d283e` | `#e9f3fc` | `#1d283e` | `--trove-row-hover` |
| Menu, dialog | `#ffffff` | `#28272d` | `#ffffff` | `#28272d` | `--trove-menu-bg` (menus), `--trove-dialog-bg` (dialogs); menus with a `#bfbfc3` / `#4c4b51` hairline |
| Hairline | `#dcdcde` | `#4c4b51` | `#dcdcde` | `#4c4b51` | `--trove-border`; decoration, no contrast rule |
| Field outline | `#89888d` | `#737278` | `#89888d` | `#737278` | `--trove-control-border`: 3.5:1 and 3.7:1 against the page |
| Text | `#3a383f` | `#ececef` | `#3a383f` | `#ececef` | 11.6:1 and 15.1:1 |
| Heading | `#18171d` | `#ffffff` | `#18171d` | `#ffffff` | `--trove-heading` |
| Muted text | `#626168` | `#bfbfc3` | `#626168` | `#bfbfc3` | 6.1:1 and 9.7:1 |
| Confirm button | `#1f75cb`, white text | `#63a6e9`, `#18171d` text | `#1f75cb` | `#63a6e9` | `--trove-accent-fill`; 4.7:1 and 6.9:1; hover `#2f68b4` / `#9dc7f1` |
| Accent as text, focus ring | `#1f75cb` | `#63a6e9` | `#2f68b4` | `#66a8ea` | `--trove-accent`, moved in its hue: `#1f75cb` is 4.0:1 on a filled field (light), `#63a6e9` 4.49:1 (dark); now 4.7:1 and 4.6:1 |
| Link | `#2f5ca0` | `#9dc7f1` | `#2f5ca0` | `#9dc7f1` | `--trove-link`; 6.6:1 and 10.1:1 |
| Default button | white, `#bfbfc3` hairline | `#89888d` at 40 %, no hairline | as measured | as measured | `--trove-btn-default-*`; the Admin button on the frame: 11.6:1 and 7.0:1 |
| Selection fills | `#e9f3fc` | `#1d283e` | `#e9f3fc` | `#1d283e` | `--trove-accent-container`: the reference's info fill |
| Current navigation entry | `#1f75cb` at 16 %, text `#0a335c` | `#68a8e8` at 25 %, text `#cae0f7` | as measured | as measured | `--trove-nav-active`, `--trove-nav-active-fg` (also the area's title); label at weight 500; 8.9:1 and 7.0:1 |
| Entry under the pointer | `#050506` at 6 % | `#ffffff` at 8 % | as measured | as measured | `--trove-nav-hover`, also the top bar's quiet buttons |
| Search field in the top bar | white, hairline `#050506` at 24 % | `#ffffff` at 8 %, hairline `#ffffff` at 16 % | white, outline `#858489` | as measured, outline `#737278` | `--trove-search-*`, 12 px round; the reference's hairlines are 1.7:1 against the frame, trove's outline 3.1:1 and 3.1:1 (WCAG 1.4.11) |
| Positive, warning, error status | `#2f7549`, `#995715`, `#c02f12` | `#52b87a`, `#d99530`, `#f6806d` | as measured | as measured | `--trove-status-ok`, `-warn`, `-bad`: the reference's text colours for success, warning and danger |
| Data: teal, violet | `#347da0`, `#8577e6` | `#347da0`, `#8577e6` | `#347da0`, `#8577e6` | `#5cb3d6`, `#a99cff` | the vulnerability trend's low and unknown, from the first reference screens (Q-UI-88), unchanged |

Since Q-UI-127 the top bar and the side bar share one fill; the panel's fill and rounded corners set the content off from
the frame instead of a step between the two bars.

No component library supplies a palette: every colour a style reads is a token of
[_tokens.scss](../../web/src/styles/_tokens.scss) (Q-UI-126). The error, the inverse (the tooltip) and the banner
colours that Angular Material's generated palette used to provide are literal `light-dark()` pairs there, with the values
the screens had.

## The two-pane browser

The file browser (`/projects/<key>/browse`, [Q-UI-98](../spec/api-and-ui.md#q-ui-98--the-file-browser-is-two-panes-with-a-toolbar-and-a-details-header))
is the one screen built from two panes; a screen that browses a hierarchy beside its details takes the same pattern.
Its styles are the global partial [_browse.scss](../../web/src/styles/_browse.scss).

- Two flat bordered surfaces (`--trove-surface-raised`, the hairline border, the 6 px radius) side by side, each `max(26rem,
  100dvh - 13.5rem)` high so the page does not scroll at 1280 x 800 and each pane scrolls on its own. The tree pane is
  `clamp(18rem, 30%, 26rem)`; the details take the rest.
- A pane head on the table-head fill: in the tree pane the filter field, then the shared filter toggle, a sort icon button and
  a count; in the details pane the path as a breadcrumb and the actions as a toolbar. Applied filters are the shared chips under
  the head. The shared filter panel opens inside the tree pane in place of the tree, without its own border or shadow.
- A tree row is 32 px high: a chevron, an icon in the muted tone, the name, then small outlined badges (11 px) for the format
  and the labels "cache", "resolves from" and "disabled". The selected row is `--trove-accent-container`; the focus ring is the
  solid 2 px ring inside the row.
- A folder's children use the shared data table, whose scroll region is capped below the pane height so its header stays
  sticky.
- Under 48 rem of content width (the `content` container query) one pane shows at a time and the details get a back button.

## Controls

Since 2026-11-06 the controls are trove's own, not Angular Material's
([Q-UI-126](../spec/api-and-ui.md#q-ui-126--troves-own-controls-in-place-of-angular-material)): the owner asked for the
design language of a sign-in page he showed, flat controls with the labels above the fields. They are plain HTML elements
with the classes of [_controls.scss](../../web/src/styles/_controls.scss), and the composite widgets take their behaviour
from the CDK and `@angular/aria`. The move is finished: `@angular/material` is no dependency of the web UI any more, and
no screen carries a Material control or a `--mat-sys-*` token. Their measures and colours are the reference's (Q-UI-128):
the confirm button, the default button with its grey fill, the quiet one in the text colour, all 32 px and 8 px round
with the label at weight 400; a tab's label is in the text colour at 14 px, the current one at weight 600 over a 2 px line
in `--trove-tab-indicator` (`#1f75cb`, `#428fdc`), the strip on a hairline. The shared data table has the reference's shape: no frame, on the
page's fill, 16 px in every cell, the head in the heading colour at weight 600 between two hairlines, a hairline under
every row.

| Control | Markup | Look |
|---|---|---|
| Primary button | `<button class="btn btn-confirm">` | the accent fill (`--trove-accent-fill`), `--trove-on-accent` text, a deeper or lighter step of the hue on hover |
| Secondary button | `class="btn btn-default"` | white with a `#bfbfc3` hairline, `#ececef` and a `#89888d` hairline on hover (light); `#89888d` at 40 % without a hairline, 64 % on hover (dark) |
| Quiet button | `class="btn btn-tertiary"` | text only, a neutral veil on hover (`#a4a3a8` at 16 %, `#89888d` at 40 %) |
| Destructive button | `class="btn btn-danger"` | `--trove-danger-fill` (the bad status's hue) |
| Icon button | `class="btn btn-icon"` with an `aria-label` | 32 × 32, text only |
| Modifiers | `btn-block`, `btn-sm` | full width; 24 px high at the same text size |
| Text field, select, area | `<label class="field-label" for>` above `<input class="text-input">`, `select-input`, `area-input` | `--trove-control-bg` (white; `#050506` at 40 % over the page in the dark scheme), a 1 px `--trove-control-border` (3.5:1 light, 3.7:1 dark against the page, WCAG 1.4.11) that darkens or lightens under the pointer, the semibold label above |
| Tabs that are links | `<nav class="tab-nav">` with `<a class="tab-link" aria-current="page">` | the reference's tab strip: labels in the text colour, 16 px above and below; the current one at weight 600 over a 2 px `--trove-tab-indicator` line; a neutral veil and a `--trove-border-strong` line under the pointer; the strip on a hairline (the package views) |
| Badge | `<span class="badge">` (neutral), with `ok`, `warn`, `bad` or `info`; every `<span class="ok\|warn\|bad">` that holds an icon and a word | the reference's badge: a pill 20 px high, 12 px text at weight 400, a 12 px icon 4 px before it; `--trove-badge-*-bg`, `-fg`, `-icon`: a pale fill with deep text (light), a full fill with near-black text (dark) |
| Checkbox, radio | native `<input type="checkbox">`; `<trove-checkbox>` ([checkbox.ts](../../web/src/app/shared/ui/checkbox.ts)) for a checkbox with its label as content, an optional `describedBy` note and an `ariaLabel` for a row's box | 16 px, `accent-color` the accent; the input sits inside its `label.check-row`, so the whole label is the click target |
| Tabs that switch a view | `<trove-tab-group>` with `<trove-tab label>` ([tabs.ts](../../web/src/app/shared/ui/tabs.ts)); `<ng-template troveTabContent>` for lazy content | the strip of the row above (`.tab-nav`, `.tab-link`, selected by `aria-selected`); the roles, arrow keys, Home and End and the single tab stop are `@angular/aria`'s tabs, the selected tab's panel is a `tabpanel` named by its tab; only the selected tab's content is in the page |
| Dialog | `TroveDialog.open(Component, { data, width, maxWidth, disableClose })` with `<h2 troveDialogTitle>`, `<div class="dialog-body">`, `<div class="dialog-actions">` and `troveDialogClose` on a closing button ([dialog.ts](../../web/src/app/shared/ui/dialog.ts), [_dialog.scss](../../web/src/styles/_dialog.scss)) | the CDK dialog: `role="dialog"`, `aria-modal`, named by its title, the focus trapped inside and returned to the opener, Escape and the backdrop close it unless `disableClose`; the panel on `--trove-dialog-bg`, 16 px round, the title in the heading-3 size, the body scrolling between title and actions, the actions right-aligned |
| Menu | `cdkMenuTriggerFor` and `<div class="menu" cdkMenu>` with `class="menu-item" cdkMenuItem` (or `cdkMenuItemRadio`) | the CDK menu, in `_shell.scss`; the user menu, the sort menu of the browser and the mirror's run menu |
| Tooltip | `[troveTooltip]="text"` ([tooltip.ts](../../web/src/app/shared/ui/tooltip.ts)) | a small bubble on `--trove-inverse-surface` under the element, on pointer and keyboard focus, closed by Escape, blur or leaving it (WCAG 1.4.13); the element is always described by the text (the CDK's hidden description), the bubble itself is `aria-hidden` |
| Text field, select with label and error | `<trove-text-field>`, `<trove-select-field>` ([fields.ts](../../web/src/app/shared/access/fields.ts)) | the label above, `text-input` / `area-input` / `select-input`, the hint and the error as text below, joined by `aria-describedby`, with `aria-invalid` on an error |
| Focus | every control | `--trove-focus-ring` (2 px solid accent), offset 2 px for buttons and 1 px for fields |

One height (`--trove-control-height`, 32 px) and one radius (`--trove-control-radius`, 8 px) for every control, the label at
14 px and weight 400 with a 16 px icon 4 px before it, as the reference sets its buttons. A disabled
control is `--trove-control-disabled-bg` with `--trove-control-disabled-fg` (WCAG exempts disabled controls from the
contrast rule). The screens without the shell (sign-in, signed out) are one column of at most 25rem in the middle of the
page, under the wordmark (`.bare`, `.bare-panel`, `.bare-wordmark` in `_shell.scss`, Q-UI-125).
[palette.spec.ts](../../web/src/styles/palette.spec.ts) computes every label on its button fill (at rest and under the
pointer), the field's text and placeholder on the field (4.5:1), and the field outline and the primary fill against the
page (3:1), in both schemes.

## Logo

In design with the owner since 2026-11-06; nothing below is final. The mark is the "v" of trove drawn as a flow: the left
arm is one solid band, a package coming in once (an upload from CI, or the first fetch through a proxy); the point at the
bottom is trove, the store; the right arm is the band split into lanes that fan out, the requests trove serves from then
on. Lanes of different colours are the idea the owner brought from a game logo's parallel stripes. The state of the draft
(owner, 2026-11-06): the band is slimmer than the lanes' bundle (26 against 44, as slim as two lanes) and has the
wordmark's colour (`#e8eaec` on a dark ground, `#1c2433` on a light one), so the v sits in the lockup like a letter of the
name; three lanes in flat neon tones, from the inside out blue (`#1fb6ff`), the band's neutral colour, and red
(`#ff2d55`); a halo was tried and dropped (it read as washed out), and so was green for the inner lane. The red lane
starts beneath the middle one (drawn first, from its start), runs hidden under it until half its height and curves to
the right from there.

[web/scripts/brand-logo.mjs](../../web/scripts/brand-logo.mjs) generates every file from one set of parameters (band
width, lane count, gap, fan, the lane shape) into [web/public/brand/](../../web/public/brand/): the mark, the square app
icon with the wordmark (dark ground in both schemes), and the horizontal lockup. The mark and the lockup switch to deeper
hues and the dark band on a light ground through `prefers-color-scheme` inside the SVG. The wordmark is outlined paths of
Inter Bold (one `<path class="word">`), not `<text>`, so it renders the same on every platform: the script reads
`inter-latin-700-normal.woff` of `@fontsource/inter` with `opentype.js` (both dev dependencies, pinned exactly, used only by
the script; the static file is the same Inter release as the UI's variable font, because opentype.js reads no `.woff2`) and
sets the word with the font's advances and the tracking the old text had (6 and 4 units). The pair kerning comes from
`Font.getKerningValue`, which returns 0 for the pairs of "trove".
[Q-UI-145](../spec/api-and-ui.md#q-ui-145--the-logos-wordmark-as-paths-from-inter-bold-and-a-reduced-mark-as-the-favicon)
has the options.

The favicon is a reduced form of the mark, because the three lanes run together below 32 px: two lanes (blue and red; the
neutral one is the band's colour) with a band of 48 and lanes of 34 units, the red lane still curving out from beneath the
blue one, on the app icon's dark tile in both schemes (`reduced` and `faviconSvg` in the script). The script writes
[web/public/favicon.svg](../../web/public/favicon.svg) and [web/public/favicon.ico](../../web/public/favicon.ico) (PNG
frames of 16, 32 and 48 px, rasterised by the script itself from the same shapes, so no image tool is needed and the bytes
are deterministic); [index.html](../../web/src/index.html) links the SVG first and the ICO as the fallback.

The sign-in and signed-out screens show the lockup; in development builds (`isDevMode()`) the sign-in screen also shows the
preview of [logo-preview.ts](../../web/src/app/features/login/logo-preview.ts): the three forms on both grounds, the full
mark and the favicon's reduced form side by side at 64, 32 and 16 px, and the alternatives of `drafts/`. Change a
parameter, run `node scripts/brand-logo.mjs` in `web/`, and the dev server reloads. The same run rewrites
`shared/brand/mark-geometry.ts` without the repository's formatting: run Prettier on it when the geometry changed.

## Where the values live

| File | What it holds |
|---|---|
| [web/src/styles/main.scss](../../web/src/styles/main.scss) | The partials in order, the CDK's overlay structure (`cdk.overlay()`, which dialogs, menus and tooltips open in), the body type and the page title |
| [web/angular.json](../../web/angular.json) | `@fontsource-variable/inter/wght.css` ahead of `main.scss` in the build's styles: Inter as one variable font (weights 100 to 900) in unicode-range subsets, so a page in Latin script loads the 48 KB Latin file; OFL-1.1, its licence in the build's `3rdpartylicenses.txt` |
| [web/src/styles/_tokens.scss](../../web/src/styles/_tokens.scss) | trove's own properties: the page palette (`--trove-surface`, `--trove-on-surface`, `--trove-border`, `--trove-text-muted`, `--trove-accent`, `--trove-dialog-bg`, `--trove-error*`, `--trove-inverse-*`, `--trove-banner-*`), the type (`--trove-font-*`, `--trove-text-*`, `--trove-heading-*`, `--trove-type-*` as font shorthands), the radii (`--trove-radius-s` to `-full`), the link, the heading colour, the focus ring, the raised surface, the card, the table head, the frame (`--trove-nav-*`, the panel's radius and gap, the breadcrumb bar's height), the search field, the menus, the status colours |
| [web/src/styles/_controls.scss](../../web/src/styles/_controls.scss) | The controls of the table above, from the `--trove-control-*`, `--trove-accent-fill`, `--trove-btn-*` and `--trove-danger-*` tokens |
| [web/src/styles/_dialog.scss](../../web/src/styles/_dialog.scss) | The dialog panel: backdrop, container, title, body, actions |
| [web/src/styles/_shell.scss](../../web/src/styles/_shell.scss) | How the frame uses them: the sidebar's entries (28 px, 8 px round, a 16 px icon in a 24 px box), the current entry, the project switcher on top of a project's sidebar (a 40 px button with the project's initial in a 32 px rounded square on `--trove-accent-fill`, a panel on the menu's fill, [Q-UI-129](../spec/api-and-ui.md#q-ui-129--the-project-is-the-context-of-the-content-chosen-from-your-projects)), the panel, the breadcrumb bar (40 px), the top bar's actions and the avatar (32 px round on `--trove-surface-variant`, holding the user's generated Bottts robot of `<trove-avatar>` instead of a letter, owner 2026-11-07, Q-UI-168; the SVG is transparent, so the fill follows the theme), the menus |
| [web/scripts/build-embed.mjs](../../web/scripts/build-embed.mjs) | The notices of the bundled software: Angular's `3rdpartylicenses.txt` (the Inter font's OFL-1.1 notice among the libraries') plus the icon font's Apache-2.0 licence, which Angular does not collect; written as `<locale>/3rdpartylicenses.txt` into the embedded build, and the build fails when either licence is missing (Q-UI-144) |
| [web/src/styles/palette.spec.ts](../../web/src/styles/palette.spec.ts) | The contrast of the pairs below, computed from the two stylesheets, and the rule that only `--trove-status-ok` is green |

A scheme is chosen by `color-scheme` on the root element: without a choice the browser's setting decides, the theme button
of the top bar sets `data-theme="light"` or `"dark"` (R-API-021). Every colour is a `light-dark()` pair, so a component
never asks which scheme is on.

## Changing a colour

1. Change the pair in `_tokens.scss`, never in a component.
2. Recompute the contrast of every pair it takes part in (text 4.5:1, large text and marks 3:1) and update the numbers in
   the comment above the token and in the table here. A text tone is not only on the page: a muted fill is
   `--trove-surface-variant` (`#ececef` in the light scheme, `#3a383f` in the dark one), a dialog sits on
   `--trove-dialog-bg`, and a selected row on `--trove-accent-container`. The accent has to pass on those too (found by axe when the accent was `#1d7a43`: 4.3:1 on the field; and
   the reason cogpit's `#0a6fd6` is not the light accent: 4.0:1 there). `palette.spec.ts` recomputes the pairs in
   `make web-test` and fails below the minimum; a new surface under the accent or the link gets a pair there.
3. Run the browser journeys ([e2e.md](e2e.md#browser-journeys)): they check `color-contrast` with axe in the light scheme
   (English) and the dark one (German). The jsdom tier does not compute colours.

## Contrast of the text tones on the surfaces they sit on

Computed on 2026-11-06 from the values in `main.scss` and `_tokens.scss` (WCAG 2 relative luminance; a translucent fill
is composited over the surface beneath it in sRGB, as a browser paints it), for the same pairs `palette.spec.ts` holds.
The browser tier's axe `color-contrast` confirmed the green palette of Q-UI-88 on 2026-11-01 (a local trove process, the
break-glass administrator, 108 checks: 28 routes with the manual's pages, the new-group dialog, the new-token form
unfocused, focused and invalid, a form field touched while empty, the help opened by "Documentation", and the list, detail and
audit screens with data, light in English and dark in German; no violation and no "incomplete" result). The palettes of
Q-UI-96 and Q-UI-128 are computed only: the browser tier has not run against them yet.

| Pair | Light | Dark |
|---|---|---|
| Accent as text on the page, the filled field, a dialog | 5.6:1, 4.7:1, 5.6:1 | 7.1:1, 4.6:1, 5.9:1 |
| Text on the confirm button (`--trove-on-accent` on `--trove-accent-fill`) | 4.7:1 | 6.9:1 |
| Link on the page, on the current audit row | 6.6:1, 5.9:1 | 10.1:1, 8.3:1 |
| Focus ring on the page, the filled field | 5.6:1, 4.7:1 | 7.1:1, 4.6:1 |
| Current navigation entry: its text on its tint, the area title on the frame | 8.9:1, 10.9:1 | 7.0:1, 10.9:1 |
| Admin button: its label on the default fill over the frame | 11.6:1 | 7.0:1 |
| Positive status on the page, on the current audit row | 5.6:1, 5.0:1 | 7.2:1, 6.0:1 |

<details>
<summary>Every pair, computed</summary>

| Foreground | On | Needs | Light | Dark | Where |
|---|---|---|---|---|---|
| `primary` | page | 4.5:1 | `#2f68b4` on `#ffffff`: 5.6:1 | `#66a8ea` on `#18171d`: 7.1:1 | accent text: focused label, outlined and text button, tab label, access tabs |
| `primary` | raised | 4.5:1 | `#2f68b4` on `#ffffff`: 5.6:1 | `#66a8ea` on `#18171d`: 7.1:1 | current entry of the help contents |
| `primary` | table head | 4.5:1 | `#2f68b4` on `#fbfafd`: 5.4:1 | `#66a8ea` on `#28272d`: 5.9:1 | button label in a table head |
| `primary` | row hover | 4.5:1 | `#2f68b4` on `#e9f3fc`: 5.0:1 | `#66a8ea` on `#1d283e`: 5.9:1 | button label in a hovered row |
| `primary` | menu | 4.5:1 | `#2f68b4` on `#ffffff`: 5.6:1 | `#66a8ea` on `#28272d`: 5.9:1 | menu and select panel |
| `primary` | dialog | 4.5:1 | `#2f68b4` on `#ffffff`: 5.6:1 | `#66a8ea` on `#28272d`: 5.9:1 | focused label and button label in a dialog |
| `primary` | filled field | 4.5:1 | `#2f68b4` on `#ececef`: 4.7:1 | `#66a8ea` on `#3a383f`: 4.6:1 | focused label of a filled form field |
| `on-primary` | primary | 4.5:1 | `#ffffff` on `#2f68b4`: 5.6:1 | `#18171d` on `#66a8ea`: 7.1:1 | filled button, step number, filter count |
| `--trove-link` | page | 4.5:1 | `#2f5ca0` on `#ffffff`: 6.6:1 | `#9dc7f1` on `#18171d`: 10.1:1 | link in the content, breadcrumb, link button |
| `--trove-link` | raised | 4.5:1 | `#2f5ca0` on `#ffffff`: 6.6:1 | `#9dc7f1` on `#18171d`: 10.1:1 | help card title |
| `--trove-link` | table head | 4.5:1 | `#2f5ca0` on `#fbfafd`: 6.4:1 | `#9dc7f1` on `#28272d`: 8.4:1 | help page contents ("On this page") |
| `--trove-link` | row hover | 4.5:1 | `#2f5ca0` on `#e9f3fc`: 5.9:1 | `#9dc7f1` on `#1d283e`: 8.3:1 | link in a hovered row |
| `--trove-link` | dialog | 4.5:1 | `#2f5ca0` on `#ffffff`: 6.6:1 | `#9dc7f1` on `#28272d`: 8.4:1 | link in a dialog |
| `--trove-link` | selected row | 4.5:1 | `#2f5ca0` on `#e9f3fc`: 5.9:1 | `#9dc7f1` on `#1d283e`: 8.3:1 | link in the current audit row |
| `on-secondary-container` | selected row | 4.5:1 | `#18171d` on `#e9f3fc`: 15.9:1 | `#ececef` on `#1d283e`: 12.5:1 | selected tree item, active access tab |
| `on-surface` | selected row | 4.5:1 | `#3a383f` on `#e9f3fc`: 10.3:1 | `#ececef` on `#1d283e`: 12.5:1 | cell text in the current audit row |
| `on-surface-variant` | selected row | 4.5:1 | `#626168` on `#e9f3fc`: 5.5:1 | `#bfbfc3` on `#1d283e`: 8.0:1 | muted text in the current audit row |
| `on-primary-container` | primary-container | 4.5:1 | `#18171d` on `#e9f3fc`: 15.9:1 | `#ececef` on `#1d283e`: 12.5:1 | text on primary-container |
| `inverse-primary` | inverse surface | 4.5:1 | `#66a8ea` on `#28272d`: 5.9:1 | `#2f68b4` on `#ececef`: 4.7:1 | action of a snack bar |
| `on-surface` | page | 4.5:1 | `#3a383f` on `#ffffff`: 11.6:1 | `#ececef` on `#18171d`: 15.1:1 | text |
| `on-surface` | raised | 4.5:1 | `#3a383f` on `#ffffff`: 11.6:1 | `#ececef` on `#18171d`: 15.1:1 | text on a card |
| `on-surface-variant` | page | 4.5:1 | `#626168` on `#ffffff`: 6.1:1 | `#bfbfc3` on `#18171d`: 9.7:1 | muted text, field label |
| `on-surface-variant` | filled field | 4.5:1 | `#626168` on `#ececef`: 5.2:1 | `#bfbfc3` on `#3a383f`: 6.3:1 | field label |
| `on-surface-variant` | dialog | 4.5:1 | `#626168` on `#ffffff`: 6.1:1 | `#bfbfc3` on `#28272d`: 8.1:1 | muted text in a dialog |
| `--trove-status-bad` | page | 4.5:1 | `#c02f12` on `#ffffff`: 5.7:1 | `#f6806d` on `#18171d`: 7.0:1 | error text |
| `--trove-status-bad` | filled field | 4.5:1 | `#c02f12` on `#ececef`: 4.9:1 | `#f6806d` on `#3a383f`: 4.5:1 | error text under a field |
| `--trove-status-warn` | page | 4.5:1 | `#995715` on `#ffffff`: 5.6:1 | `#d99530` on `#18171d`: 7.0:1 | warning text |
| `--trove-status-warn` | filled field | 4.5:1 | `#995715` on `#ececef`: 4.8:1 | `#d99530` on `#3a383f`: 4.6:1 | warning text on a field |
| `--trove-status-ok` | page | 4.5:1 | `#2f7549` on `#ffffff`: 5.6:1 | `#52b87a` on `#18171d`: 7.2:1 | positive status |
| `--trove-status-ok` | raised | 4.5:1 | `#2f7549` on `#ffffff`: 5.6:1 | `#52b87a` on `#18171d`: 7.2:1 | positive status on a card |
| `--trove-status-ok` | dialog | 4.5:1 | `#2f7549` on `#ffffff`: 5.6:1 | `#52b87a` on `#28272d`: 6.0:1 | added lines of the policy diff |
| `--trove-status-ok` | selected row | 4.5:1 | `#2f7549` on `#e9f3fc`: 5.0:1 | `#52b87a` on `#1d283e`: 6.0:1 | success in the current audit row |
| `--trove-nav-active-fg` | active entry | 4.5:1 | `#0a335c` on `#cbd9e9`: 8.9:1 | `#cae0f7` on `#38475c`: 7.0:1 | current navigation entry |
| `--trove-nav-active-fg` | active entry in an open group | 4.5:1 | `#0a335c` on `#cbd9e9`: 8.9:1 | `#cae0f7` on `#38475c`: 7.0:1 | current entry in an open group |
| `primary` | page | 3:1 | `#2f68b4` on `#ffffff`: 5.6:1 | `#66a8ea` on `#18171d`: 7.1:1 | focus ring, tab underline, checkbox, filter dot (non-text) |
| `primary` | raised | 3:1 | `#2f68b4` on `#ffffff`: 5.6:1 | `#66a8ea` on `#18171d`: 7.1:1 | focus ring, help card border on hover (non-text) |
| `primary` | dialog | 3:1 | `#2f68b4` on `#ffffff`: 5.6:1 | `#66a8ea` on `#28272d`: 5.9:1 | focus ring, drop zone border, checkbox in a dialog (non-text) |
| `primary` | filled field | 3:1 | `#2f68b4` on `#ececef`: 4.7:1 | `#66a8ea` on `#3a383f`: 4.6:1 | active indicator of a filled field (non-text) |
| `--trove-nav-accent` | side bar | 3:1 | `#0a335c` on `#ececef`: 10.9:1 | `#cae0f7` on `#28272d`: 10.9:1 | the area's title above the sidebar, focus outline (text and non-text) |
| `--trove-nav-accent` | open group | 3:1 | `#0a335c` on `#ececef`: 10.9:1 | `#cae0f7` on `#28272d`: 10.9:1 | icon of the current entry in an open group (non-text) |
| `--trove-nav-accent` | top bar | 3:1 | `#0a335c` on `#ececef`: 10.9:1 | `#cae0f7` on `#28272d`: 10.9:1 | focus outline in the top bar (non-text) |
| `--trove-nav-accent` | active entry | 3:1 | `#0a335c` on `#cbd9e9`: 8.9:1 | `#cae0f7` on `#38475c`: 7.0:1 | icon of the current entry (non-text) |
| `--trove-nav-accent` | active entry in an open group | 3:1 | `#0a335c` on `#cbd9e9`: 8.9:1 | `#cae0f7` on `#38475c`: 7.0:1 | icon of the current entry in an open group (non-text) |
| `primary` | primary-container | 4.5:1 | `#2f68b4` on `#e9f3fc`: 5.0:1 | `#66a8ea` on `#1d283e`: 5.9:1 | not used today: accent text on primary-container |
| `on-surface-variant` | primary-container | 4.5:1 | `#626168` on `#e9f3fc`: 5.5:1 | `#bfbfc3` on `#1d283e`: 8.0:1 | not used today: muted text on primary-container |
| `--trove-control-fg` | default button on the frame | 4.5:1 | `#3a383f` on `#ffffff`: 11.6:1 | `#ececef` on `#4f4e53`: 7.0:1 | the Admin button in the top bar |
| `--trove-control-fg` | default button hover on the frame | 4.5:1 | `#3a383f` on `#ececef`: 9.8:1 | `#ececef` on `#66656a`: 4.9:1 | the Admin button under the pointer |
| `--trove-nav-fg` | side bar | 4.5:1 | `#3a383f` on `#ececef`: 9.8:1 | `#ececef` on `#28272d`: 12.6:1 | sidebar entries, the top bar's quiet buttons |
| `--trove-nav-fg` | top bar hover | 4.5:1 | `#3a383f` on `#dedee1`: 8.6:1 | `#ececef` on `#39383e`: 9.8:1 | a sidebar entry or a top bar button under the pointer |
| `--trove-control-fg` | menu item hover | 4.5:1 | `#3a383f` on `#f0f0f1`: 10.2:1 | `#ececef` on `#4f4e53`: 7.0:1 | a menu item under the pointer |
| `--trove-control-fg` | switcher panel | 4.5:1 | `#3a383f` on `#ffffff`: 11.6:1 | `#ececef` on `#28272d`: 12.6:1 | the projects in the switcher of the project sidebar |
| `--trove-nav-fg-muted` | switcher panel | 4.5:1 | `#626168` on `#ffffff`: 6.1:1 | `#bfbfc3` on `#28272d`: 8.1:1 | the headings and keys in the switcher |
| `--trove-search-fg` | search | 4.5:1 | `#3a383f` on `#ffffff`: 11.6:1 | `#ececef` on `#39383e`: 9.8:1 | the text typed into the top bar's search |
| `--trove-search-muted` | search | 4.5:1 | `#626168` on `#ffffff`: 6.1:1 | `#bfbfc3` on `#39383e`: 6.3:1 | the search field's placeholder and icon |
| `--trove-heading` | card | 4.5:1 | `#18171d` on `#ececef`: 15.1:1 | `#ffffff` on `#3a383f`: 11.6:1 | a card's title on its strong fill |
| `on-surface-variant` | card | 4.5:1 | `#626168` on `#ececef`: 5.2:1 | `#bfbfc3` on `#3a383f`: 6.3:1 | a count or muted text in a card's head |
| `--trove-search-border` | top bar | 3:1 | `#858489` on `#ececef`: 3.1:1 | `#737278` on `#28272d`: 3.1:1 | outline of the top bar's search field (non-text) |
| `--trove-control-border` | page | 3:1 | `#89888d` on `#ffffff`: 3.5:1 | `#737278` on `#18171d`: 3.7:1 | outline of a field (non-text) |
| `--trove-accent-fill` | page | 3:1 | `#1f75cb` on `#ffffff`: 4.7:1 | `#63a6e9` on `#18171d`: 6.9:1 | the confirm button against the page (non-text) |
| `--trove-tab-indicator` | page | 3:1 | `#1f75cb` on `#ffffff`: 4.7:1 | `#428fdc` on `#18171d`: 5.3:1 | the current tab's line (non-text) |
| `--trove-badge-neutral-fg` | neutral badge | 4.5:1 | `#4c4b51` on `#dcdcde`: 6.3:1 | `#18171d` on `#a4a3a8`: 7.1:1 | text of a neutral badge |
| `--trove-badge-neutral-icon` | neutral badge | 3:1 | `#737278` on `#dcdcde`: 3.5:1 | `#18171d` on `#a4a3a8`: 7.1:1 | icon of a neutral badge (non-text) |
| `--trove-badge-ok-fg` | positive badge | 4.5:1 | `#306440` on `#c3e6cd`: 5.1:1 | `#17291c` on `#52b87a`: 6.2:1 | text of a positive badge |
| `--trove-badge-ok-icon` | positive badge | 3:1 | `#108548` on `#c3e6cd`: 3.5:1 | `#17291c` on `#52b87a`: 6.2:1 | icon of a positive badge (non-text) |
| `--trove-badge-warn-fg` | warning badge | 4.5:1 | `#894b16` on `#f5d9a8`: 5.0:1 | `#382315` on `#d99530`: 5.8:1 | text of a warning badge |
| `--trove-badge-warn-icon` | warning badge | 3:1 | `#ab6100` on `#f5d9a8`: 3.5:1 | `#382315` on `#d99530`: 5.8:1 | icon of a warning badge (non-text) |
| `--trove-badge-bad-fg` | error badge | 4.5:1 | `#a32c12` on `#fdd4cd`: 5.3:1 | `#3e1a14` on `#f6806d`: 6.0:1 | text of an error badge |
| `--trove-badge-bad-icon` | error badge | 3:1 | `#a32c12` on `#fdd4cd`: 5.3:1 | `#3e1a14` on `#f6806d`: 6.0:1 | icon of an error badge (non-text) |
| `--trove-badge-info-fg` | info badge | 4.5:1 | `#2f5ca0` on `#cbe2f9`: 5.0:1 | `#1d283e` on `#63a6e9`: 5.7:1 | text of an info badge |
| `--trove-badge-info-icon` | info badge | 3:1 | `#1f75cb` on `#cbe2f9`: 3.5:1 | `#1d283e` on `#63a6e9`: 5.7:1 | icon of an info badge (non-text) |
</details>

<details>
<summary>Candidates measured and not taken</summary>

| Candidate, not taken | On | Needs | Scheme | Ratio | What |
|---|---|---|---|---|---|
| `#1f75cb` | filled field `#ececef` | 4.5:1 | light | 3.99:1 fails | the reference's confirm blue as the focused label |
| `#1f75cb` | row hover `#e9f3fc` | 4.5:1 | light | 4.19:1 fails | the reference's confirm blue in a hovered row |
| `#63a6e9` | filled field `#3a383f` | 4.5:1 | dark | 4.49:1 fails | the reference's dark confirm blue as the focused label |
| `#1f75cb` | inverse surface `#ececef` | 4.5:1 | dark | 3.99:1 fails | the reference's blue as a snack bar's action |
| `#050506 @ 24 %` | frame `#ececef` | 3:1 | light | 1.74:1 fails | the reference's search hairline |
| `#ffffff @ 16 %` | frame `#28272d` | 3:1 | dark | 1.67:1 fails | the reference's search hairline |
| `#89888d` | frame `#ececef` | 3:1 | light | 2.98:1 fails | the field outline as the search outline on the frame |
| `#0a6fd6` | page `#ffffff` | 4.5:1 | light | 4.9:1 passes | cogpit's light primary as text |
| `#ffffff` | `#0a6fd6` | 4.5:1 | light | 4.9:1 passes | white text on cogpit's light primary |
| `#0a6fd6` | filled field `#e4e7ec` | 4.5:1 | light | 4.0:1 fails | cogpit's light primary as the focused label |
| `#0a6fd6` | dialog `#eaedf2` | 4.5:1 | light | 4.2:1 fails | cogpit's light primary in a dialog |
| `#0a6fd6` | row hover `#f1f4f8` | 4.5:1 | light | 4.47:1 fails | cogpit's light primary in a hovered row |
| `#0a6fd6 @ 0x99` | page `#ffffff` | 3:1 | light | 2.5:1 fails | cogpit's light focus (alpha 0x99) as a ring |
| `#4aa8ff @ 0xaa` | page `#040c18` | 3:1 | dark | 4.0:1 passes | cogpit's dark focus (alpha 0xaa) as a ring |
| `#4aa8ff @ 0xaa` | raised `#121822` | 3:1 | dark | 3.8:1 passes | cogpit's dark focus on a card |
| `#4aa8ff @ 0xaa` | filled field `#222a36` | 3:1 | dark | 3.4:1 passes | cogpit's dark focus on a filled field |
</details>

Under the palette of Q-UI-96, `primary` and the muted text missed AA on the dark `primary-container` (4.1:1 and 4.0:1);
with the reference's info fill (`#1d283e`) both pass. No component of trove sets text other than `on-primary-container`
on it today.

## What this does not cover

- The logo is a draft, and the owner has not called it final: `drafts/` and the preview ship in the build until the logo is
  chosen, and removing them then is the third open point. Done: the wordmark is paths (Inter Bold) and the favicon has a
  reduced form. Not checked: the pair kerning of the static font file reads as 0, and the favicon was checked on rendered
  frames, not in the browser tabs of every platform. Still open: the files follow the system's colour scheme, not trove's
  theme button, because an `<img>` cannot read `data-theme`.
- The reference's type is Inter with its lower-case l; trove ships Inter 4 of `@fontsource-variable/inter`, the
  reference's "GitLab Sans" is built on an earlier Inter. Glyph differences between the two versions were not compared.
- The values of Q-UI-128 come from the reference's admin dashboard and its user list; its forms and dialogs were not
  measured. The reference limits its content to a centred column of 1280 px; trove's content runs the panel's full width,
  by the owner's choice of 2026-11-06 for now. Links differ on purpose: the reference underlines none, trove underlines
  links in running text and, in tables and key-value lists, only under the pointer and on focus (Q-UI-128, R-UI-044).
- The reference's icons are its own line icons; trove's are Material Icons in the outlined variant (owner, 2026-11-06),
  close to them but not the same drawings.
- The vulnerability trend is the one chart; its series colours are tokens (`--trove-chart-*`). Another chart takes its series from the same family and adds its pairs to `palette.spec.ts`.
- The dialogs, tabs, checkboxes, menus and tooltips are trove's own since Q-UI-126 was finished; their measures follow the
  reference's modal and tab strip as far as the development agent measured them, and no screen of the owner has confirmed
  their look yet (the walkthrough does).
- The blue palette of Q-UI-96 has not been through the browser tier's axe run. axe checks text against the background it
  is rendered on; the computed pairs cover every surface the accent, the link and the containers sit on in the screens
  named above, but a component's own fill that is not in the list is covered only once the browser journeys run again.
