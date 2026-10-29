import { Injectable, inject } from '@angular/core';
import { ApiClient } from './client';

/*
 * Endpoints the OpenAPI document does not describe yet (the identity and authorization work adds
 * them). The shapes below follow the requirement pages named on each type and are replaced by the
 * generated types when the operations land in api/openapi.yaml; the service interface stays.
 */

/** `GET /api/v1/auth/whoami` (R-AUTH-… in identity-and-authentication.md, R-API-021). */
export interface Whoami {
  readonly principal: { readonly kind: string; readonly name: string; readonly id: string };
  readonly credential: {
    readonly type: string;
    readonly tokenId?: string | null;
    readonly scope?: unknown;
    readonly expiresAt?: string | null;
  };
}

/** `GET /api/v1/authz/me` (R-AUTHZ-… in authorization.md): what drives navigation. */
export interface AuthzMe {
  /** Effective system capabilities such as `settings:read` (R-AUTHZ-010). */
  readonly capabilities: readonly string[];
  /** Principal references the caller acts as: the user, its groups. Shown on the empty state. */
  readonly principals?: readonly string[];
}

/** One check of `POST /api/v1/authz/check` (R-AUTHZ-023). */
export interface PermissionCheck {
  readonly repository: string;
  readonly action: string;
  readonly package?: string;
  readonly path?: string;
}

export interface PermissionCheckAnswer {
  readonly results: readonly { readonly allowed: boolean }[];
  /** The authorization epoch at the time of the answer. */
  readonly epoch?: number;
}

/** Body of `POST /auth/logout`; `redirectTo` is the provider's end-session URL, when it has one. */
export interface LogoutAnswer {
  readonly redirectTo?: string | null;
}

/** The caller's session: who, what they may do, and the way out. */
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
    return (await this.client.getJson<Whoami>('/api/v1/auth/whoami')).body;
  }

  async authzMe(): Promise<AuthzMe> {
    return (await this.client.getJson<AuthzMe>('/api/v1/authz/me')).body;
  }

  async check(checks: readonly PermissionCheck[]): Promise<PermissionCheckAnswer> {
    return (await this.client.post<PermissionCheckAnswer>('/api/v1/authz/check', { checks })).body;
  }

  async logout(): Promise<LogoutAnswer> {
    // The answer may be 204 without a body.
    return (await this.client.post<LogoutAnswer | null>('/auth/logout')).body ?? {};
  }
}
