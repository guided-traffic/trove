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

/** `GET /api/v1/system/encryption` (R-API-049). Hand-typed until the operation is in the document. */
export interface EncryptionStatus {
  readonly answeredBy: string;
  readonly activeKeyId: string;
  readonly keys: readonly {
    readonly id: string;
    readonly active: boolean;
    readonly sealedValues: number;
    readonly canary: 'ok' | 'failed' | 'absent';
  }[];
  readonly replicas: readonly {
    readonly name: string;
    readonly role: string;
    readonly activeKeyId: string;
    readonly keyIds: readonly string[];
  }[];
  readonly ringsCompatible: boolean;
  readonly lastReencryption: {
    readonly jobId: string;
    readonly state: string;
    readonly finishedAt: string | null;
  } | null;
}

export abstract class EncryptionApi {
  abstract status(): Promise<EncryptionStatus>;
  /** `POST /api/v1/system/tasks/secrets-reencrypt/run`; resolves with the job id. */
  abstract reencrypt(idempotencyKey: string): Promise<string>;
}

@Injectable()
export class HttpEncryptionApi extends EncryptionApi {
  private readonly client = inject(ApiClient);

  async status(): Promise<EncryptionStatus> {
    return (await this.client.getJson<EncryptionStatus>('/api/v1/system/encryption')).body;
  }

  async reencrypt(idempotencyKey: string): Promise<string> {
    const r = await this.client.post<{ id: string }>(
      '/api/v1/system/tasks/secrets-reencrypt/run',
      undefined,
      { idempotencyKey },
    );
    return r.body.id;
  }
}
