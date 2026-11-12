import { DatePipe } from '@angular/common';
import { Component, LOCALE_ID, computed, effect, inject, input, resource, signal, untracked } from '@angular/core';
import { Router, RouterLink } from '@angular/router';
import { PackageSummary, PackagesApi } from '../../api/packages-api';
import { Project, ProjectRole, ProjectsApi } from '../../api/projects-api';
import { RepositoriesApi, RepositorySummary, refOf } from '../../api/repositories-api';
import { CopyButton } from '../../shared/copy-button';
import { FormatIconView } from '../../shared/format-icon';
import { PagedResource, valueOf } from '../../shared/paged-resource';
import { DataTable, TableColumn } from '../../shared/ui/data-table';
import { PageHeader } from '../../shared/ui/page-header';
import { TabContent, TabGroup, TabPage } from '../../shared/ui/tabs';
import { VisibilityIcon, levelName } from '../../shared/ui/visibility-level';
import { relativeTime } from '../packages/format-time';
import { packageCommands } from '../packages/package-links';
import { projectRoleLabel } from '../projects/project-roles';
import { classLabel } from '../repositories/repository-class';

/** The tabs of the page, in their order (R-UI-098). */
export const RESULT_KINDS = ['repository', 'package', 'project'] as const;
export type ResultKind = (typeof RESULT_KINDS)[number];

/** Rows per page of each tab, with **Load more** (R-API-010). */
const PAGE = 25;

/**
 * The results page of the main area (R-UI-098, Q-UI-411): Enter in the top bar's field without a picked suggestion opens it. Its
 * tabs are the lists of the three kinds with `q`, each the list the caller may read: Repositories (`GET /repositories?q=`) in the
 * admin area's pattern (Q-UI-330: the key with its description, project, format and class as columns, the address a quiet copy
 * button), Packages (`GET /packages?q=`, the columns of Q-UI-314 with the project and the repository), Projects (`GET
 * /projects?q=`, the start page's columns). `kind` chooses the tab; without it, the first group that had a suggestion when Enter
 * was pressed (navigation state `firstKind`), else Repositories. The page has no users, groups or admin pages and no link into the
 * admin area (R-UI-099). It takes over the stopgap's page of Q-UI-390 (Q-UI-415).
 */
@Component({
  selector: 'trove-search-results',
  imports: [DatePipe, RouterLink, CopyButton, DataTable, FormatIconView, PageHeader, TabContent, TabGroup, TabPage, VisibilityIcon],
  template: `
    <trove-page-header [heading]="heading" [description]="description()" />

    <trove-tab-group [selectedIndex]="tab()" (selectedIndexChange)="choose($event)">
      <trove-tab [label]="labels.repository">
        <ng-template troveTabContent>
          <trove-data-table
            [caption]="labels.repository"
            [columns]="repositoryColumns"
            [loaded]="tooShort() || repositories.loaded()"
            [loading]="repositories.loading()"
            [error]="repositories.error()"
            [empty]="tooShort() || repositories.items().length === 0"
            [emptyText]="emptyText()"
            [hasMore]="repositories.hasMore()"
            (more)="repositories.more()"
            (retry)="repositories.reload()"
          >
            @for (row of repositories.items(); track refOf(row)) {
              <tr>
                <th scope="row" class="wrap-name">
                  <a [routerLink]="['/projects', row.project, 'repositories', row.key]" translate="no">{{ row.key }}</a>
                  @if (row.description) {
                    <span class="secondary">{{ row.description }}</span>
                  }
                </th>
                <td class="project-cell">
                  <a [routerLink]="['/projects', row.project]" translate="no">{{ row.project }}</a>
                </td>
                <td class="format-cell">
                  <span class="format-name"
                    ><trove-format-icon [format]="row.format" /><span translate="no">{{ formatLabel(row.format) }}</span></span
                  >
                </td>
                <td class="class-cell">
                  <span class="class-name"
                    ><span class="material-icons" aria-hidden="true">{{ classIcon(row.class) }}</span>{{ classText(row.class) }}</span
                  >
                </td>
                <!-- No address field: who searches opens the repository; copying is the button's (Q-UI-330). -->
                <td class="actions">
                  @if (row.addresses[0]; as a) {
                    <trove-copy-button [value]="a" [label]="copyValueLabel(a)" [quiet]="true" />
                  }
                </td>
              </tr>
            }
          </trove-data-table>
        </ng-template>
      </trove-tab>
      <trove-tab [label]="labels.package">
        <ng-template troveTabContent>
          <trove-data-table
            [caption]="labels.package"
            [columns]="packageColumns"
            [loaded]="tooShort() || packages.loaded()"
            [loading]="packages.loading()"
            [error]="packages.error()"
            [empty]="tooShort() || packages.items().length === 0"
            [emptyText]="emptyText()"
            [hasMore]="packages.hasMore()"
            (more)="packages.more()"
            (retry)="packages.reload()"
          >
            @for (row of packages.items(); track row.id) {
              @let at = placeOf(row);
              <tr>
                <th scope="row" class="wrap-name">
                  <a [routerLink]="packageLink(row)" translate="no">{{ row.name }}</a>
                  @if (row.description) {
                    <span class="secondary">{{ row.description }}</span>
                  }
                </th>
                <td class="project-cell">
                  <a [routerLink]="['/projects', at.project]" translate="no">{{ at.project }}</a>
                </td>
                <td class="repository-cell">
                  <a [routerLink]="['/projects', at.project, 'repositories', at.key]" translate="no">{{ at.key }}</a>
                </td>
                <td class="format-cell">
                  <span class="format-name"
                    ><trove-format-icon [format]="row.format" /><span translate="no">{{ formatLabel(row.format) }}</span></span
                  >
                </td>
                <td translate="no">{{ row.latestVersion }}</td>
                <td>
                  <time [attr.datetime]="row.updatedAt" [attr.title]="row.updatedAt | date: 'medium'">{{ since(row.updatedAt) }}</time>
                </td>
              </tr>
            }
          </trove-data-table>
        </ng-template>
      </trove-tab>
      <trove-tab [label]="labels.project">
        <ng-template troveTabContent>
          <trove-data-table
            [caption]="labels.project"
            [columns]="projectColumns"
            [loaded]="tooShort() || projects.loaded()"
            [loading]="projects.loading()"
            [error]="projects.error()"
            [empty]="tooShort() || projects.items().length === 0"
            [emptyText]="emptyText()"
            [hasMore]="projects.hasMore()"
            (more)="projects.more()"
            (retry)="projects.reload()"
          >
            @for (p of projects.items(); track p.key) {
              <tr>
                <th scope="row">
                  <a [routerLink]="['/projects', p.key]">{{ p.displayName || p.key }}</a>
                  <span class="secondary" translate="no">{{ p.key }}</span>
                </th>
                <td>{{ p.description }}</td>
                <td class="level"><trove-visibility-icon [level]="p.visibility" />{{ levelText(p.visibility) }}</td>
                <td>{{ p.role ? roleLabel(p.role) : '' }}</td>
                <td class="num">{{ p.repositories }}</td>
                <td class="num">{{ p.members }}</td>
              </tr>
            }
          </trove-data-table>
        </ng-template>
      </trove-tab>
    </trove-tab-group>
  `,
})
export class SearchResults {
  private readonly router = inject(Router);
  private readonly repositoriesApi = inject(RepositoriesApi);
  private readonly packagesApi = inject(PackagesApi);
  private readonly projectsApi = inject(ProjectsApi);
  private readonly locale = inject(LOCALE_ID);

  /** The text of the search, the query parameter `q`. */
  readonly q = input<string | undefined>();
  /** The tab, the query parameter `kind`. */
  readonly kind = input<string | undefined>();

  /** The first group that had a suggestion when Enter was pressed, handed over by the field (navigation state). */
  private readonly firstKind = signal<ResultKind | null>(
    ((): ResultKind | null => {
      const state = this.router.getCurrentNavigation()?.extras.state ?? (typeof history === 'undefined' ? null : history.state);
      const k = (state as Record<string, unknown> | null)?.['firstKind'];
      return RESULT_KINDS.includes(k as ResultKind) ? (k as ResultKind) : null;
    })(),
  );

  protected readonly term = computed(() => (this.q() ?? '').trim());
  protected readonly tooShort = computed(() => this.term().length < 2);
  /** The selected tab: `kind`; without it (or with `user` or `group`) the field's first group, else Repositories. */
  protected readonly tab = computed(() => {
    const k = this.kind();
    const chosen = RESULT_KINDS.includes(k as ResultKind) ? (k as ResultKind) : (this.firstKind() ?? 'repository');
    return RESULT_KINDS.indexOf(chosen);
  });

  protected readonly repositories = new PagedResource<RepositorySummary>(PAGE);
  protected readonly packages = new PagedResource<PackageSummary>(PAGE);
  protected readonly projects = new PagedResource<Project>(PAGE);
  private readonly formats = resource({ loader: () => this.repositoriesApi.formats() });

  protected readonly heading = $localize`:@@search.results.heading:Search`;
  protected readonly labels: Record<ResultKind, string> = {
    repository: $localize`:@@search.group.repositories:Repositories`,
    package: $localize`:@@search.group.packages:Packages`,
    project: $localize`:@@search.group.projects:Projects`,
  };
  protected readonly description = computed(() =>
    this.term() ? $localize`:@@search.results.description:What you may read that matches “${this.term()}:text:”.` : '',
  );
  protected readonly emptyText = computed(() =>
    this.tooShort()
      ? $localize`:@@search.results.too-short:Type at least two characters.`
      : $localize`:@@search.results.none:Nothing you may read matches “${this.term()}:text:”.`,
  );

  /** The admin area's pattern (Q-UI-330): the repository takes what the others leave, and the row ends with the copy button. */
  protected readonly repositoryColumns: readonly TableColumn[] = [
    { id: 'key', label: $localize`:@@repo.col.repository:Repository`, width: '14rem', prefer: '14rem' },
    { id: 'project', label: $localize`:@@projects.col.project:Project`, width: '6.5rem' },
    { id: 'format', label: $localize`:@@repo.col.format:Format`, width: '8rem' },
    {
      id: 'class',
      label: $localize`:@@repo.col.class:Class`,
      help: $localize`:@@repo.help.class:Local repositories hold what you upload, remote repositories fetch and cache from another registry, virtual repositories combine other repositories behind one address.`,
      width: '5rem',
    },
    { id: 'actions', label: $localize`:@@repo.col.actions:Actions`, hideLabel: true, width: '2rem' },
  ];
  /** What a reader chooses a package by (Q-UI-314), with where it is: the project and the repository that hold it. */
  protected readonly packageColumns: readonly TableColumn[] = [
    { id: 'name', label: $localize`:@@pkg.col.package:Package`, width: '14rem', prefer: '14rem' },
    { id: 'project', label: $localize`:@@projects.col.project:Project`, width: '6.5rem' },
    { id: 'repository', label: $localize`:@@repo.col.repository:Repository`, width: '8rem' },
    { id: 'type', label: $localize`:@@pkg.col.format:Format`, width: '7rem', stackLabel: false },
    {
      id: 'latest',
      label: $localize`:@@pkg.col.latest:Latest version`,
      help: $localize`:@@pkg.help.latest:The highest stable version you may see. A pre-release is shown only while no stable version exists.`,
      width: '8rem',
    },
    {
      id: 'updatedAt',
      label: $localize`:@@pkg.col.updated:Updated`,
      help: $localize`:@@pkg.help.updated:When the latest version you may see was published.`,
      width: '7rem',
    },
  ];
  /** The start page's columns (Q-UI-311). */
  protected readonly projectColumns: readonly TableColumn[] = [
    { id: 'name', label: $localize`:@@projects.col.project:Project`, width: '14rem' },
    { id: 'description', label: $localize`:@@projects-home.col.description:Description` },
    { id: 'visibility', label: $localize`:@@projects.col.visibility:Visibility`, width: '8rem' },
    {
      id: 'role',
      label: $localize`:@@projects.col.role:Your role`,
      help: $localize`:@@projects.help.role:Your role in the project; empty when you see it through a system role and are no member.`,
      width: '7rem',
    },
    { id: 'repositories', label: $localize`:@@projects.col.repositories:Repositories`, numeric: true, width: '7rem' },
    { id: 'members', label: $localize`:@@projects.col.members:Members`, numeric: true, width: '5rem' },
  ];
  protected readonly refOf = refOf;
  protected readonly classText = classLabel;
  protected readonly levelText = levelName;

  constructor() {
    // Each tab reads its list when it is shown, with the text; a text of fewer than two characters asks nothing.
    effect(() => {
      const q = this.term();
      const tab = RESULT_KINDS[this.tab()]!;
      if (q.length < 2) return;
      untracked(() => {
        if (tab === 'repository') void this.repositories.start(this.repositoriesApi.list({ q }));
        else if (tab === 'package') void this.packages.start(this.packagesApi.list({ q }));
        else void this.projects.start(this.projectsApi.list({ q }));
      });
    });
  }

  /** A tab chosen: its kind goes into the address, so a reload and a link show the same tab. */
  protected choose(index: number): void {
    const kind = RESULT_KINDS[index];
    if (!kind || index === this.tab()) return;
    void this.router.navigate(['/search'], { queryParams: { q: this.term() || null, kind }, replaceUrl: true });
  }

  /** The project and the repository of a package: the first repository the caller reads that holds it (`<project>/<key>`). */
  protected placeOf(row: PackageSummary): { project: string; key: string } {
    const [project = '', key = ''] = (row.repositories[0] ?? '').split('/');
    return { project, key };
  }

  protected packageLink(row: PackageSummary): string[] {
    return packageCommands(this.placeOf(row).project, row.format, row.name);
  }

  protected formatLabel(name: string): string {
    return (valueOf(this.formats) ?? []).find((f) => f.name === name)?.displayName ?? name;
  }

  protected classIcon(c: RepositorySummary['class']): string {
    return c === 'local' ? 'storage' : c === 'remote' ? 'cloud_download' : 'layers';
  }

  protected since(iso: string): string {
    return relativeTime(iso, this.locale);
  }

  protected roleLabel(role: ProjectRole): string {
    return projectRoleLabel(role);
  }

  /** The copy button's name and tooltip carry the address itself, as in the admin area's list (Q-UI-330). */
  protected copyValueLabel(address: string): string {
    return $localize`:@@repo.copy-address-value:Copy client address ${address}:address:`;
  }
}
