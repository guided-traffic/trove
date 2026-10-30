import { ClientSetupApi, PackageVersion } from '../../api/packages-api';
import { ApiError } from '../../api/problem';
import { FakeClientSetupApi, FakePackagesApi } from '../../api/testing/fakes-packages';
import { Notifier } from '../../core/notifier';
import { button, expectAccessible, renderScreen, settle, text } from '../../testing/screen';
import { InstallPanel } from './install-panel';

async function version(v = '1.4.0'): Promise<PackageVersion> {
  return new FakePackagesApi().version('pkg_01JBQ6V7S3K9', v);
}

async function open(over: Partial<PackageVersion> = {}, name = '@team-a/ui') {
  const base = { ...(await version()), ...over };
  const r = await renderScreen(InstallPanel, { inputs: { format: 'npm', name, version: base } });
  return { ...r, fake: r.get(ClientSetupApi) as FakeClientSetupApi };
}

describe('Install command panel', () => {
  it('R-UI-009 shows the install command of the server for the package and version, and passes the accessibility check', async () => {
    const r = await open();
    expect(r.root.querySelector('pre')?.textContent).toBe('npm install @team-a/ui@1.4.0');
    expect(r.fake.calls).toHaveLength(1);
    expect(r.fake.calls[0]?.query).toMatchObject({ client: 'npm', package: '@team-a/ui', version: '1.4.0' });
    await expectAccessible(r.root);
  });

  it('R-UI-009 the install command carries no credentials: the request asks for none and the text holds no token placeholder', async () => {
    const r = await open();
    expect(r.fake.calls.every((c) => c.query.credentials === 'none')).toBe(true);
    expect(text(r.root)).not.toContain('{{token}}');
    expect(text(r.root)).not.toContain('TROVE_TOKEN');
    expect(text(r.root)).not.toContain('trv_');
  });

  it('R-UI-009 the client picker offers npm, pnpm, Yarn Berry and Yarn Classic and switches the command', async () => {
    const r = await open();
    const select = r.root.querySelector<HTMLSelectElement>('select')!;
    expect([...select.options].map((o) => o.value)).toEqual(['npm', 'pnpm', 'yarn', 'yarn-classic']);
    for (const [client, command] of [
      ['pnpm', 'pnpm add @team-a/ui@1.4.0'],
      ['yarn', 'yarn add @team-a/ui@1.4.0'],
      ['yarn-classic', 'yarn add @team-a/ui@1.4.0'],
    ] as const) {
      select.value = client;
      select.dispatchEvent(new Event('change', { bubbles: true }));
      await settle(r.fixture);
      expect(r.root.querySelector('pre')?.textContent, client).toBe(command);
    }
  });

  it('R-UI-009 a version in more than one repository offers a picker that starts at a virtual repository containing it', async () => {
    const r = await open();
    const pickers = r.root.querySelectorAll('select');
    expect(pickers).toHaveLength(2);
    expect(pickers[1]?.value).toBe('npm-all');
    expect(r.fake.calls[0]?.repository).toBe('npm-all');
    pickers[1]!.value = 'npm-local';
    pickers[1]!.dispatchEvent(new Event('change', { bubbles: true }));
    await settle(r.fixture);
    expect(r.fake.calls.at(-1)?.repository).toBe('npm-local');
  });

  it('R-UI-009 without a virtual repository the first local one is the default; with one repository there is no picker', async () => {
    const local = await open({
      repositories: [
        { key: 'npm-remote', class: 'remote', publishedAt: '2026-10-20T09:12:44.512Z' },
        { key: 'npm-local', class: 'local', publishedAt: '2026-10-20T09:12:44.512Z' },
      ],
    });
    expect(local.fake.calls[0]?.repository).toBe('npm-local');
    const single = await open({ repositories: [{ key: 'npm-local', class: 'local', publishedAt: '2026-10-20T09:12:44.512Z' }] });
    expect(single.root.querySelectorAll('select')).toHaveLength(1);
  });

  it('R-UI-009 R-API-030 a name the shell would interpret arrives quoted from the server and is shown as it is', async () => {
    const r = await open({}, 'a(b)');
    expect(r.root.querySelector('pre')?.textContent).toBe(`npm install 'a(b)@1.4.0'`);
  });

  it('R-UI-009 a command of more than three lines is collapsed behind an expander', async () => {
    const lines = ['yarn add a', 'yarn add b', 'yarn add c', 'yarn add d', 'yarn add e'].join('\n');
    const r = await renderScreen(InstallPanel, {
      inputs: { format: 'npm', name: '@team-a/ui', version: await version() },
      setup: (get) => {
        const fake = get(ClientSetupApi) as FakeClientSetupApi;
        const real = fake.get.bind(fake);
        fake.get = async (repository, query) => {
          const answer = await real(repository, query);
          return { ...answer, snippets: answer.snippets.map((s) => (s.tab === 'install' ? { ...s, content: lines } : s)) };
        };
      },
    });
    expect(r.root.querySelector('pre')?.textContent).toBe('yarn add a\nyarn add b\nyarn add c');
    const expander = button(r.root, 'Show the whole command');
    expect(expander.getAttribute('aria-expanded')).toBe('false');
    expander.click();
    await settle(r.fixture);
    expect(r.root.querySelector('pre')?.textContent).toBe(lines);
    expect(button(r.root, 'Show less').getAttribute('aria-expanded')).toBe('true');
    await expectAccessible(r.root);
  });

  it('R-UI-009 the copy button copies the whole command and a live region announces it', async () => {
    const written: string[] = [];
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: async (t: string) => void written.push(t) },
    });
    const r = await open();
    r.root.querySelector<HTMLButtonElement>('trove-copy-button button')!.click();
    await settle(r.fixture);
    expect(written).toEqual(['npm install @team-a/ui@1.4.0']);
    expect(r.get(Notifier).notices().map((n) => n.text)).toContain('Copied to the clipboard.');
  });

  it('R-UI-009 links to the client setup of the chosen repository and client', async () => {
    const r = await open();
    const link = [...r.root.querySelectorAll('a')].find((a) => a.textContent?.includes('Set up your client'));
    expect(link?.getAttribute('href')).toBe('/setup?repository=npm-all&client=npm');
  });

  it('R-UI-009 a failing setup answer is shown with a retry, not as an empty panel', async () => {
    const r = await renderScreen(InstallPanel, {
      inputs: { format: 'npm', name: '@team-a/ui', version: await version() },
      setup: (get) => ((get(ClientSetupApi) as FakeClientSetupApi).failure = new ApiError(503, { code: 'unavailable' }, null)),
    });
    expect(text(r.root)).toContain('temporarily unavailable');
    button(r.root, 'Retry').click();
    await settle(r.fixture);
    expect(r.root.querySelector('pre')?.textContent).toBe('npm install @team-a/ui@1.4.0');
  });

  it('R-UI-009 a format without client instructions says so', async () => {
    const r = await renderScreen(InstallPanel, {
      inputs: { format: 'nuget', name: 'Contoso.Logging', version: await version() },
    });
    expect(text(r.root)).toContain('no install instructions for this package type yet');
  });
});
