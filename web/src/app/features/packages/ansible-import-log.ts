import { DatePipe } from '@angular/common';
import { Component, effect, inject, input, untracked } from '@angular/core';
import { AnsibleApi, AnsibleImportTask } from '../../api/ansible-api';
import { ErrorNotice } from '../../shared/error-notice';
import { PagedResource } from '../../shared/paged-resource';
import { importStateText } from '../repositories/ansible-import-tasks';

/**
 * The Import log tab of a version held by a local repository (R-UI-063): the import tasks of that version, newest first, each with its
 * state as a badge, its time and its messages as text, the warnings of the generator among them (R-ANS-034). It is the task of
 * R-ANS-009, readable by whoever reads the repository, and not wiki content.
 */
@Component({
  selector: 'trove-ansible-import-log',
  imports: [DatePipe, ErrorNotice],
  template: `
    @if (list.error(); as e) {
      <trove-error-notice [error]="e" (retry)="load()" />
    } @else if (!list.loaded()) {
      <p role="status" i18n="@@pkg.loading">Loading.</p>
    } @else if (list.items().length === 0) {
      <p class="empty" data-testid="import-log-empty" i18n="@@docs.import-log.empty">
        No import task of this version is kept. A task is deleted after the retention period; the version stays.
      </p>
    } @else {
      @for (task of list.items(); track task.id) {
        <section class="task" [attr.data-state]="task.state" [attr.aria-label]="label(task)">
          <header class="task-head">
            <span class="badge" [class.ok]="task.state === 'completed'" [class.bad]="task.state === 'failed'">
              <span class="material-icons inline" aria-hidden="true">{{ task.state === 'completed' ? 'check_circle' : 'error' }}</span>
              {{ stateText(task.state) }}
            </span>
            <time [attr.datetime]="task.createdAt">{{ task.createdAt | date: 'medium' }}</time>
          </header>
          @if (task.error; as error) {
            <p class="reason" data-testid="import-log-error">{{ error.description }}</p>
          }
          @if (task.messages.length > 0) {
            <ol class="messages">
              @for (m of task.messages; track $index) {
                <li><span class="level" translate="no">{{ m.level }}</span> {{ m.message }}</li>
              }
            </ol>
          }
        </section>
      }
      @if (list.hasMore()) {
        <p>
          <button type="button" class="btn btn-default" [disabled]="list.loading()" (click)="list.more()" i18n="@@list.load-more">
            Load more
          </button>
        </p>
      }
    }
  `,
  styles: `
    .task {
      margin-block-end: var(--trove-space-4);
      padding-block-end: var(--trove-space-3);
      border-block-end: 1px solid var(--trove-border);
    }
    .task-head {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: var(--trove-space-3);
    }
    .reason {
      margin-block: var(--trove-space-2) 0;
    }
    .messages {
      margin: var(--trove-space-2) 0 0;
      padding-inline-start: var(--trove-space-6);
    }
    .level {
      font-weight: 600;
    }
  `,
})
export class AnsibleImportLog {
  private readonly api = inject(AnsibleApi);

  /** The qualified name `<project>/<key>` of the local repository that holds the version. */
  readonly repository = input.required<string>();
  /** The collection, `<namespace>.<name>`. */
  readonly collection = input.required<string>();
  readonly version = input.required<string>();

  protected readonly list = new PagedResource<AnsibleImportTask>();
  protected readonly stateText = importStateText;

  constructor() {
    effect(() => {
      this.repository();
      this.collection();
      this.version();
      untracked(() => this.load());
    });
  }

  protected label(task: AnsibleImportTask): string {
    const collection = `${task.namespace}.${task.name}`;
    return $localize`:@@docs.import-log.task:Import of ${collection}:collection: ${task.version}:version:`;
  }

  protected load(): void {
    void this.list.start(
      this.api.importTasks(this.repository(), { package: this.collection(), version: this.version() }),
    );
  }
}
