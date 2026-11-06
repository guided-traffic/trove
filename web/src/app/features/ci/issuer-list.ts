import { DatePipe } from '@angular/common';
import { Component, inject, signal } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CiApi, CiIssuer } from '../../api/ci-api';
import { ApiError } from '../../api/problem';
import { SessionStore } from '../../core/session-store';
import { Needs } from '../../shared/access/needs';
import { asApiError } from '../../shared/paged-resource';
import { DataTable, TableColumn } from '../../shared/ui/data-table';
import { ErrorNotice } from '../../shared/error-notice';

/**
 * The CI issuers (R-UI-035): name, kind, issuer URL, audiences, the number of mappings, the state of the
 * signing keys (the last fetch or the error) and the source. An issuer declared in the system
 * configuration says so (Q-CI-16) and cannot be edited here. The list is short and complete, so it
 * has no filter and no paging.
 */
@Component({
  selector: 'trove-ci-issuer-list',
  imports: [DatePipe, RouterLink, ErrorNotice, DataTable, Needs],
  template: `
    <header class="page-header">
      <h1 tabindex="-1" i18n="@@ci.issuers.heading">CI identity</h1>
      <a class="btn btn-default" routerLink="/admin/ci/issuances" i18n="@@ci.issuers.issuances">Issued tokens</a>
      @if (session.has('ci:write')) {
        <a class="btn btn-confirm" routerLink="/admin/ci/new" i18n="@@ci.issuers.add">New issuer</a>
      }
    </header>
    <trove-needs capability="ci:write" />
    <p i18n="@@ci.issuers.hint">
      A CI issuer is a GitLab instance whose jobs may exchange their ID token for a short-lived trove token. Mappings decide what a job receives.
    </p>

    @if (error(); as e) {
      <trove-error-notice [error]="e" (retry)="load()" />
    }
    <trove-data-table
      [caption]="caption"
      [columns]="columns"
      [loaded]="loaded()"
      [loading]="loading()"
      [empty]="issuers().length === 0"
      i18n-emptyText="@@ci.issuers.empty"
      emptyText="No CI issuer is registered yet."
    >
      @for (i of issuers(); track i.name) {
        <tr>
          <th scope="row">
            <a [routerLink]="['/admin/ci', i.name]" translate="no">{{ i.name }}</a>
            @if (!i.enabled) {
              <span class="badge"><span class="material-icons" aria-hidden="true">block</span><ng-container i18n="@@ci.issuers.disabled">Disabled</ng-container></span>
            }
          </th>
          <td translate="no">{{ i.kind }}</td>
          <td translate="no" class="wrap">{{ i.issuer }}</td>
          <td translate="no" class="wrap">
            @for (a of i.allowedAudiences; track a) {
              <div>{{ a }}</div>
            }
          </td>
          <td class="num">{{ i.mappingCount ?? 0 }}</td>
          <td>
            @if (i.keys?.lastError) {
              <div class="bad"><span class="material-icons" aria-hidden="true">error</span> <ng-container i18n="@@ci.keys.failed">Fetch failed</ng-container></div>
              <div class="muted detail">{{ i.keys!.lastError }}</div>
            } @else if (i.keys?.fetchedAt) {
              <div class="ok"><span class="material-icons" aria-hidden="true">check_circle</span> <ng-container i18n="@@ci.keys.usable">{{ i.keys!.usable }} usable</ng-container></div>
              <div class="muted detail"><ng-container i18n="@@ci.keys.fetched">fetched</ng-container> {{ i.keys!.fetchedAt | date: 'short' }}</div>
            } @else {
              <div class="muted"><span class="material-icons" aria-hidden="true">schedule</span> <ng-container i18n="@@ci.keys.none">Not fetched yet</ng-container></div>
            }
          </td>
          <td>
            @if (i.source === 'configuration') {
              <span class="badge"><span class="material-icons" aria-hidden="true">lock</span><ng-container i18n="@@ci.source.configuration">configuration</ng-container></span>
            } @else {
              <span class="badge"><ng-container i18n="@@ci.source.api">api</ng-container></span>
            }
          </td>
        </tr>
      }
    </trove-data-table>
  `,
  styleUrl: '../../shared/access/access.scss',
  styles: `
    td.wrap {
      overflow-wrap: anywhere;
      max-inline-size: 18rem;
    }
    .detail {
      max-inline-size: 16rem;
      overflow-wrap: anywhere;
      font-size: var(--trove-text-muted-size);
    }
  `,
})
export class IssuerList {
  private readonly api = inject(CiApi);
  protected readonly session = inject(SessionStore);

  protected readonly issuers = signal<readonly CiIssuer[]>([]);
  protected readonly loaded = signal(false);
  protected readonly loading = signal(false);
  protected readonly error = signal<ApiError | null>(null);

  protected readonly caption = $localize`:@@ci.issuers.caption:CI issuers`;
  protected readonly columns: readonly TableColumn[] = [
    { id: 'name', label: $localize`:@@ci.issuers.col.name:Name` },
    { id: 'kind', label: $localize`:@@ci.issuers.col.kind:Kind` },
    { id: 'issuer', label: $localize`:@@ci.issuers.col.issuer:Issuer URL` },
    { id: 'audiences', label: $localize`:@@ci.issuers.col.audiences:Audiences` },
    { id: 'mappings', label: $localize`:@@ci.issuers.col.mappings:Mappings`, numeric: true },
    { id: 'keys', label: $localize`:@@ci.issuers.col.keys:Signing keys` },
    { id: 'source', label: $localize`:@@ci.issuers.col.source:Source`, help: $localize`:@@ci.issuers.col.source-help:An issuer declared in the installation's configuration is read-only here.` },
  ];

  constructor() {
    void this.load();
  }

  protected async load(): Promise<void> {
    this.loading.set(true);
    this.error.set(null);
    try {
      this.issuers.set(await this.api.listIssuers());
      this.loaded.set(true);
    } catch (e) {
      this.error.set(asApiError(e));
    } finally {
      this.loading.set(false);
    }
  }
}
