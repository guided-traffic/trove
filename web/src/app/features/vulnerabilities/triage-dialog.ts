import { Component, computed, inject, signal } from '@angular/core';
import { DIALOG_DATA, DialogClose, DialogRef, DialogTitle } from '../../shared/ui/dialog';
import { newIdempotencyKey } from '../../api/client';
import { ApiError } from '../../api/problem';
import { Versioned } from '../../api/repositories-api';
import {
  DISMISSAL_REASONS,
  VulnerabilitiesApi,
  VulnerabilityDismissalReason,
  VulnerabilityFindingDetail,
  VulnerabilityFindingPatch,
  VulnerabilitySetStatusResult,
  VulnerabilityStatus,
} from '../../api/vulnerabilities-api';
import { describeProblem } from '../../core/http/problem-messages';
import { TextField, SelectField } from '../../shared/access/fields';
import { fieldErrorAt } from '../../shared/field-errors';
import { ErrorNotice } from '../../shared/error-notice';
import { asApiError } from '../../shared/paged-resource';
import { dismissalLabel, statusLabel } from './vuln-labels';

/** What a triage dialog acts on: a finding with the label the user knows it by. */
export interface TriageTarget {
  readonly id: string;
  /** `package@version in repository`, shown in the confirmation and in the per-row results. */
  readonly label: string;
  readonly repository: string;
}

export interface TriageDialogData {
  /** `single` changes one finding with its `ETag`; `bulk` posts the ids to the set-status operation. */
  readonly mode: 'single' | 'bulk';
  readonly targets: readonly TriageTarget[];
  /** The status the dialog opens with. */
  readonly status: VulnerabilityStatus;
  /** The statuses a bulk dialog offers; a single dialog has its status fixed by the action that opened it. */
  readonly choices?: readonly VulnerabilityStatus[];
  /** The `ETag` of the finding a single dialog changes. */
  readonly etag?: string;
}

export interface TriageDialogResult {
  readonly detail?: Versioned<VulnerabilityFindingDetail>;
  readonly bulk?: VulnerabilitySetStatusResult;
}

/**
 * The dialog of a status change (R-VULN-022): one finding with its `ETag` (`PATCH`) or several at once (`POST …/set-status`
 * with an idempotency key that lives as long as the dialog, R-UI-042). `dismissed` asks for the reason, which the API
 * requires; `blocked` says that every client of the repository, and of the virtual repositories that contain it, will be
 * refused the version. A bulk change reports one result per row and stays open until the user closes it, so that a
 * `finding_not_confirmed` of one row is not lost.
 */
@Component({
  selector: 'trove-triage-dialog',
  imports: [DialogTitle, DialogClose, ErrorNotice, SelectField, TextField],
  template: `
    <h2 troveDialogTitle>{{ title() }}</h2>
    @if (results(); as r) {
      <div class="dialog-body">
        <p role="status">{{ summary() }}</p>
        <table class="data bulk-results">
          <caption class="visually-hidden" i18n="@@vuln.triage.results-caption">Result per finding</caption>
          <thead>
            <tr>
              <th scope="col" i18n="@@vuln.triage.col-finding">Finding</th>
              <th scope="col" i18n="@@vuln.triage.col-result">Result</th>
            </tr>
          </thead>
          <tbody>
            @for (row of r.results; track row.id) {
              <tr>
                <th scope="row" translate="no">{{ labelOf(row.id) }}</th>
                <td>
                  @if (row.outcome === 'failed') {
                    <span class="material-icons inline bad" aria-hidden="true">error</span>
                    {{ failure(row.code, row.status) }}
                  } @else if (row.outcome === 'changed') {
                    <span class="material-icons inline ok" aria-hidden="true">check_circle</span>
                    <ng-container i18n="@@vuln.triage.changed">Changed</ng-container>
                  } @else {
                    <ng-container i18n="@@vuln.triage.unchanged">Unchanged, it had this status</ng-container>
                  }
                </td>
              </tr>
            }
          </tbody>
        </table>
      </div>
      <div class="dialog-actions">
        <button class="btn btn-confirm" type="button" (click)="finish()" i18n="@@vuln.triage.close">Close</button>
      </div>
    } @else {
      <form (submit)="submit($event)" novalidate>
        <div class="dialog-body">
          @if (error(); as e) {
            <trove-error-notice [error]="e" />
          }
          @if (data.mode === 'single') {
            <p translate="no">{{ data.targets[0]?.label }}</p>
          } @else {
            <p>{{ selectedText() }}</p>
          }
          @if (data.mode === 'bulk') {
            <trove-select-field
              i18n-label="@@vuln.triage.status"
              label="New status"
              name="status"
              [options]="statusOptions()"
              [value]="status()"
              (valueChange)="status.set($any($event))"
            />
          }
          @if (status() === 'blocked') {
            <p class="banner-warning" role="note">
              <span class="material-icons inline warn" aria-hidden="true">block</span>
              <span>{{ blockWarning() }}</span>
            </p>
          }
          @if (status() === 'dismissed') {
            <trove-select-field
              i18n-label="@@vuln.triage.reason"
              label="Reason"
              name="dismissalReason"
              [options]="reasonOptions()"
              [value]="reason()"
              [error]="reasonError()"
              (valueChange)="reason.set($any($event))"
            />
          }
          <trove-text-field
            width="full"
            i18n-label="@@vuln.triage.comment"
            label="Comment"
            name="comment"
            type="textarea"
            [(value)]="comment"
            [error]="commentError()"
            [hint]="commentHint"
          />
        </div>
        <div class="dialog-actions">
          <button class="btn btn-tertiary" type="button" troveDialogClose i18n="@@vuln.triage.cancel">Cancel</button>
          <button class="btn btn-confirm" type="submit" [disabled]="busy()">{{ submitLabel() }}</button>
        </div>
      </form>
    }
  `,
  styles: `
    .bulk-results {
      inline-size: 100%;
      border-collapse: collapse;
    }
    .bulk-results th,
    .bulk-results td {
      padding: var(--trove-space-2);
      text-align: start;
      border-block-end: 1px solid var(--trove-border);
    }
  `,
})
export class TriageDialog {
  protected readonly data = inject<TriageDialogData>(DIALOG_DATA);
  private readonly api = inject(VulnerabilitiesApi);
  private readonly ref = inject<DialogRef<TriageDialogResult, TriageDialog>>(DialogRef);
  /** One key for the dialog's whole life (R-UI-042): a repeated submit is the same request. */
  private readonly key = newIdempotencyKey();

  protected readonly status = signal<VulnerabilityStatus>(this.data.status);
  protected readonly reason = signal<VulnerabilityDismissalReason | ''>('');
  protected readonly comment = signal('');
  protected readonly busy = signal(false);
  protected readonly error = signal<ApiError | null>(null);
  protected readonly results = signal<VulnerabilitySetStatusResult | null>(null);
  protected readonly missingReason = signal(false);

  protected readonly commentHint = $localize`:@@vuln.triage.comment-hint:Optional, at most 4,000 characters. The comment is kept in the history of the finding.`;

  protected readonly statusOptions = computed(() =>
    (this.data.choices ?? [this.data.status]).map((s) => ({ value: s, label: statusLabel(s) })),
  );
  protected readonly reasonOptions = computed(() => [
    { value: '', label: $localize`:@@vuln.triage.reason-choose:Choose a reason` },
    ...DISMISSAL_REASONS.map((r) => ({ value: r, label: dismissalLabel(r) })),
  ]);
  protected readonly reasonError = computed(() =>
    this.missingReason() && this.reason() === ''
      ? $localize`:@@vuln.triage.reason-required:A dismissal needs a reason.`
      : fieldErrorAt(this.error(), '/dismissalReason'),
  );
  protected readonly commentError = computed(() =>
    this.comment().length > 4000
      ? $localize`:@@vuln.triage.comment-long:The comment is longer than 4,000 characters.`
      : fieldErrorAt(this.error(), '/comment'),
  );

  protected title(): string {
    if (this.data.mode === 'bulk') return $localize`:@@vuln.triage.title-bulk:Change the status of the selected findings`;
    switch (this.status()) {
      case 'blocked':
        return $localize`:@@vuln.triage.title-block:Block downloads`;
      case 'dismissed':
        return $localize`:@@vuln.triage.title-dismiss:Dismiss the finding`;
      case 'confirmed':
        return $localize`:@@vuln.triage.title-confirm:Confirm the finding`;
      default:
        return $localize`:@@vuln.triage.title-reopen:Mark the finding as needing triage`;
    }
  }

  protected submitLabel(): string {
    if (this.status() === 'blocked') return $localize`:@@vuln.triage.submit-block:Block downloads`;
    if (this.status() === 'dismissed') return $localize`:@@vuln.triage.submit-dismiss:Dismiss`;
    return $localize`:@@vuln.triage.submit:Save`;
  }

  protected selectedText(): string {
    const n = this.data.targets.length;
    return n === 1
      ? $localize`:@@vuln.triage.selected-one:One finding is selected.`
      : $localize`:@@vuln.triage.selected-many:${n}:count: findings are selected.`;
  }

  /** The warning before a block is set (R-VULN-022): who is refused. */
  protected blockWarning(): string {
    const repositories = [...new Set(this.data.targets.map((t) => t.repository))].join(', ');
    return this.data.mode === 'single'
      ? $localize`:@@vuln.triage.block-warning:Every client of ${repositories}:repositories:, and of the virtual repositories that contain it, will be refused this version. Lift the block to allow the downloads again.`
      : $localize`:@@vuln.triage.block-warning-bulk:Every client of ${repositories}:repositories:, and of the virtual repositories that contain them, will be refused the versions of the findings that become blocked. A finding that is not confirmed cannot be blocked and is reported below.`;
  }

  protected labelOf(id: string): string {
    return this.data.targets.find((t) => t.id === id)?.label ?? id;
  }

  protected failure(code: string | null, status: number): string {
    if (code) return describeProblem(new ApiError(status, { code }, null)).text;
    return `HTTP ${status}`;
  }

  protected summary(): string {
    const r = this.results()?.results ?? [];
    const changed = r.filter((x) => x.outcome === 'changed').length;
    const failed = r.filter((x) => x.outcome === 'failed').length;
    return $localize`:@@vuln.triage.summary:${changed}:changed: changed, ${failed}:failed: failed, ${r.length - changed - failed}:unchanged: unchanged.`;
  }

  protected async submit(event: Event): Promise<void> {
    event.preventDefault();
    if (this.busy() || this.commentError()) return;
    if (this.status() === 'dismissed' && this.reason() === '') {
      this.missingReason.set(true);
      return;
    }
    this.busy.set(true);
    this.error.set(null);
    const comment = this.comment().trim();
    const reason = this.status() === 'dismissed' && this.reason() !== '' ? (this.reason() as VulnerabilityDismissalReason) : undefined;
    try {
      if (this.data.mode === 'single') {
        const patch: VulnerabilityFindingPatch = {
          status: this.status(),
          ...(reason ? { dismissalReason: reason } : {}),
          ...(comment ? { comment } : {}),
        };
        const detail = await this.api.triage(this.data.targets[0]!.id, patch, this.data.etag ?? '');
        this.ref.close({ detail });
      } else {
        const bulk = await this.api.setStatus(
          {
            ids: this.data.targets.map((t) => t.id),
            status: this.status(),
            ...(reason ? { dismissalReason: reason } : {}),
            ...(comment ? { comment } : {}),
          },
          this.key,
        );
        this.results.set(bulk);
      }
    } catch (e) {
      this.error.set(asApiError(e));
    } finally {
      this.busy.set(false);
    }
  }

  protected finish(): void {
    this.ref.close({ bulk: this.results() ?? undefined });
  }
}
