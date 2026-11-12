import { TestBed } from '@angular/core/testing';
import { Router } from '@angular/router';
import { ApiError } from '../../api/problem';
import { ProjectsApi } from '../../api/projects-api';
import { RepositoriesApi } from '../../api/repositories-api';
import { SessionApi } from '../../api/session-api';
import { FakeSessionApi } from '../../api/testing/fakes';
import { FakeRepositoriesApi, GENERIC_FORMAT, PACKAGE_FORMAT, sampleRepositoryDefaults } from '../../api/testing/fakes-catalog';
import { FakeProjectsApi, sampleProject } from '../../api/testing/fakes-projects';
import { button, expectAccessible, renderScreen, settle, text, type } from '../../testing/screen';
import { RepositoryDetail } from './repository-detail';
import { RepositoryCreate } from './repository-create';

type Level = 'private' | 'internal' | 'public';

async function openAccess(options: {
  project?: Level;
  artifacts?: Level;
  wiki?: Level;
  format?: 'npm' | 'generic';
  cls?: 'local' | 'remote';
  capabilities?: readonly string[];
  member?: boolean;
}) {
  const format = options.format ?? 'npm';
  const r = await renderScreen(RepositoryDetail, {
    inputs: { project: 'web', key: 'repo' },
    ...(options.capabilities ? { capabilities: options.capabilities } : {}),
    setup: (get) => {
      (get(ProjectsApi) as FakeProjectsApi).projects = [sampleProject({ visibility: options.project ?? 'internal' })];
      const fake = get(RepositoriesApi) as FakeRepositoriesApi;
      fake.formatsResult = [GENERIC_FORMAT, PACKAGE_FORMAT];
      fake.repositories = [
        {
          key: 'repo',
          project: 'web',
          class: options.cls ?? 'local',
          format,
          description: 'A repository',
          visibility: { artifacts: options.artifacts ?? 'internal', wiki: options.wiki ?? 'internal' },
        },
      ];
      if (options.member) {
        (get(SessionApi) as FakeSessionApi).meResult = {
          roles: [],
          capabilities: [],
          principals: ['user:alice'],
          projects: [{ key: 'web', role: 'owner' }],
        };
      }
    },
  });
  const tab = [...r.root.querySelectorAll<HTMLElement>('[role="tab"]')].find((t) => t.textContent?.includes('Access'))!;
  tab.click();
  await settle(r.fixture);
  const group = (label: string) =>
    [...r.root.querySelectorAll<HTMLFieldSetElement>('trove-repository-visibility fieldset')].find(
      (f) => f.querySelector('legend')?.textContent?.trim() === label,
    );
  const radio = (label: string, level: string) =>
    [...(group(label)?.querySelectorAll<HTMLInputElement>('input[type="radio"]') ?? [])].find((i) =>
      i.labels?.[0]?.textContent?.includes(level),
    )!;
  const fake = r.get(RepositoriesApi) as FakeRepositoriesApi;
  return { ...r, group, radio, fake };
}

describe('The visibility block of a repository (R-UI-055)', () => {
  it('R-UI-055 replaces the field "Require authentication" with the groups Artifacts and Wiki for a format with a wiki, and passes axe', async () => {
    const r = await openAccess({});
    expect(text(r.root)).not.toContain('Require authentication');
    expect(r.group('Artifacts')).toBeTruthy();
    expect(r.group('Wiki')).toBeTruthy();
    expect(r.radio('Artifacts', 'Internal').checked).toBe(true);
    expect(r.radio('Wiki', 'Internal').checked).toBe(true);
    await expectAccessible(r.root);
  });

  it('R-UI-055 a format without wiki content shows one group named Visibility', async () => {
    const r = await openAccess({ format: 'generic' });
    expect(r.group('Wiki')).toBeUndefined();
    expect(r.group('Artifacts')).toBeUndefined();
    expect(r.group('Visibility')).toBeTruthy();
  });

  it('R-UI-055 an option above the project is disabled with the reason; the wiki is bounded by the artifacts', async () => {
    const r = await openAccess({});
    expect(r.radio('Artifacts', 'Public').disabled).toBe(true);
    expect(r.radio('Wiki', 'Public').disabled).toBe(true);
    expect(text(r.group('Artifacts')!)).toContain('The project is internal, so this repository cannot be public.');
    expect(r.radio('Artifacts', 'Private').disabled).toBe(false);

    r.radio('Artifacts', 'Private').click();
    await settle(r.fixture);
    // The wiki follows the artifacts down, and nothing above the artifacts can be chosen for it.
    expect(r.radio('Wiki', 'Private').checked).toBe(true);
    expect(r.radio('Wiki', 'Internal').disabled).toBe(true);
    expect(text(r.group('Wiki')!)).toContain('The wiki cannot be more open than the artifacts.');
  });

  it('R-UI-055 in a public project an Owner sets the artifacts public and keeps the wiki internal; only the changed part is sent', async () => {
    const r = await openAccess({ project: 'public', member: true, capabilities: [] });
    r.radio('Artifacts', 'Public').click();
    await settle(r.fixture);
    const save = button(r.root, 'Save changes (visibility)');
    expect(save.disabled).toBe(false);
    save.click();
    await settle(r.fixture);
    expect(r.fake.patches).toHaveLength(1);
    expect(r.fake.patches[0]).toMatchObject({ key: 'web/repo', patch: { visibility: { artifacts: 'public' } }, etag: '"1"' });
    expect(r.fake.repositories[0]?.visibility).toEqual({ artifacts: 'public', wiki: 'internal' });
    expect(button(r.root, 'Save changes (visibility)').disabled).toBe(true);
  });

  it('R-UI-055 lowering the artifacts below the wiki sends both parts', async () => {
    const r = await openAccess({ project: 'public', artifacts: 'public', wiki: 'public' });
    r.radio('Artifacts', 'Private').click();
    await settle(r.fixture);
    button(r.root, 'Save changes (visibility)').click();
    await settle(r.fixture);
    expect(r.fake.patches[0]?.patch).toEqual({ visibility: { artifacts: 'private', wiki: 'private' } });
  });

  it('R-UI-055 the warning of the answer is shown at the block', async () => {
    const r = await openAccess({ project: 'public', cls: 'remote', format: 'npm' });
    r.radio('Artifacts', 'Public').click();
    await settle(r.fixture);
    button(r.root, 'Save changes (visibility)').click();
    await settle(r.fixture);
    expect(text(r.root.querySelector('trove-repository-visibility')!)).toContain(
      'Anyone can fill the cache of this remote repository from its upstream.',
    );
  });

  it('R-UI-055 a reader who may not write sees the levels read-only with the reason', async () => {
    const r = await openAccess({ capabilities: ['repositories:read-config'] });
    expect(r.root.querySelector('trove-repository-visibility button[type="submit"]')).toBeNull();
    expect(r.group('Artifacts')!.disabled).toBe(true);
    expect(text(r.root.querySelector('trove-repository-visibility')!)).toContain('Only the project\'s Owner');
  });

  it('R-UI-055 the repository header shows the artifacts level as an icon with its name', async () => {
    const r = await openAccess({ artifacts: 'private', wiki: 'private' });
    const icon = r.root.querySelector('.subject trove-visibility-icon [role="img"]')!;
    expect(icon.getAttribute('aria-label')).toBe('Private');
  });
});

describe('The visibility of a repository in the creation form (R-UI-055, R-UI-081, R-REPO-063)', () => {
  /** The form of a local npm repository whose defaults (R-REPO-063) answer `artifacts` as the visibility, or fail. */
  async function openForm(project: Level, artifacts: Level | 'unavailable', capabilities?: readonly string[]) {
    const r = await renderScreen(RepositoryCreate, {
      inputs: { project: 'web', format: 'npm' },
      ...(capabilities ? { capabilities } : {}),
      setup: (get) => {
        (get(ProjectsApi) as FakeProjectsApi).projects = [sampleProject({ visibility: project })];
        const fake = get(RepositoriesApi) as FakeRepositoriesApi;
        fake.formatsResult = [GENERIC_FORMAT, PACKAGE_FORMAT];
        if (artifacts === 'unavailable') {
          fake.defaultsError = new ApiError(503, { code: 'unavailable' }, 'req-1');
        } else {
          fake.defaultsFor = (_p, format, cls) => {
            const d = sampleRepositoryDefaults(PACKAGE_FORMAT, cls);
            return { ...d, repository: { ...d.repository, format, visibility: { artifacts, wiki: artifacts } } };
          };
        }
        if (capabilities?.length === 0) {
          (get(SessionApi) as FakeSessionApi).meResult = {
            roles: [],
            capabilities: [],
            principals: ['user:alice'],
            projects: [{ key: 'web', role: 'owner' }],
          };
        }
      },
    });
    const radios = () => [...r.root.querySelectorAll<HTMLInputElement>('trove-visibility-level input[type="radio"]')];
    const create = async () => {
      button(r.root, 'Create repository').click();
      await settle(r.fixture);
    };
    return { ...r, radios, create };
  }

  it('R-UI-081 R-REPO-063 the artifacts group is preset to the visibility of the defaults, and a level above the project is disabled', async () => {
    const a = await openForm('public', 'internal');
    expect(a.radios().map((x) => x.checked)).toEqual([false, true, false]);
    const b = await openForm('private', 'private');
    expect(b.radios().map((x) => x.checked)).toEqual([true, false, false]);
    expect(b.radios().map((x) => x.disabled)).toEqual([false, true, true]);
    await expectAccessible(b.root);
  });

  it('R-UI-055 R-UI-084 a create that keeps the preset leaves visibility out; another level is sent for artifacts and wiki', async () => {
    navigateSpy();
    const kept = await openForm('public', 'internal');
    await kept.create();
    const fake = kept.get(RepositoriesApi) as FakeRepositoriesApi;
    expect(fake.repositories.find((x) => x.key === 'npm-local')?.visibility).toBeUndefined();

    const r = await openForm('public', 'internal');
    r.radios()[2]!.click();
    await settle(r.fixture);
    await r.create();
    const other = r.get(RepositoriesApi) as FakeRepositoriesApi;
    expect(other.repositories.find((x) => x.key === 'npm-local')?.visibility).toEqual({ artifacts: 'public', wiki: 'public' });
  });

  it('R-REPO-063 an Owner without settings:read gets the visibility of the defaults, not Private', async () => {
    const r = await openForm('internal', 'internal', []);
    expect(r.radios().map((x) => x.checked)).toEqual([false, true, false]);
  });

  it('R-UI-081 without the defaults the field is left out and the server applies its default', async () => {
    navigateSpy();
    const r = await openForm('public', 'unavailable');
    expect(r.root.querySelector('trove-visibility-level')).toBeNull();
    expect(r.root.querySelector<HTMLInputElement>('#f-key')!.value).toBe('');
    type(r.root.querySelector<HTMLInputElement>('#f-key')!, 'owner-repo');
    await settle(r.fixture);
    await r.create();
    const fake = r.get(RepositoriesApi) as FakeRepositoriesApi;
    expect(fake.repositories.find((x) => x.key === 'owner-repo')?.visibility).toBeUndefined();
  });
});

function navigateSpy(): void {
  vi.spyOn(TestBed.inject(Router), 'navigate').mockResolvedValue(true);
}
