import { Component, effect, inject, input, signal, untracked } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CiApi, CiIssuerChoice } from '../../api/ci-api';
import { ApiError } from '../../api/problem';
import { asApiError } from '../../shared/paged-resource';
import { DataTable, TableColumn } from '../../shared/ui/data-table';
import { ciRoutes } from './ci-common';
import { IssuanceList } from './issuance-list';
import { MappingList } from './mapping-list';
import { ErrorNotice } from '../../shared/error-notice';

/**
 * The CI identity of one project (R-UI-035, Q-CI-26): for an Owner of the project, the issuers the project may use (read-only: issuers are the system
 * administrators' and the Helm values'), the project's mappings with the editor bounded to the
 * project, the dry run over the project's tier and the tokens issued through the project's mappings.
 */
@Component({
  selector: 'trove-project-ci',
  imports: [RouterLink, ErrorNotice, DataTable, IssuanceList, MappingList],
  template: `
    <header class="page-header">
      <h1 tabindex="-1" i18n="@@ci.project.heading">CI identity</h1>
      <a class="btn btn-default" [routerLink]="testLink()" i18n="@@ci.project.test">Test with claims</a>
    </header>
    <p class="subject" translate="no">{{ project() }}</p>
    <p i18n="@@ci.project.hint">
      Jobs of your GitLab projects exchange their ID token for a short-lived token. The mappings below decide what such a job may do in the repositories of this project. They apply next to the administrators' mappings; yours can never take anything away from theirs.
    </p>

    @if (error(); as e) {
      <trove-error-notice [error]="e" (retry)="load()" />
    } @else {
      <section class="card" aria-labelledby="ci-p-issuers">
        <h2 id="ci-p-issuers" i18n="@@ci.project.issuers">Issuers you can use</h2>
        <p class="muted" i18n="@@ci.project.issuers-hint">Issuers are managed by the administrators of trove. A mapping names one of them.</p>
        <trove-data-table
          [caption]="caption"
          [columns]="columns"
          [loaded]="loaded()"
          [empty]="issuers().length === 0"
          i18n-emptyText="@@ci.project.issuers-empty"
          emptyText="No issuer is registered yet. Ask an administrator of trove."
        >
          @for (i of issuers(); track i.name) {
            <tr>
              <th scope="row" translate="no">{{ i.name }}</th>
              <td translate="no">{{ i.kind }}</td>
              <td translate="no">{{ i.issuer }}</td>
              <td>{{ i.publicIssuer ? yes : no }}</td>
            </tr>
          }
        </trove-data-table>
      </section>

      <section class="card" aria-labelledby="ci-p-mappings">
        <h2 id="ci-p-mappings" i18n="@@ci.project.mappings">Mappings of the project</h2>
        <trove-ci-mapping-list [project]="project()" />
      </section>

      <section class="card" aria-labelledby="ci-p-issuances">
        <h2 id="ci-p-issuances" i18n="@@ci.project.issuances">Issued tokens</h2>
        <trove-ci-issuance-list [project]="project()" />
      </section>
    }
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class ProjectCi {
  /** Route parameter `:project`. */
  readonly project = input.required<string>();

  private readonly api = inject(CiApi);

  protected readonly issuers = signal<readonly CiIssuerChoice[]>([]);
  protected readonly loaded = signal(false);
  protected readonly error = signal<ApiError | null>(null);
  protected readonly yes = $localize`:@@ci.yes:yes`;
  protected readonly no = $localize`:@@ci.no:no`;

  protected readonly caption = $localize`:@@ci.project.issuers-caption:CI issuers`;
  protected readonly columns: readonly TableColumn[] = [
    { id: 'name', label: $localize`:@@ci.project.col.name:Name` },
    { id: 'kind', label: $localize`:@@ci.project.col.kind:Kind` },
    { id: 'issuer', label: $localize`:@@ci.project.col.issuer:Issuer URL` },
    { id: 'public', label: $localize`:@@ci.project.col.public:Public`, help: $localize`:@@ci.project.col.public-help:On a public issuer anyone can obtain tokens, so a mapping must pin the project or namespace by its ID.` },
  ];

  constructor() {
    effect(() => {
      this.project();
      untracked(() => void this.load());
    });
  }

  protected testLink(): string[] {
    return ciRoutes.test(this.project(), '');
  }

  protected async load(): Promise<void> {
    this.error.set(null);
    try {
      this.issuers.set(await this.api.projectIssuers(this.project()));
      this.loaded.set(true);
    } catch (e) {
      this.error.set(asApiError(e));
    }
  }
}
