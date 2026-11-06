import { Router } from '@angular/router';
import { vi } from 'vitest';
import { BastionsApi } from '../../api/bastions-api';
import { GuidesApi } from '../../api/guides-api';
import { ClientSetupApi } from '../../api/packages-api';
import { RepositoriesApi } from '../../api/repositories-api';
import { FakeRepositoriesApi, GENERIC_FORMAT, PACKAGE_FORMAT } from '../../api/testing/fakes-catalog';
import { FakeBastionsApi, sampleBastion, sampleBastionContact } from '../../api/testing/fakes-bastions';
import { FakeGuidesApi } from '../../api/testing/fakes-guides';
import { FakeClientSetupApi, NPM_REPOSITORIES } from '../../api/testing/fakes-packages';
import { expectAccessible, renderScreen, settle, text } from '../../testing/screen';
import { GuidePage } from '../guides/guide-page';
import { ClientSetupPage } from './client-setup';

type Get = <A>(token: abstract new (...args: never[]) => A) => A;

/** Registrations as the Bastions screen would list them: one has reported a base URL, one has not. */
function registrations(get: Get): void {
  (get(BastionsApi) as FakeBastionsApi).bastions = [
    sampleBastion(),
    sampleBastion({ id: 'b2', name: 'fresh', contacts: [] }),
    sampleBastion({ id: 'b3', name: 'quiet', contacts: [sampleBastionContact({ baseUrl: '' })] }),
  ];
}

async function openSetup(inputs: Record<string, unknown>, capabilities?: readonly string[]) {
  const r = await renderScreen(ClientSetupPage, {
    inputs,
    capabilities,
    setup: (get) => {
      const repos = get(RepositoriesApi) as FakeRepositoriesApi;
      repos.formatsResult = [GENERIC_FORMAT, PACKAGE_FORMAT];
      repos.repositories = [...NPM_REPOSITORIES];
      registrations(get);
    },
  });
  return { ...r, setup: r.get(ClientSetupApi) as FakeClientSetupApi };
}

const target = (root: HTMLElement, name: string, value: string) =>
  root.querySelector<HTMLInputElement>(`input[name="${name}"][value="${value}"]`);

describe('Client setup for a Bastion (R-BAS-007, R-API-030)', () => {
  it('R-BAS-007 offers the Bastions that have reported a base URL as a target, and only those', async () => {
    const r = await openSetup({ repository: 'web/npm-all', client: 'npm' });
    const offered = [...r.root.querySelectorAll<HTMLInputElement>('input[name="target"]')].map((i) => i.value);
    expect(offered).toEqual(['', 'partners']);
    expect(target(r.root, 'target', '')?.checked).toBe(true);
    expect(r.setup.calls.at(-1)?.query.bastion).toBeUndefined();
    expect(text(r.root)).toContain('Install from');
    expect(r.root.querySelectorAll('.step-no')[4]?.textContent?.trim()).toBe('5');
    await expectAccessible(r.root);
  });

  it('R-BAS-007 a Bastion target asks for the setup with bastion=<name> and shows its address, replace-registry-host and no publish snippet', async () => {
    const r = await openSetup({ repository: 'web/npm-all', client: 'npm', bastion: 'partners' });
    expect(r.setup.calls.at(-1)?.query).toMatchObject({ client: 'npm', bastion: 'partners' });
    expect(target(r.root, 'target', 'partners')?.checked).toBe(true);
    const all = [...r.root.querySelectorAll('.snippet pre')].map((p) => p.textContent ?? '').join('\n');
    expect(all).toContain('registry=https://packages.example.com/api/npm/web/npm-all/');
    expect(all).toContain('replace-registry-host=always');
    expect(all).not.toContain('trove.example.net');
    expect(text(r.root)).toContain('makes npm fetch every package from this address');
    expect(text(r.root)).toContain('These snippets point at the Bastion');
    const publish = [...r.root.querySelectorAll<HTMLElement>('[role="tab"]')].find((t) => text(t) === 'Publish')!;
    publish.click();
    await settle(r.fixture);
    expect(text(r.root)).toContain('A Bastion is read only: you cannot publish through it');
    await expectAccessible(r.root);
  });

  it('R-BAS-007 choosing the Bastion, and then this installation, puts the choice into the query', async () => {
    const r = await openSetup({ repository: 'web/npm-all', client: 'npm' });
    const navigate = vi.spyOn(r.get(Router), 'navigate');
    const choose = async (value: string) => {
      const input = target(r.root, 'target', value)!;
      input.checked = true;
      input.dispatchEvent(new Event('change', { bubbles: true }));
      await settle(r.fixture);
    };
    await choose('partners');
    expect(navigate).toHaveBeenLastCalledWith([], expect.objectContaining({ queryParams: { bastion: 'partners' } }));
    await choose('');
    expect(navigate).toHaveBeenLastCalledWith([], expect.objectContaining({ queryParams: { bastion: null } }));
  });

  it('R-BAS-007 a caller without endpoints:read gets no choice, and a Bastion named in the query still works', async () => {
    const plain = await openSetup({ repository: 'web/npm-all', client: 'npm' }, ['repositories:read-config']);
    expect(plain.root.querySelector('input[name="target"]')).toBeNull();
    expect(plain.root.querySelectorAll('.step-no')[3]?.textContent?.trim()).toBe('4');
    const linked = await openSetup({ repository: 'web/npm-all', client: 'npm', bastion: 'partners' }, ['repositories:read-config']);
    expect(target(linked.root, 'target', 'partners')?.checked).toBe(true);
    expect(linked.setup.calls.at(-1)?.query.bastion).toBe('partners');
  });

  it('R-BAS-007 an address the installation does not list is explained, not shown as a bare 409', async () => {
    const r = await openSetup({ repository: 'web/npm-all', client: 'npm', bastion: 'unlisted' });
    expect(text(r.root)).toContain('server.additional_base_urls');
    expect(r.root.querySelector('.snippet')).toBeNull();
  });

  it('R-BAS-007 a name that is no registration is a not-found notice', async () => {
    const r = await openSetup({ repository: 'web/npm-all', client: 'npm', bastion: 'nobody' });
    expect(r.root.querySelector('trove-error-notice')).toBeTruthy();
    expect(r.root.querySelector('.snippet')).toBeNull();
  });

  it('R-BAS-007 the guide link carries the Bastion', async () => {
    const r = await openSetup({ repository: 'web/npm-all', client: 'npm', bastion: 'partners' });
    const link = [...r.root.querySelectorAll<HTMLAnchorElement>('a')].find((a) => text(a).startsWith('Read the guide'));
    expect(link?.getAttribute('href')).toContain('bastion=partners');
  });
});

describe('Guides with a Bastion target (R-BAS-007, R-UI-049)', () => {
  async function openGuide(inputs: Record<string, unknown>, capabilities?: readonly string[]) {
    const r = await renderScreen(GuidePage, {
      inputs,
      capabilities,
      setup: (get) => {
        const repos = get(RepositoriesApi) as FakeRepositoriesApi;
        repos.formatsResult = [GENERIC_FORMAT, PACKAGE_FORMAT];
        repos.repositories = [...repos.repositories, ...NPM_REPOSITORIES];
        registrations(get);
      },
    });
    return { ...r, guides: r.get(GuidesApi) as FakeGuidesApi, setup: r.get(ClientSetupApi) as FakeClientSetupApi };
  }

  it('R-UI-049 the guide offers the Bastion as a target when a registration has reported a base URL', async () => {
    const r = await openGuide({ format: 'npm', client: 'npm', repository: 'web/npm-all' });
    expect([...r.root.querySelectorAll<HTMLInputElement>('input[name="guide-target"]')].map((i) => i.value)).toEqual(['', 'partners']);
    expect(r.root.querySelector('[data-section]')).toBeTruthy();
    const none = await openGuide({ format: 'npm', client: 'npm', repository: 'web/npm-all' }, ['repositories:read-config']);
    expect(none.root.querySelector('input[name="guide-target"]')).toBeNull();
  });

  it('R-UI-049 with the Bastion as the target the page shows the Bastion address and the read-only snippets, not the guide of this installation', async () => {
    const r = await openGuide({ format: 'npm', client: 'npm', repository: 'web/npm-all', bastion: 'partners' });
    expect(r.setup.calls.at(-1)?.query).toMatchObject({ bastion: 'partners', credentials: 'env' });
    expect(r.guides.calls).toEqual([]);
    expect(r.root.querySelector('[data-section]')).toBeNull();
    expect(text(r.root)).toContain('Install through the Bastion');
    expect(text(r.root)).toContain('https://packages.example.com/api/npm/web/npm-all/');
    const ids = [...r.root.querySelectorAll<HTMLElement>('[data-snippet]')].map((s) => s.dataset['snippet']);
    expect(ids).toContain('npm.configure.npmrc');
    expect(ids).toContain('npm.install');
    expect(ids.some((id) => id?.endsWith('publish'))).toBe(false);
    expect([...r.root.querySelectorAll('.snippet pre')].map((p) => p.textContent).join('')).toContain('replace-registry-host=always');
    await expectAccessible(r.root);
  });

  it('R-UI-049 a Bastion target without a repository asks for one and makes no request', async () => {
    const r = await openGuide({ format: 'npm', client: 'npm', bastion: 'partners' });
    expect(text(r.root)).toContain('Choose a repository');
    expect(r.setup.calls).toEqual([]);
    expect(r.guides.calls).toEqual([]);
  });

  it('R-UI-049 the link to the client setup carries the Bastion', async () => {
    const r = await openGuide({ format: 'npm', client: 'npm', repository: 'web/npm-all', bastion: 'partners' });
    const link = [...r.root.querySelectorAll<HTMLAnchorElement>('a')].find((a) => text(a).startsWith('Open the client setup'));
    expect(link?.getAttribute('href')).toContain('bastion=partners');
  });
});
