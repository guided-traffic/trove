import { Page, PagedList, collectAll } from './pagination';

function pages(...items: string[][]): (cursor: string | null) => Promise<Page<string>> {
  return async (cursor) => {
    const index = cursor === null ? 0 : Number(cursor);
    return { items: items[index] ?? [], nextCursor: index + 1 < items.length ? String(index + 1) : null };
  };
}

describe('pagination', () => {
  it('R-API-010 collectAll walks to the last page', async () => {
    expect(await collectAll(pages(['a', 'b'], ['c'], ['d']))).toEqual(['a', 'b', 'c', 'd']);
  });

  it('R-API-010 collectAll stops on a repeated cursor instead of looping', async () => {
    const loop = async (): Promise<Page<string>> => ({ items: ['x'], nextCursor: 'same' });
    await expect(collectAll(loop)).rejects.toThrow('repeated a cursor');
  });

  it('R-UI-006 PagedList loads more until nextCursor is null', async () => {
    const list = new PagedList(pages(['a'], ['b']), 1);
    expect(list.hasMore).toBe(true);
    expect(await list.loadMore()).toEqual(['a']);
    expect(list.hasMore).toBe(true);
    expect(await list.loadMore()).toEqual(['a', 'b']);
    expect(list.hasMore).toBe(false);
    expect(await list.loadMore()).toEqual(['a', 'b']);
  });
});
