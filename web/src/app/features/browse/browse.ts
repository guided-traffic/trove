import { DOCUMENT, DatePipe, NgTemplateOutlet } from '@angular/common';
import {
  Component,
  DestroyRef,
  LOCALE_ID,
  computed,
  effect,
  inject,
  input,
  resource,
  signal,
  untracked,
  viewChild,
} from '@angular/core';
import { Tree, TreeItem, TreeItemGroup } from '@angular/aria/tree';
import { MatButtonModule } from '@angular/material/button';
import { MatDialog } from '@angular/material/dialog';
import { MatMenuModule } from '@angular/material/menu';
import { MatTabsModule } from '@angular/material/tabs';
import { ActivatedRoute, Router, RouterLink } from '@angular/router';
import { firstValueFrom } from 'rxjs';
import { newIdempotencyKey } from '../../api/client';
import { FilesApi, Item, isFilesAddress } from '../../api/files-api';
import { ApiError } from '../../api/problem';
import {
  JobReference,
  RepositoriesApi,
  Repository,
  RepositoryConsumerView,
  RepositorySummary,
  isConsumerView,
} from '../../api/repositories-api';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { Confirm } from '../../shared/confirm-dialog';
import { CopyButton } from '../../shared/copy-button';
import { ErrorNotice } from '../../shared/error-notice';
import { JobProgress } from '../../shared/job-progress';
import { PagedResource, asApiError, valueOf } from '../../shared/paged-resource';
import { DataTable, TableColumn } from '../../shared/ui/data-table';
import { FilterCategory, FilterPanel, FilterToggle } from '../../shared/ui/filter-panel';
import { formatSize } from '../packages/format-size';
import { relativeTime } from '../packages/format-time';
import { FolderDialog, FolderDialogData } from './folder-dialog';
import { MORE_PREFIX, NodeMeta, TreeNode, ancestorsOf, nameOf, nodeId, parentOf } from './tree-node';
import { UploadDialog, UploadDialogData } from './upload-dialog';

/** `ui.browser.page_size` (R-UI-010, default 200). The UI cannot read the configuration, so it is a constant. */
export const BROWSER_PAGE_SIZE = 200;

/** A delete of a folder with more items than this asks for the folder name (R-UI-011). */
export const TYPE_TO_CONFIRM_ABOVE = 100;

/** How long the filter field waits after the last keystroke before it narrows the repositories. */
export const FILTER_DELAY_MS = 250;

type Tab = 'contents' | 'general' | 'statistics' | 'api';
type SelectionKind = 'repository' | 'folder' | 'file';
/** One removable chip of an applied filter. */
interface Chip {
  readonly key: string;
  readonly label: string;
  readonly remove: Readonly<Record<string, string | null>>;
}

const CLASS_ORDER: Record<string, number> = { virtual: 0, local: 1, remote: 2 };
const CLASSES = ['virtual', 'local', 'remote'] as const;
const ARCHIVE = /\.(zip|tgz|gz|tar|jar|war|whl|nupkg|7z|bz2|xz)$/i;

/** A POSIX shell word for `value`: single quotes, with an embedded single quote closed and escaped. */
const shellQuote = (value: string): string => `'${value.replaceAll("'", "'\\''")}'`;

/**
 * The file browser (R-UI-010): two bordered panes that fill the height of the content area. The
 * tree pane has a toolbar (a filter field that narrows the repositories as you type, the shared
 * filter panel for class and format with removable chips, a sort menu), the readable repositories
 * at the root and children loaded lazily in pages of `BROWSER_PAGE_SIZE` with a "Load more" entry.
 * The details pane has the path of the selection as a breadcrumb, the actions of R-UI-011 as a
 * toolbar, and the content: a repository's summary, a folder's children as the shared data table,
 * a file's facts, API request and statistics. The selection is the URL (`/browse/{repo}/{path…}`)
 * and a deep link expands the ancestors. On a narrow screen one pane shows at a time. The tree
 * follows the WAI-ARIA tree pattern through `@angular/aria`; the context-menu key and Shift+F10
 * on the tree move focus to the actions.
 */
@Component({
  selector: 'trove-browse',
  imports: [
    DatePipe,
    NgTemplateOutlet,
    MatButtonModule,
    MatMenuModule,
    MatTabsModule,
    RouterLink,
    Tree,
    TreeItem,
    TreeItemGroup,
    CopyButton,
    DataTable,
    ErrorNotice,
    FilterCategory,
    FilterPanel,
    FilterToggle,
    JobProgress,
  ],
  templateUrl: './browse.html',
})
export class Browse {
  protected readonly valueOf = valueOf;
  private readonly repositories = inject(RepositoriesApi);
  private readonly files = inject(FilesApi);
  private readonly router = inject(Router);
  private readonly session = inject(SessionStore);
  private readonly notifier = inject(Notifier);
  private readonly confirm = inject(Confirm);
  private readonly dialog = inject(MatDialog);
  private readonly dom = inject(DOCUMENT);
  private readonly locale = inject(LOCALE_ID);
  private readonly destroyRef = inject(DestroyRef);
  protected readonly title = inject(ActivatedRoute).snapshot.title ?? '';

  /** Route parameters of the matcher and the query parameters, bound by the router. */
  readonly repo = input<string | undefined>();
  readonly path = input<string | undefined>();
  readonly q = input<string | undefined>();
  readonly class = input<string | undefined>();
  readonly format = input<string | undefined>();
  readonly sort = input<string | undefined>();
  readonly tab = input<string | undefined>();

  private readonly treeRef = viewChild(Tree);

  protected readonly formats = resource({ loader: () => this.repositories.formats() });
  protected readonly list = new PagedResource<RepositorySummary>();
  /** Every node ever shown, by id, so that a reload keeps the expanded state. */
  private readonly nodes = new Map<string, TreeNode>();
  private readonly extraRoot = signal<TreeNode | null>(null);
  /** The node of the selection, once the tree has found it; the folder table reads its children. */
  protected readonly selectedNode = signal<TreeNode | null>(null);
  private revealRun = 0;
  private filterTimer: ReturnType<typeof setTimeout> | undefined;

  /** The text of the filter field; the query parameter `q` follows it after a short pause. */
  protected readonly search = signal('');
  protected readonly panelOpen = signal(false);
  protected readonly stagedClass = signal('');
  protected readonly stagedFormat = signal('');
  protected readonly deletion = signal<{ job: JobReference; parent: string } | null>(null);
  protected readonly actionFailure = signal<ApiError | null>(null);

  protected readonly selected = computed(() => {
    const repo = this.repo();
    return repo ? { repo, path: this.path() ?? '' } : null;
  });
  protected readonly selectedId = computed(() => {
    const s = this.selected();
    return s ? (s.path === '' ? s.repo : `${s.repo}/${s.path}`) : null;
  });
  protected readonly treeValue = computed(() => {
    const id = this.selectedId();
    return id ? [id] : [];
  });

  protected readonly roots = computed<readonly TreeNode[]>(() => {
    const roots = this.list.items().map((r) => this.rootOf(r));
    const extra = this.extraRoot();
    if (extra && !roots.some((r) => r.id === extra.id)) roots.push(extra);
    if (this.sort() === 'class') {
      return roots.sort(
        (a, b) =>
          (CLASS_ORDER[a.meta().cls ?? ''] ?? 9) - (CLASS_ORDER[b.meta().cls ?? ''] ?? 9) ||
          a.label.localeCompare(b.label),
      );
    }
    return roots.sort((a, b) => a.label.localeCompare(b.label));
  });

  protected readonly chips = computed<readonly Chip[]>(() => {
    const out: Chip[] = [];
    const cls = this.class();
    if (cls) {
      out.push({
        key: 'class',
        label: `${$localize`:@@repo.col.class:Class`}: ${this.classLabel(cls)}`,
        remove: { class: null },
      });
    }
    const format = this.format();
    if (format) {
      out.push({
        key: 'format',
        label: `${$localize`:@@repo.col.format:Format`}: ${format}`,
        remove: { format: null },
      });
    }
    return out;
  });
  protected readonly filtered = computed(() => this.chips().length > 0 || !!this.q());

  /** The repository of the selection: its summary, the actions it gates and its API view. */
  protected readonly repositoryRead = resource<
    Repository | RepositoryConsumerView | undefined,
    string | null
  >({
    params: () => this.selected()?.repo ?? null,
    loader: async ({ params }) => (params ? (await this.repositories.get(params)).value : undefined),
  });
  protected readonly repositoryDoc = computed(() => valueOf(this.repositoryRead));
  protected readonly detail = resource<Item | undefined, { repo: string; path: string } | null>({
    params: () => {
      const s = this.selected();
      return s && s.path !== '' ? s : null;
    },
    loader: async ({ params }) => (params ? this.files.item(params.repo, params.path) : undefined),
  });
  protected readonly item = computed(() => valueOf(this.detail) ?? null);
  /** The repository as the details show it, when the selection is the repository itself. */
  protected readonly repository = computed(() =>
    this.selected()?.path === '' ? (this.repositoryDoc() ?? null) : null,
  );
  protected readonly fullRepository = computed<Repository | null>(() => {
    const r = this.repository();
    return r && !isConsumerView(r) ? r : null;
  });
  protected readonly detailFailure = computed(() => {
    const sel = this.selected();
    const e = sel?.path === '' ? this.repositoryRead.error() : this.detail.error();
    return e ? asApiError(e) : null;
  });
  protected readonly detailLoading = computed(() => {
    const sel = this.selected();
    if (!sel) return false;
    return sel.path === ''
      ? this.repositoryRead.isLoading() && !this.repositoryDoc()
      : this.detail.isLoading() && !this.item();
  });
  protected readonly selectionKind = computed<SelectionKind | null>(() => {
    const sel = this.selected();
    if (!sel) return null;
    if (sel.path === '') return 'repository';
    return this.item()?.kind ?? null;
  });
  protected readonly members = computed(() => this.fullRepository()?.virtual?.members ?? []);

  /** What the caller may do at the selection (R-UI-005): one batched check, cached for 60 seconds. */
  protected readonly allowed = resource({
    params: () => this.selected(),
    loader: async ({ params }) => {
      if (!params) return { deploy: false, overwrite: false, delete: false };
      const at = { repository: params.repo, ...(params.path ? { path: params.path } : {}) };
      const [deploy, overwrite, del] = await this.session.check([
        { ...at, action: 'deploy' },
        { ...at, action: 'overwrite' },
        { ...at, action: 'delete' },
      ]);
      return { deploy: !!deploy, overwrite: !!overwrite, delete: !!del };
    },
  });
  protected readonly can = computed(
    () => valueOf(this.allowed) ?? { deploy: false, overwrite: false, delete: false },
  );
  protected readonly canCreateRepository = computed(() => this.session.has('repositories:write-config'));

  /** Uploads and folders are for local repositories of a raw-files format (R-UI-012, R-API-025). */
  protected readonly acceptsUploads = computed(() => {
    const doc = this.repositoryDoc();
    if (!doc || doc.class !== 'local') return false;
    const format = valueOf(this.formats)?.find((f) => f.name === doc.format);
    return !!format && isFilesAddress(format.protocolBasePath);
  });

  protected readonly stats = resource({
    params: () =>
      this.tabName() === 'statistics' && this.item()?.kind === 'file' ? this.selected() : null,
    loader: ({ params }) =>
      params ? this.files.stats(params.repo, params.path) : Promise.resolve(null),
  });
  protected readonly statsFailure = computed(() => {
    const e = this.stats.error();
    return e ? asApiError(e) : null;
  });

  /** The tabs of the details by kind of selection; the first is the default. */
  protected readonly tabNames = computed<readonly Tab[]>(() => {
    switch (this.selectionKind()) {
      case 'folder':
        return ['contents', 'general', 'api'];
      case 'file':
        return ['general', 'statistics', 'api'];
      default:
        return ['general', 'api'];
    }
  });
  protected readonly tabName = computed<Tab>(() => {
    const names = this.tabNames();
    const t = names.find((x) => x === this.tab());
    return t ?? names[0] ?? 'general';
  });
  protected readonly tabIndex = computed(() => Math.max(0, this.tabNames().indexOf(this.tabName())));

  protected readonly itemJson = computed(() =>
    JSON.stringify(this.item() ?? this.repository(), null, 2),
  );
  protected readonly curl = computed(() => {
    const item = this.item();
    return item ? `curl -H "Authorization: Bearer $TROVE_TOKEN" ${shellQuote(item.itemUrl)}` : '';
  });
  protected readonly downloadCurl = computed(() => {
    const url = this.item()?.downloadUrl;
    return url ? `curl -L -O -H "Authorization: Bearer $TROVE_TOKEN" ${shellQuote(url)}` : '';
  });

  /** The path of the selection as breadcrumb links: the repository, then each folder, then the item. */
  protected readonly crumbs = computed(() => {
    const sel = this.selected();
    if (!sel) return [];
    const parts = sel.path.split('/').filter((p) => p !== '');
    return [
      { label: sel.repo, commands: ['/browse', sel.repo] },
      ...parts.map((p, i) => ({
        label: p,
        commands: ['/browse', sel.repo, ...parts.slice(0, i + 1)],
      })),
    ];
  });

  protected readonly folderPath = computed(() => {
    const s = this.selected();
    const item = this.item();
    if (!s) return null;
    if (s.path === '') return '';
    return item?.kind === 'folder' ? s.path : null;
  });
  protected readonly canUpload = computed(
    () => this.folderPath() !== null && this.can().deploy && this.acceptsUploads(),
  );

  protected readonly contentColumns: readonly TableColumn[] = [
    { id: 'name', label: $localize`:@@browse.col.name:Name` },
    { id: 'size', label: $localize`:@@browse.fact.size:Size`, numeric: true },
    { id: 'modified', label: $localize`:@@browse.fact.modified:Last changed` },
  ];

  protected readonly classLabel = (cls: string | undefined): string => {
    switch (cls) {
      case 'local':
        return $localize`:@@repo.class.local:Local`;
      case 'remote':
        return $localize`:@@repo.class.remote:Remote`;
      case 'virtual':
        return $localize`:@@repo.class.virtual:Virtual`;
      default:
        return cls ?? '';
    }
  };

  constructor() {
    this.destroyRef.onDestroy(() => clearTimeout(this.filterTimer));
    effect(() => {
      const filters = { q: this.q(), class: this.class(), format: this.format() };
      untracked(() => {
        // The field keeps what the user typed, unless the address changed under it.
        if ((filters.q ?? '') !== this.search().trim()) this.search.set(filters.q ?? '');
        this.stagedClass.set(filters.class ?? '');
        this.stagedFormat.set(filters.format ?? '');
        void this.list.start(this.repositories.list(filters));
      });
    });
    effect((onCleanup) => {
      // The context-menu key and Shift+F10 on the tree move focus to the actions (R-UI-011).
      const tree = this.treeRef();
      if (!tree) return;
      const handler = (event: KeyboardEvent): void => this.onTreeKey(event);
      tree.element.addEventListener('keydown', handler);
      onCleanup(() => tree.element.removeEventListener('keydown', handler));
    });
    effect(() => {
      const sel = this.selected();
      untracked(() => {
        if (sel) void this.reveal(sel.repo, sel.path);
        else {
          this.revealRun++;
          this.selectedNode.set(null);
        }
      });
    });
    effect(() => {
      // A node made by a deep link before the list arrived learns its class and format from the list.
      for (const r of this.list.items()) {
        const node = untracked(() => this.nodes.get(r.key));
        if (node && node.meta().cls === undefined) {
          node.meta.set({ cls: r.class, format: r.format, disabled: r.disabled });
        }
      }
    });
    effect(() => {
      // A repository the list did not show (a deep link) learns its class and format from the read.
      const doc = this.repositoryDoc();
      if (!doc) return;
      const node = untracked(() => this.nodes.get(doc.key));
      if (node && node.meta().cls === undefined) {
        node.meta.set({ cls: doc.class, format: doc.format, disabled: 'disabled' in doc && !!doc.disabled });
      }
    });
  }

  // --- tree ---------------------------------------------------------------------------------

  private rootOf(r: RepositorySummary): TreeNode {
    return this.nodeFor(r.key, '', 'repository', r.key, {
      cls: r.class,
      format: r.format,
      disabled: r.disabled,
    });
  }

  private nodeFor(
    repository: string,
    path: string,
    kind: TreeNode['kind'],
    label: string,
    meta: NodeMeta = {},
  ): TreeNode {
    const id = nodeId(repository, path, meta.via);
    let node = this.nodes.get(id);
    if (!node) {
      node = new TreeNode(repository, path, kind, label, meta);
      this.nodes.set(id, node);
    }
    return node;
  }

  /** The root node of a repository named by a deep link, even when the list has not shown it. */
  private rootNode(key: string): TreeNode {
    const node = this.nodeFor(key, '', 'repository', key);
    if (!this.roots().some((r) => r.id === node.id)) this.extraRoot.set(node);
    return node;
  }

  protected async onExpand(node: TreeNode, expanded: boolean): Promise<void> {
    node.expanded.set(expanded);
    if (expanded && !node.loaded()) await this.loadChildren(node, false);
  }

  protected async loadChildren(node: TreeNode, more: boolean): Promise<void> {
    if (node.loading()) return;
    node.loading.set(true);
    node.error.set(null);
    try {
      if (node.kind === 'repository' && node.meta().cls === 'virtual') {
        await this.loadMembers(node);
        return;
      }
      const fetchPage = this.files.children(node.repository, node.path, BROWSER_PAGE_SIZE);
      const page = await fetchPage(more ? node.cursor : null, BROWSER_PAGE_SIZE);
      const kids = page.items.map((c) =>
        this.nodeFor(
          node.repository,
          node.path === '' ? c.path : `${node.path}/${c.path}`,
          c.kind,
          nameOf(c.path),
          { size: c.size, modifiedAt: c.modifiedAt },
        ),
      );
      node.children.update((list) => (more ? [...list, ...kids] : kids));
      node.cursor = page.nextCursor;
      node.hasMore.set(page.nextCursor !== null);
      node.loaded.set(true);
    } catch (e) {
      node.error.set(asApiError(e));
    } finally {
      node.loading.set(false);
    }
  }

  /** A virtual repository's children are its members in resolution order (R-UI-010). */
  private async loadMembers(node: TreeNode): Promise<void> {
    const read = (await this.repositories.get(node.repository)).value;
    const keys = isConsumerView(read) ? [] : (read.virtual?.members ?? []);
    node.membersHidden.set(isConsumerView(read));
    const known = new Map(this.list.items().map((r) => [r.key, r]));
    node.children.set(
      keys.map((key, i) => {
        const r = known.get(key);
        return this.nodeFor(key, '', 'member', key, {
          via: node.repository,
          position: i + 1,
          cls: r?.class,
          format: r?.format,
        });
      }),
    );
    node.cursor = null;
    node.hasMore.set(false);
    node.loaded.set(true);
  }

  /** Expands the ancestors of the selection, loading pages until each one is found (R-UI-010). */
  private async reveal(repo: string, path: string): Promise<void> {
    const run = ++this.revealRun;
    const live = (): boolean => run === this.revealRun;
    this.selectedNode.set(null);
    const root = this.rootNode(repo);
    if (path === '') this.selectedNode.set(root);
    root.expanded.set(true);
    if (root.meta().cls === undefined) {
      // A deep link to a repository the list did not show: the class decides how it expands.
      try {
        const read = (await this.repositories.get(repo)).value;
        root.meta.set({ cls: read.class, format: read.format });
      } catch {
        // The details show the failure; the tree keeps a bare node.
      }
      if (!live()) return;
    }
    if (!root.loaded()) await this.loadChildren(root, false);
    if (!live()) return;
    let parent = root;
    const chain = path === '' ? [] : [...ancestorsOf(path), path];
    for (const [i, target] of chain.entries()) {
      const next = await this.findChild(parent, target);
      if (!next || !live()) return;
      if (i === chain.length - 1) this.selectedNode.set(next);
      if (i < chain.length - 1 || next.kind === 'folder') {
        next.expanded.set(true);
        if (!next.loaded()) await this.loadChildren(next, false);
        if (!live()) return;
      }
      parent = next;
    }
  }

  private async findChild(parent: TreeNode, path: string): Promise<TreeNode | undefined> {
    for (;;) {
      const found = parent.children().find((c) => c.path === path);
      if (found) return found;
      if (!parent.hasMore() || parent.error()) return undefined;
      await this.loadChildren(parent, true);
    }
  }

  protected reload(node: TreeNode): void {
    node.children.set([]);
    node.cursor = null;
    node.loaded.set(false);
    node.hasMore.set(false);
    if (node.expanded()) void this.loadChildren(node, false);
  }

  /** The tree reports a selection: navigate to it, or load more when "Load more" was chosen. */
  protected onTreeValue(values: readonly string[]): void {
    const id = values.at(-1);
    if (id === undefined) return;
    if (id.startsWith(MORE_PREFIX)) {
      const node = this.nodes.get(id.slice(MORE_PREFIX.length));
      if (node) void this.loadChildren(node, true);
      this.treeRef()?.value.set([...this.treeValue()]);
      return;
    }
    const node = this.nodes.get(id);
    if (!node) return;
    // A member of a virtual repository leads to the repository itself.
    const target = node.kind === 'member' ? node.repository : id;
    if (target !== this.selectedId()) this.go(node.repository, node.path);
    else if (node.kind === 'member') this.treeRef()?.value.set([...this.treeValue()]);
  }

  protected go(repo: string, path: string): void {
    void this.router.navigate(['/browse', repo, ...path.split('/').filter((s) => s !== '')], {
      queryParams: { tab: null },
      queryParamsHandling: 'merge',
    });
  }

  protected goRoot(): void {
    void this.router.navigate(['/browse'], { queryParamsHandling: 'preserve' });
  }

  protected moreId(node: TreeNode): string {
    return MORE_PREFIX + node.id;
  }

  /** The material icon of a node: the class of a repository, an archive or any other file. */
  protected iconOf(node: TreeNode): string {
    switch (node.kind) {
      case 'repository':
      case 'member':
        switch (node.meta().cls) {
          case 'remote':
            return 'cloud_download';
          case 'virtual':
            return 'layers';
          default:
            return 'inventory_2';
        }
      case 'folder':
        return node.expanded() ? 'folder_open' : 'folder';
      default:
        return ARCHIVE.test(node.label) ? 'folder_zip' : 'description';
    }
  }

  /** Context-menu key and Shift+F10 move focus to the actions (R-UI-011). */
  private onTreeKey(event: KeyboardEvent): void {
    if (event.key === 'ContextMenu' || (event.shiftKey && event.key === 'F10')) {
      event.preventDefault();
      this.dom.getElementById('browse-actions')?.querySelector<HTMLElement>('a, button')?.focus();
    }
  }

  // --- filters ------------------------------------------------------------------------------

  protected applyFilter(
    change: Readonly<Record<string, string | null>>,
    replaceUrl = false,
  ): void {
    void this.router.navigate([], {
      queryParams: change,
      queryParamsHandling: 'merge',
      replaceUrl,
    });
  }

  /** The filter field: the repositories narrow as the user types, after a short pause. */
  protected onSearchInput(value: string): void {
    this.search.set(value);
    clearTimeout(this.filterTimer);
    this.filterTimer = setTimeout(() => this.commitSearch(), FILTER_DELAY_MS);
  }

  protected commitSearch(): void {
    clearTimeout(this.filterTimer);
    const q = this.search().trim() || null;
    if (q !== (this.q() || null)) this.applyFilter({ q }, true);
  }

  protected submitFilters(): void {
    this.applyFilter({ class: this.stagedClass() || null, format: this.stagedFormat() || null });
    this.panelOpen.set(false);
  }

  protected clearPanelFilters(): void {
    this.stagedClass.set('');
    this.stagedFormat.set('');
    this.applyFilter({ class: null, format: null });
  }

  protected clearFilters(): void {
    this.search.set('');
    this.applyFilter({ q: null, class: null, format: null });
  }

  protected removeLabel(label: string): string {
    return $localize`:@@browse.chip.remove:Remove the filter ${label}:filter:`;
  }

  protected setSort(value: 'key' | 'class'): void {
    this.applyFilter({ sort: value === 'class' ? 'class' : null });
  }

  protected readonly classes = CLASSES;

  protected setTab(index: number): void {
    const name = this.tabNames()[index] ?? 'general';
    if (name !== this.tabName()) {
      this.applyFilter({ tab: name === this.tabNames()[0] ? null : name });
    }
  }

  // --- the contents table ---------------------------------------------------------------------

  protected sizeOf(node: TreeNode): string {
    const size = node.meta().size;
    return node.kind === 'file' && size !== undefined ? formatSize(size) : '–';
  }

  protected since(iso: string): string {
    return relativeTime(iso, this.locale);
  }

  protected commandsOf(node: TreeNode): string[] {
    return ['/browse', node.repository, ...node.path.split('/').filter((s) => s !== '')];
  }

  // --- actions ------------------------------------------------------------------------------

  protected async upload(): Promise<void> {
    const sel = this.selected();
    const folder = this.folderPath();
    if (!sel || folder === null) return;
    const data: UploadDialogData = {
      repository: sel.repo,
      folder,
      canOverwrite: this.can().overwrite,
    };
    const ref = this.dialog.open<UploadDialog, UploadDialogData, number>(UploadDialog, {
      data,
      width: '40rem',
      maxWidth: '95vw',
    });
    const uploaded = (await firstValueFrom(ref.afterClosed())) ?? 0;
    if (uploaded > 0) this.refreshSelectedFolder();
  }

  protected async newFolder(): Promise<void> {
    const sel = this.selected();
    const parent = this.folderPath();
    if (!sel || parent === null) return;
    const data: FolderDialogData = { repository: sel.repo, parent };
    const ref = this.dialog.open<FolderDialog, FolderDialogData, string>(FolderDialog, {
      data,
      width: '32rem',
      maxWidth: '95vw',
    });
    const created = await firstValueFrom(ref.afterClosed());
    if (created) {
      this.notifier.status($localize`:@@browse.folder.created:Folder ${created}:path: created.`);
      this.refreshSelectedFolder();
    }
  }

  private refreshSelectedFolder(): void {
    const node = this.selectedNode();
    if (node) this.reload(node);
  }

  protected async remove(): Promise<void> {
    const sel = this.selected();
    const item = this.item();
    if (!sel || !item || sel.path === '') return;
    this.actionFailure.set(null);
    const lines: string[] = [];
    let typeToConfirm: string | undefined;
    if (item.kind === 'folder') {
      try {
        const { count, capped } = await this.files.countBelow(
          sel.repo,
          sel.path,
          TYPE_TO_CONFIRM_ABOVE,
        );
        lines.push(
          capped
            ? $localize`:@@browse.delete.folder-many:The folder holds more than ${TYPE_TO_CONFIRM_ABOVE}:limit: items. They are deleted for good.`
            : $localize`:@@browse.delete.folder:The folder holds ${count}:count: items. They are deleted for good.`,
        );
        if (capped) typeToConfirm = nameOf(sel.path);
      } catch (e) {
        this.actionFailure.set(asApiError(e));
        return;
      }
    } else {
      lines.push($localize`:@@browse.delete.file:The file is deleted for good.`);
    }
    const answer = await this.confirm.ask({
      title: $localize`:@@browse.delete.title:Delete ${sel.path}:path:?`,
      lines,
      confirmLabel: $localize`:@@browse.delete.confirm:Delete`,
      typeToConfirm,
    });
    if (answer === null) return;
    try {
      const outcome = await this.files.deleteItem(sel.repo, sel.path, newIdempotencyKey());
      const parent = parentOf(sel.path);
      if (outcome.done) this.afterDelete(sel.repo, sel.path);
      else this.deletion.set({ job: outcome.job, parent });
    } catch (e) {
      this.actionFailure.set(asApiError(e));
    }
  }

  protected jobEnded(state: string): void {
    const d = this.deletion();
    const sel = this.selected();
    if (!d || !sel) return;
    if (state === 'succeeded') this.afterDelete(sel.repo, sel.path);
    else
      this.notifier.alert(
        $localize`:@@browse.delete.failed:The deletion failed. Some items may be gone.`,
      );
    this.deletion.set(null);
  }

  private afterDelete(repo: string, path: string): void {
    const parent = parentOf(path);
    const node = this.nodes.get(parent === '' ? repo : `${repo}/${parent}`);
    if (node) this.reload(node);
    this.notifier.status($localize`:@@browse.deleted:${path}:path: deleted.`);
    this.go(repo, parent);
  }

  protected readonly moreLabel = $localize`:@@browse.load-more:Load more`;
  protected readonly copyPath = $localize`:@@browse.copy-path:Copy path`;
  protected readonly copyUrl = $localize`:@@browse.copy-url:Copy download URL`;
  protected readonly copyListing = $localize`:@@browse.copy-listing:Copy listing URL`;
  protected readonly copyDigest = $localize`:@@browse.copy-digest:Copy checksum`;
  protected readonly copyCurl = $localize`:@@browse.copy-curl:Copy the curl command`;
  protected readonly deleteProgress = $localize`:@@browse.delete.progress:Deleting items`;
  protected readonly tabLabels: Readonly<Record<Tab, string>> = {
    contents: $localize`:@@browse.tab.contents:Contents`,
    general: $localize`:@@browse.tab.general:General`,
    statistics: $localize`:@@browse.tab.statistics:Statistics`,
    api: $localize`:@@browse.tab.api:API`,
  };
  protected readonly emptyFolder = $localize`:@@browse.folder-empty:This folder is empty.`;
  protected readonly contentsCaption = $localize`:@@browse.contents.caption:Contents of the selected folder`;
  protected readonly digestNames: readonly (readonly [
    'sha256' | 'sha512' | 'sha1' | 'md5',
    string,
  ])[] = [
    ['sha256', 'SHA-256'],
    ['sha512', 'SHA-512'],
    ['sha1', 'SHA-1'],
    ['md5', 'MD5'],
  ];
}
