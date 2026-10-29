import { ResourceRef, signal } from '@angular/core';
import { ApiError } from '../api/problem';
import { PagedList, PageFetcher } from '../api/pagination';

/** Wraps any failure of a list call in an `ApiError`, so that `<trove-error-notice>` can show it. */
export function asApiError(e: unknown): ApiError {
  return e instanceof ApiError ? e : new ApiError(0, null, null);
}

/**
 * The value of a resource, or undefined while it loads or after it failed. `resource.value()`
 * throws in the error state, which a template that shows the failure next to the rest must avoid.
 */
export function valueOf<T>(r: ResourceRef<T>): Exclude<T, undefined> | undefined {
  return r.hasValue() ? (r.value() as Exclude<T, undefined>) : undefined;
}

/**
 * A list with a "Load more" button (R-UI-006, R-API-010) as signals. `start` begins a new
 * traversal, which is how a filter or sort change restarts: a cursor is bound to its filters.
 * An answer of an earlier traversal that arrives late is dropped.
 */
export class PagedResource<T> {
  readonly items = signal<readonly T[]>([]);
  readonly loading = signal(false);
  readonly error = signal<ApiError | null>(null);
  /** True once the first page has arrived. */
  readonly loaded = signal(false);
  readonly hasMore = signal(false);

  private list: PagedList<T> | null = null;
  private fetcher: PageFetcher<T> | null = null;
  private limit: number | undefined;
  private generation = 0;

  /** `limit` is the page size of every traversal that does not pass its own. */
  constructor(private readonly defaultLimit?: number) {}

  /** Starts a new traversal and loads its first page. */
  start(fetcher: PageFetcher<T>, limit: number | undefined = this.defaultLimit): Promise<void> {
    this.fetcher = fetcher;
    this.limit = limit;
    this.list = new PagedList(fetcher, limit);
    this.error.set(null);
    this.items.set([]);
    this.loaded.set(false);
    this.hasMore.set(false);
    this.generation++;
    this.loading.set(false);
    return this.more();
  }

  /** Starts the current traversal again from the first page. */
  async reload(): Promise<void> {
    if (this.fetcher) await this.start(this.fetcher, this.limit);
  }

  /** Loads the next page of the current traversal. */
  async more(): Promise<void> {
    const list = this.list;
    if (!list || this.loading()) return;
    const generation = this.generation;
    this.loading.set(true);
    this.error.set(null);
    try {
      const items = await list.loadMore();
      if (generation !== this.generation) return;
      this.items.set(items);
      this.hasMore.set(list.hasMore);
      this.loaded.set(true);
    } catch (e) {
      if (generation === this.generation) this.error.set(asApiError(e));
    } finally {
      if (generation === this.generation) this.loading.set(false);
    }
  }

  /** Replaces one item in place, e.g. after a toggle. */
  replace(match: (item: T) => boolean, next: T): void {
    this.items.update((list) => list.map((i) => (match(i) ? next : i)));
  }

  /** Drops items, e.g. after a delete. */
  remove(match: (item: T) => boolean): void {
    this.items.update((list) => list.filter((i) => !match(i)));
  }
}
