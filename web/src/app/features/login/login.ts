import { Component, computed, effect, inject, input, resource, signal, untracked } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatInputModule } from '@angular/material/input';
import { Router, RouterLink } from '@angular/router';
import { LoginApi } from '../../api/login-api';
import { ApiError } from '../../api/problem';
import { BrowserNavigation, safeReturnTo } from '../../core/browser-navigation';
import { describeProblem } from '../../core/http/problem-messages';
import { PreferencesStore } from '../../core/preferences-store';
import { SessionStore } from '../../core/session-store';
import { ErrorNotice } from '../../shared/error-notice';

/**
 * Sign-in (R-UI-004). Renders only from `GET /api/v1/auth/login-options`: one button per provider,
 * each leaving the application for the provider's `startUrl` with `return_to` set to the UI route
 * (R-AUTH-009: a path under `/ui/`). `autoRedirect` navigates at once. The break-glass form shows
 * when `localLogin` is true or the URL carries `?local=1`. There is no username and password form
 * for regular users (INV-7).
 */
@Component({
  selector: 'trove-login',
  imports: [MatButtonModule, MatFormFieldModule, MatInputModule, RouterLink, ErrorNotice],
  template: `
    <h1 tabindex="-1" i18n="@@login.heading">Sign in</h1>

    @if (reauth() === '1') {
      <p role="status" class="reauth" i18n="@@login.reauth">
        The action you tried needs a recent sign-in. Sign in again, then repeat it. Your entries are kept.
      </p>
    }

    @if (options.error(); as failure) {
      <trove-error-notice [error]="asApiError(failure)" (retry)="options.reload()" />
    } @else if (options.isLoading()) {
      <p role="status" i18n="@@login.loading">Loading the sign-in options.</p>
    } @else if (options.value(); as o) {
      @if (redirecting()) {
        <p role="status" i18n="@@login.redirecting">Redirecting to the identity provider.</p>
      } @else {
        @if (o.providers.length === 0 && !showLocal()) {
          <p i18n="@@login.no-providers">No sign-in method is configured.</p>
        }
        <ul class="providers">
          @for (p of o.providers; track p.id) {
            <li>
              <button
                mat-flat-button
                type="button"
                [disabled]="!p.available"
                (click)="start(p.startUrl)"
              >
                <ng-container i18n="@@login.with">Sign in with</ng-container>
                {{ p.displayName }}
              </button>
              @if (!p.available) {
                <span class="unavailable" i18n="@@login.unavailable">Currently unavailable</span>
              }
            </li>
          }
        </ul>

        @if (o.anonymousAccess && reauth() !== '1') {
          <p>
            <a mat-button [routerLink]="continuePath()" i18n="@@login.anonymous">Continue without signing in</a>
          </p>
        }

        @if (showLocal()) {
          <form class="local" (submit)="submitLocal($event)">
            <h2 i18n="@@login.local.heading">Local administrator</h2>
            <mat-form-field>
              <mat-label i18n="@@login.local.username">Username</mat-label>
              <input
                matInput
                name="username"
                autocomplete="username"
                [value]="username()"
                (input)="username.set($any($event.target).value)"
              />
            </mat-form-field>
            <mat-form-field>
              <mat-label i18n="@@login.local.password">Password</mat-label>
              <input
                matInput
                type="password"
                name="password"
                autocomplete="current-password"
                [value]="password()"
                (input)="password.set($any($event.target).value)"
              />
            </mat-form-field>
            @if (localError(); as text) {
              <p role="alert" class="local-error">{{ text }}</p>
            }
            <button mat-flat-button type="submit" [disabled]="busy()" i18n="@@login.local.submit">
              Sign in
            </button>
          </form>
        }
      }
    }
  `,
  styles: `
    .providers {
      list-style: none;
      margin: 0 0 var(--trove-space-6);
      padding: 0;
      display: flex;
      flex-direction: column;
      align-items: flex-start;
      gap: var(--trove-space-3);
    }
    .unavailable {
      margin-inline-start: var(--trove-space-3);
    }
    .local {
      display: flex;
      flex-direction: column;
      align-items: flex-start;
      max-inline-size: 24rem;
    }
    .local-error {
      color: var(--mat-sys-error);
    }
  `,
})
export class Login {
  private readonly api = inject(LoginApi);
  private readonly navigation = inject(BrowserNavigation);
  private readonly session = inject(SessionStore);
  private readonly router = inject(Router);
  private readonly preferences = inject(PreferencesStore);

  /** Query parameters, bound by the router's component input binding. */
  readonly returnTo = input<string | undefined>();
  readonly local = input<string | undefined>();
  readonly reauth = input<string | undefined>();

  protected readonly options = resource({ loader: () => this.api.options() });
  protected readonly redirecting = signal(false);
  protected readonly username = signal('');
  protected readonly password = signal('');
  protected readonly busy = signal(false);
  protected readonly localError = signal<string | null>(null);

  protected readonly showLocal = computed(
    () => this.local() === '1' || (this.options.value()?.localLogin ?? false),
  );

  constructor() {
    effect(() => {
      const o = this.options.value();
      if (!o?.autoRedirect || this.local() === '1') return;
      const provider = o.providers.find((p) => p.id === o.autoRedirect && p.available);
      if (provider) untracked(() => this.start(provider.startUrl));
    });
  }

  /** Where "continue without signing in" leads: the route the caller wanted, or the start page. */
  protected continuePath(): string {
    return safeReturnTo(this.returnTo());
  }

  protected asApiError(e: unknown): ApiError {
    return e instanceof ApiError ? e : new ApiError(0, null, null);
  }

  /** The provider's start URL with `return_to` as a path under `/ui/{locale}/` (R-AUTH-009). */
  protected start(startUrl: string): void {
    const target = new URLSearchParams({
      return_to: `/ui/${this.preferences.locale}${safeReturnTo(this.returnTo())}`,
    });
    if (this.reauth() === '1') target.set('purpose', 'reauth');
    this.redirecting.set(true);
    this.navigation.assign(`${startUrl}?${target.toString()}`);
  }

  protected async submitLocal(event: Event): Promise<void> {
    event.preventDefault();
    this.busy.set(true);
    this.localError.set(null);
    try {
      await this.api.localLogin(this.username(), this.password());
      this.password.set('');
      await this.session.reload();
      await this.router.navigateByUrl(safeReturnTo(this.returnTo()));
    } catch (e) {
      this.localError.set(e instanceof ApiError ? describeProblem(e).text : null);
    } finally {
      this.busy.set(false);
    }
  }
}
