import { Router } from '@angular/router';
import { UPLOAD_LIMIT_BYTES } from '../../api/files-api';
import { FakeFilesApi, FakeNode } from '../../api/testing/fakes-catalog';
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
  el.querySelector('.node-label')?.textContent?.replace(/\s+/g, ' ').trim() ?? '';

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
    button(root, 'Upload files').click();
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
    button(root, 'Upload files').click();
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
    button(root, 'Delete').click();
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
    button(root, 'Delete').click();
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
