import { TestBed } from '@angular/core/testing';
import { Router } from '@angular/router';
import { ApiError } from '../../api/problem';
import { ProjectsApi } from '../../api/projects-api';
import { RepositoriesApi } from '../../api/repositories-api';
import { VisibilityApi } from '../../api/visibility-api';
import { SessionApi } from '../../api/session-api';
import { FakeSessionApi } from '../../api/testing/fakes';
import { FakeRepositoriesApi, GENERIC_FORMAT, PACKAGE_FORMAT } from '../../api/testing/fakes-catalog';
import { FakeProjectsApi, FakeVisibilityApi, sampleProject } from '../../api/testing/fakes-projects';
import { button, expectAccessible, renderScreen, settle, text, type } from '../../testing/screen';
import { RepositoryDetail } from './repository-detail';
import { RepositoryWizard } from './repository-wizard';

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

describe('The visibility of a repository in the creation wizard (R-UI-055)', () => {
  async function toBasicStep(project: Level, defaultLevel: Level) {
    const r = await renderScreen(RepositoryWizard, {
      inputs: { project: 'web' },
      setup: (get) => {
        (get(ProjectsApi) as FakeProjectsApi).projects = [sampleProject({ visibility: project })];
        (get(VisibilityApi) as FakeVisibilityApi).stored.settings.defaultLevel = defaultLevel;
        (get(RepositoriesApi) as FakeRepositoriesApi).formatsResult = [GENERIC_FORMAT, PACKAGE_FORMAT];
      },
    });
    const click = async (el: HTMLElement) => {
      el.click();
      await settle(r.fixture);
    };
    await click([...r.root.querySelectorAll<HTMLElement>('[role="option"]')].find((t) => t.textContent?.includes('npm'))!);
    await click(button(r.root, 'Next'));
    await click(r.root.querySelector<HTMLInputElement>('input[name="class"][value="local"]')!);
    await click(button(r.root, 'Next'));
    type(r.root.querySelector<HTMLInputElement>('#f-key')!, 'new-repo');
    await settle(r.fixture);
    const radios = () => [...r.root.querySelectorAll<HTMLInputElement>('trove-visibility-level input[type="radio"]')];
    return { ...r, click, radios };
  }

  it('R-UI-055 the artifacts group is preset to the lower of the installation default and the project level', async () => {
    const a = await toBasicStep('public', 'internal');
    expect(a.radios().map((x) => x.checked)).toEqual([false, true, false]);
    const b = await toBasicStep('private', 'public');
    expect(b.radios().map((x) => x.checked)).toEqual([true, false, false]);
    expect(b.radios().map((x) => x.disabled)).toEqual([false, true, true]);
  });

  it('R-UI-055 the review shows the level and the create sends artifacts and wiki at it', async () => {
    const r = await toBasicStep('public', 'internal');
    navigateSpy();
    r.radios()[2]!.click();
    await settle(r.fixture);
    await r.click(button(r.root, 'Next'));
    await r.click(button(r.root, 'Next'));
    expect(text(r.root.querySelector('dl')!)).toContain('Public');
    await r.click(button(r.root, 'Create repository'));
    const fake = r.get(RepositoriesApi) as FakeRepositoriesApi;
    expect(fake.repositories.find((x) => x.key === 'new-repo')?.visibility).toEqual({ artifacts: 'public', wiki: 'public' });
  });
});

describe('The wizard for an Owner who cannot read the installation default (R-UI-055, R-REPO-004)', () => {
  it('R-UI-055 names the default instead of claiming Private and leaves visibility out of the create', async () => {
    navigateSpy();
    const r = await renderScreen(RepositoryWizard, {
      inputs: { project: 'web' },
      capabilities: [],
      setup: (get) => {
        (get(SessionApi) as FakeSessionApi).meResult = {
          roles: [],
          capabilities: [],
          principals: ['user:alice'],
          projects: [{ key: 'web', role: 'owner' }],
        };
        (get(ProjectsApi) as FakeProjectsApi).projects = [sampleProject({ visibility: 'public' })];
        (get(VisibilityApi) as FakeVisibilityApi).failure = new ApiError(403, { code: 'forbidden' }, 'req-1');
        (get(RepositoriesApi) as FakeRepositoriesApi).formatsResult = [GENERIC_FORMAT, PACKAGE_FORMAT];
      },
    });
    const click = async (el: HTMLElement) => {
      el.click();
      await settle(r.fixture);
    };
    await click([...r.root.querySelectorAll<HTMLElement>('[role="option"]')].find((t) => t.textContent?.includes('npm'))!);
    await click(button(r.root, 'Next'));
    await click(r.root.querySelector<HTMLInputElement>('input[name="class"][value="local"]')!);
    await click(button(r.root, 'Next'));
    type(r.root.querySelector<HTMLInputElement>('#f-key')!, 'owner-repo');
    await settle(r.fixture);
    const radios = [...r.root.querySelectorAll<HTMLInputElement>('trove-visibility-level input[type="radio"]')];
    expect(radios.map((x) => x.checked)).toEqual([false, false, false]);
    expect(text(r.root)).toContain('the repository takes the default of the installation and of its project');
    await click(button(r.root, 'Next'));
    await click(button(r.root, 'Next'));
    expect(text(r.root.querySelector('dl')!)).toContain('Default of the installation and of the project');
    await click(button(r.root, 'Create repository'));
    const fake = r.get(RepositoriesApi) as FakeRepositoriesApi;
    expect(fake.repositories.find((x) => x.key === 'owner-repo')?.visibility).toBeUndefined();
  });
});

function navigateSpy(): void {
  vi.spyOn(TestBed.inject(Router), 'navigate').mockResolvedValue(true);
}
