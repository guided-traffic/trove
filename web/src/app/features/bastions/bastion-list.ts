import { Component, computed, inject, signal } from '@angular/core';
import { TroveDialog } from '../../shared/ui/dialog';
import { firstValueFrom } from 'rxjs';
import { Bastion, BastionsApi } from '../../api/bastions-api';
import { ApiError } from '../../api/problem';
import { SessionStore } from '../../core/session-store';
import { DraftStore } from '../../shared/drafts';
import { Needs } from '../../shared/access/needs';
import { ErrorNotice } from '../../shared/error-notice';
import { asApiError } from '../../shared/paged-resource';
import { BastionCard } from './bastion-card';
import { BastionCreateDialog, BastionTokenDraft } from './bastion-dialogs';

/**
 * Administration → Bastions (R-BAS-007): the registrations of the trove Bastions in front of this
 * installation, each as a card with the last contact of every replica, its tokens and the actions
 * (R-BAS-004). There are at most `bastions.max_registrations` registrations, so the list is one page
 * and the screen holds all of them. A registration the caller may not change shows no action; the
 * API decides in any case (R-UI-005).
 */
@Component({
  selector: 'trove-bastion-list',
  imports: [BastionCard, ErrorNotice, Needs],
  template: `
    <header class="page-header">
      <h1 tabindex="-1" i18n="@@bastions.heading">Bastions</h1>
      <button class="btn btn-confirm" type="button" [disabled]="!canWrite()" aria-describedby="needs-bastions" (click)="add()" i18n="@@bastions.add">New Bastion</button>
    </header>
    <trove-needs id="needs-bastions" capability="endpoints:write" />
    <p i18n="@@bastions.hint">
      A Bastion is a read-only cache of this installation for clients outside your network. Register it here, issue the token its chart reads, and check that every replica has made contact.
    </p>

    @if (error(); as e) {
      <trove-error-notice [error]="e" (retry)="load()" />
    }
    @if (!loaded()) {
      @if (!error()) {
        <p role="status" i18n="@@bastions.loading">Loading Bastions.</p>
      }
    } @else if (items().length === 0) {
      <p class="muted" i18n="@@bastions.empty">No Bastion is registered.</p>
    } @else {
      @for (b of items(); track b.name) {
        <trove-bastion-card [bastion]="b" [draft]="restored()" (changed)="load()" />
      }
    }
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class BastionList {
  private readonly api = inject(BastionsApi);
  private readonly dialog = inject(TroveDialog);
  private readonly session = inject(SessionStore);

  protected readonly items = signal<readonly Bastion[]>([]);
  protected readonly loaded = signal(false);
  protected readonly error = signal<ApiError | null>(null);
  protected readonly canWrite = computed(() => this.session.has('endpoints:write'));
  /** The entries of a token dialog kept across a re-authentication (R-UI-042), taken once; the card of its registration reopens the dialog. */
  protected readonly restored = signal<BastionTokenDraft | null>(inject(DraftStore).take<BastionTokenDraft>());

  constructor() {
    void this.load();
  }

  /** Reads the list again; the cards stay on screen meanwhile, so a card keeps a token it shows once. */
  protected async load(): Promise<void> {
    this.error.set(null);
    try {
      this.items.set(await this.api.list());
      this.loaded.set(true);
    } catch (e) {
      this.error.set(asApiError(e));
    }
  }

  protected async add(): Promise<void> {
    const ref = this.dialog.open<BastionCreateDialog, undefined, Bastion>(BastionCreateDialog, { maxWidth: '36rem' });
    const created = await firstValueFrom(ref.closed);
    if (created) await this.load();
  }
}
