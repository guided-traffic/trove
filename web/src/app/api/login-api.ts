import { Injectable, inject } from '@angular/core';
import { ApiClient } from './client';
import { Schema } from './types';

/** `GET /api/v1/auth/login-options` (R-AUTH-009): the only source of the sign-in screen. */
export type LoginOptions = Schema<'LoginOptions'>;

export abstract class LoginApi {
  abstract options(): Promise<LoginOptions>;
  /** Break-glass sign-in, `POST /auth/local/login` (R-AUTH-014); the session arrives as a cookie. */
  abstract localLogin(username: string, password: string): Promise<void>;
}

@Injectable()
export class HttpLoginApi extends LoginApi {
  private readonly client = inject(ApiClient);

  async options(): Promise<LoginOptions> {
    return (await this.client.get('/api/v1/auth/login-options')).body;
  }

  async localLogin(username: string, password: string): Promise<void> {
    await this.client.post<unknown>('/auth/local/login', { username, password });
  }
}
