import { Component, LOCALE_ID, computed, effect, inject, input, signal, untracked } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CiApi, CiMapping } from '../../api/ci-api';
import { ApiError } from '../../api/problem';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { formatDuration } from '../../shared/access/duration';
import { Needs } from '../../shared/access/needs';
import { Confirm } from '../../shared/confirm-dialog';
import { asApiError } from '../../shared/paged-resource';
import { DataTable, TableColumn } from '../../shared/ui/data-table';
import { ProjectNeeds, createState } from '../projects/project-access';
import { ciRoutes } from './ci-common';
import { summarizeCondition, summarizeGrants, tierLabel } from './ci-model';
import { ErrorNotice } from '../../shared/error-notice';

/**
 * The mappings of an issuer in evaluation order (R-UI-035, R-CI-009, R-CI-019): tier, then priority,
 * each with the summary of its condition and its grants and its maximum token lifetime. Within a tier
 * the first matching mapping wins; the tiers' matches combine. With `project` the list is the project's
 * mappings (Q-CI-26) of one issuer or of all, and every link and write goes to the project routes.
 */
@Component({
  selector: 'trove-ci-mapping-list',
  imports: [RouterLink, ErrorNotice, DataTable, Needs, ProjectNeeds],
  template: `
    <div class="toolbar">
      @if (writable()) {
        <a class="btn btn-confirm" [routerLink]="newLink()" [queryParams]="newQuery()" i18n="@@ci.mappings.add">New mapping</a>
      } @else {
        <button class="btn btn-confirm" type="button" disabled aria-describedby="needs-ci-mappings" i18n="@@ci.mappings.add-disabled">New mapping</button>
      }
      @if (testable()) {
        <a class="btn btn-default" [routerLink]="testLink()" [queryParams]="testQuery()" i18n="@@ci.mappings.test-all">Test with claims</a>
      }
    </div>
    @if (project(); as key) {
      <trove-project-needs id="needs-ci-mappings" [state]="state()" capability="ci:write" right="managePolicies" />
    } @else {
      <trove-needs id="needs-ci-mappings" capability="ci:write" />
    }
    <p class="muted" i18n="@@ci.mappings.order">
      Evaluated in this order. In a tier the first mapping that matches wins and later ones are not looked at; the matches of the tiers are combined.
    </p>

    @if (error(); as e) {
      <trove-error-notice [error]="e" (retry)="load()" />
    }
    @if (error()?.status !== 501) {
      <trove-data-table
        [caption]="caption"
        [columns]="columns()"
        [loaded]="loaded()"
        [loading]="loading()"
        [empty]="mappings().length === 0"
        i18n-emptyText="@@ci.mappings.empty"
        emptyText="No mapping yet. Without a mapping no job receives a token."
      >
        @for (m of mappings(); track m.issuer + '/' + m.name; let n = $index) {
          <tr>
            <td class="num">{{ n + 1 }}</td>
            <th scope="row">
              <a [routerLink]="mappingLink(m)" [queryParams]="linkQuery(m)" translate="no">{{ m.name }}</a>
              @if (!m.enabled) {
                <span class="badge"><span class="material-icons" aria-hidden="true">block</span><ng-container i18n="@@ci.mappings.disabled">Disabled</ng-container></span>
              }
            </th>
            @if (project()) {
              <td translate="no">{{ m.issuer }}</td>
            } @else {
              <td>{{ tier(m.tier) }}</td>
            }
            <td class="num">{{ m.priority }}</td>
            <td translate="no" class="wrap">{{ condition(m) }}</td>
            <td translate="no" class="wrap">
              @for (g of grants(m); track $index) {
                <div>{{ g }}</div>
              }
            </td>
            <td class="num">{{ lifetime(m) }}</td>
            <td class="actions">
              <a class="btn btn-tertiary" [routerLink]="mappingLink(m)" [queryParams]="linkQuery(m)">{{ writable() ? edit : view }}</a>
              @if (testable()) {
                <a class="btn btn-tertiary" [routerLink]="testLink()" [queryParams]="testQuery(m)" i18n="@@ci.mappings.test">Test</a>
              }
              @if (writable()) {
                <button class="btn btn-tertiary" type="button" (click)="remove(m)" i18n="@@ci.mappings.delete">Delete</button>
              }
            </td>
          </tr>
        }
      </trove-data-table>
    }
  `,
  styleUrl: '../../shared/access/access.scss',
  styles: `
    td.wrap {
      overflow-wrap: anywhere;
      min-inline-size: 14rem;
      max-inline-size: 28rem;
    }
  `,
})
export class MappingList {
  /** The issuer; empty with a project lists the project's mappings of every issuer. */
  readonly issuer = input('');
  /** The project key of a project's screen; null for the system tier. */
  readonly project = input<string | null>(null);

  private readonly api = inject(CiApi);
  private readonly confirmer = inject(Confirm);
  private readonly notifier = inject(Notifier);
  private readonly session = inject(SessionStore);
  private readonly locale = inject(LOCALE_ID);

  protected readonly mappings = signal<readonly CiMapping[]>([]);
  protected readonly loaded = signal(false);
  protected readonly loading = signal(false);
  protected readonly error = signal<ApiError | null>(null);

  protected readonly state = computed(() => createState(this.session, this.project() ?? '', 'managePolicies', 'ci:write'));
  protected readonly writable = computed(() => (this.project() ? this.state() === 'enabled' : this.session.has('ci:write')));
  /** The dry run is the `ci:read` operation or the project's; the project administrator holds the latter. */
  protected readonly testable = computed(() => this.issuer() !== '' || this.project() !== null);

  protected readonly caption = $localize`:@@ci.mappings.caption:Mappings in evaluation order`;
  protected readonly edit = $localize`:@@ci.mappings.edit:Edit`;
  protected readonly view = $localize`:@@ci.mappings.view:View`;
  protected readonly columns = computed<readonly TableColumn[]>(() => [
    { id: 'order', label: $localize`:@@ci.mappings.col.order:Order`, numeric: true },
    { id: 'name', label: $localize`:@@ci.mappings.col.name:Name` },
    this.project()
      ? { id: 'issuer', label: $localize`:@@ci.mappings.col.issuer:Issuer` }
      : { id: 'tier', label: $localize`:@@ci.mappings.col.tier:Tier`, help: $localize`:@@ci.mappings.col.tier-help:The system tier is the administrators'. A project's administrators manage the tier of their project. The tiers are evaluated separately and their matches combined.` },
    { id: 'priority', label: $localize`:@@ci.mappings.col.priority:Priority`, numeric: true },
    { id: 'condition', label: $localize`:@@ci.mappings.col.condition:Condition` },
    { id: 'grants', label: $localize`:@@ci.mappings.col.grants:Grants` },
    { id: 'ttl', label: $localize`:@@ci.mappings.col.ttl:Maximum lifetime`, numeric: true },
    { id: 'actions', label: $localize`:@@ci.mappings.col.actions:Actions`, hideLabel: true },
  ]);

  constructor() {
    effect(() => {
      this.issuer();
      this.project();
      untracked(() => void this.load());
    });
  }

  protected tier = tierLabel;
  protected condition = (m: CiMapping): string => summarizeCondition(m.match);
  protected grants = (m: CiMapping): string[] => summarizeGrants(m.grants);
  protected lifetime = (m: CiMapping): string => (m.maxTtlSeconds ? formatDuration(m.maxTtlSeconds, this.locale) : '–');

  protected newLink(): string[] {
    return ciRoutes.mapping(this.project(), this.issuer(), null);
  }
  protected newQuery(): Record<string, string> | null {
    return ciRoutes.mappingQuery(this.project(), this.issuer());
  }
  protected linkQuery(m: CiMapping): Record<string, string> | null {
    return ciRoutes.mappingQuery(this.project(), m.issuer);
  }
  protected mappingLink(m: CiMapping): string[] {
    return ciRoutes.mapping(this.project(), m.issuer, m.name);
  }
  protected testLink(): string[] {
    return ciRoutes.test(this.project(), this.issuer());
  }
  protected testQuery(m?: CiMapping): Record<string, string> | null {
    const issuer = m?.issuer ?? this.issuer();
    if (!this.project()) return m ? { mapping: m.name } : null;
    return { ...(issuer ? { issuer } : {}), ...(m ? { mapping: m.name } : {}) };
  }

  protected async load(): Promise<void> {
    if (!this.project() && !this.issuer()) return;
    this.loading.set(true);
    this.error.set(null);
    try {
      this.mappings.set(await this.api.listMappings(this.issuer(), this.project()));
      this.loaded.set(true);
    } catch (e) {
      this.error.set(asApiError(e));
    } finally {
      this.loading.set(false);
    }
  }

  protected async remove(m: CiMapping): Promise<void> {
    const answer = await this.confirmer.ask({
      title: $localize`:@@ci.mappings.delete-title:Delete mapping ${m.name}:name:?`,
      lines: [
        $localize`:@@ci.mappings.delete-text:The tokens issued through this mapping are revoked at once, and its principal is deleted with the grants that policies gave it. A mapping created later under the same name inherits nothing.`,
      ],
      confirmLabel: $localize`:@@ci.mappings.delete-confirm:Delete mapping`,
      typeToConfirm: m.name,
    });
    if (answer === null) return;
    try {
      await this.api.deleteMapping(m.issuer, m.name, this.project());
      this.notifier.status($localize`:@@ci.mappings.deleted:Mapping deleted.`);
      await this.load();
    } catch (e) {
      this.error.set(asApiError(e));
    }
  }
}
