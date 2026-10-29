import { DatePipe } from '@angular/common';
import { Component, computed, inject, signal } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatCheckboxModule } from '@angular/material/checkbox';
import { IdentityApi, User } from '../../api/identity-api';
import { LoadedPreferences, PreferencesApi, PreferencesPatch } from '../../api/preferences-api';
import { ApiError } from '../../api/problem';
import { Notifier } from '../../core/notifier';
import { PreferencesStore } from '../../core/preferences-store';
import { SessionStore } from '../../core/session-store';
import { SelectField, SelectOption } from '../../shared/access/fields';
import { toApiError } from '../../shared/access/paged-signal';
import { ConflictResolver } from '../../shared/conflict-dialog';
import { ErrorNotice } from '../../shared/error-notice';
import { ProfileNav } from './profile-nav';

/**
 * Profile (R-UI-029, R-AUTH-017): identity as the identity provider delivered it (read only),
 * groups with their source, the system roles of the caller and the preferences of R-API-021.
 * There is no password, no unlock and no editable email or display name: users have no password
 * in trove and both attributes are replaced at every login. A missing group claim is a notice,
 * not a dialog (R-AUTH-008). Preferences are saved with `If-Match`; a 412 opens the conflict
 * dialog with their version against yours.
 */
@Component({
  selector: 'trove-profile',
  imports: [DatePipe, MatButtonModule, MatCheckboxModule, ErrorNotice, ProfileNav, SelectField],
  templateUrl: './profile.html',
  styleUrl: '../../shared/access/access.scss',
})
export class Profile {
  private readonly identity = inject(IdentityApi);
  private readonly preferencesApi = inject(PreferencesApi);
  private readonly preferencesStore = inject(PreferencesStore);
  private readonly conflicts = inject(ConflictResolver);
  private readonly notifier = inject(Notifier);
  protected readonly session = inject(SessionStore);

  protected readonly user = signal<User | null>(null);
  protected readonly loadError = signal<ApiError | null>(null);
  protected readonly prefError = signal<ApiError | null>(null);
  protected readonly prefsLoaded = signal(false);
  protected readonly saving = signal(false);

  protected readonly locale = signal('');
  protected readonly theme = signal('');
  protected readonly shortcuts = signal(false);
  private loaded: LoadedPreferences | null = null;

  protected readonly roles = computed(() => this.session.me()?.roles ?? []);
  protected readonly localeOptions: readonly SelectOption[] = [
    { value: '', label: $localize`:@@profile.pref.locale.browser:Follow the browser` },
    { value: 'en', label: 'English' },
    { value: 'de', label: 'Deutsch' },
  ];
  protected readonly themeOptions: readonly SelectOption[] = [
    { value: '', label: $localize`:@@profile.pref.theme.system:Follow the system` },
    { value: 'light', label: $localize`:@@profile.pref.theme.light:Light` },
    { value: 'dark', label: $localize`:@@profile.pref.theme.dark:Dark` },
  ];

  constructor() {
    void this.load();
  }

  protected async load(): Promise<void> {
    this.loadError.set(null);
    try {
      this.user.set(await this.identity.me());
    } catch (e) {
      this.loadError.set(toApiError(e));
    }
    await this.loadPreferences();
  }

  private async loadPreferences(): Promise<void> {
    this.prefError.set(null);
    try {
      this.adopt(await this.preferencesApi.load());
    } catch (e) {
      // Service accounts and CI tokens have no preferences (R-API-021); the section says so.
      this.prefError.set(toApiError(e));
    }
  }

  private adopt(loaded: LoadedPreferences): void {
    this.loaded = loaded;
    this.locale.set(loaded.preferences.locale ?? '');
    this.theme.set(loaded.preferences.theme ?? '');
    this.shortcuts.set(loaded.preferences.keyboardShortcuts);
    this.prefsLoaded.set(true);
  }

  private patch(): PreferencesPatch {
    return {
      locale: this.locale() || null,
      theme: this.theme() || null,
      keyboardShortcuts: this.shortcuts(),
    };
  }

  protected async savePreferences(event: Event): Promise<void> {
    event.preventDefault();
    if (!this.loaded) return;
    this.saving.set(true);
    this.prefError.set(null);
    try {
      await this.store(this.loaded.etag);
    } catch (e) {
      const error = toApiError(e);
      if (error.status === 412) await this.resolveConflict();
      else this.prefError.set(error);
    } finally {
      this.saving.set(false);
    }
  }

  private async store(etag: string): Promise<void> {
    const saved = await this.preferencesApi.patch(this.patch(), etag);
    this.adopt(saved);
    this.preferencesStore.adopt(saved);
    this.notifier.status($localize`:@@profile.pref.saved:Preferences saved.`);
  }

  /** 412: show their version against mine; "Overwrite" sends mine with the ETag read now. */
  private async resolveConflict(): Promise<void> {
    const current = await this.preferencesApi.load();
    const theirs = {
      locale: current.preferences.locale,
      theme: current.preferences.theme,
      keyboardShortcuts: current.preferences.keyboardShortcuts,
    };
    const decision = await this.conflicts.resolve({ mine: this.patch(), theirs });
    if (decision === 'overwrite') {
      try {
        await this.store(current.etag);
      } catch (e) {
        this.prefError.set(toApiError(e));
      }
    } else {
      this.adopt(current);
    }
  }
}
