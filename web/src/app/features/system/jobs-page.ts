import { Component, computed, effect, inject, input, signal, untracked } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { Router, RouterLink } from '@angular/router';
import { Job, JobState, JobsApi, jobActive } from '../../api/jobs-api';
import { describeProblem } from '../../core/http/problem-messages';
import { Notifier } from '../../core/notifier';
import { ErrorNotice } from '../../shared/error-notice';
import { jobStateText } from '../../shared/job-progress';
import { PagedResource, asApiError } from '../../shared/paged-resource';
import { JobTable } from './job-table';

const STATES: readonly JobState[] = [
  'queued',
  'running',
  'cancelling',
  'succeeded',
  'failed',
  'cancelled',
];

/** Refresh interval of the job list while it shows an active job (WCAG 2.2.2: with a pause control). */
export const JOBS_REFRESH_MS = 5_000;

/**
 * Jobs (R-UI-038, R-API-015): every job the caller may see, newest first, filterable by kind and
 * state (query parameters `kind` and repeated `state`), with "Cancel" for queued and running jobs.
 * A cancelled running job shows as `cancelling` until it stops at its next checkpoint, then as
 * `cancelled`; the list refreshes itself while an active job is shown, and the refresh can be
 * paused.
 */
@Component({
  selector: 'trove-jobs-page',
  imports: [MatButtonModule, RouterLink, ErrorNotice, JobTable],
  template: `
    <header class="page-header">
      <h1 tabindex="-1" i18n="@@jobs.heading">Jobs</h1>
      <button
        mat-stroked-button
        type="button"
        [attr.aria-pressed]="paused()"
        (click)="paused.set(!paused())"
      >
        @if (paused()) {
          <ng-container i18n="@@system.resume">Resume automatic refresh</ng-container>
        } @else {
          <ng-container i18n="@@system.pause">Pause automatic refresh</ng-container>
        }
      </button>
      <button mat-button type="button" (click)="load()" i18n="@@system.refresh">Refresh now</button>
      <a mat-button routerLink="/admin/system" i18n="@@jobs.back">System status</a>
    </header>

    <form class="toolbar" role="search" (submit)="apply($event)">
      <div class="field">
        <label class="field-label" for="j-kind" i18n="@@jobs.filter.kind"
          >Kind (comma separated)</label
        >
        <input
          id="j-kind"
          class="text-input"
          autocomplete="off"
          spellcheck="false"
          [value]="kindDraft()"
          (input)="kindDraft.set($any($event.target).value)"
        />
      </div>
      <fieldset class="states">
        <legend class="field-label" i18n="@@jobs.filter.state">State</legend>
        @for (s of states; track s) {
          <label class="check-row">
            <input
              type="checkbox"
              [checked]="stateDraft().includes(s)"
              (change)="toggleState(s, $any($event.target).checked)"
            />
            <span>{{ stateText(s) }}</span>
          </label>
        }
      </fieldset>
      <button mat-flat-button type="submit" i18n="@@repo.filter.apply">Search</button>
    </form>

    @if (list.error(); as f) {
      <trove-error-notice [error]="f" (retry)="load()" />
    }
    @if (list.loaded()) {
      @if (list.items().length === 0) {
        <p class="empty" i18n="@@jobs.empty">No job matches the filters.</p>
      } @else {
        <trove-job-table
          [jobs]="list.items()"
          [caption]="caption"
          (cancelRequested)="cancel($event)"
        />
        @if (list.hasMore()) {
          <p>
            <button
              mat-stroked-button
              type="button"
              [disabled]="list.loading()"
              (click)="list.more()"
              i18n="@@list.load-more"
            >
              Load more
            </button>
          </p>
        }
      }
    } @else if (list.loading()) {
      <p role="status" i18n="@@system.loading">Loading.</p>
    }
  `,
  styles: `
    .states {
      display: flex;
      flex-wrap: wrap;
      gap: var(--trove-space-3);
      margin: 0;
      padding: 0;
      border: 0;
    }
  `,
})
export class JobsPage {
  private readonly api = inject(JobsApi);
  private readonly router = inject(Router);
  private readonly notifier = inject(Notifier);

  /** Query parameters; `state` repeats and arrives as a string or an array. */
  readonly kind = input<string | undefined>();
  readonly state = input<string | readonly string[] | undefined>();

  protected readonly list = new PagedResource<Job>();
  protected readonly paused = signal(false);
  protected readonly states = STATES;
  protected readonly kindDraft = signal('');
  protected readonly stateDraft = signal<readonly JobState[]>([]);
  protected readonly caption = $localize`:@@jobs.caption:Jobs, newest first`;

  private readonly kinds = computed(() =>
    (this.kind() ?? '')
      .split(',')
      .map((k) => k.trim())
      .filter((k) => k !== ''),
  );
  private readonly stateFilter = computed<readonly JobState[]>(() => {
    const raw = this.state();
    const values = raw === undefined ? [] : typeof raw === 'string' ? [raw] : raw;
    return STATES.filter((s) => values.includes(s));
  });
  private readonly hasActive = computed(() => this.list.items().some((j) => jobActive(j.state)));

  constructor() {
    effect(() => {
      this.kindDraft.set(this.kind() ?? '');
      this.stateDraft.set(this.stateFilter());
      untracked(() => this.load());
    });
    effect((onCleanup) => {
      if (this.paused() || !this.hasActive()) return;
      const timer = setInterval(() => this.load(), JOBS_REFRESH_MS);
      onCleanup(() => clearInterval(timer));
    });
  }

  protected stateText = jobStateText;

  protected load(): void {
    void this.list.start(this.api.jobs({ kind: this.kinds(), state: this.stateFilter() }));
  }

  protected toggleState(state: JobState, checked: boolean): void {
    this.stateDraft.update((list) =>
      checked ? [...list, state] : list.filter((s) => s !== state),
    );
  }

  protected apply(event: Event): void {
    event.preventDefault();
    const kinds = this.kindDraft()
      .split(',')
      .map((k) => k.trim())
      .filter((k) => k !== '');
    void this.router.navigate([], {
      queryParams: {
        kind: kinds.length > 0 ? kinds.join(',') : null,
        state: this.stateDraft().length > 0 ? [...this.stateDraft()] : null,
      },
      queryParamsHandling: 'merge',
    });
  }

  protected async cancel(job: Job): Promise<void> {
    try {
      const updated = await this.api.cancel(job.id);
      this.list.replace((j) => j.id === job.id, updated);
      this.notifier.status($localize`:@@jobs.cancelling:Job ${job.kind}:kind: is being cancelled.`);
    } catch (e) {
      const error = asApiError(e);
      this.notifier.alert(describeProblem(error).text);
      this.load();
    }
  }
}
