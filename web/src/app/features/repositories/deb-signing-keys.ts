import { DatePipe } from '@angular/common';
import { Component, computed, effect, inject, input, signal, untracked } from '@angular/core';
import { DebApi, DebSigningKey, DebSigningKeyState } from '../../api/deb-api';
import { ApiError } from '../../api/problem';
import { Notifier } from '../../core/notifier';
import { Confirm } from '../../shared/confirm-dialog';
import { CopyButton } from '../../shared/copy-button';
import { ErrorNotice } from '../../shared/error-notice';
import { asApiError } from '../../shared/paged-resource';
import { DataTable, TableColumn } from '../../shared/ui/data-table';

/** The fingerprint in groups of four, the way an operator compares it with a second channel (R-DEB-018). */
export function groupedFingerprint(fingerprint: string): string {
  return fingerprint.replace(/(.{4})(?=.)/g, '$1 ');
}

const ORDER: Readonly<Record<DebSigningKeyState, number>> = { primary: 0, next: 1, withheld: 2, retired: 3 };

/** The words of a key state; the colour is a second signal, never the only one (R-UI-044). */
export function keyStateText(state: DebSigningKeyState): string {
  switch (state) {
    case 'primary':
      return $localize`:@@deb.key.state.primary:Primary`;
    case 'next':
      return $localize`:@@deb.key.state.next:Next`;
    case 'withheld':
      return $localize`:@@deb.key.state.withheld:Withheld`;
    default:
      return $localize`:@@deb.key.state.retired:Retired`;
  }
}

function stateSentence(state: DebSigningKeyState): string {
  switch (state) {
    case 'primary':
      return $localize`:@@deb.key.state.primary.text:Signs every index and is served to clients.`;
    case 'next':
      return $localize`:@@deb.key.state.next.text:Co-signs every index and is served to clients, until it is promoted or removed.`;
    case 'withheld':
      return $localize`:@@deb.key.state.withheld.text:A restore withheld this key. It neither signs nor is served; its private key is kept until you confirm the retirement or reinstate it.`;
    default:
      return $localize`:@@deb.key.state.retired.text:Neither signs nor is served; its private key is erased. The certificate stays as the record of what signed.`;
  }
}

/** What the panel says about the refusals of the key operations; an unknown code is the shared error notice. */
export function keyErrorText(error: ApiError): string | null {
  switch (error.code) {
    case 'deb_key_in_use':
      return $localize`:@@deb.key.err.in-use:This key is already known to trove, in this or another repository, in some state. A retired or withheld key cannot come back by import.`;
    case 'deb_key_unsupported':
      return $localize`:@@deb.key.err.unsupported:trove cannot use this key. It needs an EdDSALegacy (Ed25519) or RSA primary key of at least 3072 bits that can sign, is not revoked, has no expiry, was not created in the future and has no signing subkeys.`;
    case 'deb_key_state':
      return $localize`:@@deb.key.err.state:The key is not in a state that allows this. Reload the list and try again.`;
    case 'deb_no_next_key':
      return $localize`:@@deb.key.err.no-next:There is no next key to promote.`;
    default:
      return null;
  }
}

/**
 * The signing keys of a local or virtual Debian repository (R-DEB-015, R-DEB-017, R-DEB-032, R-DEB-030): the list with fingerprint,
 * state, algorithm, creation times and the public certificate, and, for someone who may manage the repository, the rotation: create the
 * next key (generated, or imported from an armored secret key), promote it, remove it, and confirm or reinstate a key a restore
 * withheld. A private key is never part of an answer and is never shown; an imported one is sent once, cleared from the form at
 * once and, with its passphrase, kept nowhere. The server enforces the permission; `canManage` only decides what is offered.
 */
@Component({
  selector: 'trove-deb-signing-keys',
  imports: [DatePipe, CopyButton, DataTable, ErrorNotice],
  template: `
    <section class="keys" aria-labelledby="deb-keys-heading">
      <h2 id="deb-keys-heading" class="visually-hidden" i18n="@@deb.keys.heading">Signing keys</h2>
      <p class="hint" i18n="@@deb.keys.intro">
        Every index of this repository is signed. Clients trust the key through the key file the setup snippet installs. To change the
        key without breaking clients, create a next key, let the clients replace their key file while both keys sign, then promote the
        next key.
      </p>

      @if (failure(); as f) {
        <trove-error-notice [error]="f" (retry)="load()" />
      } @else if (keys(); as list) {
        @if (!primary()) {
          <p class="banner-warning" role="status" data-testid="deb-no-primary">
            <span class="material-icons inline warn" aria-hidden="true">warning</span>
            <span i18n="@@deb.keys.no-primary">
              This repository has no primary key, so its builds publish nothing and the last published indexes stay served. Reinstate
              a withheld key, or create a next key and promote it.
            </span>
          </p>
        }
        @if (hasWithheld()) {
          <p class="banner-warning" role="status" data-testid="deb-withheld-note">
            <span class="material-icons inline warn" aria-hidden="true">warning</span>
            <span i18n="@@deb.keys.withheld-note">
              A restore withheld a key because the journal records its retirement. Confirm the retirement to erase the private key, or
              reinstate the key if the entry is wrong.
            </span>
          </p>
        }
        @if (next(); as n) {
          <p class="hint" role="note" data-testid="deb-overlap">
            <span i18n="@@deb.keys.overlap">Both keys sign since</span>&ngsp;<time [attr.datetime]="n.createdAt">{{ n.createdAt | date: 'medium' }}</time>.&ngsp;<span i18n="@@deb.keys.overlap-hint">trove cannot know which clients replaced their key file. A client that holds only the old key fails to update after the promotion.</span>
          </p>
        }

        <trove-data-table [caption]="caption" [columns]="columns()" [empty]="list.length === 0" data-testid="deb-key-table">
          @for (k of list; track k.fingerprint) {
            <tr [attr.data-state]="k.state">
              <th scope="row">
                <code translate="no" [attr.title]="k.fingerprint">{{ grouped(k.fingerprint) }}</code>
                <trove-copy-button [value]="k.fingerprint" [label]="copyFingerprint" />
                @if (k.state !== 'retired') {
                  <details>
                    <summary i18n="@@deb.keys.certificate">Public certificate</summary>
                    <pre class="code-block" translate="no" tabindex="0">{{ k.publicKey }}</pre>
                  </details>
                }
              </th>
              <td>
                <span class="badge" [class.info]="k.state === 'primary' || k.state === 'next'" [class.warn]="k.state === 'withheld'" [attr.title]="sentence(k.state)">{{ stateText(k.state) }}</span>
              </td>
              <td translate="no">{{ typeText(k) }}</td>
              <td>{{ k.origin === 'imported' ? importedText : generatedText }}</td>
              <td><time [attr.datetime]="k.keyCreatedAt">{{ k.keyCreatedAt | date: 'medium' }}</time></td>
              <td>
                @if (since(k); as at) {
                  <time [attr.datetime]="at">{{ at | date: 'medium' }}</time>
                } @else {
                  –
                }
              </td>
              @if (canManage()) {
                <td class="actions">
                  @switch (k.state) {
                    @case ('next') {
                      <button class="btn btn-default btn-sm" type="button" [disabled]="busy()" (click)="promote(k)" i18n="@@deb.keys.promote">Promote</button>
                      <button class="btn btn-tertiary btn-sm" type="button" [disabled]="busy()" (click)="retire(k)" i18n="@@deb.keys.remove">Remove</button>
                    }
                    @case ('withheld') {
                      <button class="btn btn-default btn-sm" type="button" [disabled]="busy()" (click)="reinstate(k)" i18n="@@deb.keys.reinstate">Reinstate</button>
                      <button class="btn btn-danger btn-sm" type="button" [disabled]="busy()" (click)="retire(k)" i18n="@@deb.keys.confirm-retirement">Confirm retirement</button>
                    }
                  }
                </td>
              }
            </tr>
          }
        </trove-data-table>

        @if (actionFailure(); as f) {
          @if (errorText(f); as text) {
            <p class="field-error" role="alert" data-testid="deb-key-error">{{ text }}</p>
          } @else {
            <trove-error-notice [error]="f" />
          }
        }

        @if (canManage()) {
          <h3 i18n="@@deb.keys.next.heading">Next key</h3>
          @if (next()) {
            <p class="hint" i18n="@@deb.keys.next.exists">
              A next key exists. Promote it or remove it before you create another.
            </p>
          } @else {
            <p class="hint" i18n="@@deb.keys.next.intro">
              Creating a next key starts the overlap: from the next build both keys sign every index.
            </p>
            <div class="buttons">
              <button class="btn btn-confirm" type="button" [disabled]="busy()" (click)="generate()" data-testid="deb-generate" i18n="@@deb.keys.generate">
                Generate a next key
              </button>
              <button class="btn btn-default" type="button" [disabled]="busy()" (click)="importing.set(!importing())" [attr.aria-expanded]="importing()" i18n="@@deb.keys.import">
                Import a key
              </button>
            </div>
            @if (importing()) {
              <div class="collapse-animated" animate.enter="collapse-in" animate.leave="collapse-out">
              <form class="import" (submit)="$event.preventDefault(); doImport()" data-testid="deb-import">
                <div class="field">
                  <label class="field-label" for="deb-import-key" i18n="@@deb.keys.import.key">Armored secret key</label>
                  <textarea
                    id="deb-import-key"
                    class="area-input"
                    rows="8"
                    spellcheck="false"
                    autocomplete="off"
                    aria-describedby="deb-import-key-d"
                    [value]="secret()"
                    (input)="secret.set($any($event.target).value)"
                  ></textarea>
                  <p id="deb-import-key-d" class="hint" i18n="@@deb.keys.import.hint">
                    For an archive whose clients already trust a key. It is sent once over this connection and sealed on the server;
                    it is never shown again.
                  </p>
                </div>
                <div class="field">
                  <label class="field-label" for="deb-import-pass" i18n="@@deb.keys.import.pass">Passphrase</label>
                  <input
                    id="deb-import-pass"
                    class="text-input field-w-name"
                    type="password"
                    autocomplete="off"
                    aria-describedby="deb-import-pass-d"
                    [value]="passphrase()"
                    (input)="passphrase.set($any($event.target).value)"
                  />
                  <p id="deb-import-pass-d" class="hint" i18n="@@deb.keys.import.pass-hint">
                    Only if the key is protected. It is used once and never stored.
                  </p>
                </div>
                <button class="btn btn-confirm" type="submit" [disabled]="busy() || secret().trim() === ''" i18n="@@deb.keys.import.submit">
                  Import as next key
                </button>
              </form>
              </div>
            }
          }
        }
      } @else {
        <p role="status" i18n="@@deb.keys.loading">Loading the keys.</p>
      }
    </section>
  `,
  styles: `
    .keys {
      margin-block-end: var(--trove-space-6);
    }
    h3 {
      margin-block: var(--trove-space-4) var(--trove-space-2);
      font: var(--trove-type-title-small);
    }
    .actions {
      white-space: nowrap;
    }
    .buttons {
      display: flex;
      flex-wrap: wrap;
      gap: var(--trove-space-3);
      margin-block: var(--trove-space-3);
    }
    .import {
      display: flex;
      flex-direction: column;
      gap: var(--trove-space-3);
      max-width: 40rem;
    }
  `,
})
export class DebSigningKeys {
  private readonly api = inject(DebApi);
  private readonly confirm = inject(Confirm);
  private readonly notifier = inject(Notifier);

  /** The qualified name `<project>/<key>` of the repository. */
  readonly key = input.required<string>();
  /** Whether to offer the changes (the repository's `manage`, which the server enforces). */
  readonly canManage = input(false);

  protected readonly keys = signal<readonly DebSigningKey[] | null>(null);
  protected readonly failure = signal<ApiError | null>(null);
  protected readonly actionFailure = signal<ApiError | null>(null);
  protected readonly busy = signal(false);
  protected readonly importing = signal(false);
  protected readonly secret = signal('');
  protected readonly passphrase = signal('');

  protected readonly primary = computed(() => (this.keys() ?? []).some((k) => k.state === 'primary'));
  protected readonly next = computed(() => (this.keys() ?? []).find((k) => k.state === 'next') ?? null);
  protected readonly hasWithheld = computed(() => (this.keys() ?? []).some((k) => k.state === 'withheld'));

  protected readonly copyFingerprint = $localize`:@@deb.keys.copy-fingerprint:Copy fingerprint`;
  protected readonly importedText = $localize`:@@deb.keys.origin.imported:Imported`;
  protected readonly generatedText = $localize`:@@deb.keys.origin.generated:Generated`;
  protected readonly caption = $localize`:@@deb.keys.caption:Signing keys of this repository`;
  protected readonly columns = computed<readonly TableColumn[]>(() => [
    { id: 'fingerprint', label: $localize`:@@deb.keys.col.fingerprint:Fingerprint` },
    { id: 'state', label: $localize`:@@deb.keys.col.state:State` },
    { id: 'type', label: $localize`:@@deb.keys.col.type:Type` },
    { id: 'origin', label: $localize`:@@deb.keys.col.origin:Origin` },
    { id: 'created', label: $localize`:@@deb.keys.col.created:Key created` },
    { id: 'since', label: $localize`:@@deb.keys.col.since:In this state since` },
    ...(this.canManage()
      ? [{ id: 'actions', label: $localize`:@@deb.keys.col.actions:Actions`, hideLabel: true }]
      : []),
  ]);
  protected readonly grouped = groupedFingerprint;
  protected readonly stateText = keyStateText;
  protected readonly sentence = stateSentence;
  protected readonly errorText = keyErrorText;

  constructor() {
    effect(() => {
      this.key();
      untracked(() => void this.load());
    });
  }

  protected typeText(k: DebSigningKey): string {
    return k.algorithm === 22 ? 'EdDSA Ed25519' : `RSA ${k.bits}`;
  }

  /** The time the key entered its state, as far as the answer says. */
  protected since(k: DebSigningKey): string | null {
    switch (k.state) {
      case 'primary':
        return k.promotedAt ?? k.createdAt;
      case 'next':
        return k.createdAt;
      case 'withheld':
        return k.withheldAt ?? null;
      default:
        return k.retiredAt ?? null;
    }
  }

  protected async load(): Promise<void> {
    this.failure.set(null);
    try {
      const list = await this.api.signingKeys(this.key());
      this.keys.set([...list].sort((a, b) => ORDER[a.state] - ORDER[b.state]));
    } catch (e) {
      this.failure.set(asApiError(e));
    }
  }

  /** Runs one key operation, then reads the list again: the server's state is the truth after any answer, also a refusal. */
  private async act(op: () => Promise<unknown>, done: string): Promise<boolean> {
    if (this.busy()) return false;
    this.busy.set(true);
    this.actionFailure.set(null);
    try {
      await op();
      this.notifier.status(done);
      return true;
    } catch (e) {
      this.actionFailure.set(asApiError(e));
      return false;
    } finally {
      this.busy.set(false);
      await this.load();
    }
  }

  protected async generate(): Promise<void> {
    await this.act(
      () => this.api.createSigningKey(this.key(), { generate: true }),
      $localize`:@@deb.keys.done.created:The next key was created. Both keys sign from the next build.`,
    );
  }

  protected async doImport(): Promise<void> {
    const privateKey = this.secret();
    const passphrase = this.passphrase();
    const body = passphrase === '' ? { privateKey } : { privateKey, passphrase };
    const ok = await this.act(
      () => this.api.createSigningKey(this.key(), body),
      $localize`:@@deb.keys.done.imported:The key was imported as the next key. Both keys sign from the next build.`,
    );
    // The secret leaves the form whatever the answer was; a refused key is pasted again.
    this.secret.set('');
    this.passphrase.set('');
    if (ok) this.importing.set(false);
  }

  protected async promote(k: DebSigningKey): Promise<void> {
    const answer = await this.confirm.ask({
      title: $localize`:@@deb.keys.promote.title:Promote the next key`,
      lines: [
        $localize`:@@deb.keys.promote.line1:The next key ${groupedFingerprint(k.fingerprint)}:fingerprint: becomes the primary key. The old primary key is retired: it stops signing, leaves the key file clients fetch and its private key is erased.`,
        $localize`:@@deb.keys.promote.line2:A client that never replaced its key file fails to update from then on.`,
      ],
      confirmLabel: $localize`:@@deb.keys.promote.confirm:Promote`,
    });
    if (answer) {
      await this.act(
        () => this.api.promoteSigningKey(this.key()),
        $localize`:@@deb.keys.done.promoted:The key was promoted.`,
      );
    }
  }

  protected async retire(k: DebSigningKey): Promise<void> {
    const withheld = k.state === 'withheld';
    const answer = await this.confirm.ask({
      title: withheld
        ? $localize`:@@deb.keys.confirm.title:Confirm the retirement of the key`
        : $localize`:@@deb.keys.remove.title:Remove the next key`,
      lines: [
        withheld
          ? $localize`:@@deb.keys.confirm.line1:The key ${groupedFingerprint(k.fingerprint)}:fingerprint: is retired and its private key is erased. This cannot be undone.`
          : $localize`:@@deb.keys.remove.line1:The next key ${groupedFingerprint(k.fingerprint)}:fingerprint: stops signing and leaves the key file clients fetch. Its private key is erased and the key cannot be imported again.`,
      ],
      confirmLabel: withheld
        ? $localize`:@@deb.keys.confirm.confirm:Retire the key`
        : $localize`:@@deb.keys.remove.confirm:Remove the key`,
    });
    if (answer) {
      await this.act(
        () => this.api.retireSigningKey(this.key(), k.fingerprint),
        $localize`:@@deb.keys.done.retired:The key was retired.`,
      );
    }
  }

  protected async reinstate(k: DebSigningKey): Promise<void> {
    await this.act(
      () => this.api.reinstateSigningKey(this.key(), k.fingerprint),
      $localize`:@@deb.keys.done.reinstated:The key was reinstated.`,
    );
  }
}
