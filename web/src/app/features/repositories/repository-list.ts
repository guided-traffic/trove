import { CdkMenu, CdkMenuItem, CdkMenuTrigger } from '@angular/cdk/menu';
import { DatePipe } from '@angular/common';
import {
  Component,
  computed,
  effect,
  inject,
  input,
  resource,
  signal,
  untracked,
} from '@angular/core';
import { ActivatedRoute, Router, RouterLink } from '@angular/router';
import { newIdempotencyKey } from '../../api/client';
import { JobReference, RepositoriesApi, RepositorySummary, refOf } from '../../api/repositories-api';
import { ApiError } from '../../api/problem';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { ProjectContext } from '../../core/project-context';
import { Confirm } from '../../shared/confirm-dialog';
import { PageHeader } from '../../shared/ui/page-header';
import { AddressField } from '../../shared/address-field';
import { ErrorNotice } from '../../shared/error-notice';
import { JobProgress } from '../../shared/job-progress';
import { DataTable, TableColumn } from '../../shared/ui/data-table';
import { FilterCategory, FilterPanel, FilterToggle } from '../../shared/ui/filter-panel';
import { PagedResource, asApiError, valueOf } from '../../shared/paged-resource';
import { FormatIconView } from '../../shared/format-icon';
import { RecentRepositories } from './recent-repositories';
import { classLabel } from './repository-wizard';

/**
 * The repository list (R-UI-020): key, project, format, class, description, client address with a copy
 * button, last activity, and the row's actions in a menu (Q-UI-302). Every address field of the list has one width, that of
 * the list's longest address up to what the other columns leave (Q-UI-300); below 48 rem of the list's width the rows stack
 * (Q-UI-306). Filters live in the query parameters (R-UI-003) and restart the cursor
 * walk; "Load more" continues it. A repository is addressed inside its project (R-REPO-002): its key links to
 * the content page `/projects/<project>/repositories/<key>` (R-UI-052), its Settings action to `…/settings`. Delete asks for the key, may offer to detach the repository from
 * the virtual repositories that include it, and follows the deletion job until the row is gone.
 */
@Component({
  selector: 'trove-repository-list',
  imports: [
    FormatIconView,
    CdkMenu,
    CdkMenuItem,
    CdkMenuTrigger,
    DatePipe,
    RouterLink,
    AddressField,
    DataTable,
    ErrorNotice,
    FilterCategory,
    FilterPanel,
    FilterToggle,
    JobProgress,
    PageHeader,
  ],
  templateUrl: './repository-list.html',
  styleUrl: './repository-list.scss',
})
export class RepositoryList {
  private readonly api = inject(RepositoriesApi);
  private readonly router = inject(Router);
  private readonly session = inject(SessionStore);
  private readonly notifier = inject(Notifier);
  private readonly confirm = inject(Confirm);
  private readonly route = inject(ActivatedRoute);
  protected readonly recent = inject(RecentRepositories);

  /** Query parameters, bound by the router's component input binding. */
  readonly q = input<string | undefined>();
  readonly class = input<string | undefined>();
  readonly format = input<string | undefined>();
  readonly state = input<string | undefined>();
  /**
   * Route parameter `:project` below `/projects/:project/repositories`: the list is that project's (Q-UI-129). In the admin
   * area it is the query parameter `project`, a filter of the instance-wide list.
   */
  readonly project = input<string | undefined>();

  protected readonly formats = resource({ loader: () => this.api.formats() });
  /** A format's name as the catalog spells it for people (`OCI (Docker)`), its key while the catalog loads. */
  protected formatLabel(name: string): string {
    return (valueOf(this.formats) ?? []).find((f) => f.name === name)?.displayName ?? name;
  }
  protected readonly list = new PagedResource<RepositorySummary>();
  /** Repositories whose deletion job is being followed, by qualified name. */
  protected readonly deleting = signal<ReadonlyMap<string, JobReference>>(new Map());
  protected readonly deleteFailure = signal<ApiError | null>(null);
  protected readonly search = signal('');
  protected readonly stagedClass = signal('');
  protected readonly stagedFormat = signal('');
  protected readonly stagedInactive = signal(false);
  protected readonly stagedProject = signal('');
  protected readonly panelOpen = signal(false);

  protected readonly canCreate = computed(() => this.session.has('repositories:write-config'));
  /** The list belongs to a project: its screens drop the project column and the project filter. */
  protected readonly inProject = (): boolean => this.route.snapshot.paramMap.has('project');
  /** The project the list is limited to: the project of the screen, or the filter of the admin list. */
  protected readonly projectKey = computed(() => this.project() || null);
  /** Create goes to the wizard of the project when the list is limited to one the caller manages. */
  protected readonly canCreateHere = computed(() => this.canCreate() || this.session.canWriteRepositories(this.projectKey()));
  /** A repository may be deleted by the capability and by the Owners of its project. */
  protected canDeleteRow(row: RepositorySummary): boolean {
    return this.session.canWriteRepositories(row.project);
  }
  /** The settings are offered to who may read or change the configuration (UI-A-06). */
  protected canConfigureRow(row: RepositorySummary): boolean {
    return this.session.has('repositories:read-config') || this.session.canWriteRepositories(row.project);
  }
  private readonly projects = inject(ProjectContext);
  protected readonly heading = $localize`:@@repo.list.heading:Repositories`;
  /**
   * The one muted line under the heading in a project (Q-UI-317): its description, and for its plain user (at most a reader, no
   * system role) whom to ask, since their sidebar has no members or settings.
   */
  protected readonly headerLine = computed(() => {
    const key = this.inProject() ? this.projectKey() : null;
    if (!key) return '';
    const p = this.projects.projects().find((x) => x.key === key);
    const description = p?.description ?? '';
    const plain = !this.session.isAdministrator() && (this.session.projectRole(key) ?? 'reader') === 'reader';
    const owners = (p?.owners ?? []).map((o) => o.replace(/^(user|group):/, ''));
    if (!plain || owners.length === 0) return description;
    const ask = $localize`:@@repo.list.ask:Ask ${owners.join(', ')}:owners: for access or a new repository.`;
    return description ? `${description} ${ask}` : ask;
  });
  protected readonly refOf = refOf;
  protected readonly formatOptions = computed(() =>
    (valueOf(this.formats) ?? []).map((f) => f.name),
  );
  /** The project filter of the panel: the admin list only, the project's list is that project whatever the filter says. */
  protected readonly filterProject = computed(() => (this.inProject() ? undefined : this.project()));
  protected readonly filtered = computed(
    () => !!(this.q() || this.class() || this.format() || this.state() || this.filterProject()),
  );
  protected readonly activeCount = computed(
    () => [this.q(), this.class(), this.format(), this.state(), this.filterProject()].filter((v) => !!v).length,
  );
  /** The recently opened repositories, shown by the admin area's list (a project's list leaves them to the start page, Q-UI-323). */
  protected readonly recentKeys = computed(() => {
    const project = this.inProject() ? this.projectKey() : null;
    return this.recent.keys().filter((k) => project === null || k.startsWith(`${project}/`));
  });

  /** The screen of a repository, given its qualified name, inside its project. */
  protected repositoryLink(ref: string): string[] {
    const [project = '', key = ''] = ref.split('/');
    return ['/projects', project, 'repositories', key];
  }

  constructor() {
    effect(() => {
      const filters = {
        q: this.q(),
        class: this.class(),
        format: this.format(),
        state: this.state(),
        project: this.projectKey() ?? undefined,
      };
      untracked(() => {
        this.search.set(filters.q ?? '');
        this.stagedClass.set(filters.class ?? '');
        this.stagedFormat.set(filters.format ?? '');
        this.stagedInactive.set(filters.state === 'inactive');
        this.stagedProject.set(this.filterProject() ?? '');
        // An applied filter is never hidden: the panel opens with it.
        if (this.activeCount() > 0) this.panelOpen.set(true);
        void this.list.start(this.api.list(filters));
      });
    });
  }

  protected classText = classLabel;
  protected readonly tableCaption = $localize`:@@repo.list.caption:Repositories`;
  protected readonly emptyText = $localize`:@@repo.list.empty:There is no repository yet.`;
  protected readonly emptyFilteredText = $localize`:@@repo.list.empty-filtered:No repository matches the filters.`;
  protected readonly applyLabel = $localize`:@@repo.filter.apply:Search`;
  /** No sort control: the repository list is ordered by key and the API takes no sort parameter. */
  protected readonly columns = computed<readonly TableColumn[]>(() =>
    this.allColumns.filter(
      // The admin area names project, format and class under the key (Q-UI-325): the address keeps that room.
      (c) => !(c.id === 'activity' && !this.showActivity()) && !(c.id === 'format' && !this.inProject()),
    ),
  );
  /**
   * The last activity is a fact of maintenance: shown to who may change the list's repositories (Q-UI-312), and only while a
   * loaded row has one, so that it is no column of dashes (Q-UI-305).
   */
  protected readonly showActivity = computed(
    () =>
      (this.inProject() ? this.session.canWriteRepositories(this.projectKey()) : this.canCreate()) &&
      this.list.items().some((r) => !!r.lastActivityAt),
  );
  private readonly allColumns: readonly TableColumn[] = [
    // The widths are the list's budget (Q-UI-308): the address takes the rest, so at 1440 px a project's list shows an address
    // of 57 characters whole.
    { id: 'key', label: $localize`:@@repo.col.repository:Repository`, width: '16rem' },
    {
      id: 'format',
      label: $localize`:@@repo.col.format:Format`,
      help: $localize`:@@repo.help.format-class:The format, and under it the class: local repositories hold what you upload, remote repositories fetch and cache from another registry, virtual repositories combine other repositories behind one address.`,
      width: '5.5rem',
      stackLabel: false,
    },
    {
      id: 'address',
      label: $localize`:@@repo.col.address:Client address`,
      help: $localize`:@@repo.help.address:The address that package managers and download tools use for this repository.`,
      fill: true,
      stackLabel: false,
    },
    {
      id: 'activity',
      label: $localize`:@@repo.col.activity:Last activity`,
      help: $localize`:@@repo.help.activity:The last time a client read from or wrote to the repository. A dash means nothing has been recorded yet.`,
      width: '7.5rem',
      // Below 64 rem of the list's width the address takes this room (UI-A-30): the admin area's list at 1280 px.
      narrow: true,
    },
    { id: 'actions', label: $localize`:@@repo.col.actions:Actions`, hideLabel: true, width: '2rem' },
  ];

  protected classIcon(c: RepositorySummary['class']): string {
    return c === 'local' ? 'storage' : c === 'remote' ? 'cloud_download' : 'layers';
  }
  protected addressLabel(row: RepositorySummary): string {
    return $localize`:@@repo.address-of:Client address of ${row.key}:key:`;
  }
  protected copyLabel(row: RepositorySummary): string {
    return $localize`:@@repo.copy-address-of:Copy client address of ${row.key}:key:`;
  }
  protected readonly deleteLabel = $localize`:@@repo.delete.progress:Repository deletion`;

  protected address(row: RepositorySummary): string {
    // The server serves absolute URLs (public base URL plus path): shown and copied as they are.
    return row.addresses[0] ?? '';
  }

  /** The length of the list's longest address: `.address-cell` sizes the column to it, so every field has one width. */
  protected readonly addressChars = computed(() => Math.max(0, ...this.list.items().map((r) => this.address(r).length)));

  protected actionsLabel(row: RepositorySummary): string {
    return $localize`:@@repo.actions-of:Actions for ${row.key}:key:`;
  }

  protected applyFilter(change: Readonly<Record<string, string | null>>): void {
    void this.router.navigate([], { queryParams: change, queryParamsHandling: 'merge' });
  }

  /** The panel's apply button: all staged values reach the query at once. */
  protected submitFilters(): void {
    this.applyFilter({
      q: this.search().trim() || null,
      class: this.stagedClass() || null,
      format: this.stagedFormat() || null,
      state: this.stagedInactive() ? 'inactive' : null,
      ...(this.inProject() ? {} : { project: this.stagedProject().trim() || null }),
    });
  }

  protected clearFilters(): void {
    this.applyFilter({ q: null, class: null, format: null, state: null, project: null });
  }

  protected async remove(row: RepositorySummary): Promise<void> {
    this.deleteFailure.set(null);
    const idempotencyKey = newIdempotencyKey();
    let offerDetach = false;
    for (;;) {
      const answer = await this.confirm.ask({
        title: $localize`:@@repo.delete.title:Delete repository ${row.key}:key:?`,
        lines: [
          $localize`:@@repo.delete.text:The repository and everything in it are deleted for good. Deletion runs as a background job; the key stays taken until it has finished.`,
        ],
        confirmLabel: $localize`:@@repo.delete.confirm:Delete repository`,
        typeToConfirm: row.key,
        choice: offerDetach
          ? $localize`:@@repo.delete.detach:Also remove it from the virtual repositories that include it`
          : undefined,
      });
      if (answer === null) return;
      try {
        const ref = refOf(row);
        const outcome = await this.api.delete(ref, idempotencyKey, answer.checked);
        if (outcome.done) this.gone(ref);
        else this.deleting.update((m) => new Map(m).set(ref, outcome.job));
        return;
      } catch (e) {
        const error = asApiError(e);
        if (error.code === 'repository_referenced' && !offerDetach) {
          // The choice of R-UI-020: ask again, this time with the detach option.
          offerDetach = true;
          continue;
        }
        this.deleteFailure.set(error);
        return;
      }
    }
  }

  protected gone(ref: string): void {
    this.deleting.update((m) => {
      const next = new Map(m);
      next.delete(ref);
      return next;
    });
    this.list.remove((r) => refOf(r) === ref);
    this.notifier.status($localize`:@@repo.delete.done:Repository ${ref}:key: deleted.`);
  }

  protected deletionFailed(ref: string): void {
    this.deleting.update((m) => {
      const next = new Map(m);
      next.delete(ref);
      return next;
    });
    this.notifier.alert(
      $localize`:@@repo.delete.failed:Deleting repository ${ref}:key: failed. Its row is kept; try again.`,
    );
    void this.list.start(
      this.api.list({
        q: this.q(),
        class: this.class(),
        format: this.format(),
        state: this.state(),
        project: this.projectKey() ?? undefined,
      }),
    );
  }

  protected jobEnded(ref: string, state: string): void {
    if (state === 'succeeded') this.gone(ref);
    else this.deletionFailed(ref);
  }

  protected visit(ref: string): void {
    this.recent.add(ref);
  }
}
