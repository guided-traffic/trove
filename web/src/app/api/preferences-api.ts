import { Injectable, inject } from '@angular/core';
import { ApiClient } from './client';

/** `GET /api/v1/users/me/preferences` (R-API-021). */
export interface Preferences {
  readonly locale: 'en' | 'de' | null;
  readonly theme: 'light' | 'dark' | null;
  readonly keyboardShortcuts: boolean;
  readonly projectFilter: string | null;
  readonly revision: number;
}

export interface LoadedPreferences {
  readonly preferences: Preferences;
  readonly etag: string;
}

export abstract class PreferencesApi {
  abstract load(): Promise<LoadedPreferences>;
  /** Merge patch with the loaded `ETag` (R-API-013). */
  abstract patch(
    patch: Partial<Pick<Preferences, 'locale' | 'theme' | 'keyboardShortcuts'>>,
    etag: string,
  ): Promise<LoadedPreferences>;
}

@Injectable()
export class HttpPreferencesApi extends PreferencesApi {
  private readonly client = inject(ApiClient);
  private readonly url = '/api/v1/users/me/preferences';

  async load(): Promise<LoadedPreferences> {
    const r = await this.client.getJson<Preferences>(this.url);
    return { preferences: r.body, etag: r.etag ?? `"${r.body.revision}"` };
  }

  async patch(
    patch: Partial<Pick<Preferences, 'locale' | 'theme' | 'keyboardShortcuts'>>,
    etag: string,
  ): Promise<LoadedPreferences> {
    const r = await this.client.patch<Preferences>(this.url, patch, etag);
    return { preferences: r.body, etag: r.etag ?? `"${r.body.revision}"` };
  }
}
