import { DatePipe } from '@angular/common';
import { Component, computed, inject, resource, signal } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { newIdempotencyKey } from '../../api/client';
import { ApiError } from '../../api/problem';
import { JobsApi, Task } from '../../api/jobs-api';
import { describeProblem } from '../../core/http/problem-messages';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { ErrorNotice } from '../../shared/error-notice';
import { JobProgress, jobStateText } from '../../shared/job-progress';
import { asApiError, valueOf } from '../../shared/paged-resource';

/**
 * The scheduled tasks card (R-UI-038): schedule, next run, last outcome, and "Run now", which only
 * enqueues the run; a `worker` replica executes it. The `Idempotency-Key` is generated at the click
 * and kept until the run was accepted or refused (R-UI-042), so a retry cannot start it twice. A
 * run in progress shows its progress and reloads the list when it ends.
 */
@Component({
  selector: 'trove-tasks-card',
  imports: [DatePipe, MatButtonModule, ErrorNotice, JobProgress],
  template: `
    <section class="card" aria-labelledby="card-tasks">
      <div class="head">
        <h2 id="card-tasks" i18n="@@tasks.heading">Scheduled tasks</h2>
        <button mat-button type="button" (click)="tasks.reload()" i18n="@@tasks.refresh">
          Refresh
        </button>
      </div>
      @if (failure(); as f) {
        <trove-error-notice [error]="f" (retry)="tasks.reload()" />
      } @else if (valueOf(tasks); as list) {
        @if (list.length === 0) {
          <p class="empty" i18n="@@tasks.empty">No maintenance task is registered.</p>
        } @else {
          <div class="scroll" tabindex="0" role="region" aria-labelledby="card-tasks">
            <table class="data">
              <caption class="visually-hidden" i18n="@@tasks.caption">
                Maintenance tasks
              </caption>
              <thead>
                <tr>
                  <th scope="col" i18n="@@tasks.col.name">Task</th>
                  <th scope="col" i18n="@@tasks.col.schedule">Schedule (UTC)</th>
                  <th scope="col" i18n="@@tasks.col.next">Next run</th>
                  <th scope="col" i18n="@@tasks.col.last">Last run</th>
                  <th scope="col" i18n="@@repo.col.actions">Actions</th>
                </tr>
              </thead>
              <tbody>
                @for (t of list; track t.name) {
                  <tr>
                    <th scope="row" translate="no">{{ t.name }}</th>
                    <td translate="no">{{ t.schedule ?? '–' }}</td>
                    <td>
                      @if (t.nextRunAt; as at) {
                        <time [attr.datetime]="at">{{ at | date: 'medium' }}</time>
                      } @else if (t.schedule === null) {
                        <span i18n="@@tasks.manual">manual only</span>
                      } @else {
                        –
                      }
                    </td>
                    <td>
                      @if (t.lastRun; as r) {
                        {{ stateText(r.outcome) }}
                        @if (r.finishedAt; as at) {
                          <time [attr.datetime]="at"> {{ at | date: 'medium' }}</time>
                        }
                      } @else {
                        –
                      }
                    </td>
                    <td>
                      @if (t.running || active().get(t.name)) {
                        <span i18n="@@tasks.running">A run is in progress.</span>
                      }
                      @if (active().get(t.name); as id) {
                        <trove-job-progress
                          [jobId]="id"
                          [label]="runLabel(t.name)"
                          (finished)="completed(t)"
                        />
                      } @else if (canRun() && !t.running) {
                        <button
                          mat-stroked-button
                          type="button"
                          [disabled]="busy() === t.name"
                          [attr.aria-label]="runLabel(t.name)"
                          (click)="run(t)"
                          i18n="@@tasks.run"
                        >
                          Run now
                        </button>
                      }
                    </td>
                  </tr>
                }
              </tbody>
            </table>
          </div>
        }
        @if (runFailure(); as f) {
          <trove-error-notice [error]="f" />
        }
      } @else {
        <p role="status" i18n="@@system.loading">Loading.</p>
      }
    </section>
  `,
  styles: `
    .head {
      display: flex;
      align-items: center;
      justify-content: space-between;
    }
    .head h2 {
      margin: 0;
    }
  `,
})
export class TasksCard {
  protected readonly valueOf = valueOf;
  private readonly api = inject(JobsApi);
  private readonly session = inject(SessionStore);
  private readonly notifier = inject(Notifier);

  protected readonly tasks = resource({ loader: () => this.api.tasks() });
  protected readonly busy = signal<string | null>(null);
  protected readonly runFailure = signal<ApiError | null>(null);
  /** Jobs of runs started from this card, by task name. */
  protected readonly active = signal<ReadonlyMap<string, string>>(new Map());
  private readonly keys = new Map<string, string>();

  protected readonly canRun = computed(() => this.session.has('settings:write'));
  protected readonly failure = computed(() => {
    const e = this.tasks.error();
    return e ? asApiError(e) : null;
  });

  /** Reloads the list; the system screen calls this on its refresh. */
  reload(): void {
    this.tasks.reload();
  }

  protected stateText = jobStateText;

  protected runLabel(name: string): string {
    return $localize`:@@tasks.run.label:Run task ${name}:name: now`;
  }

  protected async run(task: Task): Promise<void> {
    if (this.busy() !== null) return;
    this.busy.set(task.name);
    this.runFailure.set(null);
    const key = this.keys.get(task.name) ?? newIdempotencyKey();
    this.keys.set(task.name, key);
    try {
      const job = await this.api.runTask(task.name, undefined, key);
      this.keys.delete(task.name);
      this.active.update((m) => new Map(m).set(task.name, job.id));
      this.notifier.status(
        $localize`:@@tasks.started:Task ${task.name}:name: queued. A worker replica runs it.`,
      );
    } catch (e) {
      const error = asApiError(e);
      // A refusal (a run in progress, a failed precondition) is final: the next click is a new run.
      if (error.status !== 0 && error.status !== 503) this.keys.delete(task.name);
      this.runFailure.set(error);
      this.notifier.alert(describeProblem(error).text);
    } finally {
      this.busy.set(null);
    }
  }

  protected completed(task: Task): void {
    this.active.update((m) => {
      const next = new Map(m);
      next.delete(task.name);
      return next;
    });
    this.tasks.reload();
  }
}
