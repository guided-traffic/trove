import { Injectable, inject } from '@angular/core';
import { ApiClient } from './client';
import { seg } from './query';
import { Versioned } from './repositories-api';
import { Schema } from './types';

/*
 * The registrations of trove Bastions (R-BAS-004, R-BAS-007; docs/developer/bastion.md). The shapes
 * are the generated ones. The list is one page (at most `bastions.max_registrations` exist) and
 * carries no tag; a registration read on its own carries the `ETag` a change needs (R-API-012), so a
 * switch to disabled or enabled reads the registration first.
 */

export type Bastion = Schema<'Bastion'>;
export type BastionTokenSummary = Schema<'BastionTokenSummary'>;
export type BastionContact = Schema<'BastionContact'>;
export type NewBastion = Schema<'NewBastion'>;
export type NewBastionToken = Schema<'NewBastionToken'>;
export type BastionTokenCreated = Schema<'BastionTokenCreated'>;

/** The name rule of a registration (R-BAS-004), checked live in the form; the server decides. */
export const BASTION_NAME_PATTERN = /^[a-z][a-z0-9-]{0,19}$/;

export abstract class BastionsApi {
  /** Every registration, by name; each with its unrevoked tokens and the last contact of each replica. */
  abstract list(): Promise<readonly Bastion[]>;
  abstract get(name: string): Promise<Versioned<Bastion>>;
  abstract create(body: NewBastion, idempotencyKey: string): Promise<Bastion>;
  /** Disables or enables; a disabled registration's tokens are refused (R-BAS-004). */
  abstract setEnabled(name: string, enabled: boolean, etag: string): Promise<Versioned<Bastion>>;
  /** Deletes the registration and revokes its tokens. */
  abstract delete(name: string): Promise<void>;
  /** The secret is in this answer only (R-AUTH-022). */
  abstract createToken(name: string, body: NewBastionToken, idempotencyKey: string): Promise<BastionTokenCreated>;
  abstract revokeToken(name: string, id: string): Promise<void>;
}

@Injectable()
export class HttpBastionsApi extends BastionsApi {
  private readonly client = inject(ApiClient);

  override async list(): Promise<readonly Bastion[]> {
    return (await this.client.getJson<{ items: Bastion[] }>('/api/v1/bastions')).body.items;
  }

  override async get(name: string): Promise<Versioned<Bastion>> {
    const r = await this.client.getJson<Bastion>(`/api/v1/bastions/${seg(name)}`);
    return { value: r.body, etag: r.etag ?? '' };
  }

  override async create(body: NewBastion, idempotencyKey: string): Promise<Bastion> {
    return (await this.client.post<Bastion>('/api/v1/bastions', body, { idempotencyKey })).body;
  }

  override async setEnabled(name: string, enabled: boolean, etag: string): Promise<Versioned<Bastion>> {
    const r = await this.client.patch<Bastion>(`/api/v1/bastions/${seg(name)}`, { enabled }, etag);
    return { value: r.body, etag: r.etag ?? '' };
  }

  override async delete(name: string): Promise<void> {
    await this.client.delete(`/api/v1/bastions/${seg(name)}`);
  }

  override async createToken(name: string, body: NewBastionToken, idempotencyKey: string): Promise<BastionTokenCreated> {
    return (await this.client.post<BastionTokenCreated>(`/api/v1/bastions/${seg(name)}/tokens`, body, { idempotencyKey }))
      .body;
  }

  override async revokeToken(name: string, id: string): Promise<void> {
    await this.client.delete(`/api/v1/bastions/${seg(name)}/tokens/${seg(id)}`);
  }
}
