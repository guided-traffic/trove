import { DOCUMENT, DatePipe, NgTemplateOutlet } from '@angular/common';
import {
  Component,
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
import { MatTabsModule } from '@angular/material/tabs';
import { ActivatedRoute, Router, RouterLink } from '@angular/router';
import { firstValueFrom } from 'rxjs';
import { newIdempotencyKey } from '../../api/client';
import { FilesApi, Item } from '../../api/files-api';
import { ApiError } from '../../api/problem';
import {
  JobReference,
  RepositoriesApi,
  Repository,
  RepositoryConsumerView,
  RepositorySummary,
} from '../../api/repositories-api';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { Confirm } from '../../shared/confirm-dialog';
import { CopyButton } from '../../shared/copy-button';
import { ErrorNotice } from '../../shared/error-notice';
import { JobProgress } from '../../shared/job-progress';
import { PagedResource, asApiError, valueOf } from '../../shared/paged-resource';
import { FolderDialog, FolderDialogData } from './folder-dialog';
import { MORE_PREFIX, TreeNode, ancestorsOf, nameOf, parentOf } from './tree-node';
import { UploadDialog, UploadDialogData } from './upload-dialog';

/** `ui.browser.page_size` (R-UI-010, default 200). The UI cannot read the configuration, so it is a constant. */
export const BROWSER_PAGE_SIZE = 200;

/** A delete of a folder with more items than this asks for the folder name (R-UI-011). */
export const TYPE_TO_CONFIRM_ABOVE = 100;

type Detail =
  | { readonly kind: 'repository'; readonly repository: Repository | RepositoryConsumerView }
  | { readonly kind: 'item'; readonly item: Item };

const CLASS_ORDER: Record<string, number> = { virtual: 0, local: 1, remote: 2 };
const TABS = ['general', 'statistics', 'api'] as const;

/**
 * The file tree (R-UI-010): the readable repositories at the root, children loaded lazily in pages
 * of `BROWSER_PAGE_SIZE` with a "Load more" entry, details of the selection next to it. The
 * selection is the URL (`/browse/{repo}/{path…}`) and a deep link expands the ancestors. The tree
 * follows the WAI-ARIA tree pattern through `@angular/aria` (roles, `aria-expanded`, `aria-level`,
 * `aria-setsize`, `aria-posinset`, arrow keys, Home, End, type-ahead). The actions sit in a toolbar
 * above the details; the context-menu key and Shift+F10 on the tree move focus there.
 */
@Component({
  selector: 'trove-browse',
  imports: [
    DatePipe,
    NgTemplateOutlet,
    MatButtonModule,
    MatTabsModule,
    RouterLink,
    Tree,
    TreeItem,
    TreeItemGroup,
    CopyButton,
    ErrorNotice,
    JobProgress,
  ],
  templateUrl: './browse.html',
  styleUrl: './browse.scss',
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
  protected readonly search = signal('');
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
          (CLASS_ORDER[a.meta.cls ?? ''] ?? 9) - (CLASS_ORDER[b.meta.cls ?? ''] ?? 9) ||
          a.label.localeCompare(b.label),
      );
    }
    return roots.sort((a, b) => a.label.localeCompare(b.label));
  });

  protected readonly detail = resource<Detail | undefined, { repo: string; path: string } | null>({
    params: () => this.selected(),
    loader: async ({ params }): Promise<Detail | undefined> => {
      if (!params) return undefined;
      if (params.path === '') {
        return { kind: 'repository', repository: (await this.repositories.get(params.repo)).value };
      }
      return { kind: 'item', item: await this.files.item(params.repo, params.path) };
    },
  });
  protected readonly item = computed(() => {
    const d = valueOf(this.detail);
    return d?.kind === 'item' ? d.item : null;
  });
  protected readonly repository = computed(() => {
    const d = valueOf(this.detail);
    return d?.kind === 'repository' ? d.repository : null;
  });
  protected readonly detailFailure = computed(() => {
    const e = this.detail.error();
    return e ? asApiError(e) : null;
  });

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

  protected readonly tabName = computed(() => {
    const t = this.tab();
    return TABS.find((x) => x === t) ?? 'general';
  });
  protected readonly tabIndex = computed(() => {
    const names = this.item()?.kind === 'file' ? TABS : (['general', 'api'] as const);
    return Math.max(0, (names as readonly string[]).indexOf(this.tabName()));
  });

  protected readonly itemJson = computed(() =>
    JSON.stringify(this.item() ?? this.repository(), null, 2),
  );
  protected readonly curl = computed(() => {
    const item = this.item();
    return item ? `curl -H 'Authorization: Bearer $TROVE_TOKEN' '${item.itemUrl}'` : '';
  });
  protected readonly downloadCurl = computed(() => {
    const url = this.item()?.downloadUrl;
    return url ? `curl -L -O -H 'Authorization: Bearer $TROVE_TOKEN' '${url}'` : '';
  });
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
    effect(() => {
      const filters = { q: this.q(), class: this.class(), format: this.format() };
      untracked(() => {
        this.search.set(filters.q ?? '');
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
      if (sel) untracked(() => void this.reveal(sel.repo, sel.path));
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
    meta: TreeNode['meta'] = {},
  ): TreeNode {
    const id = path === '' ? repository : `${repository}/${path}`;
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
      const fetchPage = this.files.children(node.repository, node.path, BROWSER_PAGE_SIZE);
      const page = await fetchPage(more ? node.cursor : null, BROWSER_PAGE_SIZE);
      const kids = page.items.map((c) =>
        this.nodeFor(
          node.repository,
          node.path === '' ? c.path : `${node.path}/${c.path}`,
          c.kind,
          nameOf(c.path),
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

  /** Expands the ancestors of the selection, loading pages until each one is found (R-UI-010). */
  private async reveal(repo: string, path: string): Promise<void> {
    const root = this.rootNode(repo);
    root.expanded.set(true);
    if (!root.loaded()) await this.loadChildren(root, false);
    let parent = root;
    const chain = path === '' ? [] : [...ancestorsOf(path), path];
    for (const [i, target] of chain.entries()) {
      const next = await this.findChild(parent, target);
      if (!next) return;
      if (i < chain.length - 1 || next.kind === 'folder') {
        next.expanded.set(true);
        if (!next.loaded()) await this.loadChildren(next, false);
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
    if (node && id !== this.selectedId()) this.go(node.repository, node.path);
  }

  protected go(repo: string, path: string): void {
    void this.router.navigate(['/browse', repo, ...path.split('/').filter((s) => s !== '')], {
      queryParamsHandling: 'preserve',
    });
  }

  protected goRoot(): void {
    void this.router.navigate(['/browse']);
  }

  protected moreId(node: TreeNode): string {
    return MORE_PREFIX + node.id;
  }

  /** Context-menu key and Shift+F10 move focus to the actions (R-UI-011). */
  private onTreeKey(event: KeyboardEvent): void {
    if (event.key === 'ContextMenu' || (event.shiftKey && event.key === 'F10')) {
      event.preventDefault();
      this.dom.getElementById('browse-actions')?.querySelector<HTMLElement>('a, button')?.focus();
    }
  }

  protected applyFilter(change: Readonly<Record<string, string | null>>): void {
    void this.router.navigate([], { queryParams: change, queryParamsHandling: 'merge' });
  }

  protected submitSearch(event: Event): void {
    event.preventDefault();
    this.applyFilter({ q: this.search().trim() || null });
  }

  protected setTab(index: number): void {
    const names = this.item()?.kind === 'file' ? TABS : (['general', 'api'] as const);
    const name = names[index] ?? 'general';
    if (name !== this.tabName()) this.applyFilter({ tab: name === 'general' ? null : name });
  }

  // --- actions ------------------------------------------------------------------------------

  protected readonly folderPath = computed(() => {
    const s = this.selected();
    const item = this.item();
    if (!s) return null;
    if (s.path === '') return '';
    return item?.kind === 'folder' ? s.path : null;
  });

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
    const id = this.selectedId();
    const node = id ? this.nodes.get(id) : undefined;
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
  protected readonly copyDigest = $localize`:@@browse.copy-digest:Copy checksum`;
  protected readonly copyCurl = $localize`:@@browse.copy-curl:Copy the curl command`;
  protected readonly deleteProgress = $localize`:@@browse.delete.progress:Deleting items`;
  protected readonly tabGeneral = $localize`:@@browse.tab.general:General`;
  protected readonly tabStats = $localize`:@@browse.tab.statistics:Statistics`;
  protected readonly tabApi = $localize`:@@browse.tab.api:API`;
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
