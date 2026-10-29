import { DatePipe } from '@angular/common';
import { Component, computed, effect, inject, resource, signal } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { RouterLink } from '@angular/router';
import { newIdempotencyKey } from '../../api/client';
import { ApiError } from '../../api/problem';
import { EncryptionApi, SystemApi } from '../../api/system-api';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { ErrorNotice } from '../../shared/error-notice';
import {
  degradedChecks,
  failingChecks,
  keyStatus,
  mixedVersions,
  roleGroups,
  serviceState,
  valkeySummary,
} from './system-status-model';

/** Automatic refresh interval with a pause control (R-UI-038, WCAG 2.2.2). */
export const REFRESH_INTERVAL_MS = 15_000;

/**
 * Replicas disagreeing about the Valkey primary for longer than this are reported. The spec
 * bounds it by `valkey.sentinel.refresh_interval` plus one heartbeat, which the UI cannot read;
 * this is a fixed upper bound of both defaults (Q-UI-12).
 */
export const PRIMARY_DISAGREEMENT_GRACE_MS = 30_000;

/** System status (R-UI-038): installation facts, replicas per role, Valkey, stored-secret keys. */
@Component({
  selector: 'trove-system-status',
  imports: [DatePipe, MatButtonModule, RouterLink, ErrorNotice],
  templateUrl: './system-status.html',
  styleUrl: './system-status.scss',
})
export class SystemStatus {
  private readonly system = inject(SystemApi);
  private readonly encryptionApi = inject(EncryptionApi);
  private readonly notifier = inject(Notifier);
  private readonly session = inject(SessionStore);

  protected readonly info = resource({ loader: () => this.system.info() });
  protected readonly replicas = resource({ loader: () => this.system.replicas() });
  protected readonly encryption = resource({ loader: () => this.encryptionApi.status() });

  protected readonly paused = signal(false);
  protected readonly reencrypting = signal(false);
  private disagreeSince: number | null = null;

  protected readonly failure = computed(() => {
    const e = this.info.error() ?? this.replicas.error();
    return e instanceof ApiError ? e : e ? new ApiError(0, null, null) : null;
  });
  protected readonly encryptionFailure = computed(() => {
    const e = this.encryption.error();
    return e instanceof ApiError ? e : e ? new ApiError(0, null, null) : null;
  });

  protected readonly groups = computed(() => {
    const i = this.info.value();
    const r = this.replicas.value();
    return i && r ? roleGroups(i, r) : [];
  });
  protected readonly state = computed(() => {
    const r = this.replicas.value();
    return r ? serviceState(r) : null;
  });
  protected readonly notDrainSafe = computed(() =>
    this.groups().some((g) => g.replicas.length > 0 && !g.drainSafe),
  );
  protected readonly versions = computed(() => {
    const i = this.info.value();
    return i ? mixedVersions(i) : [];
  });
  protected readonly valkey = computed(() => {
    const i = this.info.value();
    const r = this.replicas.value();
    return i && r ? valkeySummary(i, r) : null;
  });
  /** Disagreement that outlasted the grace period. */
  protected readonly primariesDisagree = signal(false);

  protected readonly schemaSupported = computed(() => {
    const db = this.info.value()?.database;
    return db ? db.supportedSchemaVersions.includes(db.schemaVersion) : true;
  });
  /** Re-encryption is an administrator's task (R-API-049); `content:bypass` is the admin role's. */
  protected readonly canReencrypt = computed(() => this.session.has('content:bypass'));

  constructor() {
    effect((onCleanup) => {
      if (this.paused()) return;
      const timer = setInterval(() => this.reload(), REFRESH_INTERVAL_MS);
      onCleanup(() => clearInterval(timer));
    });
    effect(() => {
      const v = this.valkey();
      const now = Date.now();
      if (!v?.primariesDisagree) {
        this.disagreeSince = null;
        this.primariesDisagree.set(false);
        return;
      }
      this.disagreeSince ??= now;
      this.primariesDisagree.set(now - this.disagreeSince >= PRIMARY_DISAGREEMENT_GRACE_MS);
    });
  }

  protected reload(): void {
    this.info.reload();
    this.replicas.reload();
    this.encryption.reload();
  }

  protected togglePause(): void {
    this.paused.update((p) => !p);
  }

  protected failing = failingChecks;
  protected degraded = degradedChecks;
  protected keyStatus = keyStatus;

  protected async reencrypt(): Promise<void> {
    this.reencrypting.set(true);
    try {
      await this.encryptionApi.reencrypt(newIdempotencyKey());
      this.notifier.status($localize`:@@system.reencrypt.started:Re-encryption started.`);
      this.encryption.reload();
    } catch (e) {
      this.notifier.alert(
        e instanceof ApiError
          ? e.message
          : $localize`:@@system.reencrypt.failed:Re-encryption could not be started.`,
      );
    } finally {
      this.reencrypting.set(false);
    }
  }

  protected checkText(name: string): string {
    switch (name) {
      case 'objectStore':
        return $localize`:@@system.check.objectStore:Object store: blob requests on this replica fail with a retryable 503; everything else keeps working.`;
      case 'valkey':
        return $localize`:@@system.check.valkey:Valkey: no request fails, but remote fetches are collapsed per replica only, the negative cache is per replica, rate limits fall back to per-replica shares, and changes arrive through the change-log poll.`;
      default:
        return name;
    }
  }
}
