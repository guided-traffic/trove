import { DatePipe } from '@angular/common';
import { Component, computed, inject, input, output } from '@angular/core';
import { Job, jobActive, jobCancellable } from '../../api/jobs-api';
import { SessionStore } from '../../core/session-store';
import { jobStateText } from '../../shared/job-progress';
import { DataTable, TableColumn } from '../../shared/ui/data-table';

/**
 * A table of jobs (R-UI-038): kind, state with icon and text, start, progress, replica and result.
 * "Cancel" is offered only for `queued` and `running` jobs, and only to a holder of
 * `settings:write` or the principal that started the job (R-API-015).
 */
@Component({
  selector: 'trove-job-table',
  imports: [DataTable, DatePipe],
  styles: `
    /* A replica name and a result are read in full: they wrap in their column, at any character where they must. */
    .long {
      overflow-wrap: anywhere;
    }
  `,
  template: `
    <!-- The shared data table: a column budget and stacked rows below 48 rem, so the screen never scrolls sideways at 390 px. -->
    <trove-data-table [caption]="caption()" [columns]="columns">
      @for (j of jobs(); track j.id) {
        <tr>
          <th scope="row" translate="no">{{ j.kind }}</th>
          <td>
            @switch (j.state) {
              @case ('succeeded') {
                <span class="material-icons inline ok" aria-hidden="true">check_circle</span>
              }
              @case ('failed') {
                <span class="material-icons inline bad" aria-hidden="true">error</span>
              }
              @case ('cancelled') {
                <span class="material-icons inline" aria-hidden="true">cancel</span>
              }
              @default {
                <span class="material-icons inline" aria-hidden="true">hourglass_empty</span>
              }
            }
            {{ stateText(j) }}
          </td>
          <td>
            <time [attr.datetime]="j.enqueuedAt">{{ j.enqueuedAt | date: 'medium' }}</time>
          </td>
          <td translate="no">
            {{ progressText(j) }}
          </td>
          <td translate="no" class="wrap long">{{ j.replica ?? '–' }}</td>
          <td class="long">
            @if (j.error; as err) {
              <span translate="no">{{ err.code }}</span>
              @if (err.detail) {
                <span class="error-detail" translate="no">{{ err.detail }}</span>
                <span class="error-detail" i18n="@@jobs.attempt">Attempt {{ attemptOf(j) }} of {{ j.maxAttempts }}</span>
              }
            } @else {
              <span translate="no">{{ summary(j) }}</span>
            }
          </td>
          <td>
            @if (canCancel(j)) {
              <button class="btn btn-tertiary"
                type="button"
                [attr.aria-label]="cancelLabel(j)"
                (click)="cancelRequested.emit(j)"
                i18n="@@jobs.cancel"
              >
                Cancel
              </button>
            }
          </td>
        </tr>
      }
    </trove-data-table>
  `,
})
export class JobTable {
  private readonly session = inject(SessionStore);

  readonly jobs = input.required<readonly Job[]>();
  readonly caption = input.required<string>();
  readonly cancelRequested = output<Job>();

  protected readonly columns: readonly TableColumn[] = [
    { id: 'kind', label: $localize`:@@jobs.col.kind:Kind`, width: '8rem' },
    { id: 'state', label: $localize`:@@jobs.col.state:State`, width: '6rem' },
    { id: 'enqueued', label: $localize`:@@jobs.col.enqueued:Enqueued`, width: '8rem' },
    { id: 'progress', label: $localize`:@@jobs.col.progress:Progress`, width: '4rem' },
    { id: 'replica', label: $localize`:@@jobs.col.replica:Replica`, width: '7rem' },
    { id: 'result', label: $localize`:@@jobs.col.result:Result` },
    { id: 'actions', label: $localize`:@@repo.col.actions:Actions`, hideLabel: true, width: '5rem' },
  ];

  private readonly self = computed(() => this.session.whoami()?.principal.id ?? null);

  protected stateText(job: Job): string {
    return jobStateText(job.state);
  }

  protected progressText(job: Job): string {
    const p = job.progress;
    if (!p) return '–';
    return `${p.done}${p.total === null ? '' : `/${p.total}`} ${p.unit ?? ''}`.trim();
  }

  protected canCancel(job: Job): boolean {
    if (!jobCancellable(job.state)) return false;
    return (
      this.session.has('settings:write') ||
      (job.triggeredBy !== null && job.triggeredBy === this.self())
    );
  }

  protected cancelLabel(job: Job): string {
    return $localize`:@@jobs.cancel.label:Cancel job ${job.kind}:kind:`;
  }

  /** The attempt a job's error belongs to: the error's own, else the job's (Q-UI-172). */
  protected attemptOf(job: Job): number {
    return job.error?.attempt ?? job.attempt;
  }

  /** A compact summary of a result; the full object is in the API. A list shows its length, an object its JSON. */
  protected summary(job: Job): string {
    if (job.result)
      return Object.entries(job.result)
        .map(([k, v]) => `${k}: ${resultValue(v)}`)
        .join(', ');
    return jobActive(job.state) ? '' : '–';
  }
}

function resultValue(v: unknown): string {
  if (Array.isArray(v)) return String(v.length);
  return v !== null && typeof v === 'object' ? JSON.stringify(v) : String(v);
}
