import { Injectable, inject } from '@angular/core';
import { ApiClient } from './client';

/** `GET /api/v1/auth/login-options` (R-AUTH-009): the only source of the sign-in screen. */
export interface LoginOptions {
  readonly providers: readonly {
    readonly id: string;
    readonly displayName: string;
    /** Server-relative start URL, `/auth/oidc/{providerId}/start`. */
    readonly startUrl: string;
    readonly available: boolean;
  }[];
  /** The break-glass form is offered on the page (it is reachable with `?local=1` regardless). */
  readonly localLogin: boolean;
  readonly anonymousAccess: boolean;
  /** Provider id to redirect to at once, or null. */
  readonly autoRedirect: string | null;
}

export abstract class LoginApi {
  abstract options(): Promise<LoginOptions>;
  /** Break-glass sign-in, `POST /auth/local/login` (R-AUTH-014); the session arrives as a cookie. */
  abstract localLogin(username: string, password: string): Promise<void>;
}

@Injectable()
export class HttpLoginApi extends LoginApi {
  private readonly client = inject(ApiClient);

  async options(): Promise<LoginOptions> {
    return (await this.client.getJson<LoginOptions>('/api/v1/auth/login-options')).body;
  }

  async localLogin(username: string, password: string): Promise<void> {
    await this.client.post<unknown>('/auth/local/login', { username, password });
  }
}
