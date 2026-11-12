import { Component, computed, inject, signal } from '@angular/core';
import { ApiError } from '../../api/problem';
import {
  Banner as BannerModel,
  MaintenanceMode,
  SystemApi,
  SystemInfo,
} from '../../api/system-api';
import { BannerStore } from '../../core/banner-store';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { Needs } from '../../shared/access/needs';
import { SelectField, SelectOption, TextField } from '../../shared/access/fields';
import { parseBannerText } from '../../shared/banner-text';
import { Confirm } from '../../shared/confirm-dialog';
import { ErrorNotice } from '../../shared/error-notice';
import { fieldErrorAt } from '../../shared/field-errors';
import { asApiError } from '../../shared/paged-resource';
import { Checkbox } from '../../shared/ui/checkbox';
import { Disclosure } from '../../shared/ui/disclosure';
import { PageHeader } from '../../shared/ui/page-header';

type Severity = 'info' | 'warning' | 'critical';

/** The longest messages the API accepts (R-OPS-025, R-OPS-024); the fields show them as hints. */
const MESSAGE_MAX = 2000;
const MAINTENANCE_MESSAGE_MAX = 500;

/**
 * General settings (R-UI-033): the public base URL and where it comes from (read only, `GET /api/v1/system/info`), the system banner
 * (R-OPS-025) and the read-only maintenance mode (R-OPS-024). The banner and the mode are two documents with a save each, so a
 * change of one never carries an unsaved edit of the other. Switching the mode on asks first, because it refuses every write of every
 * user until it is switched off. After a save the notice shows on this screen at once; on every other screen it appears with the
 * next read of the banner store, within a minute (R-UI-033).
 */
@Component({
  selector: 'trove-general-settings',
  imports: [PageHeader, Checkbox, ErrorNotice, Needs, SelectField, Disclosure, TextField],
  templateUrl: './general-settings.html',
  styleUrl: '../../shared/access/access.scss',
  styles: `
    .settings {
      max-inline-size: var(--trove-settings-measure);
    }
    .source {
      margin-block-start: calc(-1 * var(--trove-space-2));
    }
  `,
})
export class GeneralSettings {
  private readonly api = inject(SystemApi);
  private readonly notifier = inject(Notifier);
  private readonly confirm = inject(Confirm);
  private readonly notices = inject(BannerStore);
  protected readonly session = inject(SessionStore);

  protected readonly info = signal<SystemInfo | null>(null);
  protected readonly stored = signal<BannerModel | null>(null);
  protected readonly storedMode = signal<MaintenanceMode | null>(null);
  protected readonly loadError = signal<ApiError | null>(null);
  protected readonly bannerError = signal<ApiError | null>(null);
  protected readonly modeError = signal<ApiError | null>(null);
  protected readonly savingBanner = signal(false);
  protected readonly savingMode = signal(false);

  protected readonly bannerEnabled = signal(false);
  protected readonly severity = signal<Severity>('info');
  protected readonly title = signal('');
  protected readonly message = signal('');

  protected readonly readOnly = signal(false);
  protected readonly modeMessage = signal('');
  protected readonly cacheFill = signal(true);
  protected readonly retryAfter = signal('300');

  protected readonly canWrite = computed(() => this.session.has('settings:write'));

  /** The maintenance section opens by itself while the mode is on, so that a read-only installation is seen at once. */
  protected readonly modeOpen = signal(false);
  protected readonly modeSummary = computed(() =>
    this.storedMode()?.mode === 'read_only'
      ? $localize`:@@general.mode.summary-on:Read-only mode is on: every write is refused.`
      : $localize`:@@general.mode.summary-off:Refuse every write during a migration or an incident.`,
  );

  protected readonly severityOptions: readonly SelectOption[] = [
    { value: 'info', label: $localize`:@@general.banner.severity.info:Notice` },
    { value: 'warning', label: $localize`:@@general.banner.severity.warning:Warning` },
    { value: 'critical', label: $localize`:@@general.banner.severity.critical:Critical` },
  ];

  protected readonly bannerDirty = computed(() => {
    const s = this.stored();
    if (!s) return false;
    return (
      this.bannerEnabled() !== s.enabled ||
      this.severity() !== (s.severity ?? 'info') ||
      this.title().trim() !== (s.title ?? '') ||
      this.message().trim() !== (s.message ?? '')
    );
  });

  protected readonly modeDirty = computed(() => {
    const m = this.storedMode();
    if (!m) return false;
    const on = m.mode === 'read_only';
    if (this.readOnly() !== on) return true;
    if (!on) return false;
    return (
      this.modeMessage().trim() !== (m.message ?? '') ||
      this.cacheFill() !== m.cacheFill ||
      Number(this.retryAfter()) !== (m.retryAfterSeconds ?? 300)
    );
  });

  /** The message as the banner will show it: text and `https` links, nothing else (R-OPS-025). */
  protected readonly preview = computed(() => parseBannerText(this.message()));

  protected readonly baseUrlSourceText = computed(() => {
    const info = this.info();
    if (!info) return '';
    return info.baseUrlSource === 'config'
      ? $localize`:@@general.base-url.config:Set in the configuration file (server.base_url).`
      : $localize`:@@general.base-url.request:Derived from the request, because server.base_url is not set. This is for development only.`;
  });

  protected readonly messageHint = $localize`:@@general.banner.message-hint:Plain text. A link is written [text](https://example.com). Other links stay text. At most ${MESSAGE_MAX}:max: characters.`;
  protected readonly modeMessageHint = $localize`:@@general.mode.message-hint:Shown to every user and sent with every refused write. At most ${MAINTENANCE_MESSAGE_MAX}:max: characters.`;

  constructor() {
    void this.load();
  }

  protected async load(): Promise<void> {
    this.loadError.set(null);
    try {
      const [info, banner, mode] = await Promise.all([this.api.info(), this.api.banner(), this.api.maintenance()]);
      this.info.set(info);
      this.adoptBanner(banner);
      this.adoptMode(mode);
    } catch (e) {
      this.loadError.set(asApiError(e));
    }
  }

  private adoptBanner(b: BannerModel): void {
    this.stored.set(b);
    this.bannerEnabled.set(b.enabled);
    this.severity.set(b.severity ?? 'info');
    this.title.set(b.title ?? '');
    this.message.set(b.message ?? '');
  }

  private adoptMode(m: MaintenanceMode): void {
    this.storedMode.set(m);
    if (m.mode === 'read_only') this.modeOpen.set(true);
    this.readOnly.set(m.mode === 'read_only');
    this.modeMessage.set(m.message ?? '');
    this.cacheFill.set(m.cacheFill);
    this.retryAfter.set(String(m.retryAfterSeconds ?? 300));
  }

  protected bannerFieldError(pointer: string): string | null {
    return fieldErrorAt(this.bannerError(), pointer);
  }

  protected modeFieldError(pointer: string): string | null {
    return fieldErrorAt(this.modeError(), pointer);
  }

  protected setSeverity(value: string): void {
    this.severity.set(value as Severity);
  }

  protected async saveBanner(event: Event): Promise<void> {
    event.preventDefault();
    if (!this.bannerDirty() || this.savingBanner()) return;
    this.savingBanner.set(true);
    this.bannerError.set(null);
    try {
      const saved = await this.api.setBanner({
        enabled: this.bannerEnabled(),
        severity: this.severity(),
        title: this.title().trim(),
        message: this.message().trim(),
      });
      this.adoptBanner(saved);
      this.notices.adopt(saved, null);
      this.notifier.status($localize`:@@general.banner.saved:Banner saved.`);
    } catch (e) {
      this.bannerError.set(asApiError(e));
    } finally {
      this.savingBanner.set(false);
    }
  }

  protected async saveMode(event: Event): Promise<void> {
    event.preventDefault();
    if (!this.modeDirty() || this.savingMode()) return;
    if (this.readOnly() && this.storedMode()?.mode !== 'read_only' && !(await this.confirmSwitchOn())) return;
    this.savingMode.set(true);
    this.modeError.set(null);
    try {
      const saved = await this.api.setMaintenance(
        this.readOnly()
          ? {
              mode: 'read_only',
              message: this.modeMessage().trim(),
              cacheFill: this.cacheFill(),
              retryAfterSeconds: Number(this.retryAfter()),
            }
          : { mode: 'normal' },
      );
      this.adoptMode(saved);
      this.notices.adopt(null, saved);
      this.notifier.status(
        saved.mode === 'read_only'
          ? $localize`:@@general.mode.saved-on:Read-only maintenance mode is on.`
          : $localize`:@@general.mode.saved-off:Read-only maintenance mode is off.`,
      );
    } catch (e) {
      this.modeError.set(asApiError(e));
    } finally {
      this.savingMode.set(false);
    }
  }

  private async confirmSwitchOn(): Promise<boolean> {
    const answer = await this.confirm.ask({
      title: $localize`:@@general.mode.confirm-title:Switch to read-only maintenance mode?`,
      lines: [
        $localize`:@@general.mode.confirm-1:Every user's writes are refused with 503 until you switch the mode off again: publishing, pushing, deleting and every change of the configuration.`,
        $localize`:@@general.mode.confirm-2:Downloads, sign-in, creating and revoking tokens, and this switch keep working.`,
      ],
      confirmLabel: $localize`:@@general.mode.confirm:Switch on`,
    });
    return answer !== null;
  }
}
