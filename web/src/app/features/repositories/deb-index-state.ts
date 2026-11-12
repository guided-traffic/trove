import { DatePipe } from '@angular/common';
import { Component, DestroyRef, computed, effect, inject, input, signal, untracked } from '@angular/core';
import { RouterLink } from '@angular/router';
import { newIdempotencyKey } from '../../api/client';
import { DebApi, DebIndexState, DebSuiteState } from '../../api/deb-api';
import { JobReference } from '../../api/jobs-api';
import { ApiError } from '../../api/problem';
import { Notifier } from '../../core/notifier';
import { Confirm } from '../../shared/confirm-dialog';
import { ErrorNotice } from '../../shared/error-notice';
import { asApiError } from '../../shared/paged-resource';
import { DataTable, TableColumn } from '../../shared/ui/data-table';

/** The state is read again this often while a suite waits for its build (R-DEB-014). */
export const DEB_INDEX_POLL_INTERVAL_MS = 3_000;

/** The words of a suite state; the colour is a second signal, never the only one (R-UI-044). */
export function suiteStateText(state: DebSuiteState['state']): string {
  switch (state) {
    case 'current':
      return $localize`:@@deb.index.state.current:Current`;
    case 'failed':
      return $localize`:@@deb.index.state.failed:Failed`;
    default:
      return $localize`:@@deb.index.state.pending:Waiting for a build`;
  }
}

/**
 * Why the last build of a suite failed, in words (R-DEB-014, R-DEB-016, R-DEB-029). The API sends a code, with the member's name
 * behind a colon for the member codes of a virtual repository; a code the panel does not know is shown as it is.
 */
export function failureReasonText(reason: string): string {
  const at = reason.indexOf(':');
  const code = (at < 0 ? reason : reason.slice(0, at)).trim();
  const detail = at < 0 ? '' : reason.slice(at + 1).trim();
  switch (code) {
    case 'no_signing_key':
      return $localize`:@@deb.index.reason.no-key:The repository has no primary signing key, so nothing can be signed. Reinstate a withheld key, or create a next key and promote it.`;
    case 'member_unverified':
      return $localize`:@@deb.index.reason.unverified:A member's release could not be verified against its upstream keys: ${detail}:member:.`;
    case 'member_expired':
      return $localize`:@@deb.index.reason.expired:A member's release has expired: ${detail}:member:.`;
    case 'member_rollback':
      return $localize`:@@deb.index.reason.rollback:A member's release is older than the one already merged: ${detail}:member:. Take the member out and add it back to accept the older release.`;
    case 'deb_members_unreadable':
      return $localize`:@@deb.index.reason.unreadable:A member of this virtual repository cannot be read with your permissions: ${detail}:member:.`;
    default:
      return reason;
  }
}

/** Builds a suite is behind its changes by. */
export function behindBy(suite: DebSuiteState): number {
  return Math.max(0, suite.changeSeq - suite.publishedSeq);
}

/**
 * The index state of a local or virtual Debian repository (R-DEB-014, R-DEB-029): for each suite the change number an upload
 * answered with, the number the published generation was built from, the generation, its release date, the keys that signed it and,
 * for a failed build, the reason in words (the member reasons of a virtual repository included). While a suite waits for its build the
 * state is read again every few seconds; the polling ends when the panel is left. **Rebuild all indexes** requests the full build of
 * `POST …/reindex` and answers the refusals in words.
 */
@Component({
  selector: 'trove-deb-index-state',
  imports: [DatePipe, RouterLink, DataTable, ErrorNotice],
  template: `
    <section class="index" aria-labelledby="deb-index-heading">
      <h2 id="deb-index-heading" class="visually-hidden" i18n="@@deb.index.heading">Index state</h2>
      <p class="hint" i18n="@@deb.index.intro">
        The indexes of each suite are built from the stored packages a moment after a change. A client that must see an upload at once
        waits for the change number the upload answered with.
      </p>

      @if (failure(); as f) {
        <trove-error-notice [error]="f" (retry)="load()" />
      } @else if (state(); as s) {
        <trove-data-table [caption]="caption" [columns]="columns" [empty]="s.suites.length === 0" [emptyText]="emptyText" data-testid="deb-index-table">
          @for (suite of s.suites; track suite.distribution) {
            <tr [attr.data-state]="suite.state">
              <th scope="row" translate="no">{{ suite.distribution }}</th>
              <td>
                <span class="badge" [class.ok]="suite.state === 'current'" [class.bad]="suite.state === 'failed'" [class.info]="suite.state === 'pending'">
                  <span class="material-icons inline" aria-hidden="true">{{ icon(suite) }}</span>
                  {{ stateText(suite.state) }}
                </span>
                @if (suite.state === 'failed' && suite.reason; as reason) {
                  <p class="reason" data-testid="deb-index-reason">{{ reasonText(reason) }}</p>
                }
              </td>
              <td class="num">{{ suite.changeSeq }}</td>
              <td class="num">
                {{ suite.publishedSeq }}
                @if (behind(suite) > 0) {
                  <span class="hint">(<ng-container i18n="@@deb.index.behind">behind by</ng-container>&ngsp;{{ behind(suite) }})</span>
                }
              </td>
              <td class="num">{{ suite.generation }}</td>
              <td>
                @if (suite.releaseDate; as at) {
                  <time [attr.datetime]="at">{{ at | date: 'medium' }}</time>
                } @else {
                  –
                }
              </td>
              <td>
                @for (f of suite.signedBy ?? []; track f) {
                  <code translate="no" [attr.title]="f">{{ short(f) }}</code>
                } @empty {
                  –
                }
              </td>
            </tr>
          }
        </trove-data-table>

        @if (canManage()) {
          <div class="buttons">
            <button class="btn btn-default" type="button" [disabled]="requesting()" (click)="reindex()" data-testid="deb-reindex" i18n="@@deb.index.reindex">
              Rebuild all indexes
            </button>
          </div>
          <p class="hint" i18n="@@deb.index.reindex-hint">
            Rebuilds every suite from the stored packages, drops suites without packages and removes rows of items that no longer exist.
          </p>
        }
        @if (notice(); as n) {
          <p class="hint" role="status" data-testid="deb-index-notice">
            @if (n.kind === 'started') {
              <ng-container i18n="@@deb.index.started">The rebuild is queued.</ng-container>
            } @else {
              <ng-container i18n="@@deb.index.running">A rebuild is already queued or running.</ng-container>
            }
            @if (n.job; as job) {
              &ngsp;<a routerLink="/admin/system/jobs"><ng-container i18n="@@deb.index.job-link">Open the jobs</ng-container></a>
              (<code translate="no">{{ job.id }}</code>)
            }
          </p>
        }
        @if (actionFailure(); as f) {
          <trove-error-notice [error]="f" />
        }
      } @else {
        <p role="status" i18n="@@deb.index.loading">Loading the index state.</p>
      }
    </section>
  `,
  styles: `
    .index {
      margin-block-end: var(--trove-space-6);
    }
    .reason {
      margin: var(--trove-space-1) 0 0;
      max-width: 40rem;
    }
    .buttons {
      display: flex;
      gap: var(--trove-space-3);
      margin-block: var(--trove-space-3);
    }
  `,
})
export class DebIndexStatePanel {
  private readonly api = inject(DebApi);
  private readonly confirm = inject(Confirm);
  private readonly notifier = inject(Notifier);
  private readonly destroyed = inject(DestroyRef);

  /** The qualified name `<project>/<key>` of the repository. */
  readonly key = input.required<string>();
  /** Whether to offer the rebuild (the repository's `manage`, which the server enforces). */
  readonly canManage = input(false);
  readonly intervalMs = input(DEB_INDEX_POLL_INTERVAL_MS);

  protected readonly state = signal<DebIndexState | null>(null);
  protected readonly failure = signal<ApiError | null>(null);
  protected readonly actionFailure = signal<ApiError | null>(null);
  protected readonly requesting = signal(false);
  protected readonly notice = signal<{ readonly kind: 'started' | 'running'; readonly job: JobReference | null } | null>(null);

  protected readonly caption = $localize`:@@deb.index.caption:Suites of this repository`;
  protected readonly emptyText = $localize`:@@deb.index.empty:No suite has packages yet. A suite appears with the first package uploaded to it.`;
  protected readonly columns: readonly TableColumn[] = [
    { id: 'suite', label: $localize`:@@deb.index.col.suite:Suite` },
    { id: 'state', label: $localize`:@@deb.index.col.state:State` },
    {
      id: 'change',
      label: $localize`:@@deb.index.col.change:Change number`,
      numeric: true,
      help: $localize`:@@deb.index.help.change:The number an upload answered with. It rises with every change of the suite.`,
    },
    {
      id: 'published',
      label: $localize`:@@deb.index.col.published:Published number`,
      numeric: true,
      help: $localize`:@@deb.index.help.published:The change number the published indexes were built from. The suite is current when it reaches the change number.`,
    },
    { id: 'generation', label: $localize`:@@deb.index.col.generation:Generation`, numeric: true },
    { id: 'release', label: $localize`:@@deb.index.col.release:Release date` },
    {
      id: 'signed',
      label: $localize`:@@deb.index.col.signed:Signed by`,
      help: $localize`:@@deb.index.help.signed:The fingerprints of the keys that signed the published release, shortened to their last sixteen digits.`,
    },
  ];

  protected readonly stateText = suiteStateText;
  protected readonly reasonText = failureReasonText;
  protected readonly behind = behindBy;
  protected readonly waiting = computed(() => (this.state()?.suites ?? []).some((s) => s.state === 'pending'));

  private generation = 0;
  private timer: ReturnType<typeof setTimeout> | undefined;

  constructor() {
    this.destroyed.onDestroy(() => this.stop());
    effect(() => {
      this.key();
      untracked(() => this.load());
    });
  }

  protected short(fingerprint: string): string {
    return fingerprint.slice(-16);
  }

  protected icon(suite: DebSuiteState): string {
    switch (suite.state) {
      case 'current':
        return 'check_circle';
      case 'failed':
        return 'error';
      default:
        return 'schedule';
    }
  }

  protected load(): void {
    this.stop();
    this.failure.set(null);
    void this.poll(++this.generation);
  }

  private stop(): void {
    this.generation++;
    clearTimeout(this.timer);
  }

  private async poll(run: number): Promise<void> {
    try {
      const state = await this.api.indexState(this.key());
      if (run !== this.generation) return;
      this.state.set(state);
      if (state.suites.some((s) => s.state === 'pending')) {
        this.timer = setTimeout(() => void this.poll(run), this.intervalMs());
      }
    } catch (e) {
      if (run === this.generation) this.failure.set(asApiError(e));
    }
  }

  protected async reindex(): Promise<void> {
    if (this.requesting()) return;
    const answer = await this.confirm.ask({
      title: $localize`:@@deb.index.reindex.title:Rebuild all indexes`,
      lines: [
        $localize`:@@deb.index.reindex.line:Every suite is built again from the stored packages. Suites without packages are dropped. Clients keep reading the published indexes until the new ones are ready.`,
      ],
      confirmLabel: $localize`:@@deb.index.reindex.confirm:Rebuild`,
    });
    if (!answer) return;
    this.requesting.set(true);
    this.notice.set(null);
    this.actionFailure.set(null);
    try {
      const job = await this.api.reindex(this.key(), newIdempotencyKey());
      this.notice.set({ kind: 'started', job });
      this.notifier.status($localize`:@@deb.index.reindex.done:The rebuild was requested.`);
    } catch (e) {
      const error = asApiError(e);
      if (error.code === 'reindex_running') {
        this.notice.set({ kind: 'running', job: (error.problem?.['job'] as JobReference | undefined) ?? null });
      } else {
        this.actionFailure.set(error);
      }
    } finally {
      this.requesting.set(false);
    }
    this.load();
  }
}
