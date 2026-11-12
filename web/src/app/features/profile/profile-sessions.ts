import { DatePipe } from '@angular/common';
import { Component, inject, signal } from '@angular/core';
import { ApiError } from '../../api/problem';
import { SessionsApi, UserSession } from '../../api/sessions-api';
import { Notifier } from '../../core/notifier';
import { Confirm } from '../../shared/confirm-dialog';
import { asApiError } from '../../shared/paged-resource';
import { ErrorNotice } from '../../shared/error-notice';
import { PageHeader } from '../../shared/ui/page-header';
import { DataTable, TableColumn } from '../../shared/ui/data-table';
import { ProfileNav } from './profile-nav';

/**
 * The caller's browser sessions (R-UI-029, R-AUTH-017): created, last seen, address, user agent
 * and "this session"; end one, or end all others. The current session is not offered for ending:
 * that is signing out.
 */
@Component({
  selector: 'trove-profile-sessions',
  imports: [DatePipe, DataTable, ErrorNotice, PageHeader, ProfileNav],
  template: `
    <trove-page-header [heading]="heading">
      @if (others() > 0) {
        <button actions class="btn btn-confirm" type="button" (click)="endOthers()" i18n="@@profile.sessions.end-others">
          End all other sessions
        </button>
      }
    </trove-page-header>
    <trove-profile-nav />

    @if (error(); as e) {
      <trove-error-notice [error]="e" (retry)="load()" />
    }
    <!-- The shared data table: a column budget, the browser's long user agent wrapping in its cell, and stacked rows below
         48 rem, so the page never scrolls sideways (R-UI-029, at 390 px it did by about 230 px). -->
    @if (loaded() || !error()) {
      <trove-data-table [caption]="caption" [columns]="columns" [loaded]="loaded()" [loading]="!loaded()" [empty]="sessions().length === 0">
        @for (s of sessions(); track s.id) {
          <tr>
            <th scope="row">
              {{ s.createdAt | date: 'medium' }}
              @if (s.current) {
                <span class="badge"><span class="material-icons" aria-hidden="true">computer</span><ng-container i18n="@@profile.sessions.this">This session</ng-container></span>
              }
            </th>
            <td>{{ s.lastSeenAt | date: 'medium' }}</td>
            <td translate="no">{{ s.createdAddr ?? '' }}</td>
            <td translate="no" class="wrap agent">{{ s.userAgent ?? '' }}</td>
            <td translate="no">{{ s.providerId ?? s.authMethod }}</td>
            <td class="actions">
              @if (!s.current) {
                <button class="btn btn-tertiary" type="button" (click)="end(s)">
                  <ng-container i18n="@@profile.sessions.end">End</ng-container>
                  <span class="visually-hidden"> {{ s.createdAt | date: 'medium' }}</span>
                </button>
              }
            </td>
          </tr>
        }
      </trove-data-table>
    }
  `,
  styleUrl: '../../shared/access/access.scss',
  styles: `
    .agent {
      overflow-wrap: anywhere;
    }
  `,
})
export class ProfileSessions {
  protected readonly caption = $localize`:@@profile.sessions.caption:Your browser sessions`;
  protected readonly columns: readonly TableColumn[] = [
    { id: 'created', label: $localize`:@@profile.sessions.created:Created`, width: '11rem' },
    { id: 'lastSeen', label: $localize`:@@profile.sessions.last-seen:Last seen`, width: '11rem' },
    { id: 'address', label: $localize`:@@profile.sessions.address:Address`, width: '7rem' },
    { id: 'agent', label: $localize`:@@profile.sessions.agent:Browser` },
    { id: 'method', label: $localize`:@@profile.sessions.method:Sign-in`, width: '5rem' },
    { id: 'actions', label: $localize`:@@profile.sessions.actions:Actions`, hideLabel: true, width: '4rem' },
  ];
  protected readonly heading = $localize`:@@profile.sessions.heading:Sessions`;
  private readonly api = inject(SessionsApi);
  private readonly confirmer = inject(Confirm);
  private readonly notifier = inject(Notifier);

  protected readonly sessions = signal<readonly UserSession[]>([]);
  protected readonly loaded = signal(false);
  protected readonly error = signal<ApiError | null>(null);

  constructor() {
    void this.load();
  }

  protected others(): number {
    return this.sessions().filter((s) => !s.current).length;
  }

  protected async load(): Promise<void> {
    this.error.set(null);
    try {
      this.sessions.set(await this.api.own());
      this.loaded.set(true);
    } catch (e) {
      this.error.set(asApiError(e));
    }
  }

  protected async end(session: UserSession): Promise<void> {
    await this.act(
      () => this.api.endOwn(session.id),
      $localize`:@@profile.sessions.ended:Session ended.`,
    );
  }

  protected async endOthers(): Promise<void> {
    const answer = await this.confirmer.ask({
      title: $localize`:@@profile.sessions.end-others-title:End all other sessions?`,
      lines: [$localize`:@@profile.sessions.end-others-text:Every other browser is signed out at once. This session stays.`],
      confirmLabel: $localize`:@@profile.sessions.end-others-confirm:End other sessions`,
    });
    if (answer === null) return;
    await this.act(
      () => this.api.endOwnAll(true),
      $localize`:@@profile.sessions.ended-others:Other sessions ended.`,
    );
  }

  private async act(run: () => Promise<void>, done: string): Promise<void> {
    this.error.set(null);
    try {
      await run();
      this.notifier.status(done);
      await this.load();
    } catch (e) {
      this.error.set(asApiError(e));
    }
  }
}
