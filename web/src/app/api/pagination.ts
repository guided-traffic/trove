/** The envelope of every list endpoint (R-API-010). */
export interface Page<T> {
  readonly items: readonly T[];
  /** Opaque; null on the last page. */
  readonly nextCursor: string | null;
  /** Present only where the endpoint accepts `includeTotal=true`. */
  readonly total?: number;
}

/** Default and maximum page size (R-API-010, `api.page_size_*`). */
export const DEFAULT_PAGE_LIMIT = 50;
export const MAX_PAGE_LIMIT = 500;

/** Fetches one page; `cursor` is null for the first. */
export type PageFetcher<T> = (cursor: string | null, limit: number) => Promise<Page<T>>;

/**
 * Walks from the first page to the last and returns every item. The cursor is never inspected
 * (R-API-010: opaque); a repeated cursor ends the walk with an error so that a misbehaving
 * server cannot make the UI loop forever.
 */
export async function collectAll<T>(
  fetchPage: PageFetcher<T>,
  limit: number = MAX_PAGE_LIMIT,
): Promise<T[]> {
  const items: T[] = [];
  const seen = new Set<string>();
  let cursor: string | null = null;
  do {
    const page: Page<T> = await fetchPage(cursor, limit);
    items.push(...page.items);
    cursor = page.nextCursor;
    if (cursor !== null) {
      if (seen.has(cursor)) throw new Error('pagination: the server repeated a cursor');
      seen.add(cursor);
    }
  } while (cursor !== null);
  return items;
}

/**
 * Accumulating list for a "Load more" button (R-UI-006): keeps the items of the pages loaded so
 * far and the cursor of the next one. A new instance starts a new traversal, which is also how a
 * filter or sort change restarts (a cursor is bound to its filters and sort).
 */
export class PagedList<T> {
  private nextCursor: string | null = null;
  private started = false;
  private _items: readonly T[] = [];

  constructor(
    private readonly fetchPage: PageFetcher<T>,
    private readonly limit: number = DEFAULT_PAGE_LIMIT,
  ) {}

  get items(): readonly T[] {
    return this._items;
  }

  /** True before the first load and while a next page exists. */
  get hasMore(): boolean {
    return !this.started || this.nextCursor !== null;
  }

  /** Loads the next page and returns the whole list so far. */
  async loadMore(): Promise<readonly T[]> {
    if (!this.hasMore) return this._items;
    const page = await this.fetchPage(this.nextCursor, this.limit);
    this.started = true;
    this.nextCursor = page.nextCursor;
    this._items = [...this._items, ...page.items];
    return this._items;
  }
}
