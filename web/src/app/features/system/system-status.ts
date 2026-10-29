import { DatePipe } from '@angular/common';
import { Component, computed, effect, inject, resource, signal, viewChild } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { RouterLink } from '@angular/router';
import { newIdempotencyKey } from '../../api/client';
// >>> M1 ui-a
import { Job, JobsApi } from '../../api/jobs-api';
// <<< M1 ui-a
import { ApiError } from '../../api/problem';
import { EncryptionApi, SystemApi } from '../../api/system-api';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { ErrorNotice } from '../../shared/error-notice';
// >>> M1 ui-a
import { JobProgress } from '../../shared/job-progress';
import { describeProblem } from '../../core/http/problem-messages';
import { JobTable } from './job-table';
import { TasksCard } from './tasks-card';
// <<< M1 ui-a
import {
  degradedChecks,
  failingChecks,
  keyStatus,
  mixedVersions,
  RingGap,
  ringGaps,
  roleGroups,
  rotationStep,
  serviceState,
  valkeySummary,
} from './system-status-model';
import { valueOf } from '../../shared/paged-resource';

/** Automatic refresh interval with a pause control (R-UI-038, WCAG 2.2.2). */
export const REFRESH_INTERVAL_MS = 15_000;

// >>> M1 ui-a
/** Jobs shown on the status screen; the jobs screen lists them all. */
export const RECENT_JOBS = 10;
// <<< M1 ui-a

/**
 * Replicas disagreeing about the Valkey primary for longer than this are reported. The spec
 * bounds it by `valkey.sentinel.refresh_interval` plus one heartbeat, which the UI cannot read;
 * this is a fixed upper bound of both defaults (Q-UI-12).
 */
export const PRIMARY_DISAGREEMENT_GRACE_MS = 30_000;

/** System status (R-UI-038): installation facts, replicas per role, Valkey, stored-secret keys. */
@Component({
  selector: 'trove-system-status',
  imports: [DatePipe, MatButtonModule, RouterLink, ErrorNotice, JobProgress, JobTable, TasksCard],
  templateUrl: './system-status.html',
  styleUrl: './system-status.scss',
})
export class SystemStatus {
  protected readonly valueOf = valueOf;
  private readonly system = inject(SystemApi);
  private readonly encryptionApi = inject(EncryptionApi);
  private readonly notifier = inject(Notifier);
  private readonly session = inject(SessionStore);
  // >>> M1 ui-a
  private readonly jobsApi = inject(JobsApi);
  private readonly tasksCard = viewChild(TasksCard);
  // <<< M1 ui-a

  protected readonly info = resource({ loader: () => this.system.info() });
  protected readonly replicas = resource({ loader: () => this.system.replicas() });
  protected readonly encryption = resource({ loader: () => this.encryptionApi.status() });

  // >>> M1 ui-a
  protected readonly recentJobs = resource({
    loader: async () => (await this.jobsApi.jobs({})(null, RECENT_JOBS)).items,
  });
  protected readonly recentFailure = computed(() => {
    const e = this.recentJobs.error();
    return e instanceof ApiError ? e : e ? new ApiError(0, null, null) : null;
  });
  /** The job of a re-encryption started from the key card; followed until it ends. */
  protected readonly reencryptJob = signal<string | null>(null);
  protected readonly gaps = computed(() => {
    const e = valueOf(this.encryption);
    return e ? ringGaps(e) : [];
  });
  protected readonly step = computed(() => {
    const e = valueOf(this.encryption);
    return e ? rotationStep(e) : null;
  });
  // <<< M1 ui-a
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
    const i = valueOf(this.info);
    const r = valueOf(this.replicas);
    return i && r ? roleGroups(i, r) : [];
  });
  protected readonly state = computed(() => {
    const r = valueOf(this.replicas);
    return r ? serviceState(r) : null;
  });
  protected readonly notDrainSafe = computed(() =>
    this.groups().some((g) => g.replicas.length > 0 && !g.drainSafe),
  );
  protected readonly versions = computed(() => {
    const i = valueOf(this.info);
    return i ? mixedVersions(i) : [];
  });
  protected readonly valkey = computed(() => {
    const i = valueOf(this.info);
    const r = valueOf(this.replicas);
    return i && r ? valkeySummary(i, r) : null;
  });
  /** Disagreement that outlasted the grace period. */
  protected readonly primariesDisagree = signal(false);

  protected readonly schemaSupported = computed(() => {
    const db = valueOf(this.info)?.database;
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
    // >>> M1 ui-a
    this.recentJobs.reload();
    this.tasksCard()?.reload();
    // <<< M1 ui-a
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
      // >>> M1 ui-a
      this.reencryptJob.set(await this.encryptionApi.reencrypt(newIdempotencyKey()));
      // <<< M1 ui-a
      this.notifier.status($localize`:@@system.reencrypt.started:Re-encryption started.`);
      this.encryption.reload();
    } catch (e) {
      this.notifier.alert(
        e instanceof ApiError
          ? describeProblem(e).text
          : $localize`:@@system.reencrypt.failed:Re-encryption could not be started.`,
      );
    } finally {
      this.reencrypting.set(false);
    }
  }

  // >>> M1 ui-a
  protected gapOf(replica: string): RingGap | undefined {
    return this.gaps().find((g) => g.replica === replica);
  }

  protected reencryptEnded(): void {
    this.reencryptJob.set(null);
    this.encryption.reload();
    this.recentJobs.reload();
  }

  protected async cancelJob(job: Job): Promise<void> {
    try {
      await this.jobsApi.cancel(job.id);
      this.notifier.status($localize`:@@jobs.cancelling:Job ${job.kind}:kind: is being cancelled.`);
    } catch (e) {
      this.notifier.alert(
        e instanceof ApiError
          ? describeProblem(e).text
          : $localize`:@@jobs.cancel.failed:The job could not be cancelled.`,
      );
    } finally {
      this.recentJobs.reload();
    }
  }

  protected readonly recentCaption = $localize`:@@system.jobs.caption:Recent jobs`;
  protected readonly reencryptLabel = $localize`:@@system.reencrypt.progress:Re-encryption`;
  // <<< M1 ui-a

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
