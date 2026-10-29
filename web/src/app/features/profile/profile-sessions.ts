import { DatePipe } from '@angular/common';
import { Component, inject, signal } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { ApiError } from '../../api/problem';
import { SessionsApi, UserSession } from '../../api/sessions-api';
import { Notifier } from '../../core/notifier';
import { Confirmer } from '../../shared/access/confirm-dialog';
import { toApiError } from '../../shared/access/paged-signal';
import { ErrorNotice } from '../../shared/error-notice';
import { ProfileNav } from './profile-nav';

/**
 * The caller's browser sessions (R-UI-029, R-AUTH-017): created, last seen, address, user agent
 * and "this session"; end one, or end all others. The current session is not offered for ending:
 * that is signing out.
 */
@Component({
  selector: 'trove-profile-sessions',
  imports: [DatePipe, MatButtonModule, ErrorNotice, ProfileNav],
  template: `
    <header class="page-header">
      <h1 tabindex="-1" i18n="@@profile.sessions.heading">Sessions</h1>
      @if (others() > 0) {
        <button mat-flat-button type="button" (click)="endOthers()" i18n="@@profile.sessions.end-others">
          End all other sessions
        </button>
      }
    </header>
    <trove-profile-nav />

    @if (error(); as e) {
      <trove-error-notice [error]="e" (retry)="load()" />
    }
    @if (loaded()) {
      <div class="scroll" tabindex="0" role="region" i18n-aria-label="@@profile.sessions.region" aria-label="Sessions">
        <table class="data">
          <caption class="visually-hidden" i18n="@@profile.sessions.caption">Your browser sessions</caption>
          <thead>
            <tr>
              <th scope="col" i18n="@@profile.sessions.created">Created</th>
              <th scope="col" i18n="@@profile.sessions.last-seen">Last seen</th>
              <th scope="col" i18n="@@profile.sessions.address">Address</th>
              <th scope="col" i18n="@@profile.sessions.agent">Browser</th>
              <th scope="col" i18n="@@profile.sessions.method">Sign-in</th>
              <th scope="col"><span class="visually-hidden" i18n="@@profile.sessions.actions">Actions</span></th>
            </tr>
          </thead>
          <tbody>
            @for (s of sessions(); track s.id) {
              <tr>
                <th scope="row">
                  {{ s.createdAt | date: 'medium' }}
                  @if (s.current) {
                    <br /><span class="badge"><span class="material-icons" aria-hidden="true">computer</span><ng-container i18n="@@profile.sessions.this">This session</ng-container></span>
                  }
                </th>
                <td>{{ s.lastSeenAt | date: 'medium' }}</td>
                <td translate="no">{{ s.createdAddr ?? '' }}</td>
                <td translate="no">{{ s.userAgent ?? '' }}</td>
                <td translate="no">{{ s.providerId ?? s.authMethod }}</td>
                <td class="actions">
                  @if (!s.current) {
                    <button mat-button type="button" (click)="end(s)">
                      <ng-container i18n="@@profile.sessions.end">End</ng-container>
                      <span class="visually-hidden"> {{ s.createdAt | date: 'medium' }}</span>
                    </button>
                  }
                </td>
              </tr>
            }
          </tbody>
        </table>
      </div>
    } @else if (!error()) {
      <p role="status" i18n="@@profile.sessions.loading">Loading sessions.</p>
    }
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class ProfileSessions {
  private readonly api = inject(SessionsApi);
  private readonly confirmer = inject(Confirmer);
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
      this.error.set(toApiError(e));
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
      text: $localize`:@@profile.sessions.end-others-text:Every other browser is signed out at once. This session stays.`,
      confirm: $localize`:@@profile.sessions.end-others-confirm:End other sessions`,
    });
    if (!answer.confirmed) return;
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
      this.error.set(toApiError(e));
    }
  }
}
