import { Component, DestroyRef, LOCALE_ID, computed, inject, signal } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatCheckboxModule } from '@angular/material/checkbox';
import { RouterLink } from '@angular/router';
import {
  AuthSettings as AuthSettingsModel,
  AuthSettingsApi,
  AuthSettingsPatch,
  LoadedAuthSettings,
} from '../../api/auth-settings-api';
import { ApiError } from '../../api/problem';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { fromDays, formatDuration, toDays } from '../../shared/access/duration';
import { DraftStore } from '../../shared/access/drafts';
import { TextField } from '../../shared/access/fields';
import { fieldErrorAt } from '../../shared/access/field-problem';
import { Needs } from '../../shared/access/needs';
import { toApiError } from '../../shared/access/paged-signal';
import { ConflictResolver } from '../../shared/conflict-dialog';
import { ErrorNotice } from '../../shared/error-notice';

/** The editable form content in one object, for the draft and the conflict dialog. */
interface Form {
  anonymousAccess: boolean;
  patDefault: string;
  patMax: string;
  satDefault: string;
  satMax: string;
  expiryNotice: string;
}

/**
 * Authentication settings (R-UI-031, R-AUTH-040): the runtime values (anonymous access, token
 * lifetimes per type, the expiry notice) bounded by the ceilings of the installation's
 * configuration, which are shown next to them, and the read-only status of providers,
 * break-glass, sessions and client switches. Save sends a merge patch with `If-Match`; a 412 opens
 * the conflict dialog; recent authentication applies (the draft survives the sign-in).
 */
@Component({
  selector: 'trove-auth-settings',
  imports: [MatButtonModule, MatCheckboxModule, RouterLink, ErrorNotice, Needs, TextField],
  templateUrl: './auth-settings.html',
  styleUrl: '../../shared/access/access.scss',
})
export class AuthSettings {
  private readonly api = inject(AuthSettingsApi);
  private readonly conflicts = inject(ConflictResolver);
  private readonly notifier = inject(Notifier);
  private readonly drafts = inject(DraftStore);
  private readonly locale = inject(LOCALE_ID);
  protected readonly session = inject(SessionStore);

  protected readonly loaded = signal<LoadedAuthSettings | null>(null);
  protected readonly loadError = signal<ApiError | null>(null);
  protected readonly saveError = signal<ApiError | null>(null);
  protected readonly busy = signal(false);

  protected readonly anonymousAccess = signal(false);
  protected readonly patDefault = signal('');
  protected readonly patMax = signal('');
  protected readonly satDefault = signal('');
  protected readonly satMax = signal('');
  protected readonly expiryNotice = signal('');

  protected readonly canWrite = computed(() => this.session.has('settings:write'));
  protected readonly settings = computed<AuthSettingsModel | null>(() => this.loaded()?.settings ?? null);
  protected readonly dirty = computed(() => {
    const s = this.settings();
    return s !== null && Object.keys(this.diff(s)).length > 0;
  });

  constructor() {
    this.drafts.track(() => (this.dirty() ? this.form() : null), inject(DestroyRef));
    void this.load();
  }

  protected async load(): Promise<void> {
    this.loadError.set(null);
    try {
      const loaded = await this.api.load();
      this.adopt(loaded);
      const draft = this.drafts.take<Form>();
      if (draft) {
        this.apply(draft);
        this.notifier.status($localize`:@@auth-settings.draft:Your entries were restored. Save them again.`);
      }
    } catch (e) {
      this.loadError.set(toApiError(e));
    }
  }

  private formOf(s: AuthSettingsModel): Form {
    return {
      anonymousAccess: s.anonymousAccess,
      patDefault: toDays(s.tokens.pat.defaultTTLSeconds),
      patMax: toDays(s.tokens.pat.maxTTLSeconds),
      satDefault: toDays(s.tokens.sat.defaultTTLSeconds),
      satMax: toDays(s.tokens.sat.maxTTLSeconds),
      expiryNotice: toDays(s.tokens.expiryNoticeSeconds),
    };
  }

  private adopt(loaded: LoadedAuthSettings): void {
    this.loaded.set(loaded);
    this.apply(this.formOf(loaded.settings));
  }

  private apply(f: Form): void {
    this.anonymousAccess.set(f.anonymousAccess);
    this.patDefault.set(f.patDefault);
    this.patMax.set(f.patMax);
    this.satDefault.set(f.satDefault);
    this.satMax.set(f.satMax);
    this.expiryNotice.set(f.expiryNotice);
  }

  private form(): Form {
    return {
      anonymousAccess: this.anonymousAccess(),
      patDefault: this.patDefault(),
      patMax: this.patMax(),
      satDefault: this.satDefault(),
      satMax: this.satMax(),
      expiryNotice: this.expiryNotice(),
    };
  }

  /** The merge patch: only the members that differ from what was loaded. */
  private diff(s: AuthSettingsModel): AuthSettingsPatch {
    const f = this.form();
    const patch: {
      anonymousAccess?: boolean;
      tokens?: {
        pat?: { defaultTTLSeconds?: number; maxTTLSeconds?: number };
        sat?: { defaultTTLSeconds?: number; maxTTLSeconds?: number };
        expiryNoticeSeconds?: number;
      };
    } = {};
    if (f.anonymousAccess !== s.anonymousAccess) patch.anonymousAccess = f.anonymousAccess;
    const tokens: NonNullable<typeof patch.tokens> = {};
    const lifetimes = (
      kind: 'pat' | 'sat',
      def: string,
      max: string,
    ): { defaultTTLSeconds?: number; maxTTLSeconds?: number } | null => {
      const out: { defaultTTLSeconds?: number; maxTTLSeconds?: number } = {};
      const d = fromDays(def);
      const m = fromDays(max);
      if (!Number.isNaN(d) && d !== s.tokens[kind].defaultTTLSeconds) out.defaultTTLSeconds = d;
      if (!Number.isNaN(m) && m !== s.tokens[kind].maxTTLSeconds) out.maxTTLSeconds = m;
      return Object.keys(out).length > 0 ? out : null;
    };
    const pat = lifetimes('pat', f.patDefault, f.patMax);
    const sat = lifetimes('sat', f.satDefault, f.satMax);
    if (pat) tokens.pat = pat;
    if (sat) tokens.sat = sat;
    const notice = fromDays(f.expiryNotice);
    if (!Number.isNaN(notice) && notice !== s.tokens.expiryNoticeSeconds) tokens.expiryNoticeSeconds = notice;
    if (Object.keys(tokens).length > 0) patch.tokens = tokens;
    return patch;
  }

  protected duration(seconds: number): string {
    return formatDuration(seconds, this.locale);
  }

  /** "Configured value: 90 days": the YAML value the runtime value is bound by (R-AUTH-040). */
  protected ceilingHint(seconds: number): string {
    return $localize`:@@auth-settings.ceiling:Ceiling from the configuration: ${this.duration(seconds)}:duration:`;
  }

  protected fieldError(pointer: string): string | null {
    return fieldErrorAt(this.saveError(), pointer);
  }

  protected async save(event: Event): Promise<void> {
    event.preventDefault();
    const current = this.loaded();
    if (!current) return;
    const patch = this.diff(current.settings);
    if (Object.keys(patch).length === 0) return;
    this.busy.set(true);
    this.saveError.set(null);
    try {
      this.adopt(await this.api.patch(patch, current.etag));
      this.notifier.status($localize`:@@auth-settings.saved:Settings saved.`);
    } catch (e) {
      const error = toApiError(e);
      if (error.status === 412) await this.resolveConflict(patch);
      else {
        this.saveError.set(error);
        if (error.code === 'reauthentication_required') this.drafts.keep(this.form());
      }
    } finally {
      this.busy.set(false);
    }
  }

  /** 412: their stored values against mine; "Overwrite" sends my patch with the ETag read now. */
  private async resolveConflict(patch: AuthSettingsPatch): Promise<void> {
    const theirs = await this.api.load();
    const decision = await this.conflicts.resolve({
      mine: { ...this.form() },
      theirs: { ...this.formOf(theirs.settings) },
    });
    if (decision === 'overwrite') {
      try {
        this.adopt(await this.api.patch(patch, theirs.etag));
        this.notifier.status($localize`:@@auth-settings.saved:Settings saved.`);
      } catch (e) {
        this.saveError.set(toApiError(e));
      }
    } else {
      this.adopt(theirs);
    }
  }
}
