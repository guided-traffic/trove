import { DatePipe, NgTemplateOutlet } from '@angular/common';
import {
  Component,
  DestroyRef,
  ElementRef,
  afterRenderEffect,
  computed,
  effect,
  inject,
  input,
  linkedSignal,
  output,
  resource,
  signal,
  untracked,
  viewChild,
} from '@angular/core';
import { Tree, TreeItem, TreeItemGroup } from '@angular/aria/tree';
import { Router, RouterLink } from '@angular/router';
import { PackageDocEntry, PackageDocFile, PackageDocTree, PackagesApi } from '../../api/packages-api';
import { ApiError } from '../../api/problem';
import { ErrorNotice } from '../../shared/error-notice';
import { asApiError, valueOf } from '../../shared/paged-resource';
import { AnsibleDocEntry } from './ansible-doc-entry';
import {
  DocNode,
  README_DOC,
  allBranches,
  branchesOf,
  docNodes,
  firstDoc,
  holdsDoc,
  parseDoc,
  unavailableText,
} from './docs-model';
import { PackageReadmeView } from './package-readme';

/** The silence after the last key before a search is sent (Q-UI-243). */
export const SEARCH_DEBOUNCE_MS = 300;
/** A search text shorter than this is not sent (R-ANS-036). */
export const SEARCH_MIN_LENGTH = 2;

type DocPage =
  | { readonly kind: 'file'; readonly file: PackageDocFile }
  | { readonly kind: 'entry'; readonly entry: PackageDocEntry };

/**
 * The Documentation tab of an Ansible collection (R-UI-060, Q-UI-241, Q-UI-243): the two panes of the file browser, a tree with a
 * search field and the open page. The tree is the answer of `getPackageVersionDocs`, read once per version by the package page and
 * shared with the Contents tab; a search reads it again with `q` and narrows the rows while the counts stay. A page is one call of
 * `getPackageVersionDocEntry` or `getPackageVersionDocFile`. The open page is the query parameter `doc` and the search text `q`, so a
 * link, a reload and the back button restore both; an unknown `doc` shows the first page.
 *
 * The tree is a `role="tree"` of links (`@angular/aria`'s navigation tree): arrow keys move, Enter opens, `aria-current="page"` marks the
 * open entry. The HTML of a page is bound the way the README is; every other value is text.
 */
@Component({
  selector: 'trove-ansible-docs',
  imports: [
    DatePipe,
    NgTemplateOutlet,
    RouterLink,
    Tree,
    TreeItem,
    TreeItemGroup,
    ErrorNotice,
    PackageReadmeView,
    AnsibleDocEntry,
  ],
  templateUrl: './ansible-docs.html',
  styleUrl: './ansible-docs.scss',
})
export class AnsibleDocs {
  private readonly api = inject(PackagesApi);
  private readonly router = inject(Router);

  readonly packageId = input.required<string>();
  readonly version = input.required<string>();
  /** The whole tree of the version; null while it loads, or when only the README is there (`readmeOnly`). */
  readonly tree = input.required<PackageDocTree | null>();
  readonly loading = input(false);
  readonly failure = input<ApiError | null>(null);
  /** The version announces a README and no entries: the tab shows the README alone. */
  readonly readmeOnly = input(false);
  /** The version is held by more than one repository: the source line names the one the documentation was read from. */
  readonly severalRepositories = input(false);
  readonly doc = input<string | undefined>();
  readonly q = input<string | undefined>();

  /** The user asks for the tree again (the state `pending`, or a failed read). */
  readonly refresh = output<void>();

  /** The text of the search field; the URL follows it 300 ms after the last key. */
  protected readonly draft = signal('');
  private timer: ReturnType<typeof setTimeout> | null = null;

  /** The search text that is in the URL and long enough to be sent. */
  protected readonly committed = computed<string | null>(() => {
    const t = (this.q() ?? '').trim();
    return t.length >= SEARCH_MIN_LENGTH ? t : null;
  });
  protected readonly tooShort = computed(() => this.draft().trim().length === 1);

  protected readonly state = computed(() => this.tree()?.state ?? null);

  protected readonly search = resource({
    params: () => {
      const q = this.committed();
      const t = this.tree();
      return q && t?.state === 'available'
        ? { id: this.packageId(), version: this.version(), q, repository: t.repository }
        : undefined;
    },
    loader: ({ params }) =>
      this.api.docs(params.id, params.version, { q: params.q, repository: params.repository }),
  });
  /** The tree the rows are drawn from: the narrowed one once a search has answered, else the whole one. */
  private readonly narrowed = computed<PackageDocTree | null>(() => {
    if (this.committed() === null) return null;
    return valueOf(this.search) ?? null;
  });
  protected readonly searching = computed(() => this.committed() !== null);
  protected readonly searchPending = computed(() => this.searching() && this.search.isLoading());
  protected readonly searchFailure = computed(() => {
    const e = this.search.error();
    return e ? asApiError(e) : null;
  });
  protected readonly view = computed(() => this.narrowed() ?? this.tree());

  protected readonly nodes = computed<readonly DocNode[]>(() => {
    const all = this.tree();
    const view = this.view();
    return all && view && all.state === 'available' ? docNodes(view, all, this.narrowed() !== null) : [];
  });

  /** The page that is open: `doc` when the whole tree holds it, else the first page. */
  protected readonly current = computed<string | null>(() => {
    if (this.readmeOnly()) return README_DOC;
    const t = this.tree();
    if (!t || t.state !== 'available') return null;
    const doc = this.doc();
    return doc && holdsDoc(t, parseDoc(doc)) ? doc : firstDoc(t);
  });
  protected readonly treeValue = computed(() => {
    const c = this.current();
    return c === null ? [] : [c];
  });
  protected readonly hasSelection = computed(() => !!this.doc());

  /** What the user opened or closed by hand, forgotten when the search text changes. */
  private readonly overrides = linkedSignal<string | null, ReadonlyMap<string, boolean>>({
    source: this.committed,
    computation: () => new Map<string, boolean>(),
  });
  private readonly openByDefault = computed<ReadonlySet<string>>(() => {
    if (this.narrowed() !== null) return new Set(allBranches(this.nodes()));
    return new Set(['group:files', ...branchesOf(this.nodes(), this.current())]);
  });

  protected readonly page = resource({
    params: () => {
      const address = parseDoc(this.current());
      const t = this.tree();
      if (!address || (!t && !this.readmeOnly())) return undefined;
      return { id: this.packageId(), version: this.version(), address, repository: t?.repository };
    },
    loader: async ({ params }): Promise<DocPage> =>
      params.address.kind === 'file'
        ? { kind: 'file', file: await this.api.docFile(params.id, params.version, params.address.name, params.repository) }
        : {
            kind: 'entry',
            entry: await this.api.docEntry(params.id, params.version, params.address.type, params.address.name, params.repository),
          },
  });
  protected readonly pageValue = computed(() => valueOf(this.page));
  protected readonly pageFailure = computed(() => {
    const e = this.page.error();
    return e ? asApiError(e) : null;
  });

  /** "5 of 32 entries" while a search narrows the tree; the sentence is a live region. */
  protected readonly matchText = computed(() => {
    const t = this.tree();
    const n = this.narrowed();
    if (this.tooShort()) return $localize`:@@docs.search.too-short:Enter at least 2 characters to search.`;
    if (this.searchPending()) return $localize`:@@docs.search.pending:Searching.`;
    if (!t || !n || n.matchCount === undefined) return '';
    const total = (t.groups ?? []).reduce((sum, g) => sum + g.count, 0);
    return $localize`:@@docs.search.count:${n.matchCount}:matches: of ${total}:total: entries`;
  });
  protected readonly noMatch = computed(() => {
    const n = this.narrowed();
    return n !== null && (n.files ?? []).length === 0 && this.nodes().length === 0;
  });

  protected readonly reason = computed(() => unavailableText(this.tree()?.reason));

  protected readonly source = computed(() => {
    const t = this.tree();
    return t?.state === 'available' ? t : null;
  });

  private readonly pagePane = viewChild<ElementRef<HTMLElement>>('pagePane');

  constructor() {
    inject(DestroyRef).onDestroy(() => this.cancelTimer());
    // The field follows the URL when it changes from outside (the back button, a link) and not when it only echoes what was typed.
    effect(() => {
      const q = this.q() ?? '';
      untracked(() => {
        if (q.trim() !== this.draft().trim()) this.draft.set(q);
      });
    });
    // A new page starts at its top.
    afterRenderEffect(() => {
      this.current();
      const pane = this.pagePane()?.nativeElement;
      if (pane) pane.scrollTop = 0;
    });
  }

  protected isOpen(id: string): boolean {
    return this.overrides().get(id) ?? this.openByDefault().has(id);
  }

  protected setOpen(id: string, open: boolean): void {
    if (this.isOpen(id) === open) return;
    this.overrides.update((m) => new Map(m).set(id, open));
  }

  protected onInput(value: string): void {
    this.draft.set(value);
    this.cancelTimer();
    this.timer = setTimeout(() => {
      this.timer = null;
      this.commit();
    }, SEARCH_DEBOUNCE_MS);
  }

  protected clear(): void {
    this.cancelTimer();
    this.draft.set('');
    this.commit();
  }

  protected back(): void {
    void this.router.navigate([], { queryParams: { doc: null }, queryParamsHandling: 'merge' });
  }

  private cancelTimer(): void {
    if (this.timer !== null) clearTimeout(this.timer);
    this.timer = null;
  }

  private commit(): void {
    const text = this.draft().trim();
    const next = text.length >= SEARCH_MIN_LENGTH ? text : null;
    if (next === this.committed()) return;
    void this.router.navigate([], { queryParams: { q: next }, queryParamsHandling: 'merge', replaceUrl: true });
  }
}
