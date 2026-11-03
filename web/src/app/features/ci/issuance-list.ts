import { DatePipe } from '@angular/common';
import { Component, computed, effect, inject, input, signal, untracked } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MAT_DIALOG_DATA, MatDialog, MatDialogModule } from '@angular/material/dialog';
import { CiApi, CiIssuance, CiIssuanceDetail, IssuanceFilters, CiRevokeRequest } from '../../api/ci-api';
import { collectAll } from '../../api/pagination';
import { ApiError } from '../../api/problem';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { SelectField, SelectOption, TextField } from '../../shared/access/fields';
import { Confirm } from '../../shared/confirm-dialog';
import { PagedResource, asApiError } from '../../shared/paged-resource';
import { DataTable, TableColumn } from '../../shared/ui/data-table';
import { FilterCategory, FilterPanel, FilterToggle } from '../../shared/ui/filter-panel';
import { ErrorNotice } from '../../shared/error-notice';

const pretty = (v: unknown): string => JSON.stringify(v, null, 2);

/** What the detail of one issued token opens with: the record from the list, and whether the usage can be read (R-CI-020). */
export interface IssuanceDetailData {
  readonly issuance: CiIssuance;
  /** The usage summary is an operation of the token administrators; a project's screen shows the record only. */
  readonly withUsage: boolean;
}

/**
 * One issued CI token (R-UI-035, R-CI-020): the verified job identity, the claims snapshot, the frozen
 * grants, the requested and granted scope and, for the token administrators, the usage summary from the
 * audit store. It holds no token and no hash: a token value is never shown again.
 */
@Component({
  selector: 'trove-ci-issuance-detail',
  imports: [DatePipe, MatButtonModule, MatDialogModule, ErrorNotice],
  template: `
    <h2 mat-dialog-title i18n="@@ci.issuance.title">Issued token</h2>
    <mat-dialog-content>
      @if (error(); as e) {
        <trove-error-notice [error]="e" />
      }
      <dl class="facts">
        <dt i18n="@@ci.issuance.state">State</dt>
        <dd>
          @if (data.issuance.revokedAt) {
            <span class="bad"><span class="material-icons" aria-hidden="true">block</span> <ng-container i18n="@@ci.issuance.revoked">Revoked</ng-container> {{ data.issuance.revokedAt | date: 'medium' }}</span>
          } @else if (data.issuance.active) {
            <span class="ok"><span class="material-icons" aria-hidden="true">check_circle</span> <ng-container i18n="@@ci.issuance.active">Active</ng-container></span>
          } @else {
            <span class="muted"><span class="material-icons" aria-hidden="true">schedule</span> <ng-container i18n="@@ci.issuance.expired">Expired</ng-container></span>
          }
        </dd>
        <dt i18n="@@ci.issuance.token-id">Token ID</dt>
        <dd><code translate="no">{{ data.issuance.tokenId }}</code></dd>
        <dt i18n="@@ci.issuance.issuer">Issuer</dt>
        <dd translate="no">{{ data.issuance.issuer }}</dd>
        <dt i18n="@@ci.issuance.mappings">Mappings</dt>
        <dd translate="no">{{ data.issuance.mappingNames.join(', ') }}</dd>
        <dt i18n="@@ci.issuance.project">Project</dt>
        <dd translate="no">{{ data.issuance.projectPath ?? '–' }} ({{ data.issuance.projectId }})</dd>
        <dt i18n="@@ci.issuance.job">Pipeline and job</dt>
        <dd translate="no">{{ data.issuance.pipelineId ?? '–' }} / {{ data.issuance.jobId }}</dd>
        <dt i18n="@@ci.issuance.ref">Ref</dt>
        <dd translate="no">{{ data.issuance.ref ?? '–' }} ({{ data.issuance.refType ?? '–' }}{{ data.issuance.refProtected ? ', protected' : '' }})</dd>
        <dt i18n="@@ci.issuance.user">Started by</dt>
        <dd translate="no">{{ data.issuance.userLogin ?? '–' }}</dd>
        <dt i18n="@@ci.issuance.issued">Issued</dt>
        <dd>{{ data.issuance.issuedAt | date: 'medium' }}</dd>
        <dt i18n="@@ci.issuance.expires">Expires</dt>
        <dd>{{ data.issuance.expiresAt | date: 'medium' }}</dd>
        <dt i18n="@@ci.issuance.requested-scope">Requested scope</dt>
        <dd translate="no">{{ data.issuance.requestedScope ?? '–' }}</dd>
        <dt i18n="@@ci.issuance.granted-scope">Granted scope</dt>
        <dd translate="no">{{ data.issuance.grantedScope }}{{ data.issuance.crossProject ? ' (cross-project)' : '' }}</dd>
        <dt i18n="@@ci.issuance.client">Client</dt>
        <dd translate="no">{{ data.issuance.clientIp ?? '–' }} {{ data.issuance.userAgent ?? '' }}</dd>
        @if (usage(); as u) {
          <dt i18n="@@ci.issuance.usage">Usage</dt>
          <dd>
            <ng-container i18n="@@ci.issuance.usage-text">{{ u.requests }} requests, last used</ng-container>
            {{ u.lastUsedAt ? (u.lastUsedAt | date: 'medium') : '–' }}
            @if (u.packagesDeployed.length > 0) {
              <div translate="no">{{ u.packagesDeployed.join(', ') }}</div>
            }
          </dd>
        }
      </dl>
      <h3 i18n="@@ci.issuance.grants">Grants of the token</h3>
      <pre translate="no">{{ grants }}</pre>
      <h3 i18n="@@ci.issuance.claims">Claims at the time of the exchange</h3>
      <pre translate="no">{{ claims }}</pre>
    </mat-dialog-content>
    <mat-dialog-actions align="end">
      <button mat-button type="button" mat-dialog-close i18n="@@ci.close">Close</button>
    </mat-dialog-actions>
  `,
  styleUrl: '../../shared/access/access.scss',
  styles: `
    pre {
      margin: 0 0 var(--trove-space-3);
      padding: var(--trove-space-2) var(--trove-space-3);
      overflow: auto;
      max-block-size: 14rem;
      border: 1px solid var(--trove-border);
      border-radius: var(--trove-radius-s);
      font-family: var(--trove-font-mono);
      font-size: 0.8125rem;
    }
    h3 {
      margin: var(--trove-space-3) 0 var(--trove-space-1);
      font: var(--mat-sys-title-small);
    }
  `,
})
export class IssuanceDetailDialog {
  protected readonly data = inject<IssuanceDetailData>(MAT_DIALOG_DATA);
  private readonly api = inject(CiApi);

  protected readonly usage = signal<CiIssuanceDetail['usage'] | null>(null);
  protected readonly error = signal<ApiError | null>(null);
  protected readonly grants = pretty(this.data.issuance.grants);
  protected readonly claims = pretty(this.data.issuance.claims);

  constructor() {
    if (this.data.withUsage) {
      this.api.getIssuance(this.data.issuance.tokenId).then(
        (d) => this.usage.set(d.usage),
        (e: unknown) => this.error.set(asApiError(e)),
      );
    }
  }
}

/**
 * The start of the local day of a `yyyy-mm-dd` date input as an instant, `plusDays` later: the table shows
 * local times, so the day filters are the bounds of the local day (the API's `to` is exclusive).
 */
function dayStart(value: string, plusDays = 0): string | undefined {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!m) return undefined;
  return new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]) + plusDays).toISOString();
}

/**
 * The issued CI tokens (R-UI-035, R-CI-020), of one issuer, of all, or of a project's mappings (Q-CI-26),
 * with the filters of the API in the filter panel, the detail of one record and, for the holder of
 * `tokens:revoke`, the bulk revoke of the tokens that match the applied filters: the confirmation counts
 * the active tokens it will revoke before anything happens. A project's list understands the issuer,
 * the job and the state only, and has no bulk revoke.
 */
@Component({
  selector: 'trove-ci-issuance-list',
  imports: [DatePipe, MatButtonModule, ErrorNotice, DataTable, FilterCategory, FilterPanel, FilterToggle, SelectField, TextField],
  template: `
    <div class="list-layout" [class.panel-open]="panelOpen()">
      <div class="list-main">
        <div class="list-toolbar">
          <trove-filter-toggle [(open)]="panelOpen" [activeCount]="activeCount()" [controls]="panel.panelId" />
          @if (canRevoke()) {
            <button mat-stroked-button type="button" [disabled]="!revocable() || busy()" aria-describedby="ci-revoke-note" (click)="revokeMatching()" i18n="@@ci.issuances.revoke-matching">
              Revoke matching tokens
            </button>
          }
        </div>
        @if (canRevoke() && !revocable()) {
          @if (unsupported().length > 0) {
            <p id="ci-revoke-note" class="muted" role="note">
              <ng-container i18n="@@ci.issuances.revoke-unsupported">The bulk revoke acts on the issuer, project ID, pipeline ID, job ID and mapping only. It would revoke more than the list shows while these filters are applied:</ng-container>
              {{ ' ' + unsupported().join(', ') }}.
            </p>
          } @else {
            <p id="ci-revoke-note" class="muted" role="note" i18n="@@ci.issuances.revoke-note">
              To revoke in bulk, filter by one issuer and by a project ID, a pipeline ID, a job ID or a mapping, then apply the filters.
            </p>
          }
        }
        @if (actionError(); as e) {
          <trove-error-notice [error]="e" />
        }
        @if (list.error(); as e) {
          <trove-error-notice [error]="e" (retry)="list.reload()" />
        }
        @if (list.error()?.status !== 501) {
          <trove-data-table
            [caption]="caption"
            [columns]="columns()"
            [loaded]="list.loaded()"
            [loading]="list.loading()"
            [empty]="list.items().length === 0"
            i18n-emptyText="@@ci.issuances.empty"
            emptyText="No CI token matches."
            [hasMore]="list.hasMore()"
            (more)="list.more()"
          >
            @for (i of list.items(); track i.tokenId) {
              <tr>
                <th scope="row">{{ i.issuedAt | date: 'short' }}</th>
                @if (!project()) {
                  <td translate="no">{{ i.issuer }}</td>
                }
                <td translate="no">{{ i.mappingNames.join(', ') }}</td>
                <td translate="no" class="wrap">{{ i.projectPath ?? i.projectId }}</td>
                <td translate="no">{{ i.pipelineId ?? '–' }} / {{ i.jobId }}</td>
                <td translate="no">{{ i.ref ?? '–' }}</td>
                <td>
                  @if (i.revokedAt) {
                    <span class="bad"><span class="material-icons" aria-hidden="true">block</span> <ng-container i18n="@@ci.issuance.revoked-short">Revoked</ng-container></span>
                  } @else if (i.active) {
                    <span class="ok"><span class="material-icons" aria-hidden="true">check_circle</span> <ng-container i18n="@@ci.issuance.active">Active</ng-container></span>
                  } @else {
                    <span class="muted"><span class="material-icons" aria-hidden="true">schedule</span> <ng-container i18n="@@ci.issuance.expired">Expired</ng-container></span>
                  }
                </td>
                <td class="actions">
                  <button mat-button type="button" (click)="open(i)" i18n="@@ci.issuances.details">Details</button>
                </td>
              </tr>
            }
          </trove-data-table>
        }
      </div>

      <trove-filter-panel #panel class="list-side" [(open)]="panelOpen" [activeCount]="activeCount()" (apply)="apply()" (clearAll)="clear()">
        @if (!fixedIssuer()) {
          <trove-filter-category i18n-label="@@ci.issuances.f.issuer" label="Issuer" [active]="!!issuerFilter().trim()" [expanded]="!!issuerFilter().trim()">
            <trove-text-field i18n-label="@@ci.issuances.f.issuer" label="Issuer" name="issuer" [(value)]="issuerFilter" />
          </trove-filter-category>
        }
        @if (!project()) {
          <trove-filter-category i18n-label="@@ci.issuances.f.project-id" label="GitLab project ID" [active]="!!projectId().trim()" [expanded]="!!projectId().trim()">
            <trove-text-field i18n-label="@@ci.issuances.f.project-id" label="GitLab project ID" name="projectId" [(value)]="projectId" />
          </trove-filter-category>
          <trove-filter-category i18n-label="@@ci.issuances.f.project-path" label="Project path" [active]="!!projectPath().trim()" [expanded]="!!projectPath().trim()">
            <trove-text-field i18n-label="@@ci.issuances.f.project-path" label="Project path" name="projectPath" [(value)]="projectPath" />
          </trove-filter-category>
          <trove-filter-category i18n-label="@@ci.issuances.f.pipeline" label="Pipeline ID" [active]="!!pipelineId().trim()" [expanded]="!!pipelineId().trim()">
            <trove-text-field i18n-label="@@ci.issuances.f.pipeline" label="Pipeline ID" name="pipelineId" [(value)]="pipelineId" />
          </trove-filter-category>
        }
        <trove-filter-category i18n-label="@@ci.issuances.f.job" label="Job ID" [active]="!!jobId().trim()" [expanded]="!!jobId().trim()">
          <trove-text-field i18n-label="@@ci.issuances.f.job" label="Job ID" name="jobId" [(value)]="jobId" />
        </trove-filter-category>
        @if (!project()) {
          <trove-filter-category i18n-label="@@ci.issuances.f.mapping" label="Mapping" [active]="!!mapping().trim()" [expanded]="!!mapping().trim()">
            <trove-text-field i18n-label="@@ci.issuances.f.mapping" label="Mapping" name="mapping" [(value)]="mapping" />
          </trove-filter-category>
        }
        <trove-filter-category i18n-label="@@ci.issuances.f.state" label="State" [active]="!!state()" [expanded]="!!state()">
          <trove-select-field i18n-label="@@ci.issuances.f.state" label="State" name="state" [options]="stateOptions" [(value)]="state" />
        </trove-filter-category>
        @if (!project()) {
          <trove-filter-category i18n-label="@@ci.issuances.f.from" label="Issued from" [active]="!!from()" [expanded]="!!from()">
            <trove-text-field i18n-label="@@ci.issuances.f.from" label="Issued from" name="from" type="date" [(value)]="from" />
          </trove-filter-category>
          <trove-filter-category i18n-label="@@ci.issuances.f.to" label="Issued until" [active]="!!to()" [expanded]="!!to()">
            <trove-text-field i18n-label="@@ci.issuances.f.to" label="Issued until" name="to" type="date" [(value)]="to" />
          </trove-filter-category>
        }
      </trove-filter-panel>
    </div>
  `,
  styleUrl: '../../shared/access/access.scss',
  styles: `
    td.wrap {
      overflow-wrap: anywhere;
    }
  `,
})
export class IssuanceList {
  /** The issuer the list is fixed to; empty shows all (the filter names one). */
  readonly issuer = input('');
  /** The project key of a project's screen; null for the administrators' list. */
  readonly project = input<string | null>(null);

  private readonly api = inject(CiApi);
  private readonly dialog = inject(MatDialog);
  private readonly confirmer = inject(Confirm);
  private readonly notifier = inject(Notifier);
  private readonly session = inject(SessionStore);

  protected readonly list = new PagedResource<CiIssuance>();
  protected readonly panelOpen = signal(false);
  protected readonly activeCount = signal(0);
  protected readonly actionError = signal<ApiError | null>(null);
  protected readonly busy = signal(false);

  protected readonly issuerFilter = signal('');
  protected readonly projectId = signal('');
  protected readonly projectPath = signal('');
  protected readonly pipelineId = signal('');
  protected readonly jobId = signal('');
  protected readonly mapping = signal('');
  protected readonly state = signal('');
  protected readonly from = signal('');
  protected readonly to = signal('');
  /** The filters the list currently shows; the bulk revoke acts on exactly these. */
  private applied: IssuanceFilters = {};

  protected readonly fixedIssuer = computed(() => this.issuer() !== '');
  protected readonly canRevoke = computed(() => !this.project() && this.session.has('tokens:revoke'));
  protected readonly stateOptions: readonly SelectOption[] = [
    { value: '', label: $localize`:@@ci.issuances.state.any:Any` },
    { value: 'active', label: $localize`:@@ci.issuances.state.active:Active` },
    { value: 'inactive', label: $localize`:@@ci.issuances.state.inactive:Expired or revoked` },
  ];
  protected readonly caption = $localize`:@@ci.issuances.caption:Issued CI tokens`;
  protected readonly columns = computed<readonly TableColumn[]>(() => [
    { id: 'issued', label: $localize`:@@ci.issuances.col.issued:Issued` },
    ...(this.project() ? [] : [{ id: 'issuer', label: $localize`:@@ci.issuances.col.issuer:Issuer` }]),
    { id: 'mappings', label: $localize`:@@ci.issuances.col.mappings:Mappings` },
    { id: 'project', label: $localize`:@@ci.issuances.col.project:Project` },
    { id: 'job', label: $localize`:@@ci.issuances.col.job:Pipeline / job` },
    { id: 'ref', label: $localize`:@@ci.issuances.col.ref:Ref` },
    { id: 'state', label: $localize`:@@ci.issuances.col.state:State` },
    { id: 'actions', label: $localize`:@@ci.issuances.col.actions:Actions`, hideLabel: true },
  ]);

  /** The bulk revoke needs an issuer and one narrowing filter, as the API does, and no filter it cannot send (R-CI-020). */
  protected readonly revocable = signal(false);
  /** The applied filters the bulk revoke does not understand: it would revoke more than the list shows. */
  protected readonly unsupported = signal<readonly string[]>([]);

  constructor() {
    effect(() => {
      this.issuer();
      this.project();
      untracked(() => void this.apply());
    });
  }

  private filters(): IssuanceFilters {
    const t = (s: string): string | undefined => s.trim() || undefined;
    const active = this.state() === 'active' ? true : this.state() === 'inactive' ? false : undefined;
    const issuer = this.fixedIssuer() ? this.issuer() : t(this.issuerFilter());
    if (this.project()) return { issuer, jobId: t(this.jobId()), active };
    return {
      issuer,
      projectId: t(this.projectId()),
      projectPath: t(this.projectPath()),
      pipelineId: t(this.pipelineId()),
      jobId: t(this.jobId()),
      mapping: t(this.mapping()),
      active,
      from: dayStart(this.from()),
      to: dayStart(this.to(), 1),
    };
  }

  protected async apply(): Promise<void> {
    const f = this.filters();
    this.applied = f;
    const own = { ...f, issuer: this.fixedIssuer() ? undefined : f.issuer };
    this.activeCount.set(Object.values(own).filter((v) => v !== undefined).length);
    const blocked = [
      ...(f.projectPath ? [$localize`:@@ci.issuances.f.project-path:Project path`] : []),
      ...(f.active === false ? [$localize`:@@ci.issuances.f.state:State`] : []),
      ...(f.from ? [$localize`:@@ci.issuances.f.from:Issued from`] : []),
      ...(f.to ? [$localize`:@@ci.issuances.f.to:Issued until`] : []),
    ];
    this.unsupported.set(blocked);
    this.revocable.set(blocked.length === 0 && !!f.issuer && !!(f.projectId || f.pipelineId || f.jobId || f.mapping));
    await this.list.start(this.api.listIssuances(f, this.project()));
  }

  protected clear(): void {
    for (const s of [this.issuerFilter, this.projectId, this.projectPath, this.pipelineId, this.jobId, this.mapping, this.state, this.from, this.to]) s.set('');
    void this.apply();
  }

  protected open(issuance: CiIssuance): void {
    this.dialog.open<IssuanceDetailDialog, IssuanceDetailData>(IssuanceDetailDialog, {
      data: { issuance, withUsage: !this.project() && this.session.has('tokens:read') },
      maxWidth: '44rem',
      width: '44rem',
    });
  }

  /**
   * Revokes the active tokens that match the applied filters. The confirmation names how many that is:
   * the active ones are counted first with the same filters, so the number is what the request revokes.
   */
  protected async revokeMatching(): Promise<void> {
    const f = this.applied;
    if (!f.issuer || !this.revocable()) return;
    // Exactly what the API understands: the count above and the request below carry the same members.
    const request: CiRevokeRequest = {
      issuer: f.issuer,
      ...(f.projectId ? { projectId: f.projectId } : {}),
      ...(f.pipelineId ? { pipelineId: f.pipelineId } : {}),
      ...(f.jobId ? { jobId: f.jobId } : {}),
      ...(f.mapping ? { mapping: f.mapping } : {}),
    };
    this.busy.set(true);
    this.actionError.set(null);
    try {
      const count = (await collectAll(this.api.listIssuances({ ...request, active: true }, null))).length;
      if (count === 0) {
        this.notifier.status($localize`:@@ci.issuances.none-to-revoke:No active token matches the filters.`);
        return;
      }
      const answer = await this.confirmer.ask({
        title: $localize`:@@ci.issuances.revoke-title:Revoke ${count}:count: tokens?`,
        lines: [
          $localize`:@@ci.issuances.revoke-text:The ${count}:count: active tokens that match the applied filters stop working at once on every replica. Running jobs that use them fail. This cannot be undone.`,
        ],
        confirmLabel: $localize`:@@ci.issuances.revoke-confirm:Revoke ${count}:count: tokens`,
      });
      if (answer === null) return;
      const revoked = await this.api.revokeIssuances(request);
      this.notifier.status($localize`:@@ci.issuances.revoked:${revoked}:count: tokens revoked.`);
      await this.apply();
    } catch (e) {
      this.actionError.set(asApiError(e));
    } finally {
      this.busy.set(false);
    }
  }
}
