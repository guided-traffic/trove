import { GuidesApi } from '../../api/guides-api';
import { ApiError } from '../../api/problem';
import { RepositoriesApi } from '../../api/repositories-api';
import { FakeGuidesApi } from '../../api/testing/fakes-guides';
import {
  ANSIBLE_FORMAT,
  DEB_FORMAT,
  FakeRepositoriesApi,
  GENERIC_FORMAT,
  PACKAGE_FORMAT,
} from '../../api/testing/fakes-catalog';
import { AnsibleApi } from '../../api/ansible-api';
import { FakeAnsibleApi } from '../../api/testing/fakes-ansible';
import { DebApi } from '../../api/deb-api';
import { FakeDebApi } from '../../api/testing/fakes-deb';
import { addressValues, button, expectAccessible, renderScreen, settle, text, type } from '../../testing/screen';
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
      project: 'web',
      class: 'local',
      format: 'npm',
      description: 'Internal packages',
      ...(npm ? { npm: npm as never } : {}),
    });
  };
}

async function open(setup?: (fake: FakeRepositoriesApi) => void, key = 'generic-local') {
  return renderScreen(RepositoryDetail, {
    inputs: { project: 'web', key },
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

  it('R-UI-020 deleting the repository is a danger button last on its settings, behind Advanced, and asks for the key first', async () => {
    const { root, fixture, get } = await open();
    const fake = get(RepositoriesApi) as FakeRepositoriesApi;
    const save = button(root, 'Save changes');
    expect(save.parentElement!.textContent).not.toContain('Delete');
    const advanced = [...root.querySelectorAll('trove-disclosure')].at(-1)!;
    expect(text(advanced.querySelector('.heading span:not(.material-icons)')!)).toBe('Advanced');
    expect(save.compareDocumentPosition(advanced) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    const remove = button(advanced, 'Delete repository');
    expect(remove.classList).toContain('btn-danger');
    remove.click();
    await settle(fixture);
    const dialog = root.ownerDocument.body.querySelector<HTMLElement>('[role="dialog"]')!;
    type(dialog.querySelector<HTMLInputElement>('#confirm-name')!, 'generic-local');
    await settle(fixture);
    button(dialog, 'Delete repository').click();
    await settle(fixture);
    expect(fake.deleteCalls.map((c) => c.key)).toEqual(['web/generic-local']);
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
      key: 'web/generic-local',
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
        fake.versions.set('web/generic-local', 2);
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
    await expectAccessible(body.querySelector('[role="dialog"]')!);

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
    expect(r.root.ownerDocument.body.querySelector('[role="dialog"]')).toBeNull();
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
    expect(tabs).toEqual(['General', 'Access', 'How to use', 'API']);
  });

  it('R-UI-049 R-GENERIC-016 the How to use tab of a generic repository shows the guide for curl', async () => {
    const r = await open((fake) => (fake.formatsResult = [GENERIC_FORMAT, PACKAGE_FORMAT]));
    await openTab(r, 'How to use');
    const guides = r.get(GuidesApi) as FakeGuidesApi;
    expect(guides.calls.at(-1)).toEqual({ repository: 'web/generic-local', client: 'curl' });
    expect(r.root.querySelector('[data-section="generic.local.what"]')).not.toBeNull();
    const configure = r.root.querySelector('[data-section="generic.configure"] .snippet pre')?.textContent ?? '';
    expect(configure).toContain('trove_curl() {');
    const hrefs = [...r.root.querySelectorAll<HTMLAnchorElement>('.links a')].map((a) => a.getAttribute('href'));
    expect(hrefs).toEqual([
      '/setup?format=generic&client=curl&repository=web%2Fgeneric-local',
      '/guides?format=generic&client=curl&repository=web%2Fgeneric-local',
    ]);
    await expectAccessible(r.root);
  });

  it('R-UI-021 a caller who may use but not configure the repository sees the consumer view without a form', async () => {
    const r = await open((fake) => (fake.consumerOnly = true));
    expect(text(r.root)).toContain(
      'You may use this repository but not read or change its configuration.',
    );
    expect(r.root.querySelector('textarea')).toBeNull();
    await expectAccessible(r.root);
  });

  it('R-UI-049 a repository of a format with clients has a How to use tab with its guide for the chosen client', async () => {
    const r = await open(withNpmLocal(), 'npm-local');
    const tabs = [...r.root.querySelectorAll('[role="tab"]')].map((t) => text(t));
    expect(tabs).toEqual(['General', 'Access', 'npm settings', 'How to use', 'API']);
    const guides = r.get(GuidesApi) as FakeGuidesApi;
    expect(guides.calls).toEqual([]);
    await openTab(r, 'How to use');
    expect(guides.calls.at(-1)).toEqual({ repository: 'web/npm-local', client: 'npm' });
    expect(r.root.querySelector('[data-section="npm.local.what"]')).not.toBeNull();
    const yarn = r.root.querySelector<HTMLInputElement>('input[name="repo-guide-client"][value="yarn"]')!;
    yarn.checked = true;
    yarn.dispatchEvent(new Event('change', { bubbles: true }));
    await settle(r.fixture);
    expect(guides.calls.at(-1)).toEqual({ repository: 'web/npm-local', client: 'yarn' });
    const hrefs = [...r.root.querySelectorAll<HTMLAnchorElement>('.links a')].map((a) => a.getAttribute('href'));
    expect(hrefs).toEqual([
      '/setup?format=npm&client=yarn&repository=web%2Fnpm-local',
      '/guides?format=npm&client=yarn&repository=web%2Fnpm-local',
    ]);
    await expectAccessible(r.root);
  });

  it('R-UI-049 the consumer view of an npm repository shows the guide under its own heading', async () => {
    const r = await open((fake) => {
      withNpmLocal()(fake);
      fake.consumerOnly = true;
    }, 'npm-local');
    expect(r.root.querySelector('#repo-how-to-use')?.textContent?.trim()).toBe('How to use');
    expect(r.root.querySelector('[data-section="npm.configure"]')).not.toBeNull();
    await expectAccessible(r.root);
  });

  it('R-UI-021 an unknown repository shows the not-found error and passes the accessibility check', async () => {
    const r = await open(undefined, 'no-such-repo');
    expect(text(r.root)).toContain('does not exist');
    await expectAccessible(r.root);
  });

  it('R-REPO-002 R-UI-021 R-UI-066 the client address and the curl command carry the configured base URL even though the page is served from another origin', async () => {
    const r = await open((fake) => (fake.formatsResult = [GENERIC_FORMAT, PACKAGE_FORMAT]));
    expect(location.origin).not.toBe('https://trove.example.net');
    expect(addressValues(r.root)).toEqual(['https://trove.example.net/files/web/generic-local/']);
    type(r.root.querySelector<HTMLTextAreaElement>('#f-description')!, 'Api');
    await settle(r.fixture);
    const tab = [...r.root.querySelectorAll<HTMLElement>('[role="tab"]')].find((t) =>
      t.textContent?.includes('API'),
    )!;
    tab.click();
    await settle(r.fixture);
    expect(text(r.root)).toContain(
      "curl -X PATCH 'https://trove.example.net/api/v1/projects/web/repositories/generic-local'",
    );
    expect(text(r.root)).not.toContain(location.origin);
  });

  it('R-REPO-002 R-UI-021 R-UI-066 a server without a configured base URL leaves the client address out instead of showing the page origin', async () => {
    const r = await renderScreen(RepositoryDetail, {
      inputs: { project: 'web', key: 'generic-local' },
      capabilities: ['repositories:read-config', 'repositories:write-config'],
      setup: (get) => {
        const fake = get(RepositoriesApi) as FakeRepositoriesApi;
        fake.baseUrlResult = null;
      },
    });
    expect(text(r.root)).not.toContain(location.origin);
    expect(r.root.querySelector('trove-address-field')).toBeNull();
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

  describe('Debian', () => {
    const withDeb = (cls: 'local' | 'remote' | 'virtual', deb?: Record<string, unknown>) => (fake: FakeRepositoriesApi) => {
      fake.formatsResult = [GENERIC_FORMAT, DEB_FORMAT];
      fake.repositories.push({
        key: `deb-${cls}`,
        project: 'web',
        class: cls,
        format: 'deb',
        description: 'APT',
        ...(deb ? { deb: deb as never } : {}),
      });
    };
    const openDeb = (cls: 'local' | 'remote' | 'virtual', deb?: Record<string, unknown>, capabilities?: string[]) =>
      renderScreen(RepositoryDetail, {
        inputs: { project: 'web', key: `deb-${cls}` },
        ...(capabilities ? { capabilities } : {}),
        setup: (get) => withDeb(cls, deb)(get(RepositoriesApi) as FakeRepositoriesApi),
      });
    const tabNames = (r: Awaited<ReturnType<typeof openDeb>>) =>
      [...r.root.querySelectorAll('[role="tab"]')].map((t) => text(t));

    it('R-DEB-030 R-UI-021 a local repository has the tabs for its format settings, signing keys, index state and the APT guide', async () => {
      const r = await openDeb('local');
      expect(tabNames(r)).toEqual(['General', 'Access', 'Debian (APT) settings', 'Signing keys', 'Index', 'How to use', 'API']);
      await openTab(r, 'Signing keys');
      expect(text(r.root)).toContain('3F2A 0000');
      const api = r.get(DebApi) as FakeDebApi;
      expect(api.calls).toContain('signingKeys');
      expect(api.calls).not.toContain('indexState');
      await openTab(r, 'Index');
      expect(api.calls).toContain('indexState');
      expect(text(r.root)).toContain('Waiting for a build');
      await expectAccessible(r.root);
    });

    it('R-DEB-015 someone who may write repositories can promote; a reader of the configuration sees the keys without the buttons', async () => {
      const writer = await openDeb('virtual');
      await openTab(writer, 'Signing keys');
      expect(text(writer.root)).toContain('Generate a next key');
      const reader = await openDeb('virtual', undefined, ['repositories:read-config']);
      await openTab(reader, 'Signing keys');
      expect(text(reader.root)).toContain('3F2A 0000');
      expect(text(reader.root)).not.toContain('Generate a next key');
      await openTab(reader, 'Index');
      expect(text(reader.root)).not.toContain('Rebuild all indexes');
    });

    it('R-DEB-020 a remote repository has no signing keys and no index state of its own and shows the fingerprints of its upstream keys read-only', async () => {
      const r = await openDeb('remote', {
        upstreamKeys: 'BLOCK',
        upstreamKeyFingerprints: ['AAAA000000000000000000000000000000001111', 'BBBB000000000000000000000000000000002222'],
      });
      const names = tabNames(r);
      expect(names).not.toContain('Signing keys');
      expect(names).not.toContain('Index');
      await openTab(r, 'Debian (APT)');
      const section = r.root.querySelector('[data-testid="deb-upstream-fingerprints"]')!;
      expect([...section.querySelectorAll('code')].map((c) => text(c))).toEqual([
        'AAAA 0000 0000 0000 0000 0000 0000 0000 0000 1111',
        'BBBB 0000 0000 0000 0000 0000 0000 0000 0000 2222',
      ]);
      expect(section.querySelector('input, textarea')).toBeNull();
      expect(r.root.querySelector<HTMLTextAreaElement>('#f-deb-upstreamKeys')?.value).toBe('BLOCK');
    });

    it('R-DEB-020 the read-only fingerprints are never written back: changing another setting patches that setting only', async () => {
      const r = await openDeb('remote', {
        upstreamKeys: 'BLOCK',
        upstreamKeyFingerprints: ['AAAA000000000000000000000000000000001111'],
      });
      await openTab(r, 'Debian (APT)');
      const fake = r.get(RepositoriesApi) as FakeRepositoriesApi;
      type(r.root.querySelector<HTMLTextAreaElement>('#f-deb-mirror-suites')!, 'stable');
      await settle(r.fixture);
      button(r.root, 'Save changes').click();
      await settle(r.fixture);
      expect(fake.patches[0]?.patch).toEqual({ deb: { mirror: { suites: ['stable'] } } });
    });
  });

  describe('Ansible Galaxy', () => {
    const withAnsible = (cls: 'local' | 'remote' | 'virtual', ansible?: Record<string, unknown>) => (fake: FakeRepositoriesApi) => {
      fake.formatsResult = [GENERIC_FORMAT, ANSIBLE_FORMAT];
      fake.repositories.push({
        key: `ansible-${cls}`,
        project: 'web',
        class: cls,
        format: 'ansible',
        description: 'Galaxy',
        ...(ansible ? { ansible: ansible as never } : {}),
      });
    };
    const openAnsible = (cls: 'local' | 'remote' | 'virtual', ansible?: Record<string, unknown>, capabilities?: string[]) =>
      renderScreen(RepositoryDetail, {
        inputs: { project: 'web', key: `ansible-${cls}` },
        ...(capabilities ? { capabilities } : {}),
        setup: (get) => withAnsible(cls, ansible)(get(RepositoriesApi) as FakeRepositoriesApi),
      });
    const tabNames = (r: Awaited<ReturnType<typeof openAnsible>>) =>
      [...r.root.querySelectorAll('[role="tab"]')].map((t) => text(t));

    it('R-ANS-026 a local repository has the tabs for its import tasks, its tombstones and the ansible-galaxy guide, loaded when opened', async () => {
      const r = await openAnsible('local');
      expect(tabNames(r)).toEqual(expect.arrayContaining(['Import tasks', 'Tombstones', 'How to use']));
      const api = r.get(AnsibleApi) as FakeAnsibleApi;
      expect(api.calls).toEqual([]);
      await openTab(r, 'Import tasks');
      expect(api.calls).toEqual(['importTasks']);
      expect(text(r.root)).toContain('acme.tools');
      await openTab(r, 'Tombstones');
      expect(api.calls).toContain('tombstones');
      await expectAccessible(r.root);
    });

    it('R-ANS-026 a virtual repository shows the import tasks of its deployment target and has no tombstones', async () => {
      const r = await openAnsible('virtual');
      expect(tabNames(r)).toContain('Import tasks');
      expect(tabNames(r)).not.toContain('Tombstones');
    });

    it('R-ANS-026 a remote repository has neither tab, and its hub settings are the format settings', async () => {
      const r = await openAnsible('remote', { authUrl: 'https://sso.example.com/token', clientId: 'cloud-services' });
      expect(tabNames(r)).not.toContain('Import tasks');
      expect(tabNames(r)).not.toContain('Tombstones');
      const settings = tabNames(r).find((n) => n.includes('Ansible Galaxy'))!;
      await openTab(r, settings);
      expect(r.root.querySelector<HTMLInputElement>('#f-ansible-authUrl')?.value).toBe('https://sso.example.com/token');
      expect(r.root.querySelector<HTMLInputElement>('#f-ansible-clientId')?.value).toBe('cloud-services');
    });

    it('R-ANS-012 someone who cannot write the configuration sees the tombstones without the clearing', async () => {
      const r = await openAnsible('local', undefined, ['repositories:read-config']);
      await openTab(r, 'Tombstones');
      expect(text(r.root)).toContain('acme.tools');
      expect([...r.root.querySelectorAll('button')].map((b) => text(b))).not.toContain('Clear');
    });
  });
});
