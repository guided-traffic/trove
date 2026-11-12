import { DatePipe } from '@angular/common';
import { Component, effect, inject, input, signal, untracked } from '@angular/core';
import { AnsibleApi, AnsibleTombstone } from '../../api/ansible-api';
import { ApiError } from '../../api/problem';
import { Notifier } from '../../core/notifier';
import { Confirm } from '../../shared/confirm-dialog';
import { ErrorNotice } from '../../shared/error-notice';
import { PagedResource, asApiError } from '../../shared/paged-resource';
import { DataTable, TableColumn } from '../../shared/ui/data-table';

/**
 * The tombstones of a local Ansible Galaxy repository (R-ANS-012, R-ANS-026): the versions that were deleted and whose numbers are
 * not accepted again, so that nobody who has seen a version meets other bytes under it. A person who may manage the repository clears
 * one after a confirmation; the number can then be published again, with other bytes, and the clearing is audited.
 */
@Component({
  selector: 'trove-ansible-tombstones',
  imports: [DatePipe, DataTable, ErrorNotice],
  template: `
    <section class="tombstones" aria-labelledby="ansible-tombstones-heading">
      <h2 id="ansible-tombstones-heading" class="visually-hidden" i18n="@@ansible.tombstones.heading">Tombstones</h2>
      <p class="hint" i18n="@@ansible.tombstones.intro">
        A deleted version leaves a tombstone, and its number cannot be published again. Clear the tombstone to allow a new publish of that
        version number with other bytes.
      </p>
      <trove-data-table
        [caption]="caption"
        [columns]="columns"
        [loaded]="list.loaded()"
        [loading]="list.loading()"
        [error]="list.error()"
        [empty]="list.loaded() && list.items().length === 0"
        [emptyText]="emptyText"
        [hasMore]="list.hasMore()"
        (more)="list.more()"
        (retry)="load()"
        data-testid="ansible-tombstones-table"
      >
        @for (t of list.items(); track t.name + '/' + t.version) {
          <tr>
            <th scope="row" translate="no">{{ t.name }}</th>
            <td translate="no">{{ t.version }}</td>
            <td><code translate="no" [attr.title]="t.sha256">{{ t.sha256.slice(0, 12) }}</code></td>
            <td><time [attr.datetime]="t.deletedAt">{{ t.deletedAt | date: 'medium' }}</time></td>
            <td translate="no">{{ t.deletedBy }}</td>
            <td>
              @if (canManage()) {
                <button class="btn btn-default" type="button" [disabled]="busy()" (click)="clear(t)" data-testid="ansible-tombstone-clear" i18n="@@ansible.tombstones.clear">
                  Clear
                </button>
              }
            </td>
          </tr>
        }
      </trove-data-table>
      @if (failure(); as f) {
        <trove-error-notice [error]="f" />
      }
    </section>
  `,
  styles: `
    .tombstones {
      margin-block-end: var(--trove-space-6);
    }
  `,
})
export class AnsibleTombstones {
  private readonly api = inject(AnsibleApi);
  private readonly confirm = inject(Confirm);
  private readonly notifier = inject(Notifier);

  /** The qualified name `<project>/<key>` of the repository. */
  readonly key = input.required<string>();
  /** Whether to offer the clearing (the repository's `manage`, which the server enforces). */
  readonly canManage = input(false);

  protected readonly list = new PagedResource<AnsibleTombstone>();
  protected readonly busy = signal(false);
  protected readonly failure = signal<ApiError | null>(null);
  protected readonly caption = $localize`:@@ansible.tombstones.caption:Tombstones of this repository`;
  protected readonly emptyText = $localize`:@@ansible.tombstones.empty:No version has been deleted, so no number is blocked.`;
  protected readonly columns: readonly TableColumn[] = [
    { id: 'collection', label: $localize`:@@ansible.tombstones.col.collection:Collection` },
    { id: 'version', label: $localize`:@@ansible.tombstones.col.version:Version` },
    {
      id: 'sha256',
      label: $localize`:@@ansible.tombstones.col.sha256:SHA-256`,
      help: $localize`:@@ansible.tombstones.help.sha256:The first digits of the SHA-256 of the archive that was deleted. Hover to read all of it.`,
    },
    { id: 'deleted', label: $localize`:@@ansible.tombstones.col.deleted:Deleted` },
    { id: 'by', label: $localize`:@@ansible.tombstones.col.by:Deleted by` },
    { id: 'actions', label: $localize`:@@ansible.tombstones.col.actions:Actions` },
  ];

  constructor() {
    effect(() => {
      this.key();
      untracked(() => this.load());
    });
  }

  protected load(): void {
    void this.list.start(this.api.tombstones(this.key()));
  }

  protected async clear(t: AnsibleTombstone): Promise<void> {
    if (this.busy()) return;
    const answer = await this.confirm.ask({
      title: $localize`:@@ansible.tombstones.clear.title:Clear the tombstone of ${t.name}:collection: ${t.version}:version:`,
      lines: [
        $localize`:@@ansible.tombstones.clear.line:The number can then be published again with other bytes. Clients and caches that have seen the deleted version may still hold the old archive.`,
      ],
      confirmLabel: $localize`:@@ansible.tombstones.clear.confirm:Clear tombstone`,
    });
    if (!answer) return;
    this.busy.set(true);
    this.failure.set(null);
    try {
      await this.api.clearTombstone(this.key(), t.name, t.version);
      this.notifier.status($localize`:@@ansible.tombstones.cleared:The tombstone was cleared.`);
      await this.list.reload();
    } catch (e) {
      this.failure.set(asApiError(e));
    } finally {
      this.busy.set(false);
    }
  }
}
