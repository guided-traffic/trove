import { Injectable, computed, inject, signal } from '@angular/core';
import { Router } from '@angular/router';
import { ApiError } from '../api/problem';
import {
  AuthzMe,
  PermissionCheck,
  SessionApi,
  Whoami,
} from '../api/session-api';
import { AuthzEpoch } from './authz-epoch';
import { BrowserNavigation } from './browser-navigation';

export type SessionState = 'unknown' | 'loading' | 'authenticated' | 'anonymous' | 'failed';

/** Check answers are cached for 60 seconds (R-UI-005). */
export const CHECK_TTL_MS = 60_000;

const CHANNEL = 'trove-session';

/**
 * The session of this tab (R-UI-046): `whoami`, `authz/me`, the check cache and the authorization
 * epoch. Reset on 401 and on sign-out; a 403 drops the cached decisions only, and the epoch
 * survives so that the reload it causes still carries it.
 */
@Injectable({ providedIn: 'root' })
export class SessionStore {
  private readonly api = inject(SessionApi);
  private readonly epoch = inject(AuthzEpoch);
  private readonly router = inject(Router);
  private readonly navigation = inject(BrowserNavigation);

  private readonly _state = signal<SessionState>('unknown');
  private readonly _whoami = signal<Whoami | null>(null);
  private readonly _me = signal<AuthzMe | null>(null);
  private readonly _ended = signal(false);
  private readonly _reauthRequired = signal(false);
  private readonly _loadError = signal<ApiError | null>(null);

  private loading: Promise<void> | null = null;
  private checkCache = new Map<string, { allowed: boolean; at: number }>();
  private readonly channel: BroadcastChannel | null;

  readonly state = this._state.asReadonly();
  readonly whoami = this._whoami.asReadonly();
  readonly me = this._me.asReadonly();
  /** The server ended the session while the app was open: the "session has ended" dialog. */
  readonly ended = this._ended.asReadonly();
  /** A request needed recent authentication (401 `reauthentication_required`). */
  readonly reauthRequired = this._reauthRequired.asReadonly();
  /** Why `state` is `failed`. */
  readonly loadError = this._loadError.asReadonly();

  readonly authenticated = computed(() => this._state() === 'authenticated');
  readonly capabilities = computed(() => this._me()?.capabilities ?? []);
  /** Any capability at all: the Administration area is shown (R-UI-002). */
  readonly isAdministrator = computed(() => this.capabilities().length > 0);

  constructor() {
    this.channel = typeof BroadcastChannel === 'undefined' ? null : new BroadcastChannel(CHANNEL);
    this.channel?.addEventListener('message', (event: MessageEvent<unknown>) => {
      if (event.data === 'signed-out') {
        this.reset();
        void this.router.navigateByUrl('/logged-out');
      }
    });
  }

  has(capability: string): boolean {
    return this.capabilities().includes(capability);
  }

  /** Loads `whoami` and `authz/me` once; concurrent calls share the request. */
  load(): Promise<void> {
    if (this._state() === 'authenticated' || this._state() === 'anonymous') {
      return Promise.resolve();
    }
    this.loading ??= this.doLoad().finally(() => {
      this.loading = null;
    });
    return this.loading;
  }

  /** Loads again after a failure or a change of the principal. */
  reload(): Promise<void> {
    this._state.set('unknown');
    return this.load();
  }

  /** Re-reads `authz/me` after an own permission change (R-UI-046); clears the check cache. */
  async refreshCapabilities(): Promise<void> {
    this.checkCache.clear();
    this._me.set(await this.api.authzMe());
  }

  /**
   * Batched permission checks for one screen (R-UI-005): at most 100 per call, answers cached for
   * 60 seconds. Resolves with one boolean per input, in order.
   */
  async check(checks: readonly PermissionCheck[]): Promise<boolean[]> {
    const now = Date.now();
    const key = (c: PermissionCheck): string => JSON.stringify([c.repository, c.action, c.package, c.path]);
    const missing = checks.filter((c) => {
      const hit = this.checkCache.get(key(c));
      return !hit || now - hit.at > CHECK_TTL_MS;
    });
    const unique = [...new Map(missing.map((c) => [key(c), c])).values()];
    for (let i = 0; i < unique.length; i += 100) {
      const chunk = unique.slice(i, i + 100);
      const answer = await this.api.check(chunk);
      chunk.forEach((c, n) =>
        this.checkCache.set(key(c), { allowed: answer.results[n]?.allowed ?? false, at: now }),
      );
    }
    return checks.map((c) => this.checkCache.get(key(c))?.allowed ?? false);
  }

  /** Called on any 403: the cached decisions may be wrong; the epoch stays. */
  dropCheckCache(): void {
    this.checkCache.clear();
  }

  /** The server ended the session during use (401): show the dialog, forget the principal. */
  markEnded(): void {
    if (this._state() !== 'authenticated') return;
    this.reset();
    this._ended.set(true);
  }

  markReauthRequired(): void {
    this._reauthRequired.set(true);
  }

  clearReauthRequired(): void {
    this._reauthRequired.set(false);
  }

  /** Forgets everything the tab holds about the principal, the epoch included. */
  reset(): void {
    this._state.set('unknown');
    this._whoami.set(null);
    this._me.set(null);
    this._loadError.set(null);
    this._reauthRequired.set(false);
    this._ended.set(false);
    this.checkCache.clear();
    this.epoch.clear();
  }

  /**
   * `POST /auth/logout` with the CSRF header, then the provider's `redirectTo` or `/logged-out`.
   * Other tabs of this browser follow through the BroadcastChannel (R-UI-004).
   */
  async signOut(): Promise<void> {
    const answer = await this.api.logout();
    this.reset();
    this.channel?.postMessage('signed-out');
    if (answer.redirectTo) {
      this.navigation.assign(answer.redirectTo);
    } else {
      await this.router.navigateByUrl('/logged-out');
    }
  }

  private async doLoad(): Promise<void> {
    this._state.set('loading');
    this._loadError.set(null);
    try {
      this._whoami.set(await this.api.whoami());
      this._me.set(await this.api.authzMe());
      this._state.set('authenticated');
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) {
        this._whoami.set(null);
        this._me.set(null);
        this._state.set('anonymous');
      } else {
        this._loadError.set(e instanceof ApiError ? e : null);
        this._state.set('failed');
      }
    }
  }
}
