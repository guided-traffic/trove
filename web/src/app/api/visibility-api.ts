import { Injectable, inject } from '@angular/core';
import { ApiClient } from './client';
import { Schema } from './types';

/** `GET`, `PATCH /api/v1/settings/visibility` (R-API-056): the installation's default level and what is public now. */
export type VisibilitySettings = Schema<'VisibilitySettings'>;
export type VisibilitySettingsPatch = Schema<'VisibilitySettingsPatch'>;
export type PublicObjects = Schema<'PublicObjects'>;

export interface LoadedVisibilitySettings {
  readonly settings: VisibilitySettings;
  readonly etag: string;
}

export abstract class VisibilityApi {
  abstract load(): Promise<LoadedVisibilitySettings>;
  /** Merge patch with the loaded `ETag` (R-API-012); recent authentication applies; 409 `public_not_allowed`. */
  abstract patch(patch: VisibilitySettingsPatch, etag: string): Promise<LoadedVisibilitySettings>;
}

@Injectable()
export class HttpVisibilityApi extends VisibilityApi {
  private readonly client = inject(ApiClient);
  private readonly url = '/api/v1/settings/visibility';

  override async load(): Promise<LoadedVisibilitySettings> {
    const r = await this.client.get(this.url);
    return { settings: r.body, etag: r.etag ?? '' };
  }

  override async patch(patch: VisibilitySettingsPatch, etag: string): Promise<LoadedVisibilitySettings> {
    const r = await this.client.patch<VisibilitySettings>(this.url, patch, etag);
    return { settings: r.body, etag: r.etag ?? '' };
  }
}
