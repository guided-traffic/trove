import { GuidesApi } from '../../api/guides-api';
import { ApiError } from '../../api/problem';
import { RepositoriesApi } from '../../api/repositories-api';
import { FakeRepositoriesApi, GENERIC_FORMAT, PACKAGE_FORMAT } from '../../api/testing/fakes-catalog';
import { FakeGuidesApi } from '../../api/testing/fakes-guides';
import { FakeClientSetupApi, NPM_REPOSITORIES } from '../../api/testing/fakes-packages';
import { expectAccessible, renderScreen, settle, text } from '../../testing/screen';
import { GuidePage } from './guide-page';

type Opened = Awaited<ReturnType<typeof open>>;

/** A format of the catalog without clients, and therefore without a guide. */
const CLIENTLESS = { ...GENERIC_FORMAT, name: 'raw', displayName: 'Raw', clients: [] };

async function open(inputs: Record<string, unknown> = {}, setup?: (guides: FakeGuidesApi) => void) {
  const r = await renderScreen(GuidePage, {
    inputs,
    setup: (get) => {
      const repos = get(RepositoriesApi) as FakeRepositoriesApi;
      repos.formatsResult = [GENERIC_FORMAT, PACKAGE_FORMAT, CLIENTLESS];
      repos.repositories = [...repos.repositories, ...NPM_REPOSITORIES];
      setup?.(get(GuidesApi) as FakeGuidesApi);
    },
  });
  return { ...r, guides: r.get(GuidesApi) as FakeGuidesApi };
}

const sections = (r: Opened) =>
  [...r.root.querySelectorAll<HTMLElement>('section.guide-section')].map((s) => s.dataset['section']);
const radio = (r: Opened, name: string, value: string) =>
  r.root.querySelector<HTMLInputElement>(`input[name="${name}"][value="${value}"]`);

describe('Guides', () => {
  it('R-UI-049 R-UI-019 the page offers every format of the catalog that lists clients, in its order, and opens the first one', async () => {
    const r = await open();
    expect(r.root.querySelector('h1')?.textContent?.trim()).toBe('Guides');
    const offered = [...r.root.querySelectorAll<HTMLInputElement>('input[name="guide-format"]')].map((i) => i.value);
    expect(offered).toEqual(['generic', 'npm']);
    expect(radio(r, 'guide-format', 'generic')?.checked).toBe(true);
    expect([...r.root.querySelectorAll<HTMLInputElement>('input[name="guide-client"]')].map((i) => i.value)).toEqual(['curl']);
    expect([...r.root.querySelectorAll<HTMLInputElement>('input[name="guide-class"]')].map((i) => i.value)).toEqual(['local']);
    expect(r.guides.calls.at(-1)).toEqual({ format: 'generic', cls: 'local', client: 'curl' });
    expect(sections(r)).toEqual([
      'generic.local.what',
      'generic.configure',
      'generic.authenticate',
      'generic.install',
      'generic.publish',
      'generic.troubleshooting',
    ]);
    const configure = r.root.querySelector('[data-section="generic.configure"] .snippet pre')?.textContent ?? '';
    expect(configure).toContain('header = "Authorization: Bearer ${TROVE_TOKEN}"');
    await expectAccessible(r.root);
  });

  it('R-UI-049 npm opens the guide of the recommended class and passes the accessibility check', async () => {
    const r = await open({ format: 'npm' });
    expect(radio(r, 'guide-format', 'npm')?.checked).toBe(true);
    expect([...r.root.querySelectorAll('input[name="guide-client"]')].map((i) => (i as HTMLInputElement).value)).toEqual([
      'npm',
      'pnpm',
      'yarn',
      'yarn-classic',
    ]);
    expect(radio(r, 'guide-class', 'virtual')?.checked).toBe(true);
    expect(r.guides.calls.at(-1)).toEqual({ format: 'npm', cls: 'virtual', client: 'npm' });
    expect(sections(r)).toEqual(['npm.virtual.what', 'npm.configure', 'npm.authenticate', 'npm.install', 'npm.publish', 'npm.troubleshooting']);
    expect(text(r.root)).toContain('The addresses show <repository>');
    await expectAccessible(r.root);
  });

  it('R-UI-049 a repository in the query opens its guide; the setup link carries the same choice', async () => {
    const r = await open({ format: 'npm', client: 'yarn', repository: 'npm-remote' });
    expect(r.guides.calls.at(-1)).toEqual({ repository: 'npm-remote', client: 'yarn' });
    expect(r.root.querySelector('input[name="guide-class"]')).toBeNull();
    expect(sections(r)).not.toContain('npm.publish');
    const link = [...r.root.querySelectorAll<HTMLAnchorElement>('a')].find((a) => text(a).startsWith('Open the client setup'));
    expect(link?.getAttribute('href')).toBe('/setup?format=npm&client=yarn&repository=npm-remote');
  });

  it('R-UI-049 a class in the query opens the guide of that class', async () => {
    const r = await open({ format: 'npm', class: 'remote', client: 'pnpm' });
    expect(r.guides.calls.at(-1)).toEqual({ format: 'npm', cls: 'remote', client: 'pnpm' });
    expect(sections(r)[0]).toBe('npm.remote.what');
  });

  it('R-UI-049 R-API-053 the snippets of the guide are the client setup answer with the variable', async () => {
    const r = await open({ format: 'npm', client: 'npm', repository: 'npm-all' });
    const setup = await new FakeClientSetupApi().get('npm-all', { client: 'npm', credentials: 'env' });
    const configure = [...r.root.querySelectorAll('[data-section="npm.configure"] .snippet pre')].map((p) => p.textContent);
    expect(configure).toEqual(setup.snippets.filter((s) => s.tab === 'configure').map((s) => s.content));
    expect(configure.join('')).toContain('${TROVE_TOKEN}');
  });

  it('R-UI-049 an anonymous reader gets snippets without a credential and no publishing section', async () => {
    const r = await open({ format: 'npm', client: 'npm', repository: 'npm-all' }, (g) => (g.anonymous = true));
    const all = [...r.root.querySelectorAll('.snippet pre')].map((p) => p.textContent).join('\n');
    expect(all).not.toContain('TOKEN');
    expect(all).not.toContain('{{token}}');
    expect(sections(r)).not.toContain('npm.publish');
  });

  it('R-UI-049 the contents list moves focus to its section without navigating', async () => {
    const r = await open();
    const link = [...r.root.querySelectorAll<HTMLAnchorElement>('nav a')].find((a) => text(a) === 'Troubleshooting')!;
    link.click();
    await settle(r.fixture);
    expect(document.activeElement?.textContent?.trim()).toBe('Troubleshooting');
    expect(document.activeElement?.getAttribute('lang')).toBe('en');
  });

  it('R-UI-049 the sections carry the server HTML as a README does', async () => {
    const r = await open({ format: 'npm' });
    const what = r.root.querySelector('[data-section="npm.virtual.what"] trove-package-readme .readme');
    expect(what?.querySelector('h3')?.id).toBe('user-content-details');
  });

  it('R-UI-042 a failing guide shows the error with a retry', async () => {
    const r = await open({}, (g) => (g.failure = new ApiError(503, { code: 'unavailable', detail: 'down' }, null)));
    expect(r.root.querySelector('trove-error-notice')).not.toBeNull();
    await expectAccessible(r.root);
  });
});
