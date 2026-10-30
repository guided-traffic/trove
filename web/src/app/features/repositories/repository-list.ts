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
import { MatButtonModule } from '@angular/material/button';
import { Router, RouterLink } from '@angular/router';
import { newIdempotencyKey } from '../../api/client';
import { JobReference, RepositoriesApi, RepositorySummary } from '../../api/repositories-api';
import { ApiError } from '../../api/problem';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { Confirm } from '../../shared/confirm-dialog';
import { CopyButton } from '../../shared/copy-button';
import { ErrorNotice } from '../../shared/error-notice';
import { JobProgress } from '../../shared/job-progress';
import { DataTable, TableColumn } from '../../shared/ui/data-table';
import { FilterCategory, FilterPanel, FilterToggle } from '../../shared/ui/filter-panel';
import { PagedResource, asApiError, valueOf } from '../../shared/paged-resource';
import { RecentRepositories } from './recent-repositories';
import { classLabel } from './repository-wizard';

/**
 * The repository list (R-UI-020): key, format, class, description, client address with a copy
 * button, last activity. Filters live in the query parameters (R-UI-003) and restart the cursor
 * walk; "Load more" continues it. Delete asks for the key, may offer to detach the repository from
 * the virtual repositories that include it, and follows the deletion job until the row is gone.
 */
@Component({
  selector: 'trove-repository-list',
  imports: [
    DatePipe,
    MatButtonModule,
    RouterLink,
    CopyButton,
    DataTable,
    ErrorNotice,
    FilterCategory,
    FilterPanel,
    FilterToggle,
    JobProgress,
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
  protected readonly recent = inject(RecentRepositories);

  /** Query parameters, bound by the router's component input binding. */
  readonly q = input<string | undefined>();
  readonly class = input<string | undefined>();
  readonly format = input<string | undefined>();
  readonly state = input<string | undefined>();

  protected readonly formats = resource({ loader: () => this.api.formats() });
  protected readonly list = new PagedResource<RepositorySummary>();
  /** Repositories whose deletion job is being followed, by key. */
  protected readonly deleting = signal<ReadonlyMap<string, JobReference>>(new Map());
  protected readonly deleteFailure = signal<ApiError | null>(null);
  protected readonly search = signal('');
  protected readonly stagedClass = signal('');
  protected readonly stagedFormat = signal('');
  protected readonly stagedInactive = signal(false);
  protected readonly panelOpen = signal(false);

  protected readonly canCreate = computed(() => this.session.has('repositories:write-config'));
  protected readonly canDelete = this.canCreate;
  protected readonly formatOptions = computed(() =>
    (valueOf(this.formats) ?? []).map((f) => f.name),
  );
  protected readonly filtered = computed(
    () => !!(this.q() || this.class() || this.format() || this.state()),
  );
  protected readonly activeCount = computed(
    () => [this.q(), this.class(), this.format(), this.state()].filter((v) => !!v).length,
  );

  constructor() {
    effect(() => {
      const filters = {
        q: this.q(),
        class: this.class(),
        format: this.format(),
        state: this.state(),
      };
      untracked(() => {
        this.search.set(filters.q ?? '');
        this.stagedClass.set(filters.class ?? '');
        this.stagedFormat.set(filters.format ?? '');
        this.stagedInactive.set(filters.state === 'inactive');
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
  protected readonly columns: readonly TableColumn[] = [
    { id: 'key', label: $localize`:@@repo.col.key:Key` },
    { id: 'format', label: $localize`:@@repo.col.format:Format` },
    {
      id: 'class',
      label: $localize`:@@repo.col.class:Class`,
      help: $localize`:@@repo.help.class:Local repositories hold what you upload, remote repositories fetch and cache from another registry, virtual repositories combine other repositories behind one address.`,
    },
    { id: 'description', label: $localize`:@@repo.col.description:Description` },
    {
      id: 'address',
      label: $localize`:@@repo.col.address:Client address`,
      help: $localize`:@@repo.help.address:The address that package managers and download tools use for this repository.`,
    },
    {
      id: 'activity',
      label: $localize`:@@repo.col.activity:Last activity`,
      help: $localize`:@@repo.help.activity:The last time a client read from or wrote to the repository. A dash means nothing has been recorded yet.`,
    },
    { id: 'actions', label: $localize`:@@repo.col.actions:Actions`, hideLabel: true },
  ];

  protected classIcon(c: RepositorySummary['class']): string {
    return c === 'local' ? 'storage' : c === 'remote' ? 'cloud_download' : 'layers';
  }
  protected readonly copyLabel = $localize`:@@repo.copy-address:Copy client address`;
  protected readonly deleteLabel = $localize`:@@repo.delete.progress:Repository deletion`;

  protected address(row: RepositorySummary): string {
    // The server serves absolute URLs (public base URL plus path): shown and copied as they are.
    return row.addresses[0] ?? '';
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
    });
  }

  protected clearFilters(): void {
    this.applyFilter({ q: null, class: null, format: null, state: null });
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
        const outcome = await this.api.delete(row.key, idempotencyKey, answer.checked);
        if (outcome.done) this.gone(row.key);
        else this.deleting.update((m) => new Map(m).set(row.key, outcome.job));
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

  protected gone(key: string): void {
    this.deleting.update((m) => {
      const next = new Map(m);
      next.delete(key);
      return next;
    });
    this.list.remove((r) => r.key === key);
    this.notifier.status($localize`:@@repo.delete.done:Repository ${key}:key: deleted.`);
  }

  protected deletionFailed(key: string): void {
    this.deleting.update((m) => {
      const next = new Map(m);
      next.delete(key);
      return next;
    });
    this.notifier.alert(
      $localize`:@@repo.delete.failed:Deleting repository ${key}:key: failed. Its row is kept; try again.`,
    );
    void this.list.start(
      this.api.list({
        q: this.q(),
        class: this.class(),
        format: this.format(),
        state: this.state(),
      }),
    );
  }

  protected jobEnded(key: string, state: string): void {
    if (state === 'succeeded') this.gone(key);
    else this.deletionFailed(key);
  }

  protected visit(key: string): void {
    this.recent.add(key);
  }
}
