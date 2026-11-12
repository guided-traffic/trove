import { CdkTrapFocus } from '@angular/cdk/a11y';
import { CdkMenu, CdkMenuItemRadio, CdkMenuTrigger } from '@angular/cdk/menu';
import { NgTemplateOutlet } from '@angular/common';
import {
  Component,
  DestroyRef,
  ElementRef,
  afterNextRender,
  computed,
  effect,
  inject,
  Injector,
  signal,
  untracked,
  viewChild,
} from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { NavigationEnd, Router } from '@angular/router';
import { filter as rxFilter, map } from 'rxjs';
import { ApiError } from '../../api/problem';
import { RepositoriesApi, RepositorySummary } from '../../api/repositories-api';
import { SearchApi, SearchSuggestions } from '../../api/search-api';
import { AuthzEpoch } from '../../core/authz-epoch';
import { FrequentPlaces } from '../../core/frequent-places';
import { Navigation } from '../../core/navigation';
import { PreferencesStore } from '../../core/preferences-store';
import { ProjectContext } from '../../core/project-context';
import { SessionStore } from '../../core/session-store';
import { FormatIconView } from '../../shared/format-icon';
import { TroveAvatar } from '../../shared/ui/avatar';
import { Segment, highlight } from './highlight';
import {
  SearchEntry,
  SearchFilter,
  SearchGroup,
  adminItems,
  adminShortcuts,
  firstKindOf,
  gate,
  headings,
  kindsOf,
  liveGroups,
  pages,
  placeEntry,
  places,
  projectEntry,
  repositoryEntry,
} from './search-model';
import { SearchLayout } from './search-layout';

/** How many answers are kept while the list is open, and for how long (R-UI-097). */
const CACHE_SIZE = 10;
const CACHE_MS = 30_000;
/** An anonymous caller sends at most this many requests in this window (Q-UI-408). */
const ANONYMOUS_BURST = 4;
const ANONYMOUS_WINDOW_MS = 4_000;
/** The frequently visited groups show this many entries each (R-UI-096). */
const FREQUENT_SHOWN = 5;

interface Answer {
  readonly key: string;
  readonly q: string;
  readonly answer: SearchSuggestions;
}

/** A row of the list with its position among all entries (the option's ID and the active entry). */
interface IndexedGroup {
  readonly group: SearchGroup;
  readonly first: boolean;
  readonly rows: readonly { readonly entry: SearchEntry; readonly index: number }[];
}

/**
 * The one search of the UI (R-UI-095 to R-UI-097, R-UI-099): the top bar's field for every caller, with a filter, the list of
 * places and frequently visited repositories and projects before anything is typed, live suggestions grouped by kind while
 * typing, and below 36 rem a button that opens it as a sheet. The field is an APG combobox with a grouped listbox, written on
 * its own (Q-UI-410 (b)): the focus stays in the field, `aria-activedescendant` names the active entry, Up from the first entry
 * returns to the text and nothing wraps, and a second Escape empties the field, which the library's combobox does not do.
 * Every way out of the field passes `gate`: only an instance administrator's explicit choice of a marked suggestion opens a route
 * under `/admin`.
 */
@Component({
  selector: 'trove-search-field',
  imports: [NgTemplateOutlet, CdkMenu, CdkMenuItemRadio, CdkMenuTrigger, CdkTrapFocus, FormatIconView, TroveAvatar],
  templateUrl: './search-field.html',
  host: { '[class.narrow]': 'layout.narrow()' },
})
export class SearchField {
  private readonly api = inject(SearchApi);
  private readonly repositories = inject(RepositoriesApi);
  private readonly router = inject(Router);
  protected readonly session = inject(SessionStore);
  private readonly preferences = inject(PreferencesStore);
  private readonly navigation = inject(Navigation);
  private readonly context = inject(ProjectContext);
  private readonly frequent = inject(FrequentPlaces);
  private readonly epoch = inject(AuthzEpoch);
  private readonly injector = inject(Injector);
  protected readonly layout = inject(SearchLayout);

  private readonly field = viewChild<ElementRef<HTMLInputElement>>('field');
  private readonly sheetButton = viewChild<ElementRef<HTMLButtonElement>>('sheetButton');

  protected readonly listId = 'search-suggestions';
  protected readonly text = signal('');
  protected readonly filter = signal<SearchFilter>('all');
  /** The list under the field is open. */
  protected readonly open = signal(false);
  /** The index of the active entry among all entries, or -1 while the text is active. */
  protected readonly active = signal(-1);
  protected readonly sheetOpen = signal(false);
  /** The polite live region's text: how many suggestions, or a hint. */
  protected readonly announcement = signal('');

  private readonly answered = signal<Answer | null>(null);
  private readonly pending = signal(false);
  private readonly failure = signal<{ readonly kind: 'limited'; readonly seconds: number } | { readonly kind: 'error' } | null>(null);
  private readonly cache = new Map<string, { readonly answer: SearchSuggestions; readonly at: number }>();
  private readonly sent: number[] = [];
  private retryAt = 0;
  private timer: ReturnType<typeof setTimeout> | null = null;
  private inFlight: AbortController | null = null;

  /** The stored repositories the server confirmed (Q-UI-406), for the stored names and authorization epoch they were asked for. */
  private readonly confirmed = signal<{ readonly key: string; readonly rows: readonly RepositorySummary[] } | null>(null);
  private confirming: string | null = null;

  protected readonly heading = headings();
  protected readonly anonymous = computed(() => !this.session.authenticated());
  protected readonly instanceAdmin = this.session.isInstanceAdmin;
  /** `/` is on for a signed-in caller whose preference allows it (Q-UI-401). */
  readonly slashOn = computed(() => this.session.authenticated() && this.preferences.keyboardShortcuts());
  protected readonly placeholder = computed(() =>
    this.slashOn()
      ? $localize`:@@search.placeholder-slash:Search or go to… (/)`
      : $localize`:@@search.placeholder:Search or go to…`,
  );
  private readonly minChars = computed(() => (this.anonymous() ? 3 : 2));
  private readonly delay = computed(() => (this.anonymous() ? 500 : 250));
  protected readonly term = computed(() => this.text().trim());
  /** Whether the text is long enough for live suggestions; below it the list shows the places (R-UI-096). */
  protected readonly live = computed(() => this.term().length >= this.minChars());
  private readonly limit = computed(() => (this.filter() === 'all' ? 5 : 10));
  private readonly kinds = computed(() => kindsOf(this.filter(), this.instanceAdmin()));

  protected readonly filters = computed(() => {
    const main: { value: SearchFilter; label: string }[] = [
      { value: 'all', label: $localize`:@@search.filter.all:All` },
      { value: 'repository', label: this.heading['repository']! },
      { value: 'package', label: this.heading['package']! },
      { value: 'project', label: this.heading['project']! },
    ];
    const admin: { value: SearchFilter; label: string }[] = this.instanceAdmin()
      ? [
          { value: 'user', label: this.heading['user']! },
          { value: 'group', label: this.heading['group']! },
        ]
      : [];
    return { main, admin };
  });
  protected readonly filterLabel = computed(() => {
    const f = this.filter();
    return [...this.filters().main, ...this.filters().admin].find((x) => x.value === f)?.label ?? f;
  });
  protected readonly filterButtonLabel = computed(() => $localize`:@@search.filter.button:Filter: ${this.filterLabel()}:kind:`);
  protected readonly removeFilterLabel = computed(() => $localize`:@@search.filter.remove:Remove the filter ${this.filterLabel()}:kind:`);

  /** The pages of the main area the caller may open (R-UI-097). */
  private readonly pageList = computed(() => pages().filter((p) => !this.anonymous() || p.anonymous));

  /** The list before anything is typed (R-UI-096, Q-UI-404). */
  private readonly emptyGroups = computed<SearchGroup[]>(() => {
    const out: SearchGroup[] = [];
    const anonymous = this.anonymous();
    out.push({
      id: 'places',
      heading: this.heading['places']!,
      area: 'main',
      entries: places()
        .filter((p) => !anonymous || p.anonymous)
        .map((p) => placeEntry(p, 'main', 'place')),
    });
    if (anonymous) return out;
    const storage = this.frequent.available();
    if (storage) {
      const rows = this.confirmed()?.rows ?? [];
      const byName = new Map(rows.map((r) => [`${r.project}/${r.key}`, r]));
      const repos = this.frequent
        .frequentRepositories()
        .flatMap((v) => {
          const r = byName.get(v.id);
          return r ? [repositoryEntry(r.project, r.key, r.format, 'frequent')] : [];
        })
        .slice(0, FREQUENT_SHOWN);
      if (repos.length > 0) out.push({ id: 'frequent-repositories', heading: this.heading['frequent-repositories']!, area: 'main', entries: repos });
    }
    out.push({
      id: 'all-projects',
      heading: null,
      area: 'main',
      entries: [
        {
          id: 'all-projects',
          area: 'main',
          kind: 'all-projects',
          label: $localize`:@@search.all-projects:View all my projects`,
          icon: 'folder_shared',
          literal: false,
          commands: ['/projects'],
        },
      ],
    });
    if (storage) {
      const known = new Map(this.context.projects().map((p) => [p.key, p]));
      const projects = this.frequent
        .frequentProjects()
        .flatMap((v) => {
          const p = known.get(v.id);
          return p ? [projectEntry(p.key, p.displayName, 'frequent')] : [];
        })
        .slice(0, FREQUENT_SHOWN);
      if (projects.length > 0) out.push({ id: 'frequent-projects', heading: this.heading['frequent-projects']!, area: 'main', entries: projects });
    }
    if (this.instanceAdmin()) {
      const shortcuts = adminShortcuts(this.navigation.area('admin')).map((p) => placeEntry(p, 'admin', 'admin'));
      if (shortcuts.length > 0) out.push({ id: 'admin-area', heading: this.heading['admin-area']!, area: 'admin', entries: shortcuts });
    }
    return out;
  });

  /** The groups shown now. */
  protected readonly groups = computed<SearchGroup[]>(() => {
    if (!this.live()) return this.emptyGroups();
    const a = this.answered();
    if (!a) return [];
    return liveGroups({
      q: a.q,
      filter: this.filter(),
      answer: a.answer,
      instanceAdmin: this.instanceAdmin(),
      pages: this.pageList(),
      adminPages: adminItems(this.navigation.area('admin')),
    });
  });
  protected readonly indexed = computed<IndexedGroup[]>(() => {
    let index = 0;
    let adminSeen = false;
    return this.groups().map((group) => {
      const first = group.area === 'admin' && !adminSeen;
      if (group.area === 'admin') adminSeen = true;
      return { group, first, rows: group.entries.map((entry) => ({ entry, index: index++ })) };
    });
  });
  private readonly entries = computed(() => this.groups().flatMap((g) => g.entries));
  protected readonly activeId = computed(() => (this.active() >= 0 && this.open() ? this.optionId(this.active()) : null));

  /** The quiet line above the list: searching, the 429, an error, or no match (R-UI-097). */
  protected readonly status = computed<string | null>(() => {
    if (!this.live()) return null;
    const f = this.failure();
    if (f?.kind === 'limited') return $localize`:@@search.limited:Too many searches. Suggestions return in ${f.seconds}:seconds: s.`;
    if (f?.kind === 'error') return $localize`:@@search.error:Suggestions are not available.`;
    if (this.pending()) return $localize`:@@search.searching:Searching…`;
    const a = this.answered();
    if (a && a.key === this.requestKey() && this.groups().length === 0) {
      return $localize`:@@search.no-match:Nothing you may read matches “${this.term()}:text:”. Press Enter to search everything.`;
    }
    return null;
  });

  private readonly url = toSignal(
    this.router.events.pipe(
      rxFilter((e): e is NavigationEnd => e instanceof NavigationEnd),
      map((e) => e.urlAfterRedirects),
    ),
    { initialValue: this.router.url },
  );

  constructor() {
    // The results page keeps its text and kind in the field, also when it is opened from a link or reloaded (R-UI-098).
    effect(() => {
      const url = this.url();
      if (url !== '/search' && !url.startsWith('/search?')) return;
      const params = new URLSearchParams(url.slice('/search'.length));
      const kind = params.get('kind');
      untracked(() => {
        this.text.set(params.get('q') ?? '');
        if (kind === 'repository' || kind === 'package' || kind === 'project') this.filter.set(kind);
      });
    });
    // The stored repositories are confirmed by one request when the empty list is open (Q-UI-406).
    effect(() => {
      if (!this.open() || this.live() || this.anonymous() || !this.frequent.available()) return;
      const names = this.frequent.frequentRepositories().map((v) => v.id);
      const key = `${names.join('\n')}|${this.epoch.value() ?? ''}|${this.session.whoami()?.principal?.id ?? ''}`;
      untracked(() => void this.confirm(names, key));
    });
    inject(DestroyRef).onDestroy(() => {
      if (this.timer) clearTimeout(this.timer);
      this.inFlight?.abort();
    });
  }

  /** Focuses the field: `/`, Ctrl+K and ⌘K (R-UI-095); below 36 rem it opens the sheet first. */
  focus(): void {
    if (this.layout.narrow()) {
      this.openSheet();
      return;
    }
    this.field()?.nativeElement.focus();
  }

  protected optionId(index: number): string {
    return `search-option-${index}`;
  }

  protected segments(entry: SearchEntry): Segment[] {
    return this.live() && entry.kind !== 'more' && entry.kind !== 'all-projects'
      ? highlight(entry.label, this.term())
      : [{ text: entry.label, match: false }];
  }

  // ---- the field ----

  protected onFocus(): void {
    this.open.set(true);
    this.active.set(-1);
    if (this.live()) this.schedule();
  }

  protected onBlur(): void {
    this.open.set(false);
    this.active.set(-1);
    this.cache.clear();
  }

  protected onInput(value: string): void {
    this.text.set(value);
    this.active.set(-1);
    this.open.set(true);
    this.schedule();
  }

  protected onKeydown(event: KeyboardEvent): void {
    const count = this.entries().length;
    switch (event.key) {
      case 'ArrowDown':
        event.preventDefault();
        if (!this.open()) {
          this.open.set(true);
          if (event.altKey) return;
        }
        if (event.altKey) return;
        if (count > 0) this.active.update((i) => Math.min(i + 1, count - 1));
        this.scrollActive();
        return;
      case 'ArrowUp':
        if (!this.open()) return;
        event.preventDefault();
        this.active.update((i) => (i <= 0 ? -1 : i - 1));
        this.scrollActive();
        return;
      case 'Enter': {
        event.preventDefault();
        const entry = this.open() ? this.entries()[this.active()] : undefined;
        if (entry) this.pick(entry);
        else this.submit();
        return;
      }
      case 'Escape':
        if (this.open()) {
          event.preventDefault();
          event.stopPropagation();
          this.open.set(false);
          this.active.set(-1);
        } else if (this.sheetOpen()) {
          event.preventDefault();
          event.stopPropagation();
          this.closeSheet();
        } else if (this.text() !== '') {
          event.preventDefault();
          this.text.set('');
          this.schedule();
        }
        return;
      case 'Tab':
        this.open.set(false);
        this.active.set(-1);
        return;
      case 'Backspace':
        if (this.text() === '' && this.filter() !== 'all') {
          event.preventDefault();
          this.setFilter('all');
        }
        return;
    }
  }

  protected hover(index: number): void {
    this.active.set(index);
  }

  private scrollActive(): void {
    const id = this.activeId();
    if (!id) return;
    afterNextRender(() => document.getElementById(id)?.scrollIntoView?.({ block: 'nearest' }), { injector: this.injector });
  }

  // ---- the filter ----

  protected setFilter(value: SearchFilter): void {
    this.filter.set(value);
    this.active.set(-1);
    this.answered.set(null);
    this.field()?.nativeElement.focus();
    this.open.set(true);
    this.schedule();
  }

  // ---- the requests (Q-UI-408) ----

  private requestKey(): string {
    return `${this.kinds().join(',')}|${this.limit()}|${this.term().toLowerCase()}`;
  }

  private schedule(): void {
    if (this.timer) clearTimeout(this.timer);
    this.timer = null;
    if (!this.live()) {
      this.inFlight?.abort();
      this.inFlight = null;
      this.pending.set(false);
      this.failure.set(null);
      return;
    }
    const cached = this.cache.get(this.requestKey());
    if (cached && Date.now() - cached.at < CACHE_MS) {
      this.inFlight?.abort();
      this.pending.set(false);
      this.failure.set(null);
      this.show({ key: this.requestKey(), q: this.term(), answer: cached.answer });
      return;
    }
    this.pending.set(true);
    this.timer = setTimeout(() => this.send(), this.delay());
  }

  private async send(): Promise<void> {
    this.timer = null;
    const now = Date.now();
    if (now < this.retryAt) {
      this.timer = setTimeout(() => this.send(), this.retryAt - now);
      return;
    }
    if (this.anonymous()) {
      while (this.sent.length > 0 && now - this.sent[0]! >= ANONYMOUS_WINDOW_MS) this.sent.shift();
      if (this.sent.length >= ANONYMOUS_BURST) {
        this.timer = setTimeout(() => this.send(), this.sent[0]! + ANONYMOUS_WINDOW_MS - now);
        return;
      }
      this.sent.push(now);
    }
    this.inFlight?.abort();
    const controller = new AbortController();
    this.inFlight = controller;
    const key = this.requestKey();
    const q = this.term();
    this.pending.set(true);
    try {
      const answer = await this.api.suggest({ q, kinds: this.kinds(), limit: this.limit(), signal: controller.signal });
      if (controller.signal.aborted) return;
      this.cache.set(key, { answer, at: Date.now() });
      while (this.cache.size > CACHE_SIZE) this.cache.delete(this.cache.keys().next().value!);
      this.failure.set(null);
      this.show({ key, q, answer });
    } catch (e) {
      if (controller.signal.aborted || (e instanceof DOMException && e.name === 'AbortError')) return;
      if (e instanceof ApiError && e.status === 429) {
        const seconds = Math.max(1, e.retryAfterSeconds ?? 1);
        this.retryAt = Date.now() + seconds * 1000;
        this.failure.set({ kind: 'limited', seconds });
        this.announcement.set($localize`:@@search.limited:Too many searches. Suggestions return in ${seconds}:seconds: s.`);
        this.timer = setTimeout(() => this.send(), seconds * 1000);
      } else {
        this.failure.set({ kind: 'error' });
        this.announcement.set($localize`:@@search.error:Suggestions are not available.`);
      }
    } finally {
      if (this.inFlight === controller) {
        this.inFlight = null;
        this.pending.set(false);
      }
    }
  }

  private show(answer: Answer): void {
    this.answered.set(answer);
    this.pending.set(false);
    const count = this.entries().length;
    this.announcement.set(
      count === 0
        ? $localize`:@@search.announce-none:No suggestions.`
        : count === 1
          ? $localize`:@@search.announce-one:1 suggestion.`
          : $localize`:@@search.announce-many:${count}:count: suggestions.`,
    );
  }

  private async confirm(names: readonly string[], key: string): Promise<void> {
    if (this.confirmed()?.key === key || this.confirming === key) return;
    if (names.length === 0) {
      this.confirmed.set({ key, rows: [] });
      return;
    }
    this.confirming = key;
    try {
      const page = await this.repositories.list({ name: names })(null, names.length);
      if (this.confirming === key) this.confirmed.set({ key, rows: page.items });
    } catch {
      // A convenience: without the answer the group is left out, and the entries stay stored.
      if (this.confirming === key) this.confirmed.set({ key, rows: [] });
    } finally {
      if (this.confirming === key) this.confirming = null;
    }
  }

  // ---- the ways out (R-UI-099) ----

  /** A click on an entry, or Enter while it is active: an explicit choice. */
  protected pick(entry: SearchEntry): void {
    const opened = this.go(entry.commands, entry.queryParams, entry.area, true);
    if (opened && entry.kind !== 'more') this.text.set('');
  }

  /** Enter without an active entry: the results page of the main area, or nothing under a Users or Groups filter (Q-UI-413). */
  protected submit(): void {
    const f = this.filter();
    if (f === 'user' || f === 'group') {
      this.announcement.set(
        f === 'user'
          ? $localize`:@@search.choose-user:Choose a user from the list.`
          : $localize`:@@search.choose-group:Choose a group from the list.`,
      );
      return;
    }
    const q = this.term();
    const firstKind = f === 'all' ? firstKindOf(this.groups()) : null;
    this.go(['/search'], { q, ...(f === 'all' ? {} : { kind: f }) }, 'main', false, firstKind ? { firstKind } : undefined);
  }

  /**
   * The one function every way out of the field goes through (R-UI-099): it builds the target, asks `gate`, and opens it, or
   * opens nothing. Returns whether it opened.
   */
  private go(
    commands: readonly string[],
    queryParams: Readonly<Record<string, string>> | undefined,
    area: SearchEntry['area'],
    chosen: boolean,
    state?: Record<string, string>,
  ): boolean {
    const tree = this.router.createUrlTree([...commands], { queryParams });
    const url = gate({ url: this.router.serializeUrl(tree), area, chosen }, this.instanceAdmin());
    if (url === null) return false;
    this.open.set(false);
    this.active.set(-1);
    this.closeSheet(false);
    void this.router.navigateByUrl(tree, state ? { state } : undefined);
    return true;
  }

  // ---- the sheet (Q-UI-403) ----

  protected openSheet(): void {
    this.sheetOpen.set(true);
    afterNextRender(() => this.field()?.nativeElement.focus(), { injector: this.injector });
  }

  protected closeSheet(refocus = true): void {
    if (!this.sheetOpen()) return;
    this.sheetOpen.set(false);
    this.open.set(false);
    if (refocus) afterNextRender(() => this.sheetButton()?.nativeElement.focus(), { injector: this.injector });
  }
}
