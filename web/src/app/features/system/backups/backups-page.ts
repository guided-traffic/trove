import { DatePipe } from '@angular/common';
import { Component, computed, effect, inject, input, resource, signal, untracked } from '@angular/core';
import { Router, RouterLink } from '@angular/router';
import {
  BACKUP_OPERATIONS,
  BACKUP_STATES,
  BackupRun,
  BackupsApi,
} from '../../../api/backups-api';
import { ApiError } from '../../../api/problem';
import { describeBackupProblem } from '../../../core/http/problem-messages-backup';
import { ErrorNotice } from '../../../shared/error-notice';
import { PagedResource, asApiError, valueOf } from '../../../shared/paged-resource';
import { formatSize } from '../../packages/format-size';
import { REFRESH_INTERVAL_MS } from '../system-status';
import { BackupState } from './backup-state';
import {
  durationText,
  errorCode,
  operationText,
  phaseText,
  resultNumber,
  resultWarnings,
  runSeconds,
  stateView,
} from './backups-model';
import { ManualRunPanel } from './manual-run-panel';
import { PageHeader } from '../../../shared/ui/page-header';

/** Runs per page of the list; the automatic refresh keeps the first page current and leaves a longer list alone. */
export const RUNS_PAGE_SIZE = 25;

/**
 * Backups (R-UI-048, R-API-050): the status of the backup job, the run in progress, the runs, the stored backups the newest
 * listing run found, the command for a manual run and, after a restore, where this installation came from. The screen only
 * reads: every request it sends is a GET. It refreshes with the status screen's interval and pause control (R-UI-038).
 */
@Component({
  selector: 'trove-backups-page',
  imports: [PageHeader, DatePipe, RouterLink, ErrorNotice, BackupState, ManualRunPanel],
  templateUrl: './backups-page.html',
  styleUrl: './backups-page.scss',
})
export class BackupsPage {
  private readonly api = inject(BackupsApi);
  private readonly router = inject(Router);

  /** Query parameters of the run filters. */
  readonly operation = input<string | undefined>();
  readonly state = input<string | undefined>();

  protected readonly operations = BACKUP_OPERATIONS;
  protected readonly states = BACKUP_STATES;
  protected readonly operationText = operationText;
  protected readonly phaseText = phaseText;
  protected readonly durationText = durationText;
  protected readonly formatSize = formatSize;
  protected readonly describeBackupProblem = describeBackupProblem;
  protected readonly errorCode = errorCode;
  protected readonly resultNumber = resultNumber;
  protected readonly resultWarnings = resultWarnings;
  protected readonly caption = $localize`:@@backups.runs.caption:Backup runs, newest first`;
  protected readonly blobsLabel = $localize`:@@backups.active.blobs-label:Blobs copied`;
  protected readonly storedCaption = $localize`:@@backups.stored.caption:Complete backups found in the target`;

  protected readonly paused = signal(false);
  protected readonly now = signal(Date.now());
  protected readonly operationDraft = signal('');
  protected readonly stateDraft = signal('');
  protected readonly selectedId = signal<string | null>(null);

  protected readonly runs = new PagedResource<BackupRun>(RUNS_PAGE_SIZE);

  /** The summary and, while a run is active, that run with its progress: one load, so the two never disagree. */
  protected readonly status = resource({
    loader: async () => {
      const summary = await this.api.summary();
      const active = summary.active ? await this.api.run(summary.active.id).catch(() => null) : null;
      return { summary, active };
    },
  });
  protected readonly detail = resource({
    params: () => this.selectedId() ?? undefined,
    loader: ({ params }) => this.api.run(params),
  });

  protected readonly summary = computed(() => valueOf(this.status)?.summary ?? null);
  protected readonly activeRun = computed(() => valueOf(this.status)?.active ?? null);
  protected readonly statusFailure = computed(() => toApiError(this.status.error()));
  protected readonly detailFailure = computed(() => toApiError(this.detail.error()));
  protected readonly detailRun = computed(() => valueOf(this.detail) ?? null);

  /** The age of the last success, against the clock that ticks with the refresh. */
  protected readonly lastSuccessAge = computed(() => {
    const at = this.summary()?.lastSuccessAt;
    return at ? durationText((this.now() - Date.parse(at)) / 1000) : null;
  });
  protected readonly lastFailureText = computed(() => {
    const failure = this.summary()?.lastFailure;
    if (!failure) return null;
    return failure.code ? (describeBackupProblem(failure.code) ?? null) : null;
  });
  protected readonly storedAsOf = computed(() => this.summary()?.storedBackups ?? null);

  private readonly appliedOperation = computed(() => (this.operations as readonly string[]).includes(this.operation() ?? '') ? this.operation()! : '');
  private readonly appliedState = computed(() => (this.states as readonly string[]).includes(this.state() ?? '') ? this.state()! : '');

  constructor() {
    effect(() => {
      this.operationDraft.set(this.appliedOperation());
      this.stateDraft.set(this.appliedState());
      untracked(() => this.loadRuns());
    });
    effect((onCleanup) => {
      if (this.paused()) return;
      const timer = setInterval(() => this.refresh(), REFRESH_INTERVAL_MS);
      onCleanup(() => clearInterval(timer));
    });
  }

  private loadRuns(): void {
    void this.runs.start(
      this.api.runs({ operation: this.appliedOperation(), state: this.appliedState() }),
    );
  }

  /** Reloads the status and the open run, and the first page of the list unless the user has loaded further pages. */
  protected refresh(): void {
    this.now.set(Date.now());
    this.status.reload();
    if (this.selectedId() !== null) this.detail.reload();
    if (this.runs.items().length <= RUNS_PAGE_SIZE) this.loadRuns();
  }

  protected apply(event: Event): void {
    event.preventDefault();
    void this.router.navigate([], {
      queryParams: { operation: this.operationDraft() || null, state: this.stateDraft() || null },
      queryParamsHandling: 'merge',
    });
  }

  protected open(run: BackupRun): void {
    this.selectedId.set(run.id);
  }

  protected close(): void {
    this.selectedId.set(null);
  }

  protected duration(run: BackupRun): string {
    const seconds = runSeconds(run, this.now());
    return seconds === null ? '–' : durationText(seconds);
  }

  protected databaseBytes(run: BackupRun): string {
    const bytes = resultNumber(run, 'databaseBytes');
    return bytes === null ? '–' : formatSize(bytes);
  }

  protected blobsCopied(run: BackupRun): string {
    const copied = run.progress.blobsCopied;
    return copied === undefined ? '–' : String(copied);
  }

  /** The pretty-printed result of an open run. */
  protected resultJson(run: BackupRun): string {
    return JSON.stringify(run.result ?? {}, null, 2);
  }

  protected stateName(state: string): string {
    return stateView(state, false).text;
  }

  protected detailLabel(run: BackupRun): string {
    return $localize`:@@backups.details.label:Details of the run of pod ${run.pod}:pod:`;
  }
}

function toApiError(e: unknown): ApiError | null {
  return e ? asApiError(e) : null;
}
