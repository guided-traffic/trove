# Design language

The look of the UI: what it is made of, which values carry it in the light and the dark scheme, and where those
values live. Decision [Q-UI-88](../spec/api-and-ui.md#q-ui-88--the-design-language-and-its-two-colour-schemes);
the accent is the blue of cogpit since [Q-UI-96](../spec/api-and-ui.md#q-ui-96--cogpit-blue-as-the-primary-colour-green-only-for-positive-status);
the shell and list layout it dresses is [Q-UI-68](../spec/api-and-ui.md#q-ui-68--layout-of-the-application-shell-and-the-list-screens).
The tokens and how components use them are in [web-ui.md](web-ui.md#design-tokens).

## Where it comes from

On 2026-11-01 the owner sent two example screens, a dashboard in a dark scheme and a list screen in a light one, and asked
that trove take their design language and colours, with both schemes. The screens themselves are not in the repository.
Every colour below was measured on them (the most frequent pixel value of a region), not estimated; where a measured
value misses WCAG AA, the table says so and names the value trove uses instead.

On 2026-11-02 the owner replaced the screens' green accent with the blue of his other product, cogpit (the dark and
light palettes of `internal/ui/fyneui/theme.go` in guided-traffic/cogpit), and kept green for a positive status alone
(Q-UI-96). The surfaces, borders, chrome and type of the screens stay as measured.

## What the language is made of

- **A dark frame around the work.** A full-height side bar and a slim top bar enclose the content in both schemes. In the
  light scheme the frame is navy and the content white; in the dark scheme the frame and the content are both near-black,
  told apart by one step of lightness and a hairline border.
- **One accent, blue.** cogpit's glow blue marks what is active or primary: the current navigation entry (a bar at its
  start edge, and in the dark scheme the text), the selected tab's underline, the primary button, the focus ring, links,
  progress, and as a pale or deep fill a selected row or tree item. The accent is not a status.
- **Green is a positive status, and nothing else.** "Active", "ok", "success", "allowed", the added lines of a diff:
  `--trove-status-ok` beside its icon. No other token is green (the unit tier checks it), so green always means "fine".
- **Flat surfaces.** Cards and tables are separated by a fill and a 1 px border with a small radius (about 4 px), not by
  shadows. Buttons are rectangles with the same small radius, not pills.
- **Dense, quiet type.** A humanist sans at about 13 to 14 px for body text, semibold for titles and table headings, muted
  grey for secondary text (units, dates, captions). Upper-case is not used for labels in the content; trove keeps it for
  the small area titles of the side bar.
- **Data colours beside the accent.** The screens' charts use green, a muted teal and a violet. Green marks a positive
  status only (Q-UI-96), so a chart in trove does not take it for a series; the teal and violet never mark state.
- **Status with an icon.** A status is an icon and a word ("Completed" with a check in a circle), the icon tinted.

## The two schemes

| Role | Light, measured | Dark, measured | trove light | trove dark | Notes |
|---|---|---|---|---|---|
| Page (content background) | `#ffffff` | `#040c18` | `#ffffff` | `#040c18` | `--mat-sys-surface` |
| Raised surface (card, panel) | `#ffffff` with border | `#121822` | `#ffffff` | `#121822` | `--trove-surface-raised` |
| Table head | `#f8f9fc` | — | `#f8f9fc` | `#121822` | `--trove-table-head-bg`; the dark screen has no table, a card's fill is used |
| Hairline border | `#dbdde1` to `#e4e6ea` | `#1e2530` | `#dbdde1` | `#1e2530` | `--mat-sys-outline-variant`; decoration, no contrast rule |
| Control outline (input) | — | — | `#8a919c` | `#646c78` | `--mat-sys-outline`; set for WCAG 1.4.11 (3.2:1 and 3.4:1) |
| Text | dark navy grey | `#e8eaec` | `#1c2433` | `#e8eaec` | 15.6:1 and 16.3:1 |
| Muted text | grey | `#737984` | `#5b6270` | `#9aa1ad` | the measured dark grey is 4.1:1 on a card and fails AA; `#9aa1ad` is 6.9:1 |
| Accent | `#3eb164` | `#3db064` | `#0a62c2` | `#4aa8ff` | cogpit's blue, not the screens' green (Q-UI-96); `--mat-sys-primary`. Dark: cogpit's glow blue as it is, 7.8:1 on the page and 5.7:1 on the filled form field. Light: cogpit's primary `#0a6fd6` is 4.9:1 on white, but 4.0:1 on the filled form field (`#e4e7ec`) and 4.2:1 on a dialog, which fails the focused label; trove takes cogpit's deeper link blue `#0a62c2` of the same hue (211° against 210°) for the light accent as a whole: 5.9:1 on white and under white text, 4.8:1 on the field, 5.1:1 on a dialog |
| On the accent | `#ffffff` | — | `#ffffff` | `#04111f` | cogpit's text on primary, on a filled button: 5.9:1 and 7.5:1 |
| Link | — | — | `#0a62c2` | `#7cc4ff` | `--trove-link`, cogpit's hyperlink: the accent itself in the light scheme, a step lighter than the accent in the dark one (10.5:1 on the page), with an underline |
| Focus ring | — | — | `#0a62c2`, solid 2 px | `#4aa8ff`, solid 2 px | `--trove-focus-ring`. cogpit's focus is translucent (`#0a6fd6` at alpha `0x99`, `#4aa8ff` at `0xaa`); over white that is 2.5:1 and misses the 3:1 of WCAG 1.4.11, so trove's ring stays solid in both schemes |
| Selection fills | — | — | `#cfe2ff`, `#d6e6ff` | `#22406a`, `#183252` | `--mat-sys-primary-container` is cogpit's selection, `--mat-sys-secondary-container` (the selected tree item, the current audit row, the active access tab) cogpit's selected row; the text on both is cogpit's selected-row text `#0b1a33` and `#f2f7ff` |
| Positive status | green check | — | `#1a6e3c` | `#3db064` | `--trove-status-ok`: the green the accent had before Q-UI-96, unchanged (6.3:1 and 7.1:1 on the page) |
| Side bar | `#233564` | `#121822` | `#233564` | `#121822` | `--trove-nav-bg` |
| Open group in the side bar | `#1a264e` | — | `#1a264e` | `#0d131c` | `--trove-nav-bg-nested` |
| Top bar | `#1e2e4a` | `#060e19` | `#1e2e4a` | `#060e19` | `--trove-nav-bg-deep` |
| Side bar text, muted | white, light blue grey | `#e8eaec`, grey | `#ffffff`, `#b9c3d9` | `#e8eaec`, `#9aa1ad` | 11.9:1 and 6.7:1; 14.8:1 and 6.9:1 |
| Current navigation entry | white text, green bar | green text and icon | white bold, blue bar | blue bold, blue bar | `--trove-nav-accent` is `#4aa8ff` in both schemes, because the frame is dark in both: 4.7:1 on the light side bar, the icon 3.3:1 on the light active row (a mark); the bar and the weight carry the state as well (R-UI-044) |
| Search field in the top bar | white field | card fill with border | `#ffffff` | `#121822`, outline `#646c78` | `--trove-search-*` |
| Data: teal, violet | `#347da0`, `#8577e6` | `#347da0`, `#8577e6` | — | — | for charts once trove has one; not tokens yet |

The light top bar and side bar differ slightly (`#1e2e4a` and `#233564`); the dark ones too (`#060e19` and `#121822`). Both
are kept, because the step is what lets the top bar read as a bar.

The Material base palette is `mat.$azure-palette`: its tone 40 (`#005cbb`) has cogpit's hue, 210°, where
`mat.$blue-palette` sits at 237°, toward violet. It fills only the tokens `main.scss` does not set (the `secondary` and
`primary-fixed` families), which neither a Material component trove uses nor a trove style reads today.

## The two-pane browser

The file browser (`/browse`, [Q-UI-98](../spec/api-and-ui.md#q-ui-98--the-file-browser-is-two-panes-with-a-toolbar-and-a-details-header))
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

## Where the values live

| File | What it holds |
|---|---|
| [web/src/styles/main.scss](../../web/src/styles/main.scss) | `mat.theme` with the azure and violet palettes, then `mat.theme-overrides` with every Material system colour above as `light-dark(<light>, <dark>)`; the button shape |
| [web/src/styles/_tokens.scss](../../web/src/styles/_tokens.scss) | trove's own properties: the link, the focus ring, the raised surface, the table head, the frame (`--trove-nav-*`), the search field, the status colours |
| [web/src/styles/_shell.scss](../../web/src/styles/_shell.scss) | How the frame uses them: the active entry, the open group's fill, the top bar's actions and the avatar |
| [web/src/styles/palette.spec.ts](../../web/src/styles/palette.spec.ts) | The contrast of the pairs below, computed from the two stylesheets, and the rule that only `--trove-status-ok` is green |

A scheme is chosen by `color-scheme` on the root element: without a choice the browser's setting decides, the theme button
of the top bar sets `data-theme="light"` or `"dark"` (R-API-021). Every colour is a `light-dark()` pair, so a component
never asks which scheme is on.

## Changing a colour

1. Change the pair in `main.scss` or `_tokens.scss`, never in a component.
2. Recompute the contrast of every pair it takes part in (text 4.5:1, large text and marks 3:1) and update the numbers in
   the comment above the token and in the table here. A text tone is not only on the page: Material sets the label of a
   form field on its filled container (`surface-variant`, `#e4e7ec` in the light scheme, `#222a36` in the dark one), a
   dialog sits on `surface-container-high`, and a selected row on `secondary-container`. The focused label is the
   accent, so the accent has to pass on those too (found by axe when the accent was `#1d7a43`: 4.3:1 on the field; and
   the reason cogpit's `#0a6fd6` is not the light accent: 4.0:1 there). `palette.spec.ts` recomputes the pairs in
   `make web-test` and fails below the minimum; a new surface under the accent or the link gets a pair there.
3. Run the browser journeys ([e2e.md](e2e.md#browser-journeys)): they check `color-contrast` with axe in the light scheme
   (English) and the dark one (German). The jsdom tier does not compute colours.

## Contrast of the text tones on the surfaces they sit on

Computed on 2026-11-02 from the values in `main.scss` and `_tokens.scss` (WCAG 2 relative luminance; a translucent fill
is composited over the surface beneath it in sRGB, as a browser paints it), for the same pairs `palette.spec.ts` holds.
The browser tier's axe `color-contrast` confirmed the green palette of Q-UI-88 on 2026-11-01 (a local trove process, the
break-glass administrator, 108 checks: 28 routes with the manual's pages, the new-group dialog, the new-token form
unfocused, focused and invalid, a form field touched while empty, the help opened by the "?", and the list, detail and
audit screens with data, light in English and dark in German; no violation and no "incomplete" result). The blue palette
of Q-UI-96 is computed only: the browser tier has not run against it yet.

| Pair | Light | Dark |
|---|---|---|
| Accent as text on the page, the filled field, a dialog | 5.9:1, 4.8:1, 5.1:1 | 7.8:1, 5.7:1, 6.4:1 |
| Text on a filled button (`on-primary` on `primary`) | 5.9:1 | 7.5:1 |
| Link on the page, on the current audit row | 5.9:1, 4.7:1 | 10.5:1, 6.9:1 |
| Focus ring on the page, the filled field | 5.9:1, 4.8:1 | 7.8:1, 5.7:1 |
| Navigation accent on the side bar, the icon on the current entry | 4.7:1, 3.3:1 | 7.1:1, 5.8:1 |
| Positive status on the page, on the current audit row | 6.3:1, 5.0:1 | 7.1:1, 4.7:1 |

<details>
<summary>Every pair, computed</summary>

| Foreground | On | Needs | Light | Dark | Where |
|---|---|---|---|---|---|
| `primary` | page | 4.5:1 | `#0a62c2` on `#ffffff`: 5.9:1 | `#4aa8ff` on `#040c18`: 7.8:1 | accent text: focused label, outlined and text button, tab label, access tabs |
| `primary` | raised | 4.5:1 | `#0a62c2` on `#ffffff`: 5.9:1 | `#4aa8ff` on `#121822`: 7.1:1 | current entry of the help contents |
| `primary` | table head | 4.5:1 | `#0a62c2` on `#f8f9fc`: 5.6:1 | `#4aa8ff` on `#121822`: 7.1:1 | button label in a table head |
| `primary` | row hover | 4.5:1 | `#0a62c2` on `#f1f4f8`: 5.4:1 | `#4aa8ff` on `#0d141e`: 7.3:1 | button label in a hovered row |
| `primary` | menu | 4.5:1 | `#0a62c2` on `#f1f4f8`: 5.4:1 | `#4aa8ff` on `#121822`: 7.1:1 | menu and select panel |
| `primary` | dialog | 4.5:1 | `#0a62c2` on `#eaedf2`: 5.1:1 | `#4aa8ff` on `#1a212c`: 6.4:1 | focused label and button label in a dialog |
| `primary` | filled field | 4.5:1 | `#0a62c2` on `#e4e7ec`: 4.8:1 | `#4aa8ff` on `#222a36`: 5.7:1 | focused label of a filled form field |
| `on-primary` | primary | 4.5:1 | `#ffffff` on `#0a62c2`: 5.9:1 | `#04111f` on `#4aa8ff`: 7.5:1 | filled button, step number, filter count |
| `--trove-link` | page | 4.5:1 | `#0a62c2` on `#ffffff`: 5.9:1 | `#7cc4ff` on `#040c18`: 10.5:1 | link in the content, breadcrumb, link button |
| `--trove-link` | raised | 4.5:1 | `#0a62c2` on `#ffffff`: 5.9:1 | `#7cc4ff` on `#121822`: 9.5:1 | help card title |
| `--trove-link` | table head | 4.5:1 | `#0a62c2` on `#f8f9fc`: 5.6:1 | `#7cc4ff` on `#121822`: 9.5:1 | help page contents ("On this page") |
| `--trove-link` | row hover | 4.5:1 | `#0a62c2` on `#f1f4f8`: 5.4:1 | `#7cc4ff` on `#0d141e`: 9.9:1 | link in a hovered row |
| `--trove-link` | dialog | 4.5:1 | `#0a62c2` on `#eaedf2`: 5.1:1 | `#7cc4ff` on `#1a212c`: 8.6:1 | link in a dialog |
| `--trove-link` | selected row | 4.5:1 | `#0a62c2` on `#d6e6ff`: 4.7:1 | `#7cc4ff` on `#183252`: 6.9:1 | link in the current audit row |
| `on-secondary-container` | selected row | 4.5:1 | `#0b1a33` on `#d6e6ff`: 13.7:1 | `#f2f7ff` on `#183252`: 12.1:1 | selected tree item, active access tab |
| `on-surface` | selected row | 4.5:1 | `#1c2433` on `#d6e6ff`: 12.3:1 | `#e8eaec` on `#183252`: 10.8:1 | cell text in the current audit row |
| `on-surface-variant` | selected row | 4.5:1 | `#5b6270` on `#d6e6ff`: 4.9:1 | `#9aa1ad` on `#183252`: 5.0:1 | muted text in the current audit row |
| `on-primary-container` | primary-container | 4.5:1 | `#0b1a33` on `#cfe2ff`: 13.2:1 | `#f2f7ff` on `#22406a`: 9.7:1 | text on primary-container |
| `inverse-primary` | inverse surface | 4.5:1 | `#4aa8ff` on `#1c2433`: 6.2:1 | `#0a62c2` on `#e8eaec`: 4.9:1 | action of a snack bar |
| `on-surface` | page | 4.5:1 | `#1c2433` on `#ffffff`: 15.6:1 | `#e8eaec` on `#040c18`: 16.3:1 | text (unchanged) |
| `on-surface` | raised | 4.5:1 | `#1c2433` on `#ffffff`: 15.6:1 | `#e8eaec` on `#121822`: 14.8:1 | text on a card (unchanged) |
| `on-surface-variant` | page | 4.5:1 | `#5b6270` on `#ffffff`: 6.1:1 | `#9aa1ad` on `#040c18`: 7.5:1 | muted text, field label (unchanged) |
| `on-surface-variant` | filled field | 4.5:1 | `#5b6270` on `#e4e7ec`: 4.9:1 | `#9aa1ad` on `#222a36`: 5.6:1 | field label (unchanged) |
| `on-surface-variant` | dialog | 4.5:1 | `#5b6270` on `#eaedf2`: 5.2:1 | `#9aa1ad` on `#1a212c`: 6.2:1 | muted text in a dialog (unchanged) |
| `--trove-status-bad` | page | 4.5:1 | `#b3261e` on `#ffffff`: 6.5:1 | `#ffb4ab` on `#040c18`: 11.5:1 | error text (unchanged) |
| `--trove-status-bad` | filled field | 4.5:1 | `#b3261e` on `#e4e7ec`: 5.3:1 | `#ffb4ab` on `#222a36`: 8.5:1 | error text under a field (unchanged) |
| `--trove-status-warn` | page | 4.5:1 | `#8a5a00` on `#ffffff`: 5.9:1 | `#ffc857` on `#040c18`: 12.7:1 | warning text (unchanged) |
| `--trove-status-warn` | filled field | 4.5:1 | `#8a5a00` on `#e4e7ec`: 4.8:1 | `#ffc857` on `#222a36`: 9.4:1 | warning text on a field (unchanged) |
| `--trove-status-ok` | page | 4.5:1 | `#1a6e3c` on `#ffffff`: 6.3:1 | `#3db064` on `#040c18`: 7.1:1 | positive status (unchanged green) |
| `--trove-status-ok` | raised | 4.5:1 | `#1a6e3c` on `#ffffff`: 6.3:1 | `#3db064` on `#121822`: 6.4:1 | positive status on a card |
| `--trove-status-ok` | dialog | 4.5:1 | `#1a6e3c` on `#eaedf2`: 5.4:1 | `#3db064` on `#1a212c`: 5.9:1 | added lines of the policy diff |
| `--trove-status-ok` | selected row | 4.5:1 | `#1a6e3c` on `#d6e6ff`: 5.0:1 | `#3db064` on `#183252`: 4.7:1 | success in the current audit row |
| `--trove-nav-active-fg` | active entry | 4.5:1 | `#ffffff` on `#3d4d77`: 8.3:1 | `#4aa8ff` on `#19293d`: 5.8:1 | current navigation entry |
| `--trove-nav-active-fg` | active entry in an open group | 4.5:1 | `#ffffff` on `#354063`: 10.1:1 | `#4aa8ff` on `#142537`: 6.2:1 | current entry in an open group |
| `primary` | page | 3:1 | `#0a62c2` on `#ffffff`: 5.9:1 | `#4aa8ff` on `#040c18`: 7.8:1 | focus ring, tab underline, checkbox, filter dot (non-text) |
| `primary` | raised | 3:1 | `#0a62c2` on `#ffffff`: 5.9:1 | `#4aa8ff` on `#121822`: 7.1:1 | focus ring, help card border on hover (non-text) |
| `primary` | dialog | 3:1 | `#0a62c2` on `#eaedf2`: 5.1:1 | `#4aa8ff` on `#1a212c`: 6.4:1 | focus ring, drop zone border, checkbox in a dialog (non-text) |
| `primary` | filled field | 3:1 | `#0a62c2` on `#e4e7ec`: 4.8:1 | `#4aa8ff` on `#222a36`: 5.7:1 | active indicator of a filled field (non-text) |
| `--trove-nav-accent` | side bar | 3:1 | `#4aa8ff` on `#233564`: 4.7:1 | `#4aa8ff` on `#121822`: 7.1:1 | bar of the current entry, focus outline (non-text) |
| `--trove-nav-accent` | open group | 3:1 | `#4aa8ff` on `#1a264e`: 5.8:1 | `#4aa8ff` on `#0d131c`: 7.4:1 | bar and icon in an open group (non-text) |
| `--trove-nav-accent` | top bar | 3:1 | `#4aa8ff` on `#1e2e4a`: 5.4:1 | `#4aa8ff` on `#060e19`: 7.7:1 | focus outline in the top bar (non-text) |
| `--trove-nav-accent` | active entry | 3:1 | `#4aa8ff` on `#3d4d77`: 3.3:1 | `#4aa8ff` on `#19293d`: 5.8:1 | icon of the current entry (non-text) |
| `--trove-nav-accent` | active entry in an open group | 3:1 | `#4aa8ff` on `#354063`: 4.0:1 | `#4aa8ff` on `#142537`: 6.2:1 | icon of the current entry in an open group (non-text) |
| `primary` | primary-container | 4.5:1 | `#0a62c2` on `#cfe2ff`: 4.51:1 | `#4aa8ff` on `#22406a`: 4.1:1, fails | not used: accent text on primary-container |
| `on-surface-variant` | primary-container | 4.5:1 | `#5b6270` on `#cfe2ff`: 4.7:1 | `#9aa1ad` on `#22406a`: 4.0:1, fails | not used: muted text on primary-container |
</details>

<details>
<summary>Candidates measured and not taken</summary>

| Candidate, not taken | On | Needs | Scheme | Ratio | What |
|---|---|---|---|---|---|
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

Not met on purpose: on the dark `primary-container` (`#22406a`), `primary` is 4.1:1 and the muted text 4.0:1. No component
of trove sets text other than `on-primary-container` on it: the Material components that read `primary-container` (the
FAB, the list, the datepicker, the slide toggle) are not used, and trove's own selected fills are `secondary-container`.
Do not start using either pair.

## What this does not cover

- The type is the system font stack, not the humanist sans of the screens (Q-UI-88): no web font is shipped.
- trove has no charts yet; the data colours are recorded here and become tokens with the first chart.
- The example screens show no dialog, menu or form field; those take Material's components with the system colours above
  (menus on the card fill, dialogs one step lighter), which no screen of the owner has confirmed yet.
- The blue palette of Q-UI-96 has not been through the browser tier's axe run. axe checks text against the background it
  is rendered on; the computed pairs cover every surface the accent, the link and the containers sit on in the screens
  named above, but a component's own fill that is not in the list is covered only once the browser journeys run again.
