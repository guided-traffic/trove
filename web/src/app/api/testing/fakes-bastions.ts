import { Provider } from '@angular/core';
import {
  Bastion,
  BastionContact,
  BastionsApi,
  BastionTokenCreated,
  BastionTokenSummary,
  NewBastion,
  NewBastionToken,
} from '../bastions-api';
import { ApiError } from '../problem';
import { Versioned } from '../repositories-api';

/*
 * In-memory fake of the Bastion registrations (R-BAS-004, R-BAS-007, R-UI-047 component tier). It
 * models what the screen depends on: the `ETag` of a change (412 for a stale tag), the name rule and
 * the duplicate (422, 409), the limit of active tokens (409 `token_limit_reached`), and a token secret
 * that is returned once and never again.
 */

const problem = (status: number, code: string, extra: Record<string, unknown> = {}): ApiError =>
  new ApiError(status, { code, ...extra }, 'req-1');

/** A secret of the shape of a Bastion token: `trv_bst_` and 49 characters (R-BAS-004). */
export const BASTION_SECRET = 'trv_bst_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdefghijklm';

const DAY = 86_400_000;

/** A time `days` from now (negative: ago) as the API writes it. */
export function inDays(days: number): string {
  return new Date(Date.now() + days * DAY).toISOString();
}

export function sampleBastionToken(over: Partial<BastionTokenSummary> = {}): BastionTokenSummary {
  return {
    id: '0192f0d1-1c2a-7c3e-8a51-6f7d2b9c4e41',
    hint: 'trv_bst_3kQ9',
    description: 'partners 2026',
    createdAt: inDays(-165),
    expiresAt: inDays(200),
    lastUsedAt: inDays(-1),
    ...over,
  };
}

export function sampleBastionContact(over: Partial<BastionContact> = {}): BastionContact {
  return {
    replica: 'trove-bastion-7d9c-x2k4q',
    contactAt: inDays(-0.01),
    clientAddress: '198.51.100.20',
    release: '0.15.0',
    interfaceVersion: 1,
    baseUrl: 'https://packages.example.com',
    ...over,
  };
}

export function sampleBastion(over: Partial<Bastion> = {}): Bastion {
  return {
    id: '0192f0c4-7a7e-7b51-9b1e-2f0d6c9e1a40',
    name: 'partners',
    description: 'The partner network',
    enabled: true,
    createdAt: inDays(-170),
    tokens: [sampleBastionToken()],
    contacts: [
      sampleBastionContact(),
      sampleBastionContact({ replica: 'trove-bastion-7d9c-m8v2j', clientAddress: '198.51.100.21', contactAt: inDays(-0.02) }),
    ],
    ...over,
  };
}

export class FakeBastionsApi extends BastionsApi {
  readonly calls: string[] = [];
  /** The `If-Match` of every change, in order. */
  readonly ifMatch: string[] = [];
  readonly keys: string[] = [];
  /** The last token request. */
  lastTokenRequest: NewBastionToken | null = null;
  /** At most this many unrevoked tokens per registration (`auth.tokens.max_active_per_principal.bst`). */
  tokenLimit = 3;
  failure: ApiError | null = null;
  listFailure: ApiError | null = null;
  bastions: Bastion[] = [
    sampleBastion(),
    sampleBastion({
      id: '0192f0c4-7a7e-7b51-9b1e-2f0d6c9e1a41',
      name: 'standby',
      description: '',
      enabled: false,
      tokens: [],
      contacts: [],
    }),
  ];
  private readonly revisions = new Map<string, number>();

  etagOf(name: string): string {
    return `"${this.revisions.get(name) ?? 1}"`;
  }

  /** Simulates a change by someone else: the stored revision moves on. */
  bumpRevision(name: string): void {
    this.revisions.set(name, (this.revisions.get(name) ?? 1) + 1);
  }

  private fail(): void {
    if (this.failure) {
      const e = this.failure;
      this.failure = null;
      throw e;
    }
  }

  private find(name: string): Bastion {
    const found = this.bastions.find((b) => b.name === name);
    if (!found) throw problem(404, 'not_found');
    return found;
  }

  override async list(): Promise<readonly Bastion[]> {
    if (this.listFailure) throw this.listFailure;
    return [...this.bastions].sort((a, b) => a.name.localeCompare(b.name));
  }

  override async get(name: string): Promise<Versioned<Bastion>> {
    return { value: this.find(name), etag: this.etagOf(name) };
  }

  override async create(body: NewBastion, idempotencyKey: string): Promise<Bastion> {
    this.fail();
    this.keys.push(idempotencyKey);
    this.calls.push(`create ${body.name}`);
    if (!/^[a-z][a-z0-9-]{0,19}$/.test(body.name)) {
      throw new ApiError(422, { code: 'validation_failed', errors: [{ pointer: '/name', code: 'invalid', detail: 'invalid' }] }, 'req-1');
    }
    if (this.bastions.some((b) => b.name === body.name)) throw problem(409, 'conflict');
    const created = sampleBastion({
      id: `b-${body.name}`,
      name: body.name,
      description: body.description ?? '',
      createdAt: new Date().toISOString(),
      tokens: [],
      contacts: [],
    });
    this.bastions = [...this.bastions, created];
    return created;
  }

  override async setEnabled(name: string, enabled: boolean, etag: string): Promise<Versioned<Bastion>> {
    this.fail();
    this.ifMatch.push(etag);
    this.calls.push(`setEnabled ${name} ${enabled}`);
    const current = this.find(name);
    if (etag !== this.etagOf(name)) throw problem(412, 'precondition_failed', { currentEtag: this.etagOf(name) });
    this.bumpRevision(name);
    const changed = { ...current, enabled };
    this.bastions = this.bastions.map((b) => (b.name === name ? changed : b));
    return { value: changed, etag: this.etagOf(name) };
  }

  override async delete(name: string): Promise<void> {
    this.fail();
    this.find(name);
    this.calls.push(`delete ${name}`);
    this.bastions = this.bastions.filter((b) => b.name !== name);
  }

  override async createToken(name: string, body: NewBastionToken, idempotencyKey: string): Promise<BastionTokenCreated> {
    this.fail();
    this.keys.push(idempotencyKey);
    this.lastTokenRequest = body;
    this.calls.push(`createToken ${name} ${body.description ?? ''} ${body.ttlSeconds ?? 'default'}`);
    const current = this.find(name);
    if (current.tokens.length >= this.tokenLimit) throw problem(409, 'token_limit_reached');
    const ttl = body.ttlSeconds ?? 365 * 86_400;
    const summary = sampleBastionToken({
      id: `t-${current.tokens.length + 1}`,
      hint: 'trv_bst_Zz09',
      description: body.description ?? '',
      createdAt: new Date().toISOString(),
      expiresAt: new Date(Date.now() + ttl * 1000).toISOString(),
      lastUsedAt: null,
    });
    this.bastions = this.bastions.map((b) => (b.name === name ? { ...b, tokens: [summary, ...b.tokens] } : b));
    return {
      id: summary.id,
      token: BASTION_SECRET,
      hint: summary.hint,
      description: summary.description,
      createdAt: summary.createdAt,
      expiresAt: summary.expiresAt,
    };
  }

  override async revokeToken(name: string, id: string): Promise<void> {
    this.fail();
    this.calls.push(`revokeToken ${name} ${id}`);
    const current = this.find(name);
    if (!current.tokens.some((t) => t.id === id)) throw problem(404, 'not_found');
    this.bastions = this.bastions.map((b) => (b.name === name ? { ...b, tokens: b.tokens.filter((t) => t.id !== id) } : b));
  }
}

export function provideBastionFakes(): Provider[] {
  return [{ provide: BastionsApi, useClass: FakeBastionsApi }];
}
