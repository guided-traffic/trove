import { DOCUMENT } from '@angular/common';
import { Injectable, LOCALE_ID, inject, signal } from '@angular/core';
import { Router } from '@angular/router';
import { LoadedPreferences, PreferencesApi } from '../api/preferences-api';
import { BrowserNavigation } from './browser-navigation';

export const LOCALES = ['en', 'de'] as const;
export type UiLocale = (typeof LOCALES)[number];
export type UiTheme = 'light' | 'dark';

/**
 * UI preferences (R-API-021): language and theme. The language build is chosen by the URL
 * (`/ui/{locale}/`, Q-UI-5), so switching stores the preference and reloads the same route under
 * the other locale. The theme is applied through `data-theme` on the root element.
 */
@Injectable({ providedIn: 'root' })
export class PreferencesStore {
  private readonly api = inject(PreferencesApi);
  private readonly document = inject(DOCUMENT);
  private readonly router = inject(Router);
  private readonly navigation = inject(BrowserNavigation);

  /** The locale of the running build. */
  readonly locale: UiLocale = inject(LOCALE_ID).startsWith('de') ? 'de' : 'en';

  private readonly _theme = signal<UiTheme | null>(null);
  private loaded: LoadedPreferences | null = null;

  /** The explicit theme choice, or null when the browser setting decides. */
  readonly theme = this._theme.asReadonly();

  /** Reads the stored preferences once per sign-in and applies the theme. */
  async load(): Promise<void> {
    try {
      this.loaded = await this.api.load();
      this.applyTheme(this.loaded.preferences.theme);
    } catch {
      // Preferences are a convenience; defaults apply when they cannot be read (service
      // accounts and CI tokens get 403, R-API-021).
      this.loaded = null;
    }
  }

  async setTheme(theme: UiTheme | null): Promise<void> {
    this.applyTheme(theme);
    await this.store({ theme });
  }

  /** Stores the language and loads the same route in the other locale build. */
  async setLocale(locale: UiLocale): Promise<void> {
    if (locale === this.locale) return;
    await this.store({ locale });
    this.navigation.assign(`/ui/${locale}${this.router.url}`);
  }

  private applyTheme(theme: UiTheme | null): void {
    this._theme.set(theme);
    const root = this.document.documentElement;
    if (theme === null) root.removeAttribute('data-theme');
    else root.setAttribute('data-theme', theme);
  }

  private async store(patch: Parameters<PreferencesApi['patch']>[0]): Promise<void> {
    try {
      this.loaded ??= await this.api.load();
      this.loaded = await this.api.patch(patch, this.loaded.etag);
    } catch {
      // The choice still applies to this tab; it is not remembered for the next visit.
      this.loaded = null;
    }
  }
}
