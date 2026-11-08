import { Injectable, inject } from '@angular/core';
import { ApiClient } from './client';
import { JobReference } from './jobs-api';
import { seg } from './query';
import { repositoryUrl } from './repositories-api';
import { Schema } from './types';

/* The Debian (APT) operations of a repository: signing keys (R-DEB-015, R-DEB-017, R-DEB-032) and the index state (R-DEB-014). */

export type DebSigningKey = Schema<'DebSigningKey'>;
export type DebSigningKeyState = DebSigningKey['state'];
export type DebSigningKeyCreate = Schema<'DebSigningKeyCreate'>;
export type DebIndexState = Schema<'DebIndexState'>;
export type DebSuiteState = DebIndexState['suites'][number];

export abstract class DebApi {
  /** `GET …/deb/signing-keys`: every key in every state; a private key is never part of the answer. */
  abstract signingKeys(ref: string): Promise<readonly DebSigningKey[]>;
  /** `POST …/deb/signing-keys`: creates the next key, generated or imported. */
  abstract createSigningKey(ref: string, body: DebSigningKeyCreate): Promise<DebSigningKey>;
  /** `POST …/deb/signing-keys/promote`: the next key becomes primary and the old primary is retired. */
  abstract promoteSigningKey(ref: string): Promise<DebSigningKey>;
  /** `DELETE …/deb/signing-keys/{fingerprint}`: retires the next key or confirms the retirement of a withheld key. */
  abstract retireSigningKey(ref: string, fingerprint: string): Promise<void>;
  /** `POST …/deb/signing-keys/{fingerprint}/reinstate`: returns a withheld key to primary or next. */
  abstract reinstateSigningKey(ref: string, fingerprint: string): Promise<DebSigningKey>;
  /** `GET …/deb/index`: each suite's state. */
  abstract indexState(ref: string): Promise<DebIndexState>;
  /** `POST …/reindex`: a full build of every suite; the job that was queued. */
  abstract reindex(ref: string, idempotencyKey: string): Promise<JobReference>;
}

@Injectable()
export class HttpDebApi extends DebApi {
  private readonly client = inject(ApiClient);

  private keysUrl(ref: string): string {
    return `${repositoryUrl(ref)}/deb/signing-keys`;
  }

  override async signingKeys(ref: string): Promise<readonly DebSigningKey[]> {
    return (await this.client.getJson<Schema<'DebSigningKeyList'>>(this.keysUrl(ref))).body.keys;
  }

  override async createSigningKey(ref: string, body: DebSigningKeyCreate): Promise<DebSigningKey> {
    return (await this.client.post<DebSigningKey>(this.keysUrl(ref), body)).body;
  }

  override async promoteSigningKey(ref: string): Promise<DebSigningKey> {
    return (await this.client.post<DebSigningKey>(`${this.keysUrl(ref)}/promote`)).body;
  }

  override async retireSigningKey(ref: string, fingerprint: string): Promise<void> {
    await this.client.delete(`${this.keysUrl(ref)}/${seg(fingerprint)}`);
  }

  override async reinstateSigningKey(ref: string, fingerprint: string): Promise<DebSigningKey> {
    return (await this.client.post<DebSigningKey>(`${this.keysUrl(ref)}/${seg(fingerprint)}/reinstate`)).body;
  }

  override async indexState(ref: string): Promise<DebIndexState> {
    return (await this.client.getJson<DebIndexState>(`${repositoryUrl(ref)}/deb/index`)).body;
  }

  override async reindex(ref: string, idempotencyKey: string): Promise<JobReference> {
    const r = await this.client.post<{ job: JobReference }>(`${repositoryUrl(ref)}/reindex`, undefined, {
      idempotencyKey,
    });
    return r.body.job;
  }
}
