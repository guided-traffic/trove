import { signal } from '@angular/core';
import { ApiError } from '../../api/problem';
import { DEFAULT_PAGE_LIMIT, PageFetcher } from '../../api/pagination';

/** Any thrown value as an `ApiError`, so that a screen can hand it to `<trove-error-notice>`. */
export function toApiError(e: unknown): ApiError {
  return e instanceof ApiError ? e : new ApiError(0, null, null);
}

/**
 * The accumulating list of a "Load more" screen (R-UI-006, R-API-010) as signals. `start` begins
 * a new traversal with a new fetcher, which is how a filter change restarts (a cursor is bound
 * to its filters); an answer of a superseded traversal is dropped.
 */
export class PagedSignal<T> {
  readonly items = signal<readonly T[]>([]);
  readonly loading = signal(false);
  readonly error = signal<ApiError | null>(null);
  /** True once the first page of the current traversal arrived. */
  readonly loaded = signal(false);
  readonly hasMore = signal(false);

  private fetcher: PageFetcher<T> | null = null;
  private cursor: string | null = null;
  private generation = 0;

  constructor(private readonly limit: number = DEFAULT_PAGE_LIMIT) {}

  async start(fetcher: PageFetcher<T>): Promise<void> {
    this.generation++;
    this.fetcher = fetcher;
    this.cursor = null;
    this.items.set([]);
    this.loaded.set(false);
    this.loading.set(false);
    this.hasMore.set(false);
    this.error.set(null);
    await this.more();
  }

  /** Reloads from the first page with the same fetcher. */
  async reload(): Promise<void> {
    if (this.fetcher) await this.start(this.fetcher);
  }

  async more(): Promise<void> {
    const fetcher = this.fetcher;
    if (!fetcher || this.loading()) return;
    const generation = this.generation;
    this.loading.set(true);
    this.error.set(null);
    try {
      const page = await fetcher(this.cursor, this.limit);
      if (generation !== this.generation) return;
      this.items.update((list) => [...list, ...page.items]);
      this.cursor = page.nextCursor;
      this.hasMore.set(page.nextCursor !== null);
      this.loaded.set(true);
    } catch (e) {
      if (generation === this.generation) this.error.set(toApiError(e));
    } finally {
      if (generation === this.generation) this.loading.set(false);
    }
  }
}
