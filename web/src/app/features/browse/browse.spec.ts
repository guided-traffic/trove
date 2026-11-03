import { Router } from '@angular/router';
import { UPLOAD_LIMIT_BYTES } from '../../api/files-api';
import {
  FakeFilesApi,
  FakeNode,
  FakeRepositoriesApi,
  GENERIC_FORMAT,
  PACKAGE_FORMAT,
} from '../../api/testing/fakes-catalog';
import { RepositoriesApi, Repository } from '../../api/repositories-api';
import { FilesApi } from '../../api/files-api';
import { ApiError } from '../../api/problem';
import { SessionApi } from '../../api/session-api';
import { FakeSessionApi } from '../../api/testing/fakes';
import {
  button,
  expectAccessible,
  renderScreen,
  settle,
  sleep,
  text,
  type,
} from '../../testing/screen';
import { Browse } from './browse';

async function open(
  inputs: Record<string, unknown> = {},
  setup?: (files: FakeFilesApi) => void,
  allow?: (check: { action: string }) => boolean,
) {
  const rendered = await renderScreen(Browse, {
    inputs,
    setup: (get) => {
      if (allow) (get(SessionApi) as FakeSessionApi).allow = allow;
      vi.spyOn(get(Router), 'navigate').mockResolvedValue(true);
      setup?.(get(FilesApi) as FakeFilesApi);
    },
  });
  return {
    ...rendered,
    files: rendered.get(FilesApi) as FakeFilesApi,
    router: rendered.get(Router),
  };
}

const items = (root: HTMLElement) => [...root.querySelectorAll<HTMLElement>('[role="treeitem"]')];
const labelOf = (el: HTMLElement) =>
  (
    el.querySelector(':scope > .node-label .node-name') ?? el.querySelector(':scope > .node-label')
  )?.textContent
    ?.replace(/\s+/g, ' ')
    .trim() ?? '';

describe('File tree', () => {
  it('R-UI-010 the tree lists the readable repositories as tree items with the ARIA tree attributes, and passes the accessibility check', async () => {
    const { root } = await open();
    const tree = root.querySelector('[role="tree"]');
    expect(tree).toBeTruthy();
    const nodes = items(root);
    expect(nodes.map(labelOf)).toEqual(
      expect.arrayContaining([expect.stringContaining('generic-local')]),
    );
    expect(nodes[0]?.getAttribute('aria-level')).toBe('1');
    expect(nodes[0]?.getAttribute('aria-expanded')).toBe('false');
    expect(nodes[0]?.hasAttribute('aria-setsize')).toBe(true);
    expect(nodes[0]?.hasAttribute('aria-posinset')).toBe(true);
    expect(text(root)).toContain('Select a repository, folder or file in the tree.');
    await expectAccessible(root);
  });

  it('R-UI-010 a deep link expands the ancestors and selects the file; details show path, digests with copy buttons and the download link', async () => {
    const { root, files } = await open({ repo: 'generic-local', path: 'releases/1.0/app.zip' });
    const labels = items(root).map(labelOf);
    expect(labels).toEqual(
      expect.arrayContaining([
        expect.stringContaining('releases'),
        expect.stringContaining('1.0'),
        expect.stringContaining('app.zip'),
      ]),
    );
    const selected = items(root).filter((i) => i.getAttribute('aria-selected') === 'true');
    expect(selected.map(labelOf)).toEqual([expect.stringContaining('app.zip')]);
    expect(files.childCalls.map((c) => c.path)).toEqual(['', 'releases', 'releases/1.0']);

    expect(text(root)).toContain('releases/1.0/app.zip');
    expect(text(root)).toContain('SHA-256');
    expect(
      root.querySelectorAll('trove-copy-button button[aria-label="Copy checksum"]').length,
    ).toBeGreaterThan(1);
    const download = [...root.querySelectorAll<HTMLAnchorElement>('a')].find((a) =>
      a.textContent?.includes('Download'),
    );
    expect(download?.getAttribute('href')).toBe(
      'https://trove.example.net/files/generic-local/releases/1.0/app.zip',
    );
    await expectAccessible(root);
  });

  it('R-UI-010 with the keyboard only a user expands a repository, reaches a file, opens its details', async () => {
    const { root, fixture, router } = await open();
    const tree = root.querySelector<HTMLElement>('[role="tree"]')!;
    const repo = items(root).find((i) => labelOf(i).includes('generic-local'))!;
    repo.focus();
    repo.dispatchEvent(new FocusEvent('focusin', { bubbles: true }));
    const press = async (key: string) => {
      (root.ownerDocument.activeElement ?? tree).dispatchEvent(
        new KeyboardEvent('keydown', { key, bubbles: true }),
      );
      await settle(fixture);
    };
    await press('ArrowRight');
    expect(repo.getAttribute('aria-expanded')).toBe('true');
    await press('ArrowDown');
    await press('ArrowRight');
    await press('ArrowDown');
    await press('ArrowRight');
    await press('ArrowDown');
    expect(root.ownerDocument.activeElement?.textContent).toContain('app.zip');
    await press('Enter');
    expect(router.navigate).toHaveBeenCalledWith(
      ['/browse', 'generic-local', 'releases', '1.0', 'app.zip'],
      expect.anything(),
    );
  });

  it('R-UI-010 a folder with 450 entries shows the first page and "Load more" loads the rest', async () => {
    const { root, fixture, files } = await open({ repo: 'big', path: '' }, (f) => {
      const nodes: FakeNode[] = Array.from({ length: 450 }, (_, i) => ({
        path: `f${String(i).padStart(3, '0')}.bin`,
        kind: 'file',
        size: 1,
      }));
      f.tree.set('big', nodes);
    });
    const files1 = () => items(root).filter((i) => labelOf(i).includes('.bin')).length;
    expect(files1()).toBe(200);
    const more = items(root).find((i) => labelOf(i) === 'Load more')!;
    more.click();
    await settle(fixture);
    expect(files1()).toBe(400);
    expect(files.childCalls.at(-1)?.cursor).toBe('200');
  });

  it('R-UI-010 and R-UI-005 a folder offers upload and new folder only to a caller who may deploy there', async () => {
    const allowed = await open({ repo: 'generic-local', path: 'releases' });
    expect(text(allowed.root)).toContain('Upload files');
    expect(text(allowed.root)).toContain('New folder');

    const denied = await open(
      { repo: 'generic-local', path: 'releases' },
      undefined,
      (c) => c.action !== 'deploy',
    );
    expect(text(denied.root)).not.toContain('Upload files');
    expect(text(denied.root)).not.toContain('New folder');
    expect(text(denied.root)).toContain('Delete');
  });

  it('R-UI-012 uploads a file with progress, the SHA-256 in Repr-Digest and create-only unless "Replace" is ticked; a file above the limit is refused before any request', async () => {
    const { root, fixture, files } = await open({ repo: 'generic-local', path: 'releases' });
    button(root, /Upload files/).click();
    await settle(fixture);
    const body = root.ownerDocument.body;
    expect(body.querySelector<HTMLInputElement>('#upload-folder')?.value).toBe('releases');

    const small = new File(['hello'], 'hello.txt', { type: 'text/plain' });
    const huge = new File(['x'], 'huge.bin');
    Object.defineProperty(huge, 'size', { value: UPLOAD_LIMIT_BYTES + 1 });
    const drop = new Event('drop', { bubbles: true, cancelable: true });
    Object.defineProperty(drop, 'dataTransfer', { value: { files: [small, huge] } });
    body.querySelector('.drop')!.dispatchEvent(drop);
    await settle(fixture);
    expect(text(body)).toContain('Larger than 100 MiB');
    await expectAccessible(body.querySelector('mat-dialog-container')!);

    button(body, 'Upload').click();
    await sleep(fixture);
    await sleep(fixture);
    expect(files.uploads).toHaveLength(1);
    expect(files.uploads[0]).toMatchObject({
      repository: 'generic-local',
      path: 'releases/hello.txt',
      overwrite: false,
    });
    expect(files.uploads[0]?.reprDigest).toMatch(/^sha-256=:[A-Za-z0-9+/=]+:$/);
    expect(text(body)).toContain('uploaded');

    button(body, 'Close').click();
    await settle(fixture);
    expect(
      items(root)
        .map(labelOf)
        .some((l) => l.includes('hello.txt')),
    ).toBe(true);
  });

  it('R-UI-012 a refused upload names the reason and the file stays in the list as failed', async () => {
    const { root, fixture } = await open({ repo: 'generic-local', path: 'releases' }, (f) => {
      f.uploadError = new ApiError(403, { code: 'not_admitted', detail: 'no' }, null);
    });
    button(root, /Upload files/).click();
    await settle(fixture);
    const body = root.ownerDocument.body;
    const drop = new Event('drop', { bubbles: true, cancelable: true });
    Object.defineProperty(drop, 'dataTransfer', { value: { files: [new File(['a'], 'a.bin')] } });
    body.querySelector('.drop')!.dispatchEvent(drop);
    await settle(fixture);
    button(body, 'Upload').click();
    await sleep(fixture);
    await sleep(fixture);
    expect(text(body)).toContain('do not admit this path');
    await expectAccessible(body.querySelector('mat-dialog-container')!);
  });

  it('R-UI-011 deleting a folder with more than 100 items requires typing its name and runs as a job whose progress is shown', async () => {
    const { root, fixture, files } = await open(
      { repo: 'generic-local', path: 'releases' },
      (f) => {
        f.countResult = { count: 101, capped: true };
        f.deleteResult = {
          done: false,
          job: {
            id: 'job-del',
            kind: 'items-delete',
            state: 'queued',
            href: '/api/v1/system/jobs/job-del',
          },
        };
      },
    );
    button(root, /Delete$/).click();
    await settle(fixture);
    const body = root.ownerDocument.body;
    expect(text(body)).toContain('more than 100 items');
    const confirm = button(body, 'Delete');
    const dialogConfirm = [...body.querySelectorAll('mat-dialog-actions button')].find(
      (b) => b.textContent?.trim() === 'Delete',
    ) as HTMLButtonElement;
    expect(dialogConfirm.disabled).toBe(true);
    type(body.querySelector<HTMLInputElement>('#confirm-name')!, 'releases');
    await settle(fixture);
    expect(dialogConfirm.disabled).toBe(false);
    dialogConfirm.click();
    await settle(fixture);
    expect(files.deleted).toEqual(['generic-local/releases']);
    expect(text(root)).toContain('Deleting');
    expect(confirm).toBeTruthy();
  });

  it('R-UI-011 deleting a file asks for confirmation naming the path; the tree no longer shows it afterwards', async () => {
    const { root, fixture, files, router } = await open({
      repo: 'generic-local',
      path: 'readme.txt',
    });
    button(root, /Delete$/).click();
    await settle(fixture);
    const body = root.ownerDocument.body;
    expect(text(body)).toContain('Delete readme.txt?');
    expect(body.querySelector('#confirm-name')).toBeNull();
    (
      [...body.querySelectorAll('mat-dialog-actions button')].find(
        (b) => b.textContent?.trim() === 'Delete',
      ) as HTMLButtonElement
    ).click();
    await settle(fixture);
    expect(files.deleted).toEqual(['generic-local/readme.txt']);
    expect(router.navigate).toHaveBeenCalledWith(['/browse', 'generic-local'], expect.anything());
  });

  it('R-UI-010 the statistics tab shows downloads, last download and downloader', async () => {
    const { root } = await open({
      repo: 'generic-local',
      path: 'releases/1.0/app.zip',
      tab: 'statistics',
    });
    expect(text(root)).toContain('Downloads');
    expect(text(root)).toContain('ci:acme/web');
  });

  it('R-UI-010 the API tab shows the item JSON and the equivalent curl command (INV-17)', async () => {
    const { root } = await open({ repo: 'generic-local', path: 'readme.txt', tab: 'api' });
    expect(text(root)).toContain('curl');
    expect(text(root)).toContain('"kind": "file"');
  });

  it('R-UI-010 the curl commands quote the header in double quotes so the shell expands $TROVE_TOKEN', async () => {
    const { root } = await open({ repo: 'generic-local', path: 'readme.txt', tab: 'api' });
    const blocks = Array.from(root.querySelectorAll('pre.code-block')).map((e) => e.textContent ?? '');
    const commands = blocks.filter((b) => b.startsWith('curl'));
    expect(commands.length).toBe(2);
    for (const c of commands) {
      expect(c).toContain('-H "Authorization: Bearer $TROVE_TOKEN"');
      expect(c).not.toContain("'Authorization: Bearer $TROVE_TOKEN'");
    }
    expect(commands[0]).toMatch(/ 'https:\/\/trove\.example\.net\/api\/v1\/repositories\/generic-local\/items\?path=readme\.txt'$/);
    expect(commands[1]).toMatch(/^curl -L -O -H /);
  });

  it('R-UI-010 an unknown repository or path shows the not-found error and passes the accessibility check', async () => {
    const { root } = await open({ repo: 'nope', path: 'a/b.txt' });
    expect(text(root)).toContain('does not exist');
    await expectAccessible(root);
  });

  it('R-UI-010 the context-menu key on the tree moves focus to the actions', async () => {
    const { root, fixture } = await open({ repo: 'generic-local', path: 'readme.txt' });
    const tree = root.querySelector<HTMLElement>('[role="tree"]')!;
    tree.dispatchEvent(new KeyboardEvent('keydown', { key: 'F10', shiftKey: true, bubbles: true }));
    await settle(fixture);
    expect(root.ownerDocument.activeElement?.closest('#browse-actions')).toBeTruthy();
  });

});

// The two-pane browser (R-UI-010, R-UI-011, Q-UI-97).
const repo = (over: Partial<Repository> & Pick<Repository, 'key' | 'class' | 'format'>): Repository => ({
  description: '',
  disabled: false,
  ...over,
});

async function openWith(
  inputs: Record<string, unknown>,
  repositories: Repository[],
  options: { capabilities?: readonly string[]; allow?: (c: { action: string }) => boolean } = {},
) {
  const rendered = await renderScreen(Browse, {
    inputs,
    ...(options.capabilities ? { capabilities: options.capabilities } : {}),
    setup: (get) => {
      if (options.allow) (get(SessionApi) as FakeSessionApi).allow = options.allow;
      const repos = get(RepositoriesApi) as FakeRepositoriesApi;
      repos.repositories = repositories;
      repos.formatsResult = [GENERIC_FORMAT, PACKAGE_FORMAT];
      const files = get(FilesApi) as FakeFilesApi;
      const nodes = files.tree.get('generic-local')!;
      for (const r of repositories) files.tree.set(r.key, nodes.map((n) => ({ ...n })));
      vi.spyOn(get(Router), 'navigate').mockResolvedValue(true);
    },
  });
  return { ...rendered, router: rendered.get(Router) };
}

const MIXED = [
  repo({ key: 'files', class: 'local', format: 'generic', description: 'Build outputs' }),
  repo({ key: 'npm-local', class: 'local', format: 'npm' }),
  repo({ key: 'npm-cache', class: 'remote', format: 'npm' }),
  repo({ key: 'npm-all', class: 'virtual', format: 'npm', virtual: { members: ['npm-local', 'npm-cache'] } }),
];

describe('Browser panes', () => {
  it('R-UI-010 the screen has a tree pane and a details pane, a filter field, a count, and passes axe', async () => {
    const { root } = await openWith({}, MIXED);
    expect(root.querySelector('.pane-tree [role="tree"]')).toBeTruthy();
    expect(root.querySelector('.pane-details')).toBeTruthy();
    expect(root.querySelector('input[type=search][aria-label="Filter repositories"]')).toBeTruthy();
    expect(text(root.querySelector('.pane-count')!)).toBe('Repositories: 4');
    expect(root.querySelector('.tree-toolbar button[type=submit]')).toBeNull();
    expect(text(root)).toContain('Nothing selected');
    await expectAccessible(root);
  });

  it('R-UI-010 typing in the filter field narrows the repositories after a pause, without a search button', async () => {
    const { root, fixture, router } = await openWith({}, MIXED);
    const input = root.querySelector<HTMLInputElement>('input[type=search]')!;
    type(input, 'npm');
    expect(router.navigate).not.toHaveBeenCalled();
    await sleep(fixture, 320);
    expect(router.navigate).toHaveBeenCalledWith(
      [],
      expect.objectContaining({ queryParams: { q: 'npm' }, replaceUrl: true }),
    );
  });

  it('R-UI-010 an applied class and format show as removable chips; a chip removes its filter', async () => {
    const { root, fixture, router } = await openWith({ class: 'local', format: 'npm' }, MIXED);
    const chips = [...root.querySelectorAll('.chip')].map(text);
    expect(chips).toEqual(['Class: Localclose', 'Format: npmclose']);
    expect(text(root.querySelector('.filter-count')!)).toBe('2');
    root.querySelector<HTMLButtonElement>('.chip button[aria-label="Remove the filter Class: Local"]')!.click();
    await settle(fixture);
    expect(router.navigate).toHaveBeenCalledWith(
      [],
      expect.objectContaining({ queryParams: { class: null } }),
    );
  });

  it('R-UI-010 the filter panel sets class and format, replaces the tree while open and clears all', async () => {
    const { root, fixture, router } = await openWith({}, MIXED);
    button(root, /Filters/).click();
    await settle(fixture);
    expect(root.querySelector('.tree-scroll')?.hasAttribute('hidden')).toBe(true);
    button(root, /^Class/).click();
    await settle(fixture);
    const local = [...root.querySelectorAll<HTMLInputElement>('input[name="tree-class"]')][2]!;
    local.click();
    await settle(fixture);
    button(root, 'Apply filters').click();
    await settle(fixture);
    expect(router.navigate).toHaveBeenCalledWith(
      [],
      expect.objectContaining({ queryParams: { class: 'local', format: null } }),
    );
    expect(root.querySelector('.tree-scroll')?.hasAttribute('hidden')).toBe(false);
  });

  it('R-UI-010 the sort menu offers key and class order; the class order lists virtual before local before remote', async () => {
    const { root, fixture, router } = await openWith({ sort: 'class' }, MIXED);
    const names = items(root).map(labelOf);
    expect(names).toEqual(['npm-all', 'files', 'npm-local', 'npm-cache']);
    root.querySelector<HTMLButtonElement>('.sort-button')!.click();
    await settle(fixture);
    const entries = [...root.ownerDocument.querySelectorAll('[role="menuitemradio"]')];
    expect(entries.map((e) => e.getAttribute('aria-checked'))).toEqual(['false', 'true']);
    (entries[0] as HTMLElement).click();
    await settle(fixture);
    expect(router.navigate).toHaveBeenCalledWith([], expect.objectContaining({ queryParams: { sort: null } }));
  });

  it('R-UI-010 a virtual repository lists its members in resolution order labelled "resolves from"; a remote one is labelled cache', async () => {
    const { root, fixture } = await openWith({ sort: 'class' }, MIXED);
    expect(text(items(root).find((i) => labelOf(i) === 'npm-cache')!)).toContain('cache');
    const virtual = items(root).find((i) => labelOf(i) === 'npm-all')!;
    virtual.focus();
    virtual.dispatchEvent(new FocusEvent('focusin', { bubbles: true }));
    (root.ownerDocument.activeElement ?? virtual).dispatchEvent(
      new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }),
    );
    await settle(fixture);
    const members = items(root).filter((i) => i.getAttribute('aria-level') === '2');
    expect(members.map(labelOf)).toEqual(['npm-local', 'npm-cache']);
    for (const m of members) expect(text(m)).toContain('resolves from');
  });

  it('R-UI-010 a repository shows its summary, a link to its setup and its content as the shared table', async () => {
    const { root } = await openWith({ repo: 'files', path: '' }, MIXED);
    expect(text(root.querySelector('.path')!)).toBe('files');
    expect(text(root)).toContain('Build outputs');
    expect(root.querySelector('a[href*="/setup"]')).toBeTruthy();
    const rows = [...root.querySelectorAll('trove-data-table tbody th')].map(text);
    expect(rows).toEqual(expect.arrayContaining([expect.stringContaining('releases'), expect.stringContaining('readme.txt')]));
  });
});

describe('Browser empty states and actions', () => {
  it('R-UI-010 with no repository an administrator is offered to create one, others are told to ask for access', async () => {
    const admin = await openWith({}, []);
    expect(text(admin.root)).toContain('There is no repository you may read yet.');
    expect(admin.root.querySelector('.pane-tree a[href*="/admin/repositories/new"]')).toBeTruthy();
    expect(admin.root.querySelector('.pane-tree a[href*="/help/user/files"]')).toBeTruthy();
    await expectAccessible(admin.root);

    const user = await openWith({}, [], { capabilities: [] });
    expect(user.root.querySelector('a[href*="/admin/repositories/new"]')).toBeNull();
    expect(text(user.root)).toContain('Ask an administrator for read access');
  });

  it('R-UI-010 a filter that matches nothing says so and offers to clear all filters', async () => {
    const { root, fixture, router } = await openWith({ q: 'zzz' }, MIXED);
    expect(text(root)).toContain('No repository matches.');
    button(root, 'Clear all filters').click();
    await settle(fixture);
    expect(router.navigate).toHaveBeenCalledWith(
      [],
      expect.objectContaining({ queryParams: { q: null, class: null, format: null } }),
    );
  });

  it('R-UI-011 a file offers download and copy, a folder of a generic local repository offers upload and new folder, a package repository offers neither', async () => {
    const file = await openWith({ repo: 'files', path: 'readme.txt' }, MIXED);
    expect(text(file.root.querySelector('#browse-actions')!)).toContain('Download');
    expect(text(file.root.querySelector('#browse-actions')!)).not.toContain('Upload');
    expect(file.root.querySelector('button[aria-label="Copy download URL"]')).toBeTruthy();
    expect(file.root.querySelector('button[aria-label="Copy path"]')).toBeTruthy();

    const folder = await openWith({ repo: 'files', path: 'releases' }, MIXED);
    expect(text(folder.root.querySelector('#browse-actions')!)).toContain('Upload files');
    expect(text(folder.root.querySelector('#browse-actions')!)).toContain('New folder');

    const npm = await openWith({ repo: 'npm-local', path: '' }, MIXED);
    expect(text(npm.root.querySelector('#browse-actions')!)).not.toContain('Upload');
    expect(text(npm.root.querySelector('#browse-actions')!)).toContain('Client setup');
  });

  it('R-UI-011 without the delete permission there is no Delete; without deploy no upload', async () => {
    const { root } = await openWith({ repo: 'files', path: 'releases' }, MIXED, {
      allow: (c) => c.action === 'read',
    });
    const actions = text(root.querySelector('#browse-actions')!);
    expect(actions).not.toContain('Delete');
    expect(actions).not.toContain('Upload');
  });

  it('R-UI-010 on a narrow screen the details pane has a way back to the tree', async () => {
    const { root, router, fixture } = await openWith({ repo: 'files', path: 'readme.txt' }, MIXED);
    expect(root.querySelector('.browse')?.classList.contains('has-selection')).toBe(true);
    button(root, /Back to the tree/).click();
    await settle(fixture);
    expect(router.navigate).toHaveBeenCalledWith(['/browse'], expect.anything());
  });

  it('R-UI-010 a folder is browsed from the right pane: its children link to their own address', async () => {
    const { root } = await openWith({ repo: 'files', path: 'releases' }, MIXED);
    const link = [...root.querySelectorAll<HTMLAnchorElement>('trove-data-table a.entry')].find((a) =>
      text(a).includes('1.0'),
    );
    expect(link?.getAttribute('href')).toContain('/browse/files/releases/1.0');
  });
});


describe('Browser tree nodes', () => {
  const big = (f: FakeFilesApi): void => {
    f.tree.set(
      'big',
      Array.from({ length: 450 }, (_, i) => ({ path: `f${String(i).padStart(3, '0')}.bin`, kind: 'file' as const, size: 1 })),
    );
  };
  interface Internals {
    reload(node: unknown): void;
    nodes: Map<string, { kind: string; meta(): { size?: number } }>;
  }

  it('R-UI-010 a selection revealed while "Load more" of the same folder is in flight waits for that load instead of looping', async () => {
    const { root, fixture, files } = await open({ repo: 'big', path: '' }, big);
    let release!: () => void;
    files.childGate = new Promise<void>((r) => (release = r));
    items(root).find((i) => labelOf(i) === 'Load more')!.click();
    await settle(fixture);
    // The deep link points past the first page while the second page is still on its way.
    fixture.componentRef.setInput('path', 'f300.bin');
    await settle(fixture);
    release();
    files.childGate = null;
    await sleep(fixture, 100);
    expect(items(root).some((i) => labelOf(i).includes('f300.bin') && i.getAttribute('aria-selected') === 'true')).toBe(true);
    expect(files.childCalls.filter((c) => c.path === '' && c.cursor === '200')).toHaveLength(1);
  });

  it('R-UI-010 a reloaded folder shows the new size of a file and a file that became a folder is a folder', async () => {
    const { fixture, files } = await open({ repo: 'generic-local', path: 'releases/1.0/app.zip' });
    const view = fixture.componentInstance as unknown as Internals;
    expect(view.nodes.get('generic-local/readme.txt')?.meta().size).toBe(120);
    expect(view.nodes.has('generic-local/releases/1.0/app.zip')).toBe(true);
    const nodes = files.tree.get('generic-local')!;
    const replace = (path: string, node: FakeNode): void => void nodes.splice(nodes.findIndex((n) => n.path === path), 1, node);
    replace('readme.txt', { path: 'readme.txt', kind: 'file', size: 999 });
    view.reload(view.nodes.get('generic-local')!);
    await sleep(fixture, 60);
    expect(view.nodes.get('generic-local/readme.txt')!.meta().size).toBe(999);
    replace('releases/1.0', { path: 'releases/1.0', kind: 'file', size: 7 });
    view.reload(view.nodes.get('generic-local/releases')!);
    await sleep(fixture, 60);
    expect(view.nodes.get('generic-local/releases/1.0')!.kind).toBe('file');
    expect(view.nodes.get('generic-local/releases/1.0')!.meta().size).toBe(7);
    // The vanished subtree does not stay in the map for the life of the screen.
    expect(view.nodes.has('generic-local/releases/1.0/app.zip')).toBe(false);
  });
});
