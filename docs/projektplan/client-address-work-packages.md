# T-20 · Client addresses shown in full — the work package

Task T-20 of docs/projektplan/promt.md section 10 (owner, 2026-11-11). Spec: R-UI-065, R-UI-066 and Q-UI-270 to Q-UI-274 in
[api-and-ui.md](../spec/api-and-ui.md); the component's entry is [ui-style-guide.md](../developer/ui-style-guide.md#address-field).
One builder, Angular only; no API, schema or Go change.

## WP1 · `trove-address-field` and its call sites

**The component.** `web/src/app/shared/address-field.ts`, selector `trove-address-field`, inputs `value` (required), `label`,
`copyLabel`; it renders `<input readonly>` and the existing `trove-copy-button` ([copy-button.ts](../../web/src/app/shared/copy-button.ts))
in one inline flex group. Its stylesheet restates the control tokens (border, fill, radius, focus ring) and joins the button
(left radius 0, `margin-inline-start: -1px`), sets the width to `calc(<length>ch + padding)` with `max-inline-size: 100%`
and `min-inline-size: 8rem`. Select-all on `focus` and `click`. `copy-button.ts` stays as it is; if the joined look needs a hook
on its button, add a class input, not a second implementation.

**Call sites to switch** (each keeps its `@if` for an empty address):

| File | Today | Becomes |
|---|---|---|
| [repository-list.html](../../web/src/app/features/repositories/repository-list.html) line 75 to 78 | `<code class="ellipsis">` and `trove-copy-button`; label `copyLabel` = **Copy client address** | field with `label` and `copyLabel` naming the row (`repo.copy-address` with the key); the cell `white-space: nowrap`; drop `CopyButton` from `repository-list.ts` imports if unused |
| [repository-detail.html](../../web/src/app/features/repositories/repository-detail.html) line 38 to 42 (consumer view) | one `<code>` per address, no copy button | one field per address |
| the same, line 76 to 81 (settings view) | `<code>` and `trove-copy-button` | field |
| `repository-wizard.html` line 250 | `<dd><address or –></dd>` | field where an address exists, the dash otherwise |
| [client-setup.html](../../web/src/app/features/setup/client-setup.html) line 288 | `<code>{{ s.registryUrl }}</code>` in the banner sentence | the sentence without the code, the field below it |
| [guide-page.html](../../web/src/app/features/guides/guide-page.html) line 120 | `<code>{{ bastionSetup.value().registryUrl }}</code>` in a paragraph | the label as text, the field below |
| [endpoint-detail.html](../../web/src/app/features/endpoints/endpoint-detail.html) line 140 to 143 | `<code>` and `trove-copy-button` with `copyAddress` | field |

Not touched (R-UI-066 "Not in scope"): `guide-view.ts`, the snippets of client-setup and guide-page, `install-command.html`,
the `ellipsis` of digests and of the endpoint list's Base URL and Hosts, `endpoint-detail.html` line 43.

Styles: remove the `ellipsis` and `td code` rules only where nothing else uses them (the class still serves digests and the
audit log, so it stays in `_list-screens.scss`). Add `i18n` for the new strings to `messages.xlf` and `messages.de.xlf`
(`repo.copy-address` gains the row name; the existing `ep.copy-address` and the wizard's label follow the same pattern).

## Tests

New: `web/src/app/shared/address-field.spec.ts`, the acceptance list of R-UI-065 (full value for the three lengths, readonly,
accessible name, no ellipsis, clipboard write and status, failure alert, select on focus, empty renders nothing, axe).

Existing specs that **break** (they read an address as text content or by `code`; an input's value is not text content):

| Spec | Line | Why | Change |
|---|---|---|---|
| [repository-list-panel.spec.ts](../../web/src/app/features/repositories/repository-list-panel.spec.ts) | 63 to 74 | reads `tbody tr code` text and its `title` | read the input's `value`; the button query stays |
| [repository-list.spec.ts](../../web/src/app/features/repositories/repository-list.spec.ts) | 17 | looks for the label **Copy client address** exactly | the label now names the row: match the new text |
| [repository-detail.spec.ts](../../web/src/app/features/repositories/repository-detail.spec.ts) | 262, 276 to 288 | `text(r.root)` contains the address; the absence test checks `generic-local/` is not in the text | read the field's value (and `querySelector('trove-address-field')` null for the absence) |
| `repository-wizard.spec.ts` | 308, 315 to 330 | `text(r.root)` contains the address | read the field's value; Q-UI-75 case likewise |
| [endpoints.spec.ts](../../web/src/app/features/endpoints/endpoints.spec.ts) | to check: any assertion on a rule's address text | same | read the value |
| [client-setup.spec.ts](../../web/src/app/features/setup/client-setup.spec.ts), [guides.spec.ts](../../web/src/app/features/guides/guides.spec.ts) | to check: the Bastion address tests | same | read the value |

The component spec's name carries `R-UI-065`, each screen test `R-UI-066` (and its old IDs). Test names carry no Q- ID.

Browser journeys (`web/e2e/tests`): grep for **Client address**, the copy button and `code` finds no journey that reads a
repository's client address; `16-backups.spec.ts` and `bastion/14-bastions.spec.ts` click other copy buttons (**Copy the
command**, **Copy**) and are unaffected. `test/e2e` (Go) never opens the UI. Add one Playwright check to
`02-repository-and-files.spec.ts`: the repository list's row holds an input with the API's address as value, and the copy button
puts it on the clipboard (`grantPermissions` as in `16-backups.spec.ts`). Not run by the builder unless the kind rules of the
brief allow it; the check is written and type-checked.

## Gates

`make web-test`, `make web-lint`; for the e2e file `npx tsc --noEmit -p web/e2e` (as the web lint does). The messages
extraction check of the web build (the `.xlf` files carry every new ID in both languages).
