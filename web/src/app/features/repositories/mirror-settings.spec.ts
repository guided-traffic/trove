import { TestBed } from '@angular/core/testing';
import { Router } from '@angular/router';
import { ApiError } from '../../api/problem';
import { FormatDescriptor, RepositoriesApi, Repository } from '../../api/repositories-api';
import { SessionApi } from '../../api/session-api';
import { FakeSessionApi } from '../../api/testing/fakes';
import {
  FakeRepositoriesApi,
  GENERIC_FORMAT,
  NUGET_FORMAT,
  PACKAGE_FORMAT,
} from '../../api/testing/fakes-catalog';
import { button, expectAccessible, renderScreen, settle, text, type } from '../../testing/screen';
import {
  boundsMirror,
  mirrorPointerLabel,
  mirrorTarget,
} from './mirror-settings';
import { MIRROR_DEFAULTS, EMPTY_FORM, mirrorOf, remoteOf, settingsOf } from './repository-model';
import { RepositoryDetail } from './repository-detail';
import { RepositoryWizard } from './repository-wizard';

/** npm as the catalog lists it once its remote adapter can discover packages: `mirror: true`, a well-known upstream. */
const NPM: FormatDescriptor = {
  ...PACKAGE_FORMAT,
  mirror: true,
};
/** A format whose remote adapter cannot discover packages. */
const NO_MIRROR: FormatDescriptor = { ...NUGET_FORMAT, mirror: false };
/** A second preset of a format, which the server refuses to mirror without a bound as it refuses the default (Q-REPO-323). */
const SECOND_PRESET = {
  id: 'second',
  displayName: 'Second registry',
  url: 'https://npm.second.example/',
  extraHosts: [],
  default: false,
};
/** A format that can mirror, with no well-known upstream of its own. */
const OPEN_UPSTREAM: FormatDescriptor = { ...NUGET_FORMAT, mirror: true, upstreamPresets: [] };

type Wizard = Awaited<ReturnType<typeof renderScreen<RepositoryWizard>>>;

const tile = (r: Wizard, name: string) =>
  [...r.root.querySelectorAll<HTMLElement>('[role="option"]')].find(
    (t) => t.querySelector('.tile-name')?.textContent?.trim() === name,
  )!;

async function next(r: Wizard) {
  button(r.root, 'Next').click();
  await settle(r.fixture);
}

/** Opens the wizard on the class-specific step of a remote repository of `format`. */
async function remoteStep(
  formats: readonly FormatDescriptor[],
  format: string,
  options: { capabilities?: readonly string[]; setup?: (fake: FakeRepositoriesApi) => void } = {},
): Promise<Wizard> {
  const r = await renderScreen(RepositoryWizard, {
    inputs: { project: 'web' },
    ...(options.capabilities ? { capabilities: options.capabilities } : {}),
    setup: (get) => {
      const fake = get(RepositoriesApi) as FakeRepositoriesApi;
      fake.formatsResult = formats;
      options.setup?.(fake);
    },
  });
  tile(r, format).click();
  await settle(r.fixture);
  await next(r);
  r.root.querySelector<HTMLInputElement>('input[name="class"][value="remote"]')!.click();
  await settle(r.fixture);
  await next(r);
  type(r.root.querySelector<HTMLInputElement>('#f-key')!, 'upstream-cache');
  await settle(r.fixture);
  await next(r);
  return r;
}

const control = (root: ParentNode, id: string) => root.querySelector<HTMLInputElement>(`#${id}`)!;

async function chooseMirror(r: Wizard) {
  const radio = r.root.querySelector<HTMLInputElement>('input[name="remote-mode"][value="mirror"]')!;
  radio.checked = true;
  radio.dispatchEvent(new Event('change', { bubbles: true }));
  await settle(r.fixture);
}

describe('Mirror settings of a remote repository', () => {
  describe('the pure rules', () => {
    it('R-REPO-052 an include pattern bounds a mirror with an exact name or a literal prefix beyond the scope sigils, as the server counts it', () => {
      for (const ok of ['@types/*', 'lodash', 'Microsoft.Extensions.*', '\\*literal*']) {
        expect(boundsMirror(ok), ok).toBe(true);
      }
      for (const bad of ['*', '**', '@*/*', '?x', '', '/*']) {
        expect(boundsMirror(bad), bad).toBe(false);
      }
    });

    it('R-REPO-050 R-UI-021 a member is written only where it differs from its default, and a create in mode pullThrough writes no remote member at all', () => {
      const form = {
        ...EMPTY_FORM,
        mirror: { intervalSeconds: 3600, concurrency: 8, includePrereleases: true, maxBytes: null, paused: false },
      };
      expect(mirrorOf(form)).toEqual({ concurrency: 8 });
      expect(remoteOf(form, 'sparse')).toBeUndefined();
      expect(remoteOf({ ...form, remoteMode: 'mirror' }, 'sparse')).toEqual({
        mode: 'mirror',
        mirror: { concurrency: 8 },
      });
      expect(remoteOf({ ...EMPTY_FORM, remoteMode: 'mirror' }, 'sparse')).toEqual({ mode: 'mirror' });
      // The edit form always writes the structure, so a merge patch never replaces the whole remote section.
      expect(remoteOf(EMPTY_FORM, 'structural')).toEqual({ mode: 'pullThrough', mirror: {} });
      expect(settingsOf(EMPTY_FORM, { name: 'npm', schema: null })['remote']).toBeUndefined();
      expect(Object.keys(MIRROR_DEFAULTS).sort()).toEqual(
        [
          'concurrency',
          'discovery',
          'fullPassIntervalSeconds',
          'includePrereleases',
          'intervalSeconds',
          'maxBytes',
          'maxVersionsPerPackage',
          'paused',
        ].sort(),
      );
    });

    it('R-UI-044 server pointers of the section map to their control and label', () => {
      expect(mirrorTarget('/remote/mode')).toBe('f-remote-mode');
      expect(mirrorTarget('/remote/mirror/intervalSeconds')).toBe('f-remote-mirror-intervalSeconds');
      expect(mirrorTarget('/remote/mirror/unknown')).toBe('f-remote-mirror');
      expect(mirrorTarget('/key')).toBeNull();
      expect(mirrorPointerLabel('/remote/mirror/maxBytes')).toBe('Storage limit in bytes');
      expect(mirrorPointerLabel('/key')).toBeNull();
    });
  });

  describe('in the wizard', () => {
    it('R-UI-021 R-REPO-050 a remote repository of a format with mirror: true shows the Mirror section, in mode pullThrough with no members, and writes no remote member', async () => {
      const r = await remoteStep([GENERIC_FORMAT, NPM], 'npm');
      const fake = r.get(RepositoriesApi) as FakeRepositoriesApi;
      vi.spyOn(TestBed.inject(Router), 'navigate').mockResolvedValue(true);

      expect(r.root.querySelector('trove-mirror-settings')).not.toBeNull();
      expect(text(r.root.querySelector('nav')!)).toContain('4. Access, upstream, npm and mirror settings');
      expect(control(r.root, 'f-remote-mode').checked).toBe(true);
      expect(r.root.querySelector('#f-remote-mirror-intervalSeconds')).toBeNull();
      await expectAccessible(r.root);

      await next(r);
      expect(text(r.root)).toContain('Pull-through (no mirror).');
      button(r.root, 'Create repository').click();
      await settle(r.fixture);
      const created = fake.repositories.find((x) => x.key === 'upstream-cache') as Repository;
      expect(created.remote).toBeUndefined();
    });

    it('R-UI-021 the section is absent for a format without mirror, and for a local or virtual repository of one with it', async () => {
      const without = await remoteStep([GENERIC_FORMAT, NO_MIRROR], 'NuGet');
      expect(without.root.querySelector('trove-mirror-settings')).toBeNull();
      expect(text(without.root.querySelector('nav')!)).not.toContain('mirror');

      for (const cls of ['local', 'virtual']) {
        const r = await renderScreen(RepositoryWizard, {
          inputs: { project: 'web' },
          setup: (get) => ((get(RepositoriesApi) as FakeRepositoriesApi).formatsResult = [NPM]),
        });
        tile(r, 'npm').click();
        await settle(r.fixture);
        await next(r);
        r.root.querySelector<HTMLInputElement>(`input[name="class"][value="${cls}"]`)!.click();
        await settle(r.fixture);
        await next(r);
        type(r.root.querySelector<HTMLInputElement>('#f-key')!, 'npm-x');
        await settle(r.fixture);
        await next(r);
        expect(r.root.querySelector('trove-mirror-settings'), cls).toBeNull();
      }
    });

    it('R-REPO-050 R-UI-021 mode mirror shows the members with their defaults and ranges; a create writes only what differs from the default', async () => {
      const r = await remoteStep([NPM], 'npm');
      const fake = r.get(RepositoriesApi) as FakeRepositoriesApi;
      vi.spyOn(TestBed.inject(Router), 'navigate').mockResolvedValue(true);
      await chooseMirror(r);

      for (const id of [
        'f-remote-mirror-paused',
        'f-remote-mirror-intervalSeconds',
        'f-remote-mirror-fullPassIntervalSeconds',
        'f-remote-mirror-discovery',
        'f-remote-mirror-concurrency',
        'f-remote-mirror-maxVersionsPerPackage',
        'f-remote-mirror-includePrereleases',
        'f-remote-mirror-maxBytes',
      ]) {
        expect(r.root.querySelector(`#${id}`), id).not.toBeNull();
      }
      expect(text(r.root)).toContain('300 to 604800. Default: 3600.');
      expect(control(r.root, 'f-remote-mirror-includePrereleases').checked).toBe(true);

      type(control(r.root, 'f-remote-mirror-intervalSeconds'), '7200');
      type(control(r.root, 'f-remote-mirror-concurrency'), '4');
      type(control(r.root, 'f-remote-mirror-maxBytes'), '1073741824');
      control(r.root, 'f-remote-mirror-includePrereleases').checked = false;
      control(r.root, 'f-remote-mirror-includePrereleases').dispatchEvent(
        new Event('change', { bubbles: true }),
      );
      await settle(r.fixture);
      await expectAccessible(r.root);

      await next(r);
      expect(text(r.root)).toContain('Mirror mode: a background pass stores');
      expect(text(r.root)).toContain('intervalSeconds: 7200');
      expect(text(r.root)).not.toContain('concurrency: 4');
      button(r.root, 'Create repository').click();
      await settle(r.fixture);
      const created = fake.repositories.find((x) => x.key === 'upstream-cache') as Repository;
      expect(created.remote).toEqual({
        mode: 'mirror',
        mirror: { intervalSeconds: 7200, includePrereleases: false, maxBytes: 1073741824 },
      });
    });

    it('R-REPO-052 R-UI-021 the well-known upstream without a bound shows the refusal sentence, a literal-prefix include or a storage limit removes it, every other preset is refused too, another upstream shows the warning', async () => {
      const r = await remoteStep([NPM], 'npm');
      await chooseMirror(r);
      const note = () => r.root.querySelector('#mirror-unbounded')?.textContent?.replace(/\s+/g, ' ');
      expect(note()).toContain('A mirror of a well-known public registry needs a bound.');
      expect(note()).toContain('the server refuses to save it');
      await expectAccessible(r.root);

      type(r.root.querySelector<HTMLTextAreaElement>('#f-includePatterns')!, '**');
      await settle(r.fixture);
      expect(note()).toContain('needs a bound');
      type(r.root.querySelector<HTMLTextAreaElement>('#f-includePatterns')!, '@types/*');
      await settle(r.fixture);
      expect(note()).toBeUndefined();
      type(r.root.querySelector<HTMLTextAreaElement>('#f-includePatterns')!, '');
      type(control(r.root, 'f-remote-mirror-maxBytes'), '2097152');
      await settle(r.fixture);
      expect(note()).toBeUndefined();

      const twoPresets = await remoteStep([{ ...NPM, upstreamPresets: [...NPM.upstreamPresets, SECOND_PRESET] }], 'npm');
      type(control(twoPresets.root, 'f-remote-url'), 'https://npm.second.example');
      await settle(twoPresets.fixture);
      await chooseMirror(twoPresets);
      const second = twoPresets.root.querySelector('#mirror-unbounded')?.textContent?.replace(/\s+/g, ' ');
      expect(second).toContain('needs a bound');

      const other = await remoteStep([OPEN_UPSTREAM], 'NuGet');
      await chooseMirror(other);
      const warning = other.root.querySelector('#mirror-unbounded')?.textContent?.replace(/\s+/g, ' ');
      expect(warning).toContain('it stores everything the upstream offers');
      expect(warning).toContain('saved with a warning');
    });

    it('R-UI-021 choosing another format starts the mirror section afresh', async () => {
      const r = await remoteStep([NPM, OPEN_UPSTREAM], 'npm');
      await chooseMirror(r);
      type(control(r.root, 'f-remote-mirror-intervalSeconds'), '7200');
      await settle(r.fixture);
      button(r.root, /1\. Format/).click();
      await settle(r.fixture);
      tile(r, 'NuGet').click();
      await settle(r.fixture);
      await next(r);
      await next(r);
      await next(r);
      expect(control(r.root, 'f-remote-mode').checked).toBe(true);
      await chooseMirror(r);
      expect(control(r.root, 'f-remote-mirror-intervalSeconds').value).toBe('');
    });

    it('R-UI-021 Q-REPO-142 a caller without repositories:write-config (a project Owner) sees the section read-only and cannot create a mirror', async () => {
      const r = await remoteStep([NPM], 'npm', { capabilities: [] });
      expect(text(r.root.querySelector('trove-mirror-settings')!)).toContain(
        'Only an administrator of the installation can switch a remote repository to mirror',
      );
      for (const radio of r.root.querySelectorAll<HTMLInputElement>('input[name="remote-mode"]')) {
        expect(radio.disabled).toBe(true);
      }
      expect(r.root.querySelector('#f-remote-mirror-intervalSeconds')).toBeNull();
      await expectAccessible(r.root);
    });

    it('R-UI-021 R-UI-044 server errors at /remote/mode and /remote/mirror/... return to the step and appear at their fields and in the summary', async () => {
      const r = await remoteStep([NPM], 'npm', {
        setup: (fake) => {
          fake.create = async () => {
            throw new ApiError(
              422,
              {
                code: 'validation_failed',
                errors: [
                  { pointer: '/remote/mode', code: 'mirror_unbounded', detail: 'needs a bound' },
                  { pointer: '/remote/mirror/intervalSeconds', code: 'out_of_range', detail: 'too small' },
                ],
              },
              null,
            );
          };
        },
      });
      await chooseMirror(r);
      type(control(r.root, 'f-remote-mirror-intervalSeconds'), '10');
      await settle(r.fixture);
      await next(r);
      button(r.root, 'Create repository').click();
      await settle(r.fixture);

      expect(text(r.root.querySelector('nav [aria-current="step"]')!)).toContain('4.');
      expect(control(r.root, 'f-remote-mode').getAttribute('aria-invalid')).toBe('true');
      expect(control(r.root, 'f-remote-mirror-intervalSeconds').getAttribute('aria-invalid')).toBe('true');
      const items = [...r.root.querySelectorAll<HTMLButtonElement>('[role="alert"] button')];
      expect(items.map((b) => b.textContent?.trim())).toEqual(['Mode', 'Pass interval in seconds']);
      items[1]!.click();
      expect(document.activeElement).toBe(control(r.root, 'f-remote-mirror-intervalSeconds'));
      await expectAccessible(r.root);
    });
  });

  describe('on the edit form', () => {
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
      repo: Repository,
      options: { projectAdmin?: boolean; formats?: readonly FormatDescriptor[] } = {},
    ) {
      const r = await renderScreen(RepositoryDetail, {
        inputs: { project: repo.project ?? 'web', key: repo.key },
        setup: (get) => {
          const fake = get(RepositoriesApi) as FakeRepositoriesApi;
          fake.formatsResult = options.formats ?? [GENERIC_FORMAT, NPM];
          fake.repositories.push(repo);
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
      return { r, tab, fake: r.get(RepositoriesApi) as FakeRepositoriesApi };
    }

    it('R-UI-021 Q-UI-104 a remote of a format with mirror: true has a Mirror tab; a local one, and a remote of a format without mirror, do not', async () => {
      expect((await open(stored('pullThrough'))).tab).toBeDefined();
      const local = await open({ ...stored('pullThrough'), key: 'npm-l', class: 'local', remote: undefined });
      expect(local.tab).toBeUndefined();
      const plain = await open({ ...stored('pullThrough'), key: 'nuget-r', format: 'nuget' }, {
        formats: [GENERIC_FORMAT, NO_MIRROR],
      });
      expect(plain.tab).toBeUndefined();
    });

    it('R-REPO-050 R-UI-021 the stored document read and not touched is not a change (members at their defaults are not written)', async () => {
      const { r } = await open(stored('pullThrough'));
      expect(button(r.root, 'Save changes').disabled).toBe(true);
      expect(r.fixture.componentInstance.hasUnsavedChanges()).toBe(false);
    });

    it('R-UI-021 R-REPO-050 switching to mirror and changing a member sends a merge patch of mode and that member only, and keeps the rest of the remote section', async () => {
      const { r, fake } = await open(stored('pullThrough'));
      await chooseMirrorOnEdit(r);
      type(control(r.root, 'f-remote-mirror-intervalSeconds'), '7200');
      await settle(r.fixture);
      type(control(r.root, 'f-remote-mirror-concurrency'), '4');
      await settle(r.fixture);
      button(r.root, 'Save changes').click();
      await settle(r.fixture);

      expect(fake.patches[0]?.patch).toEqual({
        remote: { mode: 'mirror', mirror: { intervalSeconds: 7200 } },
      });
      const saved = fake.repositories.find((x) => x.key === 'npm-remote') as Repository;
      expect(saved.remote?.url).toBe('https://registry.npmjs.org/');
      expect(saved.remote?.mode).toBe('mirror');
    });

    it('R-UI-021 a member set back to its default is cleared with null', async () => {
      const { r, fake } = await open(stored('mirror', { intervalSeconds: 7200 }));
      expect(control(r.root, 'f-remote-mirror-intervalSeconds').value).toBe('7200');
      type(control(r.root, 'f-remote-mirror-intervalSeconds'), '');
      await settle(r.fixture);
      button(r.root, 'Save changes').click();
      await settle(r.fixture);
      expect(fake.patches[0]?.patch).toEqual({ remote: { mirror: { intervalSeconds: null } } });
    });

    it('R-UI-021 Q-REPO-142 a project Owner sees the section read-only except Pause, and Turn off sends the mode back to pullThrough', async () => {
      const { r, fake } = await open(stored('mirror', { intervalSeconds: 7200 }), { projectAdmin: true });
      const section = r.root.querySelector('trove-mirror-settings')!;
      expect(text(section)).toContain('Only an administrator of the installation');
      expect(control(r.root, 'f-remote-mirror-intervalSeconds').disabled).toBe(true);
      expect(control(r.root, 'f-remote-mirror-concurrency').disabled).toBe(true);
      expect(control(r.root, 'f-remote-mirror-includePrereleases').disabled).toBe(true);
      expect(control(r.root, 'f-remote-mirror-paused').disabled).toBe(false);
      expect(r.root.querySelector<HTMLInputElement>('input[name="remote-mode"][value="mirror"]')!.disabled).toBe(true);
      await expectAccessible(r.root);

      const paused = control(r.root, 'f-remote-mirror-paused');
      paused.checked = true;
      paused.dispatchEvent(new Event('change', { bubbles: true }));
      await settle(r.fixture);
      button(r.root, 'Save changes').click();
      await settle(r.fixture);
      expect(fake.patches[0]?.patch).toEqual({ remote: { mirror: { paused: true } } });

      button(section as HTMLElement, 'Turn off').click();
      await settle(r.fixture);
      button(r.root, 'Save changes').click();
      await settle(r.fixture);
      expect(fake.patches[1]?.patch).toEqual({ remote: { mode: 'pullThrough' } });
    });

    it('R-UI-021 R-UI-044 a server error at a mirror member appears at its field and in the summary', async () => {
      const { r, fake } = await open(stored('mirror'));
      fake.patch = async () => {
        throw new ApiError(
          422,
          {
            code: 'validation_failed',
            errors: [{ pointer: '/remote/mirror/concurrency', code: 'out_of_range', detail: 'too big' }],
          },
          null,
        );
      };
      type(control(r.root, 'f-remote-mirror-concurrency'), '100');
      await settle(r.fixture);
      button(r.root, 'Save changes').click();
      await settle(r.fixture);
      expect(control(r.root, 'f-remote-mirror-concurrency').getAttribute('aria-invalid')).toBe('true');
      const summary = r.root.querySelector<HTMLButtonElement>('[role="alert"] button')!;
      summary.click();
      expect(document.activeElement).toBe(control(r.root, 'f-remote-mirror-concurrency'));
      await expectAccessible(r.root);
    });

    async function chooseMirrorOnEdit(r: Awaited<ReturnType<typeof open>>['r']) {
      const radio = r.root.querySelector<HTMLInputElement>('input[name="remote-mode"][value="mirror"]')!;
      radio.checked = true;
      radio.dispatchEvent(new Event('change', { bubbles: true }));
      await settle(r.fixture);
    }
  });
});
