import { Component, computed, inject, resource, signal } from '@angular/core';
import { Router, RouterLink } from '@angular/router';
import { firstValueFrom } from 'rxjs';
import { collectAll } from '../../api/pagination';
import { Project, ProjectRole, ProjectsApi } from '../../api/projects-api';
import { RepositoriesApi, RepositorySummary, refOf } from '../../api/repositories-api';
import { FrequentPlaces } from '../../core/frequent-places';
import { ProjectContext } from '../../core/project-context';
import { SessionStore } from '../../core/session-store';
import { AddressField } from '../../shared/address-field';
import { valueOf } from '../../shared/paged-resource';
import { TextField } from '../../shared/access/fields';
import { Checkbox } from '../../shared/ui/checkbox';
import { TroveDialog } from '../../shared/ui/dialog';
import { VisibilityIcon, levelName } from '../../shared/ui/visibility-level';
import { DataTable, TableColumn } from '../../shared/ui/data-table';
import { PageHeader } from '../../shared/ui/page-header';
import { ProjectDialog } from './project-dialog';
import { projectRoleLabel } from './project-roles';

/** How many recently opened repositories the start page shows. */
const RECENT_SHOWN = 5;

/**
 * The start page (Q-UI-129, Q-UI-311): what the plain user comes for first. The repositories opened last in this browser with
 * their client address and **Set up client** (the search's store, ordered by the last visit, confirmed by one `GET
 * /repositories?name=…`, R-UI-096), then the projects the caller may see (`GET /api/v1/projects`). The top bar's field is the one
 * search (Q-UI-414). An anonymous visitor sees the public projects, without a role. A caller with exactly one project never
 * sees it: the start route sends them into that project. Creating a project needs `projects:write` (R-UI-041).
 */
@Component({
  selector: 'trove-projects-home',
  imports: [RouterLink, AddressField, Checkbox, DataTable, PageHeader, TextField, VisibilityIcon],
  template: `
    <trove-page-header [heading]="heading" [description]="hint()">
      @if (canCreate()) {
        <button actions class="btn btn-confirm" type="button" (click)="create()" i18n="@@projects-home.add">New project</button>
      }
    </trove-page-header>

    @if (recent().length > 0) {
      <section class="recent" aria-labelledby="recent-heading">
        <h2 id="recent-heading" i18n="@@projects-home.recent">Recently opened</h2>
        <ul>
          @for (r of recent(); track refOf(r)) {
            <li>
              <div class="recent-name">
                <a [routerLink]="['/projects', r.project, 'repositories', r.key]" translate="no">{{ r.key }}</a>
                <span class="muted" translate="no">{{ r.project }}</span>
              </div>
              <!-- The client address only (Q-UI-320); a further address is on the repository's page. -->
              @if (r.addresses[0]; as a) {
                <trove-address-field [value]="a" [label]="addressLabel(r.key)" [copyLabel]="copyLabel(r.key)" />
              }
              <a class="btn btn-default" routerLink="/setup" [queryParams]="{ repository: refOf(r) }" i18n="@@repo.content.tab.setup"
                >Set up client</a
              >
            </li>
          }
        </ul>
      </section>
    }

    <h2 class="projects-heading" i18n="@@projects-home.projects">Projects</h2>
    @if (context.projects().length > 5) {
      <trove-text-field width="name" i18n-label="@@projects.q" label="Key or name" name="q" [(value)]="q" />
    }
    @if (session.authenticated() && hasOthers()) {
      <trove-checkbox [checked]="onlyMine()" (checkedChange)="onlyMine.set($event)">
        <ng-container i18n="@@projects-home.mine">Your projects</ng-container>
      </trove-checkbox>
    }
    <trove-data-table
      [caption]="caption()"
      [columns]="columns()"
      [loaded]="context.loaded()"
      [loading]="!context.loaded()"
      [empty]="shown().length === 0"
      [emptyText]="emptyText()"
    >
      @for (p of shown(); track p.key) {
        <tr>
          <th scope="row">
            <a [routerLink]="['/projects', p.key]">{{ p.displayName || p.key }}</a>
            <span class="secondary" translate="no">{{ p.key }}</span>
          </th>
          <td>{{ p.description }}</td>
          <td class="level"><trove-visibility-icon [level]="p.visibility" />{{ levelText(p.visibility) }}</td>
          @if (!anonymous()) {
            <td>{{ p.role ? roleLabel(p.role) : '' }}</td>
          }
          <td class="num">{{ p.repositories }}</td>
          <td class="num">{{ p.members }}</td>
        </tr>
      }
    </trove-data-table>
  `,
  styleUrl: '../../shared/access/access.scss',
  styles: `
    .find {
      display: flex;
      flex-direction: column;
      gap: var(--trove-space-1);
      max-inline-size: var(--trove-address-measure);
      margin-block-end: var(--trove-space-6);
    }

    .recent ul {
      display: flex;
      flex-direction: column;
      gap: var(--trove-space-3);
      margin: var(--trove-space-2) 0 0;
      padding: 0;
      list-style: none;
    }

    .recent-name {
      display: flex;
      flex-wrap: wrap;
      align-items: baseline;
      gap: var(--trove-space-2);
    }

    .recent {
      margin-block-end: var(--trove-space-6);
    }

    .recent li {
      display: flex;
      flex-direction: column;
      align-items: flex-start;
      gap: var(--trove-space-2);
      padding-block-end: var(--trove-space-3);
      border-block-end: 1px solid var(--trove-border);
    }

    .recent li trove-address-field {
      inline-size: 100%;
    }

    .muted {
      color: var(--trove-text-muted);
    }

    h2 {
      margin-block: 0 var(--trove-space-2);
      font-size: var(--trove-heading-3);
      font-weight: 600;
    }
  `,
})
export class ProjectsHome {
  protected readonly context = inject(ProjectContext);
  protected readonly session = inject(SessionStore);
  private readonly projectsApi = inject(ProjectsApi);
  private readonly dialog = inject(TroveDialog);
  private readonly router = inject(Router);
  private readonly repositories = inject(RepositoriesApi);
  private readonly frequent = inject(FrequentPlaces);

  protected readonly heading = $localize`:@@projects-home.heading:Projects`;
  protected readonly anonymous = computed(() => this.session.state() === 'anonymous');
  protected readonly hint = computed(() =>
    this.anonymous()
      ? $localize`:@@projects-home.hint-anonymous:The projects with public content. Sign in to see your own.`
      : $localize`:@@projects-home.hint-recent:Take up a repository you opened before, or open a project.`,
  );
  protected readonly canCreate = computed(() => this.session.has('projects:write'));
  protected readonly refOf = refOf;

  /**
   * The repositories opened last, newest first, as one `GET /repositories?name=…` returns them (Q-UI-406): an entry the caller may
   * no longer read is left out, and stays stored.
   */
  private readonly opened = resource({
    params: () => this.frequent.recentRepositories().slice(0, RECENT_SHOWN).map((v) => v.id),
    loader: async ({ params }) => (params.length === 0 ? [] : (await this.repositories.list({ name: params })(null, params.length)).items),
  });
  protected readonly recent = computed<readonly RepositorySummary[]>(() => {
    const rows = new Map((valueOf(this.opened) ?? []).map((r) => [refOf(r), r]));
    return this.frequent
      .recentRepositories()
      .slice(0, RECENT_SHOWN)
      .flatMap((v) => {
        const r = rows.get(v.id);
        return r ? [r] : [];
      });
  });

  /** The filter of a long project list. */
  protected readonly q = signal('');
  protected readonly levelText = levelName;
  protected readonly onlyMine = signal(false);
  /** The memberships alone, from the server (`membership=true`); read only while the filter is on (R-UI-054). */
  private readonly mine = resource({
    params: () => (this.onlyMine() ? true : undefined),
    loader: async () => collectAll(this.projectsApi.list({ membership: true })),
  });
  /** The caller sees projects it is no member of: the filter has something to do. */
  protected readonly hasOthers = computed(() => this.context.projects().some((p) => p.role === undefined));
  /** The caller's projects first, then the others its level admits, each group in key order. */
  protected readonly shown = computed<readonly Project[]>(() => {
    const q = this.q().trim().toLowerCase();
    const source = this.onlyMine() ? (valueOf(this.mine) ?? []) : this.context.projects();
    const matching = source.filter((p) => !q || p.key.includes(q) || p.displayName.toLowerCase().includes(q));
    return [...matching.filter((p) => p.role !== undefined), ...matching.filter((p) => p.role === undefined)];
  });
  protected readonly emptyText = computed(() => {
    if (this.context.projects().length > 0) return $localize`:@@projects-home.empty-filtered:No project matches.`;
    return this.session.state() === 'anonymous'
      ? $localize`:@@projects-home.empty-anonymous:No project is public. Sign in to see your projects.`
      : $localize`:@@projects-home.empty:You are not a member of a project yet. An Owner of a project adds you with a role.`;
  });
  /** An anonymous visitor has no projects of its own: the list is the public projects, without a role. */
  protected readonly caption = computed(() =>
    this.anonymous()
      ? $localize`:@@projects-home.caption-public:Public projects`
      : $localize`:@@projects-home.caption:Your projects`,
  );
  /** The project's name first, its key under it (the style guide's Lists); the list's budget (Q-UI-308). */
  protected readonly columns = computed<readonly TableColumn[]>(() =>
    this.anonymous() ? this.allColumns.filter((c) => c.id !== 'role') : this.allColumns,
  );
  private readonly allColumns: readonly TableColumn[] = [
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

  protected addressLabel(key: string): string {
    return $localize`:@@repo.address-of:Client address of ${key}:key:`;
  }

  protected copyLabel(key: string): string {
    return $localize`:@@repo.copy-address-of:Copy client address of ${key}:key:`;
  }

  protected roleLabel(role: ProjectRole): string {
    return projectRoleLabel(role);
  }

  protected async create(): Promise<void> {
    const ref = this.dialog.open<ProjectDialog, { project?: undefined }, Project>(ProjectDialog, {
      data: {},
      maxWidth: '40rem',
    });
    const created = await firstValueFrom(ref.closed);
    if (created) {
      await this.context.refresh();
      await this.router.navigate(['/projects', created.key, 'settings']);
    }
  }
}
