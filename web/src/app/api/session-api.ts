import { Injectable, inject } from '@angular/core';
import { ApiClient } from './client';
import { Schema } from './types';

/*
 * The caller's session: who, what they may do, and the way out. Shapes are the generated ones
 * (R-AUTH-009 and docs/developer/login.md for whoami, R-AUTHZ-023 for check, authorization.md for
 * authz/me). `/auth/logout` lives outside the management API document.
 */

/** `GET /api/v1/auth/whoami`. */
export type Whoami = Schema<'Whoami'>;

/** `GET /api/v1/authz/me`: what drives navigation (capabilities), plus roles and principals. */
export type AuthzMe = Schema<'AuthzMe'>;

/**
 * One check of `POST /api/v1/authz/check` (R-AUTHZ-023). `action` is one of the six actions of
 * R-AUTHZ-006; the type stays a string so that callers can pass an action they hold in a variable,
 * and the API answers 400 for an unknown one.
 */
export type PermissionCheck = Omit<Schema<'PermissionCheckItem'>, 'action'> & { readonly action: string };

export type PermissionCheckAnswer = Schema<'PermissionCheckResponse'>;

/** Body of `POST /auth/logout`; `redirectTo` is the provider's end-session URL, when it has one. */
export interface LogoutAnswer {
  readonly redirectTo?: string | null;
}

export abstract class SessionApi {
  abstract whoami(): Promise<Whoami>;
  abstract authzMe(): Promise<AuthzMe>;
  abstract check(checks: readonly PermissionCheck[]): Promise<PermissionCheckAnswer>;
  abstract logout(): Promise<LogoutAnswer>;
}

@Injectable()
export class HttpSessionApi extends SessionApi {
  private readonly client = inject(ApiClient);

  async whoami(): Promise<Whoami> {
    return (await this.client.get('/api/v1/auth/whoami')).body;
  }

  async authzMe(): Promise<AuthzMe> {
    return (await this.client.get('/api/v1/authz/me')).body;
  }

  async check(checks: readonly PermissionCheck[]): Promise<PermissionCheckAnswer> {
    return (await this.client.post<PermissionCheckAnswer>('/api/v1/authz/check', { checks })).body;
  }

  async logout(): Promise<LogoutAnswer> {
    // The answer may be 204 without a body.
    return (await this.client.post<LogoutAnswer | null>('/auth/logout')).body ?? {};
  }
}
