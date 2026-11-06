import { ApiError } from '../../api/problem';
import { FormatDescriptor, MirrorStatus, RepositoriesApi, Repository } from '../../api/repositories-api';
import { SessionApi } from '../../api/session-api';
import { FakeSessionApi } from '../../api/testing/fakes';
import {
  FakeRepositoriesApi,
  GENERIC_FORMAT,
  PACKAGE_FORMAT,
  sampleMirrorStatus,
} from '../../api/testing/fakes-catalog';
import { button, expectAccessible, renderScreen, settle, text, type } from '../../testing/screen';
import { RepositoryDetail } from './repository-detail';
import { MIRROR_DEFAULTS } from './repository-model';

const NPM: FormatDescriptor = {
  ...PACKAGE_FORMAT,
  mirror: true,
  defaultUpstreamUrl: 'https://registry.npmjs.org/',
};

const stored = (mode: 'pullThrough' | 'mirror', mirror: Record<string, unknown> = {}): Repository =>
  ({
    key: 'npm-remote',
    class: 'remote',
    format: 'npm',
    project: 'web',
    remote: {
      url: 'https://registry.npmjs.org/',
      mode,
      mirror: { ...MIRROR_DEFAULTS, ...mirror },
    },
  }) as unknown as Repository;

async function open(
  repo: Repository = stored('mirror'),
  options: {
    capabilities?: readonly string[];
    projectAdmin?: boolean;
    statuses?: readonly MirrorStatus[];
  } = {},
) {
  const r = await renderScreen(RepositoryDetail, {
    inputs: { project: repo.project ?? 'web', key: repo.key },
    ...(options.capabilities ? { capabilities: options.capabilities } : {}),
    setup: (get) => {
      const fake = get(RepositoriesApi) as FakeRepositoriesApi;
      fake.formatsResult = [GENERIC_FORMAT, NPM];
      fake.repositories.push(repo);
      if (options.statuses) fake.mirrorStatuses = [...options.statuses];
      if (options.projectAdmin) {
        (get(SessionApi) as FakeSessionApi).meResult = {
          roles: [],
          capabilities: [],
          principals: ['user:alice'],
          projects: [{ key: 'web', role: 'owner' }],
        };
      }
    },
  });
  const tab = [...r.root.querySelectorAll<HTMLElement>('[role="tab"]')].find(
    (t) => t.textContent?.trim() === 'Mirror',
  );
  tab?.click();
  await settle(r.fixture);
  return { r, fake: r.get(RepositoriesApi) as FakeRepositoriesApi };
}

const stateOf = (root: HTMLElement) =>
  root.querySelector('[data-testid="mirror-state"]')?.getAttribute('data-state');
const control = (root: ParentNode, id: string) => root.querySelector<HTMLInputElement>(`#${id}`)!;

describe('The Mirror tab of the edit form: status, pause, resume, run (R-UI-021, R-REPO-056, Q-UI-104, Q-UI-105)', () => {
  it('R-UI-021 Q-UI-119 a remote stored in mode mirror shows the status above the settings', async () => {
    const { r, fake } = await open();
    expect(fake.mirrorStatusCalls).toBe(1);
    expect(stateOf(r.root)).toBe('scheduled');
    const tab = r.root.querySelector('.tab')!;
    const status = tab.querySelector('trove-mirror-status')!;
    const settings = tab.querySelector('trove-mirror-settings')!;
    expect(status.compareDocumentPosition(settings) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(text(tab)).toContain('Mirror status');
    await expectAccessible(r.root);
  });

  it('R-UI-021 a remote stored in pull-through mode reads no status and shows only the settings; choosing mirror says the status comes after the save', async () => {
    const { r, fake } = await open(stored('pullThrough'));
    expect(fake.mirrorStatusCalls).toBe(0);
    expect(r.root.querySelector('trove-mirror-status')).toBeNull();
    const radio = r.root.querySelector<HTMLInputElement>('input[name="remote-mode"][value="mirror"]')!;
    radio.checked = true;
    radio.dispatchEvent(new Event('change', { bubbles: true }));
    await settle(r.fixture);
    expect(text(r.root)).toContain('The status appears once the repository is saved in mode mirror.');
    expect(fake.mirrorStatusCalls).toBe(0);
  });

  it('R-UI-021 the status is not read before the tab is opened, and not again after it is left', async () => {
    const r = await renderScreen(RepositoryDetail, {
      inputs: { project: 'web', key: 'npm-remote' },
      setup: (get) => {
        const fake = get(RepositoriesApi) as FakeRepositoriesApi;
        fake.formatsResult = [GENERIC_FORMAT, NPM];
        fake.repositories.push(stored('mirror'));
      },
    });
    const fake = r.get(RepositoriesApi) as FakeRepositoriesApi;
    expect(fake.mirrorStatusCalls).toBe(0);
    const tabs = [...r.root.querySelectorAll<HTMLElement>('[role="tab"]')];
    tabs.find((t) => t.textContent?.trim() === 'Mirror')!.click();
    await settle(r.fixture);
    expect(fake.mirrorStatusCalls).toBe(1);
    tabs.find((t) => t.textContent?.trim() === 'General')!.click();
    await settle(r.fixture);
    expect(r.root.querySelector('trove-mirror-status')).toBeNull();
  });

  describe('who sees the buttons (Q-UI-120)', () => {
    it('R-UI-021 R-REPO-056 an administrator with write-config sees Pause and Run', async () => {
      const { r } = await open();
      expect(button(r.root, 'Pause')).toBeDefined();
      expect(button(r.root, 'Run a pass now')).toBeDefined();
    });

    it('R-UI-021 R-REPO-056 a project Owner sees the status and the buttons', async () => {
      const { r } = await open(stored('mirror'), { projectAdmin: true });
      expect(stateOf(r.root)).toBe('scheduled');
      expect(button(r.root, 'Pause')).toBeDefined();
      expect(button(r.root, 'Run a pass now')).toBeDefined();
    });

    it('R-UI-021 R-REPO-056 a caller who may read the configuration but not write it sees the status and no buttons', async () => {
      const { r } = await open(stored('mirror'), { capabilities: ['repositories:read-config'] });
      expect(stateOf(r.root)).toBe('scheduled');
      expect(() => button(r.root, 'Pause')).toThrow();
      expect(() => button(r.root, 'Run a pass now')).toThrow();
      expect(() => button(r.root, 'Resume')).toThrow();
    });
  });

  describe('pause and resume patch the document with If-Match (Q-UI-105)', () => {
    it('R-UI-021 R-REPO-056 Pause sends {"remote":{"mirror":{"paused":true}}} with the loaded ETag and reads the status again', async () => {
      const { r, fake } = await open();
      fake.mirrorStatuses = [sampleMirrorStatus(), sampleMirrorStatus({ state: 'paused' })];
      button(r.root, 'Pause').click();
      await settle(r.fixture);
      expect(fake.patches).toHaveLength(1);
      expect(fake.patches[0]).toMatchObject({
        key: 'web/npm-remote',
        patch: { remote: { mirror: { paused: true } } },
        etag: '"1"',
      });
      expect(stateOf(r.root)).toBe('paused');
      expect(button(r.root, 'Resume')).toBeDefined();
      expect(control(r.root, 'f-remote-mirror-paused').checked).toBe(true);
      expect(r.fixture.componentInstance.hasUnsavedChanges()).toBe(false);
    });

    it('R-UI-021 Resume sends paused false with the ETag of the pause', async () => {
      const { r, fake } = await open(stored('mirror', { paused: true }), {
        statuses: [sampleMirrorStatus({ state: 'paused' }), sampleMirrorStatus()],
      });
      button(r.root, 'Resume').click();
      await settle(r.fixture);
      expect(fake.patches[0]).toMatchObject({ patch: { remote: { mirror: { paused: false } } }, etag: '"1"' });
      expect(stateOf(r.root)).toBe('scheduled');
      expect(control(r.root, 'f-remote-mirror-paused').checked).toBe(false);
    });

    it('R-UI-021 a pause keeps the other unsaved edits of the form, and a later save uses the new ETag', async () => {
      const { r, fake } = await open();
      type(control(r.root, 'f-remote-mirror-intervalSeconds'), '7200');
      await settle(r.fixture);
      button(r.root, 'Pause').click();
      await settle(r.fixture);
      expect(control(r.root, 'f-remote-mirror-intervalSeconds').value).toBe('7200');
      expect(r.fixture.componentInstance.hasUnsavedChanges()).toBe(true);
      button(r.root, 'Save changes').click();
      await settle(r.fixture);
      expect(fake.patches[1]).toMatchObject({
        patch: { remote: { mirror: { intervalSeconds: 7200 } } },
        etag: '"2"',
      });
    });

    it('R-UI-042 a 412 on a pause that another administrator already made is a success without a dialog', async () => {
      const { r, fake } = await open();
      fake.beforePatch = () => {
        fake.repositories[0] = stored('mirror', { paused: true });
        fake.versions.set('web/npm-remote', 2);
        fake.beforePatch = null;
      };
      button(r.root, 'Pause').click();
      await settle(r.fixture);
      expect(r.root.ownerDocument.body.querySelector('[role="dialog"]')).toBeNull();
      expect(fake.patches).toHaveLength(1);
      expect(control(r.root, 'f-remote-mirror-paused').checked).toBe(true);
    });

    it('R-UI-042 a 412 on a pause after another administrator changed the document opens the conflict dialog; Overwrite pauses with the new ETag', async () => {
      const { r, fake } = await open();
      fake.beforePatch = () => {
        fake.repositories[0] = { ...stored('mirror', { intervalSeconds: 600 }) };
        fake.versions.set('web/npm-remote', 2);
        fake.beforePatch = null;
      };
      button(r.root, 'Pause').click();
      await settle(r.fixture);
      const body = r.root.ownerDocument.body;
      expect(text(body)).toContain('Changed by someone else since you opened it');
      button(body, 'Overwrite').click();
      await settle(r.fixture);
      expect(fake.patches.at(-1)).toMatchObject({ patch: { remote: { mirror: { paused: true } } }, etag: '"2"' });
      expect((fake.repositories[0] as Repository).remote?.mirror?.paused).toBe(true);
      expect((fake.repositories[0] as Repository).remote?.mirror?.intervalSeconds).toBe(600);
    });

    it('R-UI-042 Reload in that dialog takes the stored document and writes nothing', async () => {
      const { r, fake } = await open();
      fake.beforePatch = () => {
        fake.repositories[0] = stored('mirror', { intervalSeconds: 600 });
        fake.versions.set('web/npm-remote', 2);
        fake.beforePatch = null;
      };
      button(r.root, 'Pause').click();
      await settle(r.fixture);
      button(r.root.ownerDocument.body, /Reload/).click();
      await settle(r.fixture);
      expect(fake.patches).toHaveLength(1);
      expect(control(r.root, 'f-remote-mirror-intervalSeconds').value).toBe('600');
    });

    it('R-UI-021 a refused pause (Q-REPO-142 and others) is shown as an error and changes nothing', async () => {
      const { r, fake } = await open();
      fake.patch = async () => {
        throw new ApiError(403, { code: 'forbidden' }, null);
      };
      button(r.root, 'Pause').click();
      await settle(r.fixture);
      expect(r.root.querySelector('[role="alert"]')?.textContent).toContain('permission');
      expect(stateOf(r.root)).toBe('scheduled');
    });
  });

  describe('run a pass', () => {
    it('R-UI-021 R-REPO-056 Run a pass now posts the run for the repository and shows its job', async () => {
      const { r, fake } = await open();
      button(r.root, 'Run a pass now').click();
      await settle(r.fixture);
      expect(fake.mirrorRuns).toMatchObject([{ key: 'web/npm-remote', full: false }]);
      expect(text(r.root.querySelector('trove-mirror-status')!)).toContain('A pass is queued.');
    });
  });
});
