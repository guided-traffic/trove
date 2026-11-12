import { DatePipe, DecimalPipe, NgTemplateOutlet } from '@angular/common';
import { Component, computed, effect, inject, resource, signal } from '@angular/core';
import { RouterLink } from '@angular/router';
import { EncryptionApi, SystemApi } from '../../api/system-api';
import { ApiError } from '../../api/problem';
import { SessionStore } from '../../core/session-store';
import { ErrorNotice } from '../../shared/error-notice';
import { valueOf } from '../../shared/paged-resource';
import { formatSize } from '../packages/format-size';
import { CheckSummary, checkSummary, roleLoad, share } from './dashboard-model';
import { REFRESH_INTERVAL_MS } from './system-status';
import { roleGroups, serviceState } from './system-status-model';
import { PageHeader } from '../../shared/ui/page-header';

/**
 * The admin dashboard (Q-OPS-175): the first screen of the admin area, an overview of the installation in cards.
 * The instance bar, the components (each role's replicas and their load from the heartbeat measurements), the
 * dependencies (PostgreSQL, Valkey, the object store with the content it holds, each with its readiness check), and
 * the counts of content and access with the way to their screens. It refreshes like the status screen, with a pause.
 */
@Component({
  selector: 'trove-dashboard',
  imports: [PageHeader, DatePipe, DecimalPipe, NgTemplateOutlet, RouterLink, ErrorNotice],
  templateUrl: './dashboard.html',
  styleUrl: './dashboard.scss',
})
export class Dashboard {
  private readonly system = inject(SystemApi);
  private readonly encryptionApi = inject(EncryptionApi);
  protected readonly session = inject(SessionStore);
  protected readonly size = formatSize;
  /** A resource's value without the throw of its error state, which would stop the whole template. */
  protected readonly valueOf = valueOf;

  protected readonly info = resource({ loader: () => this.system.info() });
  protected readonly replicas = resource({ loader: () => this.system.replicas() });
  protected readonly overview = resource({ loader: () => this.system.overview() });
  protected readonly encryption = resource({ loader: () => this.encryptionApi.status() });
  protected readonly paused = signal(false);
  /** The repository classes in the order the repository screens name them. */
  protected readonly classes = [
    { key: 'local', label: $localize`:@@dashboard.class.local:Local` },
    { key: 'remote', label: $localize`:@@dashboard.class.remote:Remote` },
    { key: 'virtual', label: $localize`:@@dashboard.class.virtual:Virtual` },
  ] as const;

  protected readonly state = computed(() => {
    const r = valueOf(this.replicas);
    return r ? serviceState(r) : null;
  });
  /**
   * One card per role. A live replica of the development role `all` serves every role, so the main roles it covers
   * are not shown as missing next to it.
   */
  protected readonly loads = computed(() => {
    const info = valueOf(this.info);
    const replicas = valueOf(this.replicas);
    const loads = info && replicas ? roleGroups(info, replicas).map(roleLoad) : [];
    const all = loads.some((l) => l.role === 'all' && l.live > 0);
    return all ? loads.filter((l) => l.role === 'all' || l.live > 0) : loads;
  });
  protected readonly checks = computed(() => {
    const replicas = valueOf(this.replicas) ?? [];
    return {
      database: checkSummary(replicas, 'database'),
      valkey: checkSummary(replicas, 'valkey'),
      objectStore: checkSummary(replicas, 'objectStore'),
    };
  });
  protected readonly pool = computed(() => {
    const pools = valueOf(this.info)?.database?.pools ?? {};
    const all = Object.values(pools);
    const inUse = all.reduce((a, p) => a + p.inUse, 0);
    const max = all.reduce((a, p) => a + p.max, 0);
    return { inUse, max, share: share(inUse, max) };
  });
  protected readonly failure = computed(() => {
    const e = this.info.error() ?? this.replicas.error();
    return e ? (e instanceof ApiError ? e : new ApiError(0, null, null)) : null;
  });
  protected readonly overviewFailure = computed(() => {
    const e = this.overview.error();
    return e ? (e instanceof ApiError ? e : new ApiError(0, null, null)) : null;
  });

  constructor() {
    effect((onCleanup) => {
      if (this.paused()) return;
      const timer = setInterval(() => this.reload(), REFRESH_INTERVAL_MS);
      onCleanup(() => clearInterval(timer));
    });
  }

  protected reload(): void {
    this.info.reload();
    this.replicas.reload();
    this.overview.reload();
    this.encryption.reload();
  }

  protected togglePause(): void {
    this.paused.update((p) => !p);
  }

  /** A bar's width in percent, or null without a known whole. */
  protected percent(part: number, whole: number | null): number | null {
    const s = share(part, whole);
    return s === null ? null : Math.round(s * 100);
  }

  protected checkState(c: CheckSummary): 'ok' | 'bad' | 'none' {
    return c.reporting === 0 ? 'none' : c.failing > 0 ? 'bad' : 'ok';
  }
}
