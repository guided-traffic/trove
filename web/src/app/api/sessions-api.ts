import { Injectable, inject } from '@angular/core';
import { ApiClient } from './client';
import { seg } from './query';
import { Schema } from './types';

/** Browser sessions of a user (R-AUTH-017; docs/developer/login.md). A user holds a bounded number. */
export type UserSession = Schema<'Session'>;

export abstract class SessionsApi {
  abstract own(): Promise<readonly UserSession[]>;
  abstract endOwn(id: string): Promise<void>;
  /** Ends the caller's other sessions (`keepCurrent=true`) or all of them. */
  abstract endOwnAll(keepCurrent: boolean): Promise<void>;
  abstract ofUser(username: string): Promise<readonly UserSession[]>;
  abstract endAllOfUser(username: string): Promise<void>;
}

@Injectable()
export class HttpSessionsApi extends SessionsApi {
  private readonly client = inject(ApiClient);

  override async own(): Promise<readonly UserSession[]> {
    return (await this.client.get('/api/v1/users/me/sessions')).body.items;
  }

  override async endOwn(id: string): Promise<void> {
    await this.client.delete(`/api/v1/users/me/sessions/${seg(id)}`);
  }

  override async endOwnAll(keepCurrent: boolean): Promise<void> {
    await this.client.delete('/api/v1/users/me/sessions', { params: { keepCurrent } });
  }

  override async ofUser(username: string): Promise<readonly UserSession[]> {
    return (await this.client.getJson<{ items: UserSession[] }>(`/api/v1/users/${seg(username)}/sessions`))
      .body.items;
  }

  override async endAllOfUser(username: string): Promise<void> {
    await this.client.delete(`/api/v1/users/${seg(username)}/sessions`);
  }
}
