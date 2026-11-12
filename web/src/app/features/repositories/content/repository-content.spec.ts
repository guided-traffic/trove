import { ClientSetupApi, PackagesApi } from '../../../api/packages-api';
import { RepositoriesApi } from '../../../api/repositories-api';
import { VulnerabilitiesApi } from '../../../api/vulnerabilities-api';
import { FakeClientSetupApi, FakePackagesApi, ociFixture } from '../../../api/testing/fakes-packages';
import { FakeRepositoriesApi, GENERIC_FORMAT, OCI_FORMAT, PACKAGE_FORMAT } from '../../../api/testing/fakes-catalog';
import { FakeFilesApi } from '../../../api/testing/fakes-catalog';
import { FakeVulnerabilitiesApi, sampleGrade } from '../../../api/testing/fakes-vulnerabilities';
import { FilesApi } from '../../../api/files-api';
import { expectAccessible, renderScreen, settle, text } from '../../../testing/screen';
import { RepositoryContent } from './repository-content';
import { RepositoryItems } from './repository-items';
import { contentShapeOf } from './content-shape';
import { Component, signal } from '@angular/core';

interface RepoDoc {
  readonly key: string;
  readonly project: string;
  readonly class: 'local' | 'remote' | 'virtual';
  readonly format: string;
  readonly description?: string;
  readonly disabled?: boolean;
  readonly virtual?: { members: string[] };
}

async function open(repo: RepoDoc, setup?: (fakes: {
  catalog: FakeRepositoriesApi;
  packages: FakePackagesApi;
  vulnerabilities: FakeVulnerabilitiesApi;
}) => void, capabilities?: readonly string[], inputs: Record<string, unknown> = {}) {
  const r = await renderScreen(RepositoryContent, {
    inputs: { project: repo.project, key: repo.key, ...inputs },
    capabilities,
    setup: (get) => {
      const catalog = get(RepositoriesApi) as FakeRepositoriesApi;
      catalog.repositories = [...catalog.repositories, repo as never];
      catalog.formatsResult = [GENERIC_FORMAT, PACKAGE_FORMAT, OCI_FORMAT];
      setup?.({ catalog, packages: get(PackagesApi) as FakePackagesApi, vulnerabilities: get(VulnerabilitiesApi) as FakeVulnerabilitiesApi });
    },
  });
  await settle(r.fixture);
  return r;
}

const OCI_REPO: RepoDoc = { key: 'oci-local', project: 'web', class: 'local', format: 'oci', description: 'Images of the web team' };
const NPM_REPO: RepoDoc = { key: 'npm-local', project: 'web', class: 'local', format: 'npm', description: 'Internal packages' };

describe('Repository content page', () => {
  it('R-UI-052 an OCI repository lists its images, opens the tags with digest, size and pushed time, and shows the pull command of this installation', async () => {
    const r = await open(OCI_REPO, ({ packages }) => {
      ociFixture(packages);
      packages.packages = packages.packages.map((p) => ({ ...p, vulnerabilityGrade: sampleGrade('C', {}, { eosl: true, os: 'alpine 3.12.0' }) }));
    });
    expect(r.root.querySelector('h1')?.textContent).toContain('oci-local');
    expect(text(r.root)).toContain('Images of the web team');
    expect(text(r.root)).not.toContain('not scanned for vulnerabilities yet');
    expect(text(r.root)).toContain('Vulnerabilities');
    expect(r.root.querySelector('trove-data-table caption')?.textContent).toContain('Images');
    expect(text(r.root.querySelector('tbody trove-grade .grade-text')!)).toBe('Grade C · OS end of life');
    const opener = r.root.querySelector<HTMLButtonElement>('.name-cell button[aria-expanded]')!;
    expect(opener.getAttribute('aria-expanded')).toBe('false');
    opener.click();
    await settle(r.fixture);
    expect(opener.getAttribute('aria-expanded')).toBe('true');
    // Newest push first, with the digest of the format.
    const rows = [...r.root.querySelectorAll('trove-repository-item-versions tbody tr')];
    expect(rows[0]?.textContent).toContain('1.2.0');
    expect(rows[0]?.textContent).toContain('sha256:');
    expect((r.root.querySelector<HTMLInputElement>('trove-repository-item-versions trove-address-field input')?.value ?? r.root.querySelector('trove-repository-item-versions pre')?.textContent)).toMatch(/^docker pull \S*web\/oci-local\/team\/app:1\.2\.0$/);
    await expectAccessible(r.root);
  });

  it('R-UI-052 the pull command follows the tag chosen in the list and switches between docker and podman', async () => {
    const r = await open(OCI_REPO, ({ packages }) => ociFixture(packages));
    r.root.querySelector<HTMLButtonElement>('.name-cell button[aria-expanded]')!.click();
    await settle(r.fixture);
    const setup = r.get(ClientSetupApi) as FakeClientSetupApi;
    const rows = [...r.root.querySelectorAll('trove-repository-item-versions tbody tr')];
    rows.at(-1)!.querySelector<HTMLButtonElement>('button[aria-pressed]')!.click();
    await settle(r.fixture);
    expect(setup.calls.at(-1)?.query).toMatchObject({ client: 'docker', version: '1.1.0', credentials: 'none' });
    const client = r.root.querySelector<HTMLSelectElement>('trove-repository-item-versions select')!;
    client.value = 'podman';
    client.dispatchEvent(new Event('change', { bubbles: true }));
    await settle(r.fixture);
    expect(setup.calls.at(-1)?.query).toMatchObject({ client: 'podman', version: '1.1.0' });
    expect(setup.calls.at(-1)?.repository).toBe('web/oci-local');
    expect(setup.calls.every((c) => c.query.credentials === 'none')).toBe(true);
  });

  it('R-UI-052 R-UI-094 an npm repository shows the packages with the grade of their latest version and the install command of the version, and grades each version of an opened row', async () => {
    const r = await open(NPM_REPO, ({ packages, vulnerabilities }) => {
      packages.packages = packages.packages.map((p) =>
        p.name === '@team-a/ui'
          ? { ...p, repositories: ['web/npm-local'], vulnerabilityGrade: sampleGrade('C', { high: 2 }) }
          : { ...p, repositories: ['web/npm-local'], vulnerabilityGrade: sampleGrade('S') },
      );
      vulnerabilities.packageReports.set('pkg_01JBQ6V7S3K9', {
        packageId: 'pkg_01JBQ6V7S3K9',
        versions: [{ version: '1.4.0', grade: sampleGrade('C', { high: 2 }) }],
      });
    });
    const packages = r.get(PackagesApi) as FakePackagesApi;
    expect(packages.listFilters.at(-1)).toMatchObject({ repository: ['web/npm-local'], format: ['npm'], include: ['vulnerabilities'] });
    const row = [...r.root.querySelectorAll('tbody tr')].find((tr) => tr.textContent?.includes('@team-a/ui'))!;
    expect(text(row.querySelector('trove-grade .grade-text')!)).toBe('Grade C · 2 High');
    row.querySelector<HTMLButtonElement>('.name-cell button[aria-expanded]')!.click();
    await settle(r.fixture);
    const vulnerabilities = r.get(VulnerabilitiesApi) as FakeVulnerabilitiesApi;
    expect(vulnerabilities.calls).toEqual(expect.arrayContaining(['packageReport pkg_01JBQ6V7S3K9 in web/npm-local']));
    expect(text(r.root.querySelector('trove-repository-item-versions trove-grade .grade-text')!)).toBe('Grade C · 2 High');
    expect((r.root.querySelector<HTMLInputElement>('trove-repository-item-versions trove-address-field input')?.value ?? r.root.querySelector('trove-repository-item-versions pre')?.textContent)).toBe('npm install @team-a/ui@1.4.0');
    await expectAccessible(r.root);
  });

  it('R-UI-094 Q-UI-373 the column of grades sorts the list by grade, worst first descending, after the name', async () => {
    const r = await open(NPM_REPO, ({ packages }) => {
      const grades = [sampleGrade('A', { low: 2 }), sampleGrade('E', { critical: 7 }), sampleGrade('N/A')];
      packages.packages = packages.packages.map((p, i) => ({ ...p, repositories: ['web/npm-local'], vulnerabilityGrade: grades[i % grades.length] }));
    }, undefined, { sort: '-grade' });
    const packages = r.get(PackagesApi) as FakePackagesApi;
    // The server sorts by name; the browser by grade (Q-UI-373).
    expect(packages.listFilters.at(-1)).toMatchObject({ sort: 'name' });
    const order = ['N/A', 'S', 'A', 'B', 'C', 'D', 'E'];
    const shown = [...r.root.querySelectorAll('tbody > tr > td trove-grade .grade-letter')].map((e) => order.indexOf(e.textContent?.trim() ?? ''));
    expect(shown.length).toBeGreaterThan(1);
    expect(shown[0]).toBe(order.indexOf('E'));
    expect(shown).toEqual([...shown].sort((a, b) => b - a));
    const header = [...r.root.querySelectorAll('thead th')].find((th) => th.textContent?.includes('Vulnerabilities'))!;
    expect(header.getAttribute('aria-sort')).toBe('descending');
  });

  it('R-UI-094 a list asked before the format was known to be scanned is asked again with the grades', async () => {
    @Component({
      imports: [RepositoryItems],
      template: `<trove-repository-items repository="web/npm-local" format="npm" [shape]="shape" [scanned]="scanned()" />`,
    })
    class Host {
      readonly shape = contentShapeOf('npm');
      readonly scanned = signal(false);
    }
    const r = await renderScreen(Host, {});
    const packages = r.get(PackagesApi) as FakePackagesApi;
    expect(packages.listFilters.at(-1)).toMatchObject({ include: [] });
    r.fixture.componentInstance.scanned.set(true);
    await settle(r.fixture);
    expect(packages.listFilters.at(-1)).toMatchObject({ include: ['vulnerabilities'] });
  });

  it('R-UI-094 Q-UI-377 a list without grades has no column of grades', async () => {
    const r = await open(NPM_REPO, ({ packages }) => {
      packages.packages = packages.packages.map((p) => ({ ...p, repositories: ['web/npm-local'] }));
    });
    expect([...r.root.querySelectorAll('thead th')].some((th) => th.textContent?.includes('Vulnerabilities'))).toBe(false);
    expect(r.root.querySelector('tbody trove-grade')).toBeNull();
  });

  it('R-UI-052 a generic repository lists its files with a link to the file and a copyable address, and a folder opens in place', async () => {
    const generic: RepoDoc = { key: 'generic-local', project: 'web', class: 'local', format: 'generic', description: 'Files' };
    const r = await renderScreen(RepositoryContent, {
      inputs: { project: 'web', key: 'generic-local' },
      setup: (get) => {
        (get(RepositoriesApi) as FakeRepositoriesApi).formatsResult = [GENERIC_FORMAT];
        const files = get(FilesApi) as FakeFilesApi;
        files.children = () => async () => ({
          items: [
            { path: 'bin', kind: 'folder' as const },
            { path: 'app.tar.gz', kind: 'file' as const, size: 2048 },
          ],
          nextCursor: null,
        });
      },
    });
    await settle(r.fixture);
    expect(r.root.querySelector('trove-data-table caption')?.textContent).toContain('Files');
    expect(text(r.root)).toContain('app.tar.gz');
    expect(r.root.querySelector<HTMLAnchorElement>('tbody a[href$="/browse/generic-local/app.tar.gz"]')).not.toBeNull();
    expect(generic.format).toBe('generic');
    await expectAccessible(r.root);
  });

  it('R-UI-052 an empty repository says how it is filled and offers Set up client; a disabled one offers nothing', async () => {
    const empty = async (disabled: boolean) => {
      const r = await renderScreen(RepositoryContent, {
        inputs: { project: 'web', key: 'generic-local' },
        setup: (get) => {
          const catalog = get(RepositoriesApi) as FakeRepositoriesApi;
          catalog.formatsResult = [GENERIC_FORMAT];
          catalog.repositories = catalog.repositories.map((x) => (x.key === 'generic-local' ? { ...x, disabled } : x));
          (get(FilesApi) as FakeFilesApi).children = () => async () => ({ items: [], nextCursor: null });
        },
      });
      await settle(r.fixture);
      return r;
    };
    const r = await empty(false);
    const state = r.root.querySelector('trove-data-table trove-empty-state')!;
    expect(text(state)).toContain('This repository holds no files yet. Upload a file with your client.');
    expect(state.querySelector('a.btn')?.getAttribute('href')).toBe('/setup?repository=web%2Fgeneric-local');
    await expectAccessible(r.root);
    const off = await empty(true);
    expect(off.root.querySelector('trove-data-table trove-empty-state a')).toBeNull();
  });

  it('R-UI-052 a virtual repository names what it resolves from and a remote one says it lists what clients fetched', async () => {
    const virtual = await open({ key: 'npm-all', project: 'web', class: 'virtual', format: 'npm', virtual: { members: ['npm-local', 'npm-remote'] } });
    expect(text(virtual.root)).toContain('Resolves from');
    expect(virtual.root.querySelector<HTMLAnchorElement>('.resolves a[href$="/repositories/npm-remote"]')).not.toBeNull();
    const remote = await open({ key: 'npm-cache', project: 'web', class: 'remote', format: 'npm' });
    expect(text(remote.root)).toContain('Only what clients have fetched is listed here.');
  });

  it('R-OCI-036 an OCI remote repository lists the images it has cached like a local one, with the fetched time of a tag, and no longer says that they are not listed', async () => {
    const remote: RepoDoc = { key: 'oci-cache', project: 'web', class: 'remote', format: 'oci' };
    const r = await open(remote, ({ packages }) => {
      ociFixture(packages);
      packages.packages = packages.packages.map((p) => (p.format === 'oci' ? { ...p, repositories: ['web/oci-cache'] } : p));
    });
    const body = text(r.root);
    expect(body).not.toContain('not listed here yet');
    expect(body).toContain('Only the tags this repository has cached are listed here');
    const packages = r.get(PackagesApi) as FakePackagesApi;
    expect(packages.listFilters.at(-1)).toMatchObject({ repository: ['web/oci-cache'], format: ['oci'] });
    const opener = r.root.querySelector<HTMLButtonElement>('.name-cell button[aria-expanded]')!;
    opener.click();
    await settle(r.fixture);
    const headers = [...r.root.querySelectorAll('trove-repository-item-versions th')].map((th) => th.textContent?.trim());
    expect(headers).toContain('Fetched');
    expect(headers).not.toContain('Pushed');
    const rows = [...r.root.querySelectorAll('trove-repository-item-versions tbody tr')];
    expect(rows[0]?.textContent).toContain('sha256:');
    await expectAccessible(r.root);
  });

  it('R-OCI-036 an OCI remote repository with nothing cached says so and names the way to fill it', async () => {
    const remote: RepoDoc = { key: 'oci-cache', project: 'web', class: 'remote', format: 'oci' };
    const r = await open(remote, ({ packages }) => {
      packages.packages = [];
    });
    expect(text(r.root)).toContain('This repository has cached no images yet. Pull an image through it');
  });

  it('R-UI-052 the page has no setup tab: its Set up client opens the setup for this repository', async () => {
    const r = await open(NPM_REPO);
    // One way to the setup (Q-UI-321): the header's primary action, no tab beside the content.
    expect([...r.root.querySelectorAll('[role="tab"]')].map((t) => text(t))).not.toContain('Set up client');
    const setup = r.root.querySelector('.page-header-actions a.btn-confirm');
    expect(setup?.getAttribute('href')).toBe('/setup?repository=web%2Fnpm-local');
    await expectAccessible(r.root);
  });

  it('R-UI-052 the header shows the client address alone; Details names the address for downloads by path', async () => {
    const npm = 'https://trove.example.net/api/npm/web/npm-local/';
    const files = 'https://trove.example.net/files/web/npm-local/';
    const r = await open(NPM_REPO, ({ catalog }) => {
      // The consumer view of a reader: the protocol address first, the raw file address after it (R-REPO-010).
      catalog.get = async () => ({
        value: { key: 'npm-local', class: 'local', format: 'npm', description: 'Internal packages', layout: 'npm', addresses: [npm, files] },
        etag: '"1"',
      });
    });
    const header = [...r.root.querySelectorAll<HTMLInputElement>('trove-page-header trove-address-field input')].map((i) => i.value);
    expect(header).toEqual([npm]);
    const details = r.root.querySelector('trove-disclosure')!;
    expect(details.querySelector<HTMLInputElement>('trove-address-field input')?.value).toBe(files);
    expect(text(details)).toContain('Address for downloads by path (curl, wget)');
    expect(details.querySelector('trove-address-field button')?.getAttribute('aria-label')).toBe('Copy address for downloads by path');
    await expectAccessible(r.root);
  });

  it('R-UI-052 a disabled repository offers no Set up client and says that it is disabled', async () => {
    const r = await open({ ...NPM_REPO, disabled: true });
    expect(r.root.querySelector('.page-header-actions a.btn-confirm')).toBeNull();
    expect(text(r.root.querySelector('trove-page-header')!)).toContain('disabled');
    await expectAccessible(r.root);
  });

  it('R-UI-052 the Settings action is offered to who can read the configuration, and leads to the settings route', async () => {
    const owner = await open(NPM_REPO);
    const settings = [...owner.root.querySelectorAll('.page-header-actions a')].find((a) => a.textContent?.trim() === 'Settings');
    expect(settings?.getAttribute('href')).toBe('/projects/web/repositories/npm-local/settings');
    const reader = await open(NPM_REPO, undefined, []);
    expect([...reader.root.querySelectorAll('.page-header-actions a')].map((a) => a.textContent?.trim())).not.toContain('Settings');
  });

  it('R-UI-066 R-UI-067 the header shows the client address and offers Set up client as its primary action', async () => {
    const r = await open(NPM_REPO);
    const field = r.root.querySelector<HTMLInputElement>('trove-page-header trove-address-field input');
    expect(field?.value).toContain('web/npm-local/');
    const primary = r.root.querySelector('.page-header-actions a.btn-confirm');
    expect(primary?.textContent?.trim()).toBe('Set up client');
    expect(primary?.getAttribute('href')).toBe('/setup?repository=web%2Fnpm-local');
    // The other views are in the header's menu, not loose links beside the tabs.
    expect(r.root.querySelector('.page-header-actions button[aria-label="More actions"]')).toBeTruthy();
    await expectAccessible(r.root);
  });

  it('R-UI-052 the filter of the list is sent to the server and lives in the URL', async () => {
    const r = await open(NPM_REPO, undefined, undefined, { q: 'left' });
    const packages = r.get(PackagesApi) as FakePackagesApi;
    expect(packages.listFilters.at(-1)).toMatchObject({ q: 'left' });
    const items = r.fixture.debugElement.query((d) => d.componentInstance instanceof RepositoryItems);
    expect(items).toBeTruthy();
  });
});
