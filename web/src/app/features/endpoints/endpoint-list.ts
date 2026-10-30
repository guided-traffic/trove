import { Component, computed, inject, resource, signal } from '@angular/core';
import { RouterLink } from '@angular/router';
import { Endpoint, EndpointsApi } from '../../api/endpoints-api';
import { DataTable, TableColumn, TableSort } from '../../shared/ui/data-table';
import { asApiError, valueOf } from '../../shared/paged-resource';

/**
 * The endpoint list (R-EP-017): name, base URL, hosts, surface, exposure mode, rule count and pool.
 * An endpoint that is no longer configured but still has rules is flagged "inert": its rules have
 * no effect and can only be read and deleted. The API answers the whole list in one call, so the
 * sortable columns (name, rule count) sort in the browser, which is exact.
 */
@Component({
  selector: 'trove-endpoint-list',
  imports: [RouterLink, DataTable],
  template: `
    <header class="page-header">
      <h1 tabindex="-1" i18n="@@ep.list.heading">Endpoints</h1>
    </header>
    <p class="hint" i18n="@@ep.list.hint">
      Endpoints come from the configuration file. Exposure rules decide what an endpoint in mode
      "rules" makes retrievable.
    </p>

    <trove-data-table
      [caption]="caption"
      [columns]="columns"
      [sort]="sort()"
      (sortChange)="sort.set($event)"
      [loaded]="valueOf(endpoints) !== undefined"
      [loading]="endpoints.isLoading()"
      [error]="failure()"
      [empty]="rows().length === 0"
      i18n-emptyText="@@ep.list.empty"
      emptyText="No endpoint is configured."
      (retry)="endpoints.reload()"
    >
      @for (e of rows(); track e.name) {
        <tr>
          <th scope="row">
            <a [routerLink]="['/admin/endpoints', e.name]" translate="no">{{ e.name }}</a>
            @if (e.inert) {
              <span class="badge" i18n="@@ep.badge.inert">not configured</span>
            }
          </th>
          @if (e.configuration; as c) {
            <td translate="no"><span class="ellipsis" [attr.title]="c.baseUrl">{{ c.baseUrl }}</span></td>
            <td translate="no"><span class="ellipsis" [attr.title]="c.hosts.join(', ')">{{ c.hosts.join(', ') }}</span></td>
            <td translate="no">{{ c.surface.join(', ') }}</td>
            <td>
              @if (c.exposure === 'all') {
                <span class="material-icons inline warn" aria-hidden="true">warning</span>
                <span i18n="@@ep.mode.all">all (rules have no effect)</span>
              } @else {
                <span i18n="@@ep.mode.rules">rules</span>
              }
            </td>
            <td class="num">{{ e.ruleCount }} ({{ e.enabledRuleCount }})</td>
            <td translate="no">{{ c.pool ?? '–' }}</td>
          } @else {
            <td colspan="4">
              <span i18n="@@ep.inert.text"
                >No longer configured: {{ e.ruleCount }} rules without effect.</span
              >
            </td>
            <td class="num">{{ e.ruleCount }} ({{ e.enabledRuleCount }})</td>
            <td></td>
          }
        </tr>
      }
    </trove-data-table>
  `,
  styles: `
    .badge {
      margin-inline-start: var(--trove-space-2);
      padding: 0 var(--trove-space-2);
      border: 1px solid var(--trove-status-warn);
      border-radius: var(--mat-sys-corner-extra-small);
      font-size: 0.75rem;
      font-weight: 400;
    }
  `,
})
export class EndpointList {
  protected readonly valueOf = valueOf;
  private readonly api = inject(EndpointsApi);

  protected readonly endpoints = resource({ loader: () => this.api.list() });
  protected readonly failure = computed(() => {
    const e = this.endpoints.error();
    return e ? asApiError(e) : null;
  });

  protected readonly sort = signal<TableSort>({ column: 'name', direction: 'asc' });
  protected readonly caption = $localize`:@@ep.list.caption:Endpoints`;
  protected readonly columns: readonly TableColumn[] = [
    { id: 'name', label: $localize`:@@ep.col.name:Name`, sortable: true },
    { id: 'baseUrl', label: $localize`:@@ep.col.base-url:Base URL` },
    { id: 'hosts', label: $localize`:@@ep.col.hosts:Hosts` },
    {
      id: 'surface',
      label: $localize`:@@ep.col.surface:Surface`,
      help: $localize`:@@ep.help.surface:Which parts of trove this endpoint serves: the web interface, the management API, the CI token exchange, package clients and file downloads.`,
    },
    {
      id: 'mode',
      label: $localize`:@@ep.col.mode:Exposure`,
      help: $localize`:@@ep.help.mode:In mode "all" the endpoint serves every repository its surface allows; in mode "rules" only what its exposure rules select.`,
    },
    { id: 'rules', label: $localize`:@@ep.col.rules:Rules (enabled)`, sortable: true, numeric: true },
    { id: 'pool', label: $localize`:@@ep.col.pool:Pool` },
  ];

  /** The list in the chosen order; the whole list is loaded, so sorting here is exact. */
  protected readonly rows = computed<readonly Endpoint[]>(() => {
    const list = valueOf(this.endpoints) ?? [];
    const { column, direction } = this.sort();
    const factor = direction === 'asc' ? 1 : -1;
    return [...list].sort((a, b) =>
      column === 'rules' ? factor * (a.ruleCount - b.ruleCount) : factor * a.name.localeCompare(b.name),
    );
  });
}
