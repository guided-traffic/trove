import { Injectable, inject } from '@angular/core';
import { ApiClient } from './client';
import { Schema } from './types';

/** `GET`, `PATCH /api/v1/settings/auth` (R-AUTH-040). The runtime members are writable, the rest is read-only. */
export type AuthSettings = Schema<'AuthSettings'>;
export type AuthSettingsPatch = Schema<'AuthSettingsPatch'>;

export interface LoadedAuthSettings {
  readonly settings: AuthSettings;
  readonly etag: string;
}

export abstract class AuthSettingsApi {
  abstract load(): Promise<LoadedAuthSettings>;
  /** Merge patch with the loaded `ETag` (R-API-012); recent authentication applies. */
  abstract patch(patch: AuthSettingsPatch, etag: string): Promise<LoadedAuthSettings>;
}

@Injectable()
export class HttpAuthSettingsApi extends AuthSettingsApi {
  private readonly client = inject(ApiClient);
  private readonly url = '/api/v1/settings/auth';

  override async load(): Promise<LoadedAuthSettings> {
    const r = await this.client.get(this.url);
    return { settings: r.body, etag: r.etag ?? '' };
  }

  override async patch(patch: AuthSettingsPatch, etag: string): Promise<LoadedAuthSettings> {
    const r = await this.client.patch<AuthSettings>(this.url, patch, etag);
    return { settings: r.body, etag: r.etag ?? '' };
  }
}
