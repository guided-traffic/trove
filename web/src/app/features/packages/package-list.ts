import { DatePipe, DecimalPipe } from '@angular/common';
import {
  Component,
  LOCALE_ID,
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
import { PackageFilters, PackageSort, PackageSummary, PackagesApi } from '../../api/packages-api';
import { RepositoriesApi } from '../../api/repositories-api';
import { PagedResource, valueOf } from '../../shared/paged-resource';
import { DataTable, TableColumn, TableSort } from '../../shared/ui/data-table';
import { FilterCategory, FilterPanel, FilterToggle } from '../../shared/ui/filter-panel';
import { relativeTime } from './format-time';
import { packageCommands } from './package-links';
import { PackageMemory, SavedView } from './package-memory';

type Param = string | readonly string[] | undefined;
type ListTab = 'all' | 'recent' | 'views';
export type CreatedPeriod = '7' | '30' | '90' | 'custom';

const DAY_MS = 86_400_000;

function many(value: Param): string[] {
  return value === undefined ? [] : typeof value === 'string' ? [value] : [...value];
}

/** One removable chip of an applied filter. */
interface Chip {
  readonly key: string;
  readonly label: string;
  /** The query change that removes it. */
  readonly remove: Readonly<Record<string, string | readonly string[] | null>>;
}

/**
 * The packages list (R-UI-006), the start page of the packages area. It uses the shared data table
 * and the shared filter panel (Q-UI-68): the filters are the panel's categories, the sort is a
 * column header, "Load more" is the table's. Filters, sort and tab live in the query parameters
 * (R-UI-003) and every change restarts the cursor walk, because a cursor is bound to its filters and
 * sort (R-API-010). Filtering and sorting happen on the server (R-API-029); the screen sorts and
 * filters nothing itself. "Recently viewed" and "Saved views" are browser-storage features and
 * disappear where storage is blocked.
 */
@Component({
  selector: 'trove-package-list',
  imports: [
    DatePipe,
    DecimalPipe,
    MatButtonModule,
    RouterLink,
    DataTable,
    FilterCategory,
    FilterPanel,
    FilterToggle,
  ],
  templateUrl: './package-list.html',
  styleUrl: './package-list.scss',
})
export class PackageList {
  private readonly api = inject(PackagesApi);
  private readonly repositories = inject(RepositoriesApi);
  private readonly router = inject(Router);
  protected readonly memory = inject(PackageMemory);
  private readonly locale = inject(LOCALE_ID);

  /** Query parameters, bound by the router's component input binding. */
  readonly format = input<Param>();
  readonly repository = input<Param>();
  readonly q = input<string | undefined>();
  readonly keyword = input<string | undefined>();
  readonly period = input<string | undefined>();
  readonly from = input<string | undefined>();
  readonly to = input<string | undefined>();
  readonly sort = input<string | undefined>();
  readonly tab = input<string | undefined>();

  protected readonly formats = resource({ loader: () => this.repositories.formats() });
  protected readonly repositoryList = resource({ loader: () => this.repositories.listAll() });
  protected readonly list = new PagedResource<PackageSummary>();

  protected readonly panelOpen = signal(false);
  protected readonly search = signal('');
  protected readonly stagedKeyword = signal('');
  protected readonly stagedFormats = signal<readonly string[]>([]);
  protected readonly stagedRepositories = signal<readonly string[]>([]);
  protected readonly stagedPeriod = signal('');
  protected readonly stagedFrom = signal('');
  protected readonly stagedTo = signal('');
  protected readonly viewName = signal('');
  protected readonly renaming = signal<string | null>(null);
  protected readonly renameText = signal('');

  protected readonly activeTab = computed<ListTab>(() => {
    const t = this.tab();
    return this.memory.available() && (t === 'recent' || t === 'views') ? t : 'all';
  });
  protected readonly formatNames = computed(() => many(this.format()));
  protected readonly repositoryNames = computed(() => many(this.repository()));
  protected readonly apiSort = computed<PackageSort>(() => {
    const s = this.sort();
    const known: readonly string[] = ['name', '-name', 'createdAt', '-createdAt', 'updatedAt', '-updatedAt'];
    return s !== undefined && known.includes(s) ? (s as PackageSort) : 'name';
  });
  protected readonly tableSort = computed<TableSort>(() => {
    const s = this.apiSort();
    return { column: s.replace('-', ''), direction: s.startsWith('-') ? 'desc' : 'asc' };
  });

  protected readonly filters = computed<PackageFilters>(() => {
    const range = this.createdRange();
    return {
      format: this.formatNames(),
      repository: this.repositoryNames(),
      q: this.q(),
      keyword: this.keyword(),
      createdFrom: range.from,
      createdTo: range.to,
      sort: this.apiSort(),
    };
  });
  protected readonly chips = computed<readonly Chip[]>(() => {
    const out: Chip[] = [];
    const typeLabel = $localize`:@@pkg.filter.type:Package type`;
    for (const f of this.formatNames()) {
      out.push({
        key: `format:${f}`,
        label: `${typeLabel}: ${this.formatLabel(f)}`,
        remove: { format: this.formatNames().filter((x) => x !== f) },
      });
    }
    const repoLabel = $localize`:@@pkg.filter.repository:Repository`;
    for (const r of this.repositoryNames()) {
      out.push({
        key: `repository:${r}`,
        label: `${repoLabel}: ${r}`,
        remove: { repository: this.repositoryNames().filter((x) => x !== r) },
      });
    }
    if (this.q()) {
      out.push({
        key: 'q',
        label: `${$localize`:@@pkg.filter.text:Name or description`}: ${this.q()}`,
        remove: { q: null },
      });
    }
    if (this.keyword()) {
      out.push({
        key: 'keyword',
        label: `${$localize`:@@pkg.filter.keyword:Keyword`}: ${this.keyword()}`,
        remove: { keyword: null },
      });
    }
    const created = this.createdLabel();
    if (created) {
      out.push({
        key: 'created',
        label: `${$localize`:@@pkg.filter.created:Created`}: ${created}`,
        remove: { period: null, from: null, to: null },
      });
    }
    return out;
  });
  protected readonly filtered = computed(() => this.chips().length > 0);
  protected readonly activeCount = computed(() => this.chips().length);

  protected readonly formatOptions = computed(() =>
    (valueOf(this.formats) ?? []).filter((f) => f.name !== 'generic'),
  );
  protected readonly repositoryOptions = computed(() =>
    (valueOf(this.repositoryList) ?? []).filter((r) => r.format !== 'generic').map((r) => r.key),
  );

  protected readonly tableCaption = $localize`:@@pkg.list.caption:Packages`;
  protected readonly emptyText = $localize`:@@pkg.list.empty:No package has been published yet.`;
  protected readonly emptyFilteredText = $localize`:@@pkg.list.empty-filtered:No package matches the filters.`;
  protected readonly applyLabel = $localize`:@@pkg.filter.apply:Search`;
  protected readonly columns: readonly TableColumn[] = [
    { id: 'type', label: $localize`:@@pkg.col.type:Type` },
    { id: 'name', label: $localize`:@@pkg.col.name:Name`, sortable: true },
    {
      id: 'latest',
      label: $localize`:@@pkg.col.latest:Latest version`,
      help: $localize`:@@pkg.help.latest:The highest stable version you may see. A pre-release is shown only while no stable version exists.`,
    },
    { id: 'description', label: $localize`:@@pkg.col.description:Description` },
    {
      id: 'createdAt',
      label: $localize`:@@pkg.col.created:First created`,
      sortable: true,
      help: $localize`:@@pkg.help.created:When the first version you may see was published.`,
    },
    {
      id: 'updatedAt',
      label: $localize`:@@pkg.col.updated:Updated`,
      sortable: true,
      help: $localize`:@@pkg.help.updated:When the latest version you may see was published.`,
    },
    {
      id: 'versions',
      label: $localize`:@@pkg.col.versions:Versions`,
      numeric: true,
      help: $localize`:@@pkg.help.versions:The number of versions you may see.`,
    },
    {
      id: 'repositories',
      label: $localize`:@@pkg.col.repositories:Repositories`,
      numeric: true,
      help: $localize`:@@pkg.help.repositories:The number of repositories that hold a version of the package.`,
    },
    {
      id: 'downloads',
      label: $localize`:@@pkg.col.downloads:Downloads`,
      numeric: true,
      help: $localize`:@@pkg.help.downloads:All downloads of the versions you may see.`,
    },
  ];
  protected readonly commands = packageCommands;
  protected readonly periods: readonly { value: CreatedPeriod; label: string }[] = [
    { value: '7', label: $localize`:@@pkg.period.7:Last 7 days` },
    { value: '30', label: $localize`:@@pkg.period.30:Last 30 days` },
    { value: '90', label: $localize`:@@pkg.period.90:Last 90 days` },
    { value: 'custom', label: $localize`:@@pkg.period.custom:Custom period` },
  ];

  private defaultChecked = false;

  constructor() {
    effect(() => {
      const filters = this.filters();
      untracked(() => {
        this.search.set(this.q() ?? '');
        this.stagedKeyword.set(this.keyword() ?? '');
        this.stagedFormats.set(this.formatNames());
        this.stagedRepositories.set(this.repositoryNames());
        this.stagedPeriod.set(this.period() ?? '');
        this.stagedFrom.set(this.from() ?? '');
        this.stagedTo.set(this.to() ?? '');
        // An applied filter is never hidden: the panel opens with it.
        if (this.activeCount() > 0) this.panelOpen.set(true);
        void this.list.start(this.api.list(filters));
      });
    });
    // A list opened without any query lands on the default saved view, once.
    effect(() => {
      const view = this.memory.defaultView();
      if (this.defaultChecked || !view) return;
      this.defaultChecked = true;
      if (!this.filtered() && !this.sort() && !this.tab()) void this.router.navigate([], { queryParams: view.query, replaceUrl: true });
    });
  }

  protected since(iso: string): string {
    return relativeTime(iso, this.locale);
  }

  protected formatLabel(name: string): string {
    return (valueOf(this.formats) ?? []).find((f) => f.name === name)?.displayName ?? name;
  }

  protected formatIcon(name: string): string {
    return name === 'nuget' ? 'extension' : 'inventory_2';
  }

  private createdRange(): { from: string | undefined; to: string | undefined } {
    const p = this.period();
    if (p === '7' || p === '30' || p === '90') {
      return { from: new Date(Date.now() - Number(p) * DAY_MS).toISOString(), to: undefined };
    }
    if (p === 'custom') {
      const from = this.from() ? `${this.from()}T00:00:00.000Z` : undefined;
      // The end day is included, the API's upper bound is exclusive.
      const to = this.to() ? new Date(Date.parse(`${this.to()}T00:00:00.000Z`) + DAY_MS).toISOString() : undefined;
      return { from, to };
    }
    return { from: undefined, to: undefined };
  }

  private createdLabel(): string | null {
    const p = this.period();
    if (p === '7' || p === '30' || p === '90') return this.periods.find((x) => x.value === p)?.label ?? null;
    if (p === 'custom' && (this.from() || this.to())) {
      return `${this.from() ?? '…'} – ${this.to() ?? '…'}`;
    }
    return null;
  }

  protected stageFormat(value: string, on: boolean): void {
    this.stagedFormats.update((v) => (on ? [...new Set([...v, value])] : v.filter((x) => x !== value)));
  }

  protected stageRepository(value: string, on: boolean): void {
    this.stagedRepositories.update((v) => (on ? [...new Set([...v, value])] : v.filter((x) => x !== value)));
  }

  protected submitFilters(): void {
    const custom = this.stagedPeriod() === 'custom';
    this.navigate({
      format: [...this.stagedFormats()],
      repository: [...this.stagedRepositories()],
      q: this.search().trim() || null,
      keyword: this.stagedKeyword().trim() || null,
      period: this.stagedPeriod() || null,
      from: custom ? this.stagedFrom() || null : null,
      to: custom ? this.stagedTo() || null : null,
    });
  }

  protected clearFilters(): void {
    this.navigate({ format: null, repository: null, q: null, keyword: null, period: null, from: null, to: null });
  }

  protected removeLabel(label: string): string {
    return $localize`:@@pkg.chips.remove:Remove the filter ${label}:filter:`;
  }

  protected removeChip(chip: Chip): void {
    const change: Record<string, string | readonly string[] | null> = {};
    for (const [k, v] of Object.entries(chip.remove)) change[k] = Array.isArray(v) && v.length === 0 ? null : v;
    this.navigate(change);
  }

  protected sortBy(sort: TableSort): void {
    this.navigate({ sort: `${sort.direction === 'desc' ? '-' : ''}${sort.column}` });
  }

  protected saveView(): void {
    const query: Record<string, string | readonly string[]> = {};
    const add = (k: string, v: Param): void => {
      if (v !== undefined && v !== '' && v.length !== 0) query[k] = v;
    };
    add('format', this.format());
    add('repository', this.repository());
    add('q', this.q());
    add('keyword', this.keyword());
    add('period', this.period());
    add('from', this.from());
    add('to', this.to());
    add('sort', this.sort());
    if (this.memory.saveView(this.viewName(), query)) this.viewName.set('');
  }

  protected startRename(view: SavedView): void {
    this.renaming.set(view.id);
    this.renameText.set(view.name);
  }

  protected finishRename(view: SavedView): void {
    this.memory.renameView(view.id, this.renameText());
    this.renaming.set(null);
  }

  private navigate(queryParams: Record<string, string | readonly string[] | null>): void {
    void this.router.navigate([], { queryParams, queryParamsHandling: 'merge' });
  }
}
