import { Component, DestroyRef, LOCALE_ID, computed, inject, signal } from '@angular/core';
import { Checkbox } from '../../shared/ui/checkbox';
import { Disclosure } from '../../shared/ui/disclosure';
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
import { DraftStore } from '../../shared/drafts';
import { TextField } from '../../shared/access/fields';
import { fieldErrorAt } from '../../shared/field-errors';
import { Needs } from '../../shared/access/needs';
import { asApiError } from '../../shared/paged-resource';
import { ConflictResolver } from '../../shared/conflict-dialog';
import { ErrorNotice } from '../../shared/error-notice';
import { PageHeader } from '../../shared/ui/page-header';

/** The editable form content in one object, for the draft and the conflict dialog. */
interface Form {
  anonymousAccess: boolean;
  patDefault: string;
  patMax: string;
  satDefault: string;
  satMax: string;
  expiryNotice: string;
}

/** The two groups of the document that have a save button each (Q-UI-222). */
type Part = 'anonymous' | 'tokens';

/**
 * Authentication settings (R-UI-031, R-AUTH-040): the runtime values (anonymous access open at the top, token
 * lifetimes per type and the expiry notice in a section) bounded by the ceilings of the installation's
 * configuration, which are shown next to them, and the read-only status of providers,
 * break-glass, sessions and client switches in a section. The two groups are one document with one ETag, and each save sends a
 * merge patch of its own group only with `If-Match`, so saving one never carries an unsaved edit of the other; a 412 opens
 * the conflict dialog; recent authentication applies (the draft survives the sign-in).
 */
@Component({
  selector: 'trove-auth-settings',
  imports: [PageHeader, Checkbox, RouterLink, ErrorNotice, Needs, Disclosure, TextField],
  templateUrl: './auth-settings.html',
  styleUrl: '../../shared/access/access.scss',
  styles: `
    .settings {
      max-inline-size: var(--trove-settings-measure);
    }
    form {
      padding-block-end: var(--trove-space-5);
    }
  `,
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
  protected readonly tokensOpen = signal(false);
  protected readonly dirtyAnonymous = computed(() => this.dirtyPart('anonymous'));
  protected readonly dirtyTokens = computed(() => this.dirtyPart('tokens'));
  protected readonly dirty = computed(() => this.dirtyAnonymous() || this.dirtyTokens());

  private dirtyPart(part: Part): boolean {
    const s = this.settings();
    return s !== null && Object.keys(this.diff(s, part)).length > 0;
  }

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
        if (this.dirtyTokens()) this.tokensOpen.set(true);
        this.notifier.status($localize`:@@auth-settings.draft:Your entries were restored. Save them again.`);
      }
    } catch (e) {
      this.loadError.set(asApiError(e));
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

  /** Takes the stored document and its ETag; the fields of `part` follow it, those of the other group keep their edits. */
  private adopt(loaded: LoadedAuthSettings, part?: Part): void {
    this.loaded.set(loaded);
    this.apply(this.formOf(loaded.settings), part);
  }

  private apply(f: Form, part?: Part): void {
    if (part !== 'tokens') this.anonymousAccess.set(f.anonymousAccess);
    if (part === 'anonymous') return;
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

  /** The merge patch: only the members that differ from what was loaded, of one group or of both. */
  private diff(s: AuthSettingsModel, part?: Part): AuthSettingsPatch {
    const f = this.form();
    const patch: {
      anonymousAccess?: boolean;
      tokens?: {
        pat?: { defaultTTLSeconds?: number; maxTTLSeconds?: number };
        sat?: { defaultTTLSeconds?: number; maxTTLSeconds?: number };
        expiryNoticeSeconds?: number;
      };
    } = {};
    if (part !== 'tokens' && f.anonymousAccess !== s.anonymousAccess) patch.anonymousAccess = f.anonymousAccess;
    if (part === 'anonymous') return patch;
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

  protected async save(event: Event, part: Part): Promise<void> {
    event.preventDefault();
    const current = this.loaded();
    if (!current) return;
    const patch = this.diff(current.settings, part);
    if (Object.keys(patch).length === 0) return;
    this.busy.set(true);
    this.saveError.set(null);
    try {
      this.adopt(await this.api.patch(patch, current.etag), part);
      this.notifier.status($localize`:@@auth-settings.saved:Settings saved.`);
    } catch (e) {
      const error = asApiError(e);
      if (error.status === 412) await this.resolveConflict(patch, part);
      else {
        this.saveError.set(error);
        if (error.code === 'reauthentication_required') this.drafts.keep(this.form());
      }
    } finally {
      this.busy.set(false);
    }
  }

  /** 412: their stored values against mine; "Overwrite" sends my patch with the ETag read now. */
  private async resolveConflict(patch: AuthSettingsPatch, part: Part): Promise<void> {
    const theirs = await this.api.load();
    const decision = await this.conflicts.resolve({
      mine: { ...this.form() },
      theirs: { ...this.formOf(theirs.settings) },
    });
    if (decision === 'overwrite') {
      try {
        this.adopt(await this.api.patch(patch, theirs.etag), part);
        this.notifier.status($localize`:@@auth-settings.saved:Settings saved.`);
      } catch (e) {
        this.saveError.set(asApiError(e));
      }
    } else {
      this.adopt(theirs, part);
    }
  }
}
