import { Injectable, inject } from '@angular/core';
import { ApiClient } from './client';
import { PageFetcher } from './pagination';
import { cleanQuery, seg } from './query';
import { Schema } from './types';

/*
 * Tokens (R-AUTH-022 to R-AUTH-032; docs/developer/identity.md). A created or rotated token
 * carries its value once: callers keep it in a component signal and nowhere else (R-UI-043).
 */

export type Token = Schema<'Token'>;
export type TokenCreated = Schema<'TokenCreated'>;
export type TokenScope = Schema<'TokenScope'>;
export type TokenScopeGrant = Schema<'TokenScopeGrant'>;
export type CreateTokenRequest = Schema<'CreateTokenRequest'>;
export type RevokedToken = Schema<'RevokedToken'>;

export interface TokenFilters {
  /** `me`, a username or `sa:<name>`. */
  readonly owner?: string;
  readonly type?: Token['type'];
  readonly state?: Token['state'] | 'all';
  readonly expiresBefore?: string;
  readonly lastUsedBefore?: string;
}

export abstract class TokensApi {
  abstract list(filters: TokenFilters): PageFetcher<Token>;
  /** For the caller (`POST /tokens`); `ownerName` is only the caller's own name (no impersonation). */
  abstract create(request: CreateTokenRequest, idempotencyKey: string): Promise<TokenCreated>;
  abstract createForServiceAccount(
    name: string,
    request: CreateTokenRequest,
    idempotencyKey: string,
  ): Promise<TokenCreated>;
  abstract rotate(id: string, overlapSeconds: number, idempotencyKey: string): Promise<TokenCreated>;
  abstract revoke(id: string): Promise<void>;
  abstract revokeByValue(value: string): Promise<RevokedToken>;
  abstract revokeAllOfUser(username: string): Promise<void>;
  abstract revokeAllOfServiceAccount(name: string): Promise<void>;
  abstract revokeAllOwn(): Promise<void>;
}

@Injectable()
export class HttpTokensApi extends TokensApi {
  private readonly client = inject(ApiClient);

  override list(filters: TokenFilters): PageFetcher<Token> {
    return this.client.page<Token>('/api/v1/tokens', cleanQuery(filters));
  }

  override async create(request: CreateTokenRequest, idempotencyKey: string): Promise<TokenCreated> {
    return (await this.client.post<TokenCreated>('/api/v1/tokens', request, { idempotencyKey })).body;
  }

  override async createForServiceAccount(
    name: string,
    request: CreateTokenRequest,
    idempotencyKey: string,
  ): Promise<TokenCreated> {
    return (
      await this.client.post<TokenCreated>(`/api/v1/service-accounts/${seg(name)}/tokens`, request, {
        idempotencyKey,
      })
    ).body;
  }

  override async rotate(id: string, overlapSeconds: number, idempotencyKey: string): Promise<TokenCreated> {
    return (
      await this.client.post<TokenCreated>(`/api/v1/tokens/${seg(id)}/rotate`, { overlapSeconds }, {
        idempotencyKey,
      })
    ).body;
  }

  override async revoke(id: string): Promise<void> {
    await this.client.delete(`/api/v1/tokens/${seg(id)}`);
  }

  override async revokeByValue(value: string): Promise<RevokedToken> {
    return (await this.client.post<RevokedToken>('/api/v1/tokens/revoke', { token: value })).body;
  }

  override async revokeAllOfUser(username: string): Promise<void> {
    await this.client.delete(`/api/v1/users/${seg(username)}/tokens`);
  }

  override async revokeAllOfServiceAccount(name: string): Promise<void> {
    await this.client.delete(`/api/v1/service-accounts/${seg(name)}/tokens`);
  }

  override async revokeAllOwn(): Promise<void> {
    await this.client.delete('/api/v1/users/me/tokens');
  }
}
