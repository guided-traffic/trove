import { Component, computed, effect, inject, input, resource, signal, untracked } from '@angular/core';
import { Router, RouterLink } from '@angular/router';
import { firstValueFrom } from 'rxjs';
import { PackageSummary, PackagesApi } from '../../api/packages-api';
import { Project, ProjectRole } from '../../api/projects-api';
import { RepositoriesApi, isConsumerView } from '../../api/repositories-api';
import { ProjectContext } from '../../core/project-context';
import { ServerAddress } from '../../core/server-address';
import { SessionStore } from '../../core/session-store';
import { AddressField } from '../../shared/address-field';
import { FormatIconView } from '../../shared/format-icon';
import { PagedResource, valueOf } from '../../shared/paged-resource';
import { TextField } from '../../shared/access/fields';
import { TroveDialog } from '../../shared/ui/dialog';
import { DataTable, TableColumn } from '../../shared/ui/data-table';
import { PageHeader } from '../../shared/ui/page-header';
import { packageCommands } from '../packages/package-links';
import { RecentRepositories } from '../repositories/recent-repositories';
import { clientAddress } from '../repositories/repository-model';
import { ProjectDialog } from './project-dialog';
import { projectRoleLabel } from './project-roles';

/** A repository opened before, with what the start page shows of it. */
interface RecentRepository {
  readonly ref: string;
  readonly project: string;
  readonly key: string;
  readonly addresses: readonly string[];
}

/** How many recently opened repositories the start page shows. */
const RECENT_SHOWN = 5;

/**
 * The start page (Q-UI-129, Q-UI-311): what the plain user comes for first. A search for a package across the caller's
 * projects (`GET /api/v1/packages?q=`, what the caller may read), the repositories opened last in this browser with their client
 * address and **Set up client**, then the projects the caller may see (`GET /api/v1/projects`). A caller with exactly one
 * project never sees it: the start route sends them into that project. Creating a project needs `projects:write` (R-UI-041).
 */
@Component({
  selector: 'trove-projects-home',
  imports: [RouterLink, AddressField, DataTable, FormatIconView, PageHeader, TextField],
  template: `
    <trove-page-header [heading]="heading" [description]="hint">
      @if (canCreate()) {
        <button actions class="btn btn-confirm" type="button" (click)="create()" i18n="@@projects-home.add">New project</button>
      }
    </trove-page-header>

    <section class="find" aria-labelledby="find-heading">
      <h2 id="find-heading" class="visually-hidden" i18n="@@projects-home.find.heading">Find a package</h2>
      <label class="field-label" for="find-package" i18n="@@projects-home.find.label">Find a package</label>
      <input
        id="find-package"
        class="text-input"
        type="search"
        autocomplete="off"
        spellcheck="false"
        i18n-placeholder="@@projects-home.find.placeholder"
        placeholder="Name or description, for example ui-kit"
        [value]="term()"
        (input)="term.set($any($event.target).value)"
      />
      @if (searching()) {
        @if (found.loaded() && found.items().length === 0) {
          <p class="muted" role="status" i18n="@@projects-home.find.none">No package you may read matches.</p>
        }
        <ul class="found">
          @for (p of found.items(); track p.id) {
            <li>
              <a [routerLink]="linkOf(p)" translate="no">{{ p.name }}</a>
              <span class="muted">
                <trove-format-icon [format]="p.format" />
                <span translate="no">{{ p.format }} {{ p.latestVersion }}</span>
              </span>
            </li>
          }
        </ul>
      }
    </section>

    @if (recent().length > 0) {
      <section class="recent" aria-labelledby="recent-heading">
        <h2 id="recent-heading" i18n="@@projects-home.recent">Recently opened</h2>
        <ul>
          @for (r of recent(); track r.ref) {
            <li>
              <div class="recent-name">
                <a [routerLink]="['/projects', r.project, 'repositories', r.key]" translate="no">{{ r.key }}</a>
                <span class="muted" translate="no">{{ r.project }}</span>
              </div>
              <!-- The client address only (Q-UI-320); a further address is on the repository's page. -->
              @if (r.addresses[0]; as a) {
                <trove-address-field [value]="a" [label]="addressLabel(r.key)" [copyLabel]="copyLabel(r.key)" />
              }
              <a class="btn btn-default" routerLink="/setup" [queryParams]="{ repository: r.ref }" i18n="@@repo.content.tab.setup"
                >Set up client</a
              >
            </li>
          }
        </ul>
      </section>
    }

    <h2 class="projects-heading" i18n="@@projects-home.projects">Projects</h2>
    @if (context.projects().length > 5) {
      <trove-text-field i18n-label="@@projects.q" label="Key or name" name="q" [(value)]="q" />
    }
    <trove-data-table
      [caption]="caption"
      [columns]="columns"
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
          <td>{{ p.role ? roleLabel(p.role) : '' }}</td>
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

    .found,
    .recent ul {
      display: flex;
      flex-direction: column;
      gap: var(--trove-space-3);
      margin: var(--trove-space-2) 0 0;
      padding: 0;
      list-style: none;
    }

    .found li,
    .recent-name {
      display: flex;
      flex-wrap: wrap;
      align-items: baseline;
      gap: var(--trove-space-2);
    }

    .found trove-format-icon {
      font-size: 1rem;
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
  private readonly session = inject(SessionStore);
  private readonly dialog = inject(TroveDialog);
  private readonly router = inject(Router);
  private readonly packages = inject(PackagesApi);
  private readonly repositories = inject(RepositoriesApi);
  private readonly recentStore = inject(RecentRepositories);
  private readonly serverAddress = inject(ServerAddress);

  protected readonly heading = $localize`:@@projects-home.heading:Projects`;
  protected readonly hint = $localize`:@@projects-home.hint-short:Find a package, take up a repository you used before, or open a project.`;
  protected readonly canCreate = computed(() => this.session.has('projects:write'));

  /** The package search's text from the address (`?find=`), as the search results page hands it over (Q-UI-390). */
  readonly find = input<string | undefined>();
  /** The text of the package search; two characters start it, 300 ms after the last key. */
  protected readonly term = signal('');
  protected readonly found = new PagedResource<PackageSummary>();
  protected readonly searching = computed(() => this.term().trim().length >= 2);

  private readonly formats = resource({ loader: () => this.repositories.formats() });
  private readonly baseUrl = resource({ loader: () => this.serverAddress.baseUrl() });
  /** The repositories opened last, read once per change of the list; one that cannot be read is left out. */
  private readonly opened = resource({
    params: () => this.recentStore.keys().slice(0, RECENT_SHOWN),
    loader: async ({ params }) => {
      const docs = await Promise.all(params.map((ref) => this.repositories.get(ref).catch(() => null)));
      return docs.flatMap((d, i) => (d ? [{ ref: params[i]!, doc: d.value }] : []));
    },
  });
  protected readonly recent = computed<readonly RecentRepository[]>(() =>
    (valueOf(this.opened) ?? []).map(({ ref, doc }) => {
      const [project = '', key = ''] = ref.split('/');
      const format = (valueOf(this.formats) ?? []).find((f) => f.name === doc.format);
      const addresses = isConsumerView(doc) ? doc.addresses : [clientAddress(valueOf(this.baseUrl), format, ref)].filter((a) => !!a);
      return { ref, project, key, addresses };
    }),
  );

  /** The filter of a long project list. */
  protected readonly q = signal('');
  protected readonly shown = computed<readonly Project[]>(() => {
    const q = this.q().trim().toLowerCase();
    return this.context.projects().filter((p) => !q || p.key.includes(q) || p.displayName.toLowerCase().includes(q));
  });
  protected readonly emptyText = computed(() => {
    if (this.context.projects().length > 0) return $localize`:@@projects-home.empty-filtered:No project matches.`;
    return this.session.state() === 'anonymous'
      ? $localize`:@@projects-home.empty-anonymous:Sign in to see your projects.`
      : $localize`:@@projects-home.empty:You are not a member of a project yet. An Owner of a project adds you with a role.`;
  });
  protected readonly caption = $localize`:@@projects-home.caption:Your projects`;
  /** The project's name first, its key under it (the style guide's Lists); the list's budget (Q-UI-308). */
  protected readonly columns: readonly TableColumn[] = [
    { id: 'name', label: $localize`:@@projects.col.project:Project`, width: '14rem' },
    { id: 'description', label: $localize`:@@projects-home.col.description:Description` },
    {
      id: 'role',
      label: $localize`:@@projects.col.role:Your role`,
      help: $localize`:@@projects.help.role:Your role in the project; empty when you see it through a system role and are no member.`,
      width: '7rem',
    },
    { id: 'repositories', label: $localize`:@@projects.col.repositories:Repositories`, numeric: true, width: '7rem' },
    { id: 'members', label: $localize`:@@projects.col.members:Members`, numeric: true, width: '5rem' },
  ];

  constructor() {
    effect(() => {
      const find = this.find();
      if (find) untracked(() => this.term.set(find));
    });
    let timer: ReturnType<typeof setTimeout> | undefined;
    effect((onCleanup) => {
      const q = this.term().trim();
      if (q.length < 2) return;
      timer = setTimeout(() => untracked(() => void this.found.start(this.packages.list({ q }), 10)), 300);
      onCleanup(() => clearTimeout(timer));
    });
  }

  /** A package opens in the project of the first repository that holds it. */
  protected linkOf(p: PackageSummary): string[] {
    const project = (p.repositories[0] ?? '').split('/')[0] ?? '';
    return packageCommands(project, p.format, p.name);
  }

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
