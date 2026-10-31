import { ApiError } from '../../api/problem';
import { RepositoriesApi } from '../../api/repositories-api';
import {
  FakeRepositoriesApi,
  GENERIC_FORMAT,
  PACKAGE_FORMAT,
} from '../../api/testing/fakes-catalog';
import { button, expectAccessible, renderScreen, settle, text, type } from '../../testing/screen';
import { RepositoryDetail } from './repository-detail';

async function openTab(r: Awaited<ReturnType<typeof open>>, label: string) {
  const tab = [...r.root.querySelectorAll<HTMLElement>('[role="tab"]')].find((t) =>
    t.textContent?.includes(label),
  );
  if (!tab) throw new Error(`no tab ${label}`);
  tab.click();
  await settle(r.fixture);
}

function withNpmLocal(npm?: Record<string, unknown>) {
  return (fake: FakeRepositoriesApi) => {
    fake.formatsResult = [GENERIC_FORMAT, PACKAGE_FORMAT];
    fake.repositories.push({
      key: 'npm-local',
      class: 'local',
      format: 'npm',
      description: 'Internal packages',
      ...(npm ? { npm: npm as never } : {}),
    });
  };
}

async function open(setup?: (fake: FakeRepositoriesApi) => void, key = 'generic-local') {
  return renderScreen(RepositoryDetail, {
    inputs: { key },
    setup: (get) => setup?.(get(RepositoriesApi) as FakeRepositoriesApi),
  });
}

describe('Repository edit form', () => {
  it('R-UI-021 key, class and format are read-only; the settings are editable; the page passes the accessibility check', async () => {
    const r = await open();
    const t = text(r.root);
    expect(t).toContain('generic-local');
    expect(r.root.querySelector('input#f-key')).toBeNull();
    expect(r.root.querySelector<HTMLTextAreaElement>('#f-description')?.value).toBe(
      'Installers and build outputs',
    );
    await expectAccessible(r.root);
  });

  it('R-UI-021 saving sends a merge patch of the changed members only, with the loaded ETag', async () => {
    const r = await open();
    const fake = r.get(RepositoriesApi) as FakeRepositoriesApi;
    expect(button(r.root, 'Save changes').disabled).toBe(true);

    type(r.root.querySelector<HTMLTextAreaElement>('#f-description')!, 'Changed');
    await settle(r.fixture);
    expect(r.fixture.componentInstance.hasUnsavedChanges()).toBe(true);
    button(r.root, 'Save changes').click();
    await settle(r.fixture);

    expect(fake.patches).toHaveLength(1);
    expect(fake.patches[0]).toMatchObject({
      key: 'generic-local',
      patch: { description: 'Changed' },
      etag: '"1"',
    });
    expect(r.fixture.componentInstance.hasUnsavedChanges()).toBe(false);
    expect(fake.repositories[0]?.description).toBe('Changed');
  });

  it('R-UI-021 and R-UI-042 saving after another administrator saved first opens the conflict dialog; Overwrite stores the draft with the new ETag', async () => {
    const r = await open((fake) => {
      fake.beforePatch = () => {
        // The other administrator saved first.
        fake.repositories[0] = { ...fake.repositories[0]!, description: 'Theirs' };
        fake.versions.set('generic-local', 2);
        fake.beforePatch = null;
      };
    });
    const fake = r.get(RepositoriesApi) as FakeRepositoriesApi;
    type(r.root.querySelector<HTMLTextAreaElement>('#f-description')!, 'Mine');
    await settle(r.fixture);
    button(r.root, 'Save changes').click();
    await settle(r.fixture);

    const body = r.root.ownerDocument.body;
    expect(text(body)).toContain('Changed by someone else since you opened it');
    expect(text(body)).toContain('Theirs');
    expect(text(body)).toContain('Mine');
    await expectAccessible(body.querySelector('mat-dialog-container')!);

    button(body, 'Overwrite').click();
    await settle(r.fixture);
    expect(fake.repositories[0]?.description).toBe('Mine');
    expect(fake.patches.at(-1)?.etag).toBe('"2"');
  });

  it('R-UI-042 a 412 on a retried PATCH whose first attempt committed is a success, not a conflict', async () => {
    const r = await open();
    const fake = r.get(RepositoriesApi) as FakeRepositoriesApi;
    const original = fake.patch.bind(fake);
    fake.patch = async (key, patch, etag) => {
      await original(key, patch, etag);
      const error = new ApiError(412, { code: 'precondition_failed' }, null);
      error.afterRetry = true;
      throw error;
    };
    type(r.root.querySelector<HTMLTextAreaElement>('#f-description')!, 'Once');
    await settle(r.fixture);
    button(r.root, 'Save changes').click();
    await settle(r.fixture);
    expect(r.root.ownerDocument.body.querySelector('mat-dialog-container')).toBeNull();
    expect(r.fixture.componentInstance.hasUnsavedChanges()).toBe(false);
  });

  it('R-UI-021 a server field error appears at its field and in the summary', async () => {
    const r = await open((fake) => {
      fake.patch = async () => {
        throw new ApiError(
          422,
          {
            code: 'invalid_pattern',
            errors: [{ pointer: '/description', code: 'invalid_value', detail: 'too long' }],
          },
          null,
        );
      };
    });
    type(r.root.querySelector<HTMLTextAreaElement>('#f-description')!, 'x');
    await settle(r.fixture);
    button(r.root, 'Save changes').click();
    await settle(r.fixture);
    expect(r.root.querySelector('[role="alert"]')?.textContent).toContain(
      'Some entries are not valid',
    );
    expect(r.root.querySelector('#f-description')?.getAttribute('aria-invalid')).toBe('true');
    await expectAccessible(r.root);
  });

  it('R-UI-021 R-ARCH-004 the npm settings tab is rendered from the schema for the class; turning the guard off patches the npm member only', async () => {
    const r = await open(withNpmLocal(), 'npm-local');
    const fake = r.get(RepositoriesApi) as FakeRepositoriesApi;
    await openTab(r, 'npm settings');
    const guard = r.root.querySelector<HTMLInputElement>('#f-npm-latestGuard')!;
    expect(guard.checked).toBe(true);
    expect(r.root.querySelector('#f-npm-latestStrategy')).toBeNull();
    expect(button(r.root, 'Save changes').disabled).toBe(true);
    await expectAccessible(r.root);

    guard.checked = false;
    guard.dispatchEvent(new Event('change', { bubbles: true }));
    await settle(r.fixture);
    button(r.root, 'Save changes').click();
    await settle(r.fixture);
    expect(fake.patches.at(-1)?.patch).toEqual({ npm: { latestGuard: false } });
    expect(fake.repositories.find((x) => x.key === 'npm-local')?.npm).toEqual({
      latestGuard: false,
    });
    expect(r.fixture.componentInstance.hasUnsavedChanges()).toBe(false);
  });

  it('R-UI-021 Q-UI-80 setting a value back to its default removes it from the document, and a stored default is no change', async () => {
    const r = await open(withNpmLocal({ latestGuard: false }), 'npm-local');
    const fake = r.get(RepositoriesApi) as FakeRepositoriesApi;
    await openTab(r, 'npm settings');
    const guard = r.root.querySelector<HTMLInputElement>('#f-npm-latestGuard')!;
    expect(guard.checked).toBe(false);
    guard.checked = true;
    guard.dispatchEvent(new Event('change', { bubbles: true }));
    await settle(r.fixture);
    button(r.root, 'Save changes').click();
    await settle(r.fixture);
    expect(fake.patches.at(-1)?.patch).toEqual({ npm: null });

    const stored = await open(withNpmLocal({ latestGuard: true }), 'npm-local');
    expect(stored.fixture.componentInstance.hasUnsavedChanges()).toBe(false);
  });

  it('R-UI-021 a repository of a format without settings has no format tab', async () => {
    const r = await open((fake) => (fake.formatsResult = [GENERIC_FORMAT, PACKAGE_FORMAT]));
    const tabs = [...r.root.querySelectorAll('[role="tab"]')].map((t) => text(t));
    expect(tabs).toEqual(['General', 'Access', 'API']);
  });

  it('R-UI-021 a caller who may use but not configure the repository sees the consumer view without a form', async () => {
    const r = await open((fake) => (fake.consumerOnly = true));
    expect(text(r.root)).toContain(
      'You may use this repository but not read or change its configuration.',
    );
    expect(r.root.querySelector('textarea')).toBeNull();
    await expectAccessible(r.root);
  });

  it('R-UI-021 an unknown repository shows the not-found error and passes the accessibility check', async () => {
    const r = await open(undefined, 'no-such-repo');
    expect(text(r.root)).toContain('does not exist');
    await expectAccessible(r.root);
  });

  it('R-REPO-002 R-UI-021 the client address and the curl command carry the configured base URL even though the page is served from another origin', async () => {
    const r = await open((fake) => (fake.formatsResult = [GENERIC_FORMAT, PACKAGE_FORMAT]));
    expect(location.origin).not.toBe('https://trove.example.net');
    expect(text(r.root)).toContain('https://trove.example.net/files/generic-local/');
    type(r.root.querySelector<HTMLTextAreaElement>('#f-description')!, 'Api');
    await settle(r.fixture);
    const tab = [...r.root.querySelectorAll<HTMLElement>('[role="tab"]')].find((t) =>
      t.textContent?.includes('API'),
    )!;
    tab.click();
    await settle(r.fixture);
    expect(text(r.root)).toContain(
      "curl -X PATCH 'https://trove.example.net/api/v1/repositories/generic-local'",
    );
    expect(text(r.root)).not.toContain(location.origin);
  });

  it('R-REPO-002 R-UI-021 a server without a configured base URL leaves the client address out instead of showing the page origin', async () => {
    const r = await renderScreen(RepositoryDetail, {
      inputs: { key: 'generic-local' },
      capabilities: ['repositories:read-config', 'repositories:write-config'],
      setup: (get) => {
        const fake = get(RepositoriesApi) as FakeRepositoriesApi;
        fake.baseUrlResult = null;
      },
    });
    expect(text(r.root)).not.toContain(location.origin);
    expect(text(r.root)).not.toContain('generic-local/');
  });

  it('R-UI-021 the API tab shows the stored document and the request that saves the change', async () => {
    const r = await open();
    type(r.root.querySelector<HTMLTextAreaElement>('#f-description')!, 'Api');
    await settle(r.fixture);
    const tab = [...r.root.querySelectorAll<HTMLElement>('[role="tab"]')].find((t) =>
      t.textContent?.includes('API'),
    )!;
    tab.click();
    await settle(r.fixture);
    expect(text(r.root)).toContain('curl -X PATCH');
    expect(text(r.root)).toContain('If-Match: "1"');
  });
});
