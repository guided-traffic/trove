import { DOCUMENT } from '@angular/common';
import { Component, computed, effect, inject, input, isDevMode, resource, signal, untracked } from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { ActivatedRoute, Router, RouterLink } from '@angular/router';
import { map } from 'rxjs';
import { LoginApi } from '../../api/login-api';
import { ApiError } from '../../api/problem';
import { BrowserNavigation, safeReturnTo } from '../../core/browser-navigation';
import { describeProblem } from '../../core/http/problem-messages';
import { SessionStore } from '../../core/session-store';
import { Banner } from '../../shared/banner';
import { ErrorNotice } from '../../shared/error-notice';
import { LogoPreview } from './logo-preview';

/**
 * Sign-in (R-UI-004, Q-UI-125). A screen without the shell, laid out as one column: the wordmark, the
 * heading, the break-glass form when it is offered, then the providers, below "or sign in with" when
 * the form is there. Renders only from `GET /api/v1/auth/login-options`: each provider button leaves
 * the application for the provider's `startUrl` with `return_to` set to the UI route (R-AUTH-009: a
 * path under `/ui/`). `autoRedirect` navigates at once unless the URL carries `?auto_sign_in=false`,
 * which also shows the break-glass form; without it the form shows when `localLogin` is true. There is
 * no username and password form for regular users (INV-7).
 */
@Component({
  selector: 'trove-login',
  imports: [RouterLink, Banner, ErrorNotice, LogoPreview],
  template: `
    <trove-banner />
    <div class="bare-panel">
      <img class="bare-logo" src="brand/trove-horizontal.svg" alt="trove" width="190" height="64" />
      <h1 tabindex="-1" i18n="@@login.heading">Sign in</h1>

      @if (reauth() === '1') {
        <p role="status" class="reauth" i18n="@@login.reauth">
          The action you tried needs a recent sign-in. Sign in again, then repeat it. Your entries are kept.
        </p>
      }

      @if (options.error(); as failure) {
        <trove-error-notice [error]="asApiError(failure)" (retry)="options.reload()" />
      } @else if (options.isLoading()) {
        <p role="status" class="state" i18n="@@login.loading">Loading the sign-in options.</p>
      } @else if (options.value(); as o) {
        @if (redirecting()) {
          <p role="status" class="state" i18n="@@login.redirecting">Redirecting to the identity provider.</p>
        } @else {
          @if (o.providers.length === 0 && !showLocal()) {
            <p class="state" i18n="@@login.no-providers">No sign-in method is configured.</p>
          }

          @if (showLocal()) {
            <form
              class="local"
              i18n-aria-label="@@login.local.heading"
              aria-label="Local administrator"
              (submit)="submitLocal($event)"
            >
              <div class="field">
                <label class="field-label" for="login-username" i18n="@@login.local.username">Username</label>
                <input
                  id="login-username"
                  class="text-input field-w-name"
                  name="username"
                  autocomplete="username"
                  [value]="username()"
                  (input)="username.set($any($event.target).value)"
                />
              </div>
              <div class="field">
                <label class="field-label" for="login-password" i18n="@@login.local.password">Password</label>
                <input
                  id="login-password"
                  class="text-input field-w-name"
                  type="password"
                  name="password"
                  autocomplete="current-password"
                  [value]="password()"
                  (input)="password.set($any($event.target).value)"
                />
              </div>
              @if (localError(); as text) {
                <p role="alert" class="local-error">{{ text }}</p>
              }
              <button class="btn btn-confirm btn-block" type="submit" [disabled]="busy()" i18n="@@login.local.submit">
                Sign in
              </button>
            </form>
          }

          @if (o.providers.length > 0) {
            @if (showLocal()) {
              <p class="divider"><span i18n="@@login.or">or sign in with</span></p>
            }
            <ul class="providers">
              @for (p of o.providers; track p.id) {
                <li>
                  <!-- Below the form a provider is the second way in; alone it is the primary action. -->
                  <button
                    class="btn btn-block"
                    [class.btn-confirm]="!showLocal()"
                    [class.btn-default]="showLocal()"
                    type="button"
                    [disabled]="!p.available"
                    (click)="start(p.startUrl)"
                  >
                    @if (!showLocal()) {
                      <ng-container i18n="@@login.with">Sign in with</ng-container>
                    }
                    {{ p.displayName }}
                  </button>
                  @if (!p.available) {
                    <span class="unavailable" i18n="@@login.unavailable">Currently unavailable</span>
                  }
                </li>
              }
            </ul>
          }

          @if (o.publicContent && reauth() !== '1') {
            <p class="anonymous">
              <a [routerLink]="continuePath()" i18n="@@login.anonymous">Continue without signing in</a>
            </p>
          }
        }
      }
    </div>
    @if (logoPreview) {
      <trove-logo-preview />
    }
  `,
  styles: `
    .local,
    .providers {
      display: flex;
      flex-direction: column;
      gap: var(--trove-space-4);
    }
    .field {
      display: flex;
      flex-direction: column;
      gap: var(--trove-space-2);
    }
    .providers {
      list-style: none;
      margin: 0;
      padding: 0;
      gap: var(--trove-space-3);
    }
    .providers li {
      display: flex;
      flex-direction: column;
      gap: var(--trove-space-1);
    }
    .unavailable,
    .state {
      color: var(--trove-text-muted);
      font-size: var(--trove-text-muted-size);
      text-align: center;
    }
    .local-error {
      margin: 0;
      color: var(--trove-status-bad);
    }
    .divider {
      display: flex;
      align-items: center;
      gap: var(--trove-space-3);
      margin: var(--trove-space-6) 0 var(--trove-space-4);
      color: var(--trove-text-muted);
      font-size: var(--trove-text-muted-size);
    }
    .divider::before,
    .divider::after {
      content: '';
      flex: 1 1 auto;
      border-block-start: 1px solid var(--trove-border);
    }
    .anonymous {
      margin-block-start: var(--trove-space-6);
      text-align: center;
    }
  `,
})
export class Login {
  private readonly api = inject(LoginApi);
  private readonly navigation = inject(BrowserNavigation);
  private readonly session = inject(SessionStore);
  private readonly router = inject(Router);
  private readonly document = inject(DOCUMENT);

  /** Query parameters, bound by the router's component input binding. */
  readonly returnTo = input<string | undefined>();

  readonly reauth = input<string | undefined>();

  /** The logo drafts under the sign-in column, while the logo is designed; never in a production build. */
  protected readonly logoPreview = isDevMode();
  protected readonly options = resource({ loader: () => this.api.options() });
  protected readonly redirecting = signal(false);
  protected readonly username = signal('');
  protected readonly password = signal('');
  protected readonly busy = signal(false);
  protected readonly localError = signal<string | null>(null);

  /**
   * `?auto_sign_in=false` waives the automatic sign-in at the provider for this visit and shows the
   * break-glass form (Q-UI-125). The parameter keeps the snake_case name operators know from other
   * tools' sign-in pages, so it is read from the query, not bound as an input.
   */
  private readonly autoSignInOff = toSignal(
    inject(ActivatedRoute).queryParamMap.pipe(map((q) => q.get('auto_sign_in') === 'false')),
    { initialValue: false },
  );

  protected readonly showLocal = computed(
    () => this.autoSignInOff() || (this.options.value()?.localLogin ?? false),
  );

  constructor() {
    effect(() => {
      const o = this.options.value();
      if (!o?.autoRedirect || this.autoSignInOff()) return;
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

  /**
   * The provider's start URL with `return_to` as the route under the application's own base (R-AUTH-009): the path of the
   * resolved `document.baseURI`, so a relative `<base href>` counts as the browser resolves it; `/ui/en/` in a build per locale,
   * `/ui/de/` in the German one, `/ui/` on the development server, which serves no locale folder (UI-A-49). A base outside
   * `/ui/` (no `<base>` element) gives the application's base `/ui/`.
   */
  protected start(startUrl: string): void {
    const resolved = new URL(this.document.baseURI).pathname;
    const base = (resolved.startsWith('/ui/') ? resolved : '/ui/').replace(/\/+$/, '');
    const target = new URLSearchParams({
      return_to: `${base}${safeReturnTo(this.returnTo())}`,
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
