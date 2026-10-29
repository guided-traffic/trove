import { Component, computed, inject, resource } from '@angular/core';
import { RouterLink } from '@angular/router';
import { EndpointsApi } from '../../api/endpoints-api';
import { ErrorNotice } from '../../shared/error-notice';
import { asApiError, valueOf } from '../../shared/paged-resource';

/**
 * The endpoint list (R-EP-017): name, base URL, hosts, surface, exposure mode, rule count and pool.
 * An endpoint that is no longer configured but still has rules is flagged "inert": its rules have
 * no effect and can only be read and deleted.
 */
@Component({
  selector: 'trove-endpoint-list',
  imports: [RouterLink, ErrorNotice],
  template: `
    <header class="page-header">
      <h1 tabindex="-1" i18n="@@ep.list.heading">Endpoints</h1>
    </header>
    <p class="hint" i18n="@@ep.list.hint">
      Endpoints come from the configuration file. Exposure rules decide what an endpoint in mode
      "rules" makes retrievable.
    </p>

    @if (failure(); as f) {
      <trove-error-notice [error]="f" (retry)="endpoints.reload()" />
    } @else if (endpoints.isLoading() && !valueOf(endpoints)) {
      <p role="status" i18n="@@repo.loading">Loading.</p>
    } @else if (valueOf(endpoints); as list) {
      @if (list.length === 0) {
        <p class="empty" i18n="@@ep.list.empty">No endpoint is configured.</p>
      } @else {
        <div class="scroll" tabindex="0" role="region" aria-labelledby="ep-caption">
          <table class="data">
            <caption id="ep-caption" class="visually-hidden" i18n="@@ep.list.caption">
              Endpoints
            </caption>
            <thead>
              <tr>
                <th scope="col" i18n="@@ep.col.name">Name</th>
                <th scope="col" i18n="@@ep.col.base-url">Base URL</th>
                <th scope="col" i18n="@@ep.col.hosts">Hosts</th>
                <th scope="col" i18n="@@ep.col.surface">Surface</th>
                <th scope="col" i18n="@@ep.col.mode">Exposure</th>
                <th scope="col" i18n="@@ep.col.rules">Rules (enabled)</th>
                <th scope="col" i18n="@@ep.col.pool">Pool</th>
              </tr>
            </thead>
            <tbody>
              @for (e of list; track e.name) {
                <tr>
                  <th scope="row">
                    <a [routerLink]="['/admin/endpoints', e.name]" translate="no">{{ e.name }}</a>
                    @if (e.inert) {
                      <span class="badge" i18n="@@ep.badge.inert">not configured</span>
                    }
                  </th>
                  @if (e.configuration; as c) {
                    <td translate="no">{{ c.baseUrl }}</td>
                    <td translate="no">{{ c.hosts.join(', ') }}</td>
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
                    <td colspan="5">
                      <span i18n="@@ep.inert.text"
                        >No longer configured: {{ e.ruleCount }} rules without effect.</span
                      >
                    </td>
                    <td class="num">{{ e.ruleCount }} ({{ e.enabledRuleCount }})</td>
                  }
                </tr>
              }
            </tbody>
          </table>
        </div>
      }
    }
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
}
