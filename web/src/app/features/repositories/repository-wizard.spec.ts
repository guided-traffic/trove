import { TestBed } from '@angular/core/testing';
import { Router } from '@angular/router';
import { ApiError } from '../../api/problem';
import { FormatDescriptor, RepositoriesApi } from '../../api/repositories-api';
import {
  FakeRepositoriesApi,
  GENERIC_FORMAT,
  PACKAGE_FORMAT,
} from '../../api/testing/fakes-catalog';
import { SystemApi } from '../../api/system-api';
import { FakeSystemApi, sampleInfo } from '../../api/testing/fakes';
import { button, expectAccessible, renderScreen, settle, text, type } from '../../testing/screen';
import { RepositoryWizard } from './repository-wizard';

const FAKE_NPM: FormatDescriptor = PACKAGE_FORMAT;
const NO_VIRTUAL: FormatDescriptor = {
  ...FAKE_NPM,
  name: 'nuget',
  displayName: 'NuGet',
  classes: ['local', 'remote'],
};

async function pick(
  rendered: Awaited<ReturnType<typeof renderScreen<RepositoryWizard>>>,
  kind: 'class' | 'format',
  value: string,
) {
  const radio = rendered.root.querySelector<HTMLInputElement>(
    `input[name="${kind}"][value="${value}"]`,
  );
  if (!radio) throw new Error(`no ${kind} ${value}`);
  radio.checked = true;
  radio.dispatchEvent(new Event('change', { bubbles: true }));
  await settle(rendered.fixture);
}

async function next(rendered: Awaited<ReturnType<typeof renderScreen<RepositoryWizard>>>) {
  button(rendered.root, 'Next').click();
  await settle(rendered.fixture);
}

describe('Repository creation wizard', () => {
  it('R-UI-021 the class step offers only classes that a format implements, with no list of its own', async () => {
    const r = await renderScreen(RepositoryWizard);
    const classes = [...r.root.querySelectorAll<HTMLInputElement>('input[name="class"]')].map(
      (i) => i.value,
    );
    expect(classes).toEqual(['local']);
    await expectAccessible(r.root);
  });

  it('R-UI-021 the format step offers only formats whose classes contain the chosen class (a format without Virtual is not offered for virtual)', async () => {
    const r = await renderScreen(RepositoryWizard, {
      setup: (get) =>
        ((get(RepositoriesApi) as FakeRepositoriesApi).formatsResult = [
          GENERIC_FORMAT,
          FAKE_NPM,
          NO_VIRTUAL,
        ]),
    });
    await pick(r, 'class', 'virtual');
    await next(r);
    expect(
      [...r.root.querySelectorAll<HTMLInputElement>('input[name="format"]')].map((i) => i.value),
    ).toEqual(['npm']);
    await expectAccessible(r.root);

    const local = await renderScreen(RepositoryWizard, {
      setup: (get) =>
        ((get(RepositoriesApi) as FakeRepositoriesApi).formatsResult = [
          GENERIC_FORMAT,
          FAKE_NPM,
          NO_VIRTUAL,
        ]),
    });
    await pick(local, 'class', 'local');
    await next(local);
    expect(
      [...local.root.querySelectorAll<HTMLInputElement>('input[name="format"]')].map(
        (i) => i.value,
      ),
    ).toEqual(['generic', 'npm', 'nuget']);
  });

  it('R-UI-021 walks all five steps, shows the review with the API request, and a double click creates one repository with the key generated when the wizard opened (R-UI-042)', async () => {
    const r = await renderScreen(RepositoryWizard);
    const fake = r.get(RepositoriesApi) as FakeRepositoriesApi;
    const navigate = vi.spyOn(TestBed.inject(Router), 'navigate').mockResolvedValue(true);

    await pick(r, 'class', 'local');
    await next(r);
    await pick(r, 'format', 'generic');
    await next(r);
    type(r.root.querySelector<HTMLInputElement>('#f-key')!, 'build-output');
    await settle(r.fixture);
    type(r.root.querySelector<HTMLTextAreaElement>('#f-description')!, 'Build output');
    await settle(r.fixture);
    await next(r);
    type(
      r.root.querySelector<HTMLTextAreaElement>('#f-includePatterns')!,
      'releases/**\nsnapshots/**',
    );
    await expectAccessible(r.root);
    await next(r);

    expect(text(r.root)).toContain('Show as API request');
    expect(r.root.querySelector('pre')?.textContent).toContain('"key": "build-output"');
    expect(r.root.querySelector('details pre:last-of-type')?.textContent).toContain('curl -X POST');
    await expectAccessible(r.root);

    const create = button(r.root, 'Create repository');
    create.click();
    create.click();
    await settle(r.fixture);

    expect(fake.createKeys).toHaveLength(1);
    expect(fake.repositories.find((x) => x.key === 'build-output')?.includePatterns).toEqual([
      'releases/**',
      'snapshots/**',
    ]);
    expect(navigate).toHaveBeenCalledWith(['/admin/repositories', 'build-output']);
  });

  it('R-REPO-002 R-UI-021 the review shows the client address and the curl command on the configured base URL, not on the origin of the page', async () => {
    const r = await renderScreen(RepositoryWizard, {
      setup: (get) =>
        ((get(RepositoriesApi) as FakeRepositoriesApi).formatsResult = [GENERIC_FORMAT, FAKE_NPM]),
    });
    // The page is served from jsdom's origin; the installation's base URL is another one.
    expect(location.origin).not.toBe('https://trove.example.net');
    await pick(r, 'class', 'local');
    await next(r);
    await pick(r, 'format', 'npm');
    await next(r);
    type(r.root.querySelector<HTMLInputElement>('#f-key')!, 'web-libs');
    await settle(r.fixture);
    await next(r);
    await next(r);

    const t = text(r.root);
    expect(t).toContain('https://trove.example.net/api/npm/web-libs/');
    expect(r.root.querySelector('details pre:last-of-type')?.textContent).toContain(
      "curl -X POST 'https://trove.example.net/api/v1/repositories'",
    );
    expect(t).not.toContain(location.origin);
  });

  it('R-REPO-002 R-UI-021 a caller without settings:read gets the base URL from the addresses of the repositories it may see', async () => {
    const r = await renderScreen(RepositoryWizard, {
      capabilities: ['repositories:write-config'],
      setup: (get) => {
        (get(RepositoriesApi) as FakeRepositoriesApi).formatsResult = [GENERIC_FORMAT, FAKE_NPM];
        (get(SystemApi) as FakeSystemApi).infoResult = sampleInfo({
          baseUrl: 'https://must-not-be-asked.example',
        });
      },
    });
    await pick(r, 'class', 'local');
    await next(r);
    await pick(r, 'format', 'npm');
    await next(r);
    type(r.root.querySelector<HTMLInputElement>('#f-key')!, 'web-libs');
    await settle(r.fixture);
    await next(r);
    await next(r);
    expect(text(r.root)).toContain('https://trove.example.net/api/npm/web-libs/');
  });

  it('R-UI-021 the key is checked live: shape at once, availability after a pause (a 404 means free)', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    try {
      const r = await renderScreen(RepositoryWizard);
      await pick(r, 'class', 'local');
      await next(r);
      await pick(r, 'format', 'generic');
      await next(r);
      type(r.root.querySelector<HTMLInputElement>('#f-key')!, 'generic-local');
      await settle(r.fixture);
      vi.advanceTimersByTime(500);
      await settle(r.fixture);
      expect(text(r.root)).toContain('A repository with this key already exists.');
      button(r.root, 'Next').click();
      await settle(r.fixture);
      expect(text(r.root)).toContain('Basic settings');

      type(r.root.querySelector<HTMLInputElement>('#f-key')!, 'fresh-key');
      await settle(r.fixture);
      vi.advanceTimersByTime(500);
      await settle(r.fixture);
      expect(text(r.root)).toContain('The key is available.');
    } finally {
      vi.useRealTimers();
    }
  });

  it('R-UI-021 server field errors appear at their field and in the summary, and the wizard returns to the step of the field (R-UI-044)', async () => {
    const r = await renderScreen(RepositoryWizard);
    const fake = r.get(RepositoriesApi) as FakeRepositoriesApi;
    fake.create = async () => {
      throw new ApiError(
        422,
        {
          code: 'validation_failed',
          errors: [
            { pointer: '/key', code: 'reserved_name', detail: 'reserved' },
            { pointer: '/includePatterns/0', code: 'invalid_pattern', detail: 'bad' },
          ],
        },
        null,
      );
    };
    await pick(r, 'class', 'local');
    await next(r);
    await pick(r, 'format', 'generic');
    await next(r);
    type(r.root.querySelector<HTMLInputElement>('#f-key')!, 'batch-create');
    await settle(r.fixture);
    await next(r);
    await next(r);
    button(r.root, 'Create repository').click();
    await settle(r.fixture);

    expect(r.root.querySelector('[role="alert"]')?.textContent).toContain(
      'Some entries are not valid',
    );
    expect(text(r.root)).toContain('This name is reserved.');
    expect(r.root.querySelector('#f-key')?.getAttribute('aria-invalid')).toBe('true');
    await expectAccessible(r.root);
  });

  it('R-UI-021 a failing format catalog shows the error and passes the accessibility check', async () => {
    const r = await renderScreen(RepositoryWizard, {
      setup: (get) =>
        ((get(RepositoriesApi) as FakeRepositoriesApi).formatsError = new ApiError(
          503,
          { code: 'unavailable' },
          null,
        )),
    });
    expect(text(r.root)).toContain('temporarily unavailable');
    await expectAccessible(r.root);
  });

  it('R-UI-021 leaving with entered data counts as unsaved changes, a pristine wizard does not', async () => {
    const r = await renderScreen(RepositoryWizard);
    expect(r.fixture.componentInstance.hasUnsavedChanges()).toBe(false);
    await pick(r, 'class', 'local');
    expect(r.fixture.componentInstance.hasUnsavedChanges()).toBe(true);
  });
});
