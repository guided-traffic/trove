import {
  Component,
  DestroyRef,
  computed,
  effect,
  inject,
  input,
  output,
  signal,
  untracked,
} from '@angular/core';
import { Job, JobState, JobsApi, jobActive } from '../api/jobs-api';
import { ApiError } from '../api/problem';
import { ErrorNotice } from './error-notice';
import { asApiError } from './paged-resource';

/** Poll interval of a running job (R-API-015 suggests it through `Retry-After`; the client uses a fixed one). */
export const JOB_POLL_INTERVAL_MS = 2_000;

/** The progress is announced to assistive technology at most this often (R-UI-042). */
export const JOB_ANNOUNCE_INTERVAL_MS = 10_000;

/** The translated name of a job state. */
export function jobStateText(state: JobState): string {
  switch (state) {
    case 'queued':
      return $localize`:@@job.state.queued:queued`;
    case 'running':
      return $localize`:@@job.state.running:running`;
    case 'cancelling':
      return $localize`:@@job.state.cancelling:cancelling`;
    case 'succeeded':
      return $localize`:@@job.state.succeeded:succeeded`;
    case 'failed':
      return $localize`:@@job.state.failed:failed`;
    case 'cancelled':
      return $localize`:@@job.state.cancelled:cancelled`;
  }
}

/**
 * Polls a job until it ends and shows its state and progress (R-UI-042, R-API-015). The visible
 * progress is live; the text for assistive technology changes at most every 10 seconds and on the
 * final state. `finished` emits the last job once, so the screen can reload what the job changed.
 */
@Component({
  selector: 'trove-job-progress',
  imports: [ErrorNotice],
  template: `
    <div class="job">
      @if (failure(); as f) {
        <trove-error-notice [error]="f" (retry)="resume()" />
      } @else {
        @if (job(); as j) {
          @if (bar(); as b) {
            <progress [attr.aria-label]="label()" [value]="b.done" [max]="b.total"></progress>
          } @else if (active()) {
            <progress [attr.aria-label]="label()"></progress>
          }
          <span class="state" [attr.data-state]="j.state">{{ stateText() }}</span>
          @if (counts(); as c) {
            <span class="counts" translate="no">{{ c }}</span>
          }
        }
        <span class="visually-hidden" role="status">{{ announcement() }}</span>
      }
    </div>
  `,
  styles: `
    .job {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: var(--trove-space-3);
    }
    progress {
      inline-size: 12rem;
    }
    .visually-hidden {
      position: absolute;
      inline-size: 1px;
      block-size: 1px;
      overflow: hidden;
      clip-path: inset(50%);
    }
  `,
})
export class JobProgress {
  private readonly api = inject(JobsApi);
  private readonly destroyed = inject(DestroyRef);

  /** The ID of the job to follow (from `Location` or the job reference). */
  readonly jobId = input.required<string>();
  /** What the job does, for the accessible name of the bar. */
  readonly label = input<string>($localize`:@@job.progress.label:Job progress`);
  readonly intervalMs = input(JOB_POLL_INTERVAL_MS);
  readonly finished = output<Job>();

  protected readonly job = signal<Job | null>(null);
  protected readonly failure = signal<ApiError | null>(null);
  protected readonly announcement = signal('');
  protected readonly active = computed(() => {
    const j = this.job();
    return j === null || jobActive(j.state);
  });
  protected readonly bar = computed(() => {
    const p = this.job()?.progress;
    return p && p.total ? { done: p.done, total: p.total } : null;
  });
  protected readonly counts = computed(() => {
    const p = this.job()?.progress;
    if (!p) return '';
    const of = p.total === null ? '' : `/${p.total}`;
    return `${p.done}${of} ${p.unit ?? ''}`.trim();
  });
  protected readonly stateText = computed(() => {
    const j = this.job();
    return j ? jobStateText(j.state) : '';
  });

  private lastAnnounced = 0;
  private run = 0;
  private timer: ReturnType<typeof setTimeout> | undefined;

  constructor() {
    this.destroyed.onDestroy(() => this.stop());
    effect(() => {
      this.jobId();
      untracked(() => this.resume());
    });
  }

  protected resume(): void {
    this.stop();
    this.failure.set(null);
    const run = ++this.run;
    void this.poll(run);
  }

  private stop(): void {
    this.run++;
    clearTimeout(this.timer);
  }

  private async poll(run: number): Promise<void> {
    try {
      const job = await this.api.job(this.jobId());
      if (run !== this.run) return;
      this.job.set(job);
      this.announce(job);
      if (jobActive(job.state)) {
        this.timer = setTimeout(() => void this.poll(run), this.intervalMs());
      } else {
        this.finished.emit(job);
      }
    } catch (e) {
      if (run === this.run) this.failure.set(asApiError(e));
    }
  }

  private announce(job: Job): void {
    const now = Date.now();
    if (jobActive(job.state) && now - this.lastAnnounced < JOB_ANNOUNCE_INTERVAL_MS) return;
    this.lastAnnounced = now;
    const progress = job.progress;
    const state = jobStateText(job.state);
    this.announcement.set(
      progress?.total
        ? $localize`:@@job.announce.progress:${this.label()}:label: ${state}:state:, ${progress.done}:done: of ${progress.total}:total:`
        : $localize`:@@job.announce.state:${this.label()}:label: ${state}:state:`,
    );
  }
}
