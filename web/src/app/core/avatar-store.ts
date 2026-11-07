import { Injectable, computed, inject, signal } from '@angular/core';
import { IdentityApi } from '../api/identity-api';
import { SessionStore } from './session-store';

/**
 * The uploaded avatar of the signed-in user (R-UI-051): `url` is the `avatarUrl` of their user record, or null for the
 * generated robot. `whoami` does not carry it, so the shell reads the record once per session and the profile page sets
 * the new URL after an upload or a removal; the top bar follows without a reload. A principal that has no user record
 * (a service account, a CI token) never has an avatar, and a failed read leaves the robot.
 */
@Injectable({ providedIn: 'root' })
export class AvatarStore {
  private readonly identity = inject(IdentityApi);
  private readonly session = inject(SessionStore);

  private readonly _url = signal<string | null>(null);
  /** The ID of the principal the URL belongs to, so a late answer of a signed-out session is dropped. */
  private owner: string | null = null;

  readonly url = this._url.asReadonly();
  /** Whether the principal can have an avatar: users and the break-glass account (R-API-055). */
  readonly available = computed(() => {
    const kind = this.session.whoami()?.principal.kind;
    return kind === 'user' || kind === 'break_glass';
  });

  /** Reads the record of the signed-in user; the robot stays when it cannot be read. */
  async refresh(): Promise<void> {
    const id = this.session.whoami()?.principal.id ?? null;
    this.owner = id;
    if (id === null || !this.available()) {
      this._url.set(null);
      return;
    }
    try {
      const user = await this.identity.me();
      if (this.owner === id) this._url.set(user.avatarUrl);
    } catch {
      if (this.owner === id) this._url.set(null);
    }
  }

  /** The URL the server returned for the new image, or null after a removal. */
  set(url: string | null): void {
    this._url.set(url);
  }

  clear(): void {
    this.owner = null;
    this._url.set(null);
  }
}
