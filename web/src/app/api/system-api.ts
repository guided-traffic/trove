import { Injectable, inject } from '@angular/core';
import { ApiClient } from './client';
import { Schema } from './types';

export type SystemInfo = Schema<'SystemInfo'>;
export type Replica = Schema<'Replica'>;
export type ValkeyView = Schema<'ValkeyView'>;
export type RoleSummary = Schema<'RoleSummary'>;

/** Replica registry and installation facts: `GET /api/v1/system/info` and `/system/replicas`. */
export abstract class SystemApi {
  abstract info(): Promise<SystemInfo>;
  abstract replicas(): Promise<readonly Replica[]>;
}

@Injectable()
export class HttpSystemApi extends SystemApi {
  private readonly client = inject(ApiClient);

  async info(): Promise<SystemInfo> {
    return (await this.client.get('/api/v1/system/info')).body;
  }

  async replicas(): Promise<readonly Replica[]> {
    return (await this.client.get('/api/v1/system/replicas')).body.items;
  }
}

// >>> M1 ui-a
/** `GET /api/v1/system/encryption` (R-API-049), from the generated document (Q-UI-12). */
export type EncryptionStatus = Schema<'EncryptionStatus'>;
// <<< M1 ui-a

export abstract class EncryptionApi {
  abstract status(): Promise<EncryptionStatus>;
  /** `POST /api/v1/system/tasks/secrets-reencrypt/run`; resolves with the job id. */
  abstract reencrypt(idempotencyKey: string): Promise<string>;
}

@Injectable()
export class HttpEncryptionApi extends EncryptionApi {
  private readonly client = inject(ApiClient);

  async status(): Promise<EncryptionStatus> {
    return (await this.client.get('/api/v1/system/encryption')).body;
  }

  async reencrypt(idempotencyKey: string): Promise<string> {
    // >>> M1 ui-a
    const r = await this.client.post<{ job: { id: string } }>(
      '/api/v1/system/tasks/secrets-reencrypt/run',
      undefined,
      { idempotencyKey },
    );
    return r.body.job.id;
    // <<< M1 ui-a
  }
}
