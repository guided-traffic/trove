import { DatePipe } from '@angular/common';
import { Component, computed, inject, input, output } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { Job, jobActive, jobCancellable } from '../../api/jobs-api';
import { SessionStore } from '../../core/session-store';
import { jobStateText } from '../../shared/job-progress';

/**
 * A table of jobs (R-UI-038): kind, state with icon and text, start, progress, replica and result.
 * "Cancel" is offered only for `queued` and `running` jobs, and only to a holder of
 * `settings:write` or the principal that started the job (R-API-015).
 */
@Component({
  selector: 'trove-job-table',
  imports: [DatePipe, MatButtonModule],
  template: `
    <div class="scroll" tabindex="0" role="region" [attr.aria-label]="caption()">
      <table class="data">
        <caption class="visually-hidden">
          {{
            caption()
          }}
        </caption>
        <thead>
          <tr>
            <th scope="col" i18n="@@jobs.col.kind">Kind</th>
            <th scope="col" i18n="@@jobs.col.state">State</th>
            <th scope="col" i18n="@@jobs.col.enqueued">Enqueued</th>
            <th scope="col" i18n="@@jobs.col.progress">Progress</th>
            <th scope="col" i18n="@@jobs.col.replica">Replica</th>
            <th scope="col" i18n="@@jobs.col.result">Result</th>
            <th scope="col" i18n="@@repo.col.actions">Actions</th>
          </tr>
        </thead>
        <tbody>
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
              <td translate="no">{{ j.replica ?? '–' }}</td>
              <td translate="no">{{ summary(j) }}</td>
              <td>
                @if (canCancel(j)) {
                  <button
                    mat-button
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
        </tbody>
      </table>
    </div>
  `,
})
export class JobTable {
  private readonly session = inject(SessionStore);

  readonly jobs = input.required<readonly Job[]>();
  readonly caption = input.required<string>();
  readonly cancelRequested = output<Job>();

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

  /** A compact result or error summary; the full object is in the API. */
  protected summary(job: Job): string {
    if (job.error) {
      const code = job.error['code'];
      return typeof code === 'string' ? code : JSON.stringify(job.error);
    }
    if (job.result)
      return Object.entries(job.result)
        .map(([k, v]) => `${k}: ${String(v)}`)
        .join(', ');
    return jobActive(job.state) ? '' : '–';
  }
}
