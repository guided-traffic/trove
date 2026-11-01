# Design language

The look of the UI: what it is made of, which values carry it in the light and the dark scheme, and where those
values live. Decision [Q-UI-88](../spec/api-and-ui.md#q-ui-88--the-design-language-and-its-two-colour-schemes);
the shell and list layout it dresses is [Q-UI-68](../spec/api-and-ui.md#q-ui-68--layout-of-the-application-shell-and-the-list-screens).
The tokens and how components use them are in [web-ui.md](web-ui.md#design-tokens).

## Where it comes from

On 2026-11-01 the owner sent two example screens, a dashboard in a dark scheme and a list screen in a light one, and asked
that trove take their design language and colours, with both schemes. The screens themselves are not in the repository.
Every colour below was measured on them (the most frequent pixel value of a region), not estimated; where a measured
value misses WCAG AA, the table says so and names the value trove uses instead.

## What the language is made of

- **A dark frame around the work.** A full-height side bar and a slim top bar enclose the content in both schemes. In the
  light scheme the frame is navy and the content white; in the dark scheme the frame and the content are both near-black,
  told apart by one step of lightness and a hairline border.
- **One accent, green.** Green marks what is active or primary: the current navigation entry (a bar at its start edge,
  and in the dark scheme the text), the selected tab's underline, the primary button, links, progress, success. Nothing
  else is green, so green always means "here" or "go".
- **Flat surfaces.** Cards and tables are separated by a fill and a 1 px border with a small radius (about 4 px), not by
  shadows. Buttons are rectangles with the same small radius, not pills.
- **Dense, quiet type.** A humanist sans at about 13 to 14 px for body text, semibold for titles and table headings, muted
  grey for secondary text (units, dates, captions). Upper-case is not used for labels in the content; trove keeps it for
  the small area titles of the side bar.
- **Data colours beside the accent.** Charts use green, a muted teal and a violet; the teal and violet never mark state.
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
| Accent | `#3eb164` | `#3db064` | `#1a6e3c` | `#3db064` | the measured green is 2.8:1 on white, as text and under white text; `#1a6e3c` is 6.3:1 both ways, and 5.1:1 on the filled form field (`#e4e7ec`) where its predecessor `#1d7a43` gave 4.3:1 and failed the focused label. On the dark page it is 7.1:1 and stays |
| On the accent | `#ffffff` | — | `#ffffff` | `#040c18` | text on a filled primary button |
| Side bar | `#233564` | `#121822` | `#233564` | `#121822` | `--trove-nav-bg` |
| Open group in the side bar | `#1a264e` | — | `#1a264e` | `#0d131c` | `--trove-nav-bg-nested` |
| Top bar | `#1e2e4a` | `#060e19` | `#1e2e4a` | `#060e19` | `--trove-nav-bg-deep` |
| Side bar text, muted | white, light blue grey | `#e8eaec`, grey | `#ffffff`, `#b9c3d9` | `#e8eaec`, `#9aa1ad` | 11.9:1 and 6.7:1; 14.8:1 and 6.9:1 |
| Current navigation entry | white text, green bar | green text and icon | white bold, green bar | green bold, green bar | the bar and the weight carry the state as well (R-UI-044) |
| Search field in the top bar | white field | card fill with border | `#ffffff` | `#121822`, outline `#646c78` | `--trove-search-*` |
| Data: teal, violet | `#347da0`, `#8577e6` | `#347da0`, `#8577e6` | — | — | for charts once trove has one; not tokens yet |

The light top bar and side bar differ slightly (`#1e2e4a` and `#233564`); the dark ones too (`#060e19` and `#121822`). Both
are kept, because the step is what lets the top bar read as a bar.

## Where the values live

| File | What it holds |
|---|---|
| [web/src/styles/main.scss](../../web/src/styles/main.scss) | `mat.theme` with the green and violet palettes, then `mat.theme-overrides` with every Material system colour above as `light-dark(<light>, <dark>)`; the button shape |
| [web/src/styles/_tokens.scss](../../web/src/styles/_tokens.scss) | trove's own properties: the raised surface, the table head, the frame (`--trove-nav-*`), the search field, the status colours |
| [web/src/styles/_shell.scss](../../web/src/styles/_shell.scss) | How the frame uses them: the active entry, the open group's fill, the top bar's actions and the avatar |

A scheme is chosen by `color-scheme` on the root element: without a choice the browser's setting decides, the theme button
of the top bar sets `data-theme="light"` or `"dark"` (R-API-021). Every colour is a `light-dark()` pair, so a component
never asks which scheme is on.

## Changing a colour

1. Change the pair in `main.scss` or `_tokens.scss`, never in a component.
2. Recompute the contrast of every pair it takes part in (text 4.5:1, large text and marks 3:1) and update the numbers in
   the comment above the token and in the table here. A text tone is not only on the page: Material sets the label of a
   form field on its filled container (`surface-variant`, `#e4e7ec` in the light scheme, `#222a36` in the dark one), a
   dialog sits on `surface-container-high`, and a selected row or chip on `primary-container`. The focused label is the
   accent, so the accent has to pass on those too (found by axe when the accent was `#1d7a43`: 4.3:1 on the field).
3. Run the browser journeys ([e2e.md](e2e.md#browser-journeys)): they check `color-contrast` with axe in the light scheme
   (English) and the dark one (German). The jsdom tier does not compute colours.

## Contrast of the text tones on the surfaces they sit on

Computed from the values above (WCAG relative luminance) and confirmed by axe's `color-contrast` in a browser on
2026-11-01: a local trove process, the break-glass administrator, 108 checks (28 routes with the manual's pages, the
new-group dialog, the new-token form unfocused, focused and invalid, a form field touched while empty, the help opened
by the "?", and the list, detail and audit screens with data), light in English and dark in German; no violation and no
"incomplete" result.

| Text | On | Light | Dark |
|---|---|---|---|
| Text (`on-surface`) | page, raised surface | 15.6:1 | 16.3:1 |
| Muted text and field label (`on-surface-variant`) | page | 6.1:1 | 7.5:1 |
| Muted text and field label | filled field `surface-variant` | 4.9:1 | 5.6:1 |
| Muted text | dialog `surface-container-high` | 5.2:1 | 6.2:1 |
| Muted text | `primary-container` | 5.1:1 | not used |
| Focused label, link (`primary`) | page | 6.3:1 | 7.1:1 |
| Focused label (`primary`) | filled field `surface-variant` | 5.1:1 | 5.2:1 |
| Focused label (`primary`) | dialog `surface-container-high` | 5.4:1 | 5.9:1 |
| Error text | filled field | 5.3:1 (`#b3261e`) | 8.5:1 (`#ffb4ab`) |
| Warning text | filled field / page | 4.8:1 (`#8a5a00`, on the field) | 12.8:1 (`#ffc857`, on the page) |

Not met on purpose: `primary` (`#3db064`) on the dark `primary-container` (`#123d27`) is 4.4:1; no component sets text in
that pair (the text on a container is `on-primary-container`), and axe finds none. Do not start using it.

## What this does not cover

- The type is the system font stack, not the humanist sans of the screens (Q-UI-88): no web font is shipped.
- trove has no charts yet; the data colours are recorded here and become tokens with the first chart.
- The example screens show no dialog, menu or form field; those take Material's components with the system colours above
  (menus on the card fill, dialogs one step lighter), which no screen of the owner has confirmed yet.
