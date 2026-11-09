import { DestroyRef, Injectable, computed, inject, signal } from '@angular/core';
import { Banner, MaintenanceMode, SystemApi } from '../api/system-api';
import { SessionStore } from './session-store';

/** The banner and the mode are read again this often, so a saved change shows on every screen within a minute (R-UI-033). */
export const BANNER_POLL_MS = 30_000;

/** One notice above the content (R-UI-002, R-UI-033): the system banner or the maintenance mode. */
export interface Notice {
  readonly kind: 'banner' | 'maintenance';
  readonly severity: 'info' | 'warning' | 'critical';
  readonly title: string;
  readonly message: string;
}

/**
 * The system banner (`GET /api/v1/system/banner`, public: the sign-in page shows it) and the read-only maintenance mode
 * (`GET /api/v1/system/maintenance`, for a signed-in caller). Every screen that shows them calls [watch], which reads them at once and
 * then every [BANNER_POLL_MS] until the screen is gone. A failed read keeps what was shown: a notice is no reason for an error
 * of its own.
 */
@Injectable({ providedIn: 'root' })
export class BannerStore {
  private readonly api = inject(SystemApi);
  private readonly session = inject(SessionStore);

  private readonly _banner = signal<Banner | null>(null);
  private readonly _maintenance = signal<MaintenanceMode | null>(null);
  private watchers = 0;
  private timer: ReturnType<typeof setInterval> | undefined;

  readonly banner = this._banner.asReadonly();
  readonly maintenance = this._maintenance.asReadonly();

  /** The notices in the order they are shown: the maintenance mode first, because it changes what a user can do. */
  readonly notices = computed<readonly Notice[]>(() => {
    const out: Notice[] = [];
    const m = this._maintenance();
    if (m?.mode === 'read_only') {
      out.push({ kind: 'maintenance', severity: 'warning', title: '', message: m.message ?? '' });
    }
    const b = this._banner();
    if (b?.enabled && (b.message ?? '').trim() !== '') {
      out.push({ kind: 'banner', severity: b.severity ?? 'info', title: b.title ?? '', message: b.message ?? '' });
    }
    return out;
  });

  /** Starts reading for the lifetime of `destroyed`; the polling ends with the last watcher. */
  watch(destroyed: DestroyRef): void {
    this.watchers++;
    if (this.watchers === 1) this.timer = setInterval(() => void this.refresh(), BANNER_POLL_MS);
    void this.refresh();
    destroyed.onDestroy(() => {
      this.watchers--;
      if (this.watchers === 0 && this.timer !== undefined) {
        clearInterval(this.timer);
        this.timer = undefined;
      }
    });
  }

  /** Reads both now; the settings screen calls it after a save, so the writer sees its own notice without waiting. */
  async refresh(): Promise<void> {
    await Promise.all([this.readBanner(), this.readMaintenance()]);
  }

  /** Replaces what is shown with what a save answered. */
  adopt(banner: Banner | null, maintenance: MaintenanceMode | null): void {
    if (banner) this._banner.set(banner);
    if (maintenance) this._maintenance.set(maintenance);
  }

  private async readBanner(): Promise<void> {
    try {
      this._banner.set(await this.api.banner());
    } catch {
      // keep what is shown
    }
  }

  private async readMaintenance(): Promise<void> {
    if (!this.session.authenticated()) {
      this._maintenance.set(null);
      return;
    }
    try {
      this._maintenance.set(await this.api.maintenance());
    } catch {
      // keep what is shown
    }
  }
}
