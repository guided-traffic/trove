import { Injectable, inject } from '@angular/core';
import { ApiClient } from './client';
import { Schema } from './types';

/** `GET`, `PATCH /api/v1/users/me/preferences` (R-API-021); `revision` is the ETag number. */
export type Preferences = Schema<'Preferences'>;
export type PreferencesPatch = Schema<'PreferencesPatch'>;

export interface LoadedPreferences {
  readonly preferences: Preferences;
  readonly etag: string;
}

export abstract class PreferencesApi {
  abstract load(): Promise<LoadedPreferences>;
  /** Merge patch with the loaded `ETag` (R-API-013); `null` resets a member to its default. */
  abstract patch(patch: PreferencesPatch, etag: string): Promise<LoadedPreferences>;
}

@Injectable()
export class HttpPreferencesApi extends PreferencesApi {
  private readonly client = inject(ApiClient);
  private readonly url = '/api/v1/users/me/preferences';

  async load(): Promise<LoadedPreferences> {
    const r = await this.client.get(this.url);
    return { preferences: r.body, etag: r.etag ?? `"${r.body.revision}"` };
  }

  async patch(patch: PreferencesPatch, etag: string): Promise<LoadedPreferences> {
    const r = await this.client.patch<Preferences>(this.url, patch, etag);
    return { preferences: r.body, etag: r.etag ?? `"${r.body.revision}"` };
  }
}
