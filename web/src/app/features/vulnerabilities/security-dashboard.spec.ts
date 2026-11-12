import { TestBed } from '@angular/core/testing';
import { Router } from '@angular/router';
import { vi } from 'vitest';
import { RepositoriesApi } from '../../api/repositories-api';
import { FakeRepositoriesApi } from '../../api/testing/fakes-catalog';
import { NPM_REPOSITORIES, NUGET_REPOSITORIES } from '../../api/testing/fakes-packages';
import { FINDING_VULN, FakeVulnerabilitiesApi, sampleFinding } from '../../api/testing/fakes-vulnerabilities';
import { VulnerabilitiesApi } from '../../api/vulnerabilities-api';
import { helpFor } from '../../core/help-topics';
import { Navigation } from '../../core/navigation';
import { buttonByText, expectAccessible, inProject, renderScreen, settle, text, type } from '../../testing/screen';
import { SECURITY_ROUTES } from './security.routes';
import { SECURITY_CONTEXT, VULN_CONTEXT, canonicalAdvisoryId } from './vuln-context';
import { VulnerabilityReport } from './vulnerability-report';

const SHOP_FINDING = '0193a000-0000-7000-8000-0000000000a1';
const AUDITOR = ['content:metadata-read', 'vulnerabilities:triage', 'settings:read'];

async function openDashboard(inputs: Record<string, unknown> = {}, capabilities: string[] = AUDITOR) {
  const r = await renderScreen(VulnerabilityReport, {
    inputs,
    capabilities,
    providers: [{ provide: VULN_CONTEXT, useValue: SECURITY_CONTEXT }],
    setup: (get) => {
      (get(RepositoriesApi) as FakeRepositoriesApi).repositories = [...NPM_REPOSITORIES, ...NUGET_REPOSITORIES];
      const fake = get(VulnerabilitiesApi) as FakeVulnerabilitiesApi;
      fake.rows.push(
        sampleFinding({
          id: SHOP_FINDING,
          project: 'shop',
          repository: 'shop/npm-local',
          package: 'minimist',
          version: '1.2.0',
          advisory: 'GHSA-xvch-5gv4-984h',
          aliases: ['CVE-2021-44906'],
          summary: 'Prototype pollution in minimist',
          severity: 'critical',
          cvss: null,
        }),
      );
    },
  });
  return { ...r, fake: r.get(VulnerabilitiesApi) as FakeVulnerabilitiesApi };
}

const rows = (r: { root: HTMLElement }) => [...r.root.querySelectorAll('trove-data-table tbody tr')];
/** The names of the severity bar's links (R-UI-092, Q-UI-376). */
const bar = (r: { root: HTMLElement }) => [...r.root.querySelectorAll('trove-severity-bar a')].map((a) => a.getAttribute('aria-label') ?? '');
const navigate = () => vi.spyOn(TestBed.inject(Router), 'navigate').mockResolvedValue(true);
const field = (root: HTMLElement, name: string) => root.querySelector<HTMLInputElement>(`form[role="search"] input[name="${name}"]`)!;

describe('The auditor\'s security dashboard (Q-UI-130, R-VULN-022)', () => {
  it('Q-UI-130 shows every project with the project as a column, the counts and the trend over all of them, and passes axe', async () => {
    const r = await openDashboard();
    expect(text(r.root.querySelector('h1')!)).toBe('Security dashboard');
    const headers = [...r.root.querySelectorAll('th')].map((th) => text(th));
    expect(headers).toContain('Project');
    expect(headers.indexOf('Project')).toBeLessThan(headers.indexOf('Repository'));
    const all = rows(r).map((tr) => text(tr));
    expect(all.some((t) => t.includes('shop') && t.includes('minimist'))).toBe(true);
    expect(all.some((t) => t.includes('web') && t.includes('trove-test-vuln'))).toBe(true);
    // the API was asked without a project: the counts and the trend cover every project the caller may read
    expect(r.fake.listed.at(-1)?.project).toEqual([]);
    expect(r.fake.calls).toContain('summary');
    expect(r.fake.calls.some((c) => c.startsWith('trend'))).toBe(true);
    expect(bar(r)).toContain('2 Critical');
    expect(r.root.querySelector('trove-vuln-trend')).toBeTruthy();
    await expectAccessible(r.root);
  });

  it('Q-UI-130 a project filter of the URL reaches the list, the counts and the trend, shows as a chip, and keeps in the count links', async () => {
    const r = await openDashboard({ project: 'shop' });
    expect(r.fake.listed.at(-1)?.project).toEqual(['shop']);
    expect(rows(r)).toHaveLength(1);
    expect(text(rows(r)[0]!)).toContain('minimist');
    expect(bar(r)).toContain('1 Critical');
    expect(bar(r)).toContain('0 High');
    const critical = [...r.root.querySelectorAll<HTMLAnchorElement>('trove-severity-bar a')].find((a) => a.getAttribute('aria-label') === '1 Critical')!;
    expect(critical.getAttribute('href')).toContain('project=shop');
    const chip = [...r.root.querySelectorAll('.chip')].find((c) => text(c).includes('Project: shop'))!;
    const nav = navigate();
    chip.querySelector('button')!.click();
    expect(nav).toHaveBeenCalledWith([], { queryParams: { project: null }, queryParamsHandling: 'merge' });
  });

  it('Q-UI-130 a malformed project key in the URL never reaches the API', async () => {
    const r = await openDashboard({ project: ['Not A Key', 'shop'] });
    expect(r.fake.listed.at(-1)?.project).toEqual(['shop']);
  });

  it('Q-UI-130 the search sends the advisory identifier in its canonical spelling, the package prefix and the project key', async () => {
    const r = await openDashboard();
    const nav = navigate();
    type(field(r.root, 'advisory'), ' cve-2021-44906 ');
    type(field(r.root, 'package'), 'minimist');
    type(field(r.root, 'project'), 'Shop');
    buttonByText(r.root.querySelector('form[role="search"]')!, 'Search').click();
    expect(nav).toHaveBeenCalledWith([], {
      queryParams: { advisory: 'CVE-2021-44906', packagePrefix: 'minimist', project: 'shop' },
      queryParamsHandling: 'merge',
    });
  });

  it('Q-UI-130 an emptied search field drops its filter, the other filters stay', async () => {
    const r = await openDashboard({ advisory: 'CVE-2021-44906', packagePrefix: 'min', project: 'shop' });
    expect(field(r.root, 'advisory').value).toBe('CVE-2021-44906');
    expect(field(r.root, 'package').value).toBe('min');
    expect(field(r.root, 'project').value).toBe('shop');
    const nav = navigate();
    type(field(r.root, 'advisory'), '');
    buttonByText(r.root.querySelector('form[role="search"]')!, 'Search').click();
    expect(nav).toHaveBeenCalledWith([], { queryParams: { advisory: null, packagePrefix: 'min', project: 'shop' }, queryParamsHandling: 'merge' });
  });

  it('Q-UI-130 a search by an alias finds the finding through the API, which matches aliases', async () => {
    const r = await openDashboard({ advisory: 'CVE-2021-44906' });
    expect(r.fake.listed.at(-1)?.advisory).toBe('CVE-2021-44906');
    expect(rows(r)).toHaveLength(1);
    expect(text(rows(r)[0]!)).toContain('GHSA-xvch-5gv4-984h');
  });

  it('Q-UI-130 a project cell narrows the list to that project', async () => {
    const r = await openDashboard();
    const link = [...rows(r)[0]!.querySelectorAll<HTMLAnchorElement>('a')].find((a) => a.getAttribute('href')?.includes('project='))!;
    expect(link.getAttribute('href')).toMatch(/project=(shop|web)/);
  });

  it('Q-UI-130 the findings open inside the admin area, where the triage of one finding lives', async () => {
    const r = await openDashboard();
    expect(r.root.querySelector(`a[href="/admin/security/findings/${SHOP_FINDING}"]`)).toBeTruthy();
    expect(r.root.querySelector(`a[href="/admin/security/findings/${FINDING_VULN}"]`)).toBeTruthy();
    expect(r.root.querySelector('a[href^="/vulnerabilities"]')).toBeNull();
    expect(r.root.querySelector('a[href="/admin/advisory-sources"]')).toBeTruthy();
    // A package opens in the project of its finding (Q-UI-163), never at a content address without a project.
    expect(r.root.querySelector('a[href^="/projects/shop/packages/npm/minimist"]')).toBeTruthy();
    expect(r.root.querySelector('a[href^="/packages/"]')).toBeNull();
  });

  it('Q-UI-130 a holder of vulnerabilities:triage selects findings and changes their status; a reader does not', async () => {
    const triager = await openDashboard({}, AUDITOR);
    expect(triager.root.querySelector('tbody input[type="checkbox"]')).toBeTruthy();
    expect(buttonByText(triager.root, 'Select all loaded findings')).toBeTruthy();
    expect(text(triager.root)).not.toContain('Changing their status needs');

    const reader = await openDashboard({}, ['content:metadata-read']);
    expect(rows(reader).length).toBeGreaterThan(0);
    expect(reader.root.querySelector('tbody input[type="checkbox"]')).toBeNull();
    expect([...reader.root.querySelectorAll('button')].some((b) => text(b).includes('Change status'))).toBe(false);
    expect(text(reader.root)).toContain('Changing their status needs the permission to triage vulnerabilities');
    await expectAccessible(reader.root);
  });

  it('Q-UI-130 a project\'s report has no project column and no search, and its links stay in the project', async () => {
    const r = await renderScreen(VulnerabilityReport, inProject({ capabilities: ['vulnerabilities:triage'] }, '/projects/web/vulnerabilities'));
    expect([...r.root.querySelectorAll('th')].map((th) => text(th))).not.toContain('Project');
    expect(r.root.querySelector('form[role="search"]')).toBeNull();
    expect(text(r.root.querySelector('h1')!)).toBe('Vulnerabilities');
    expect(r.root.querySelector('a[href^="/projects/web/vulnerabilities/findings/"]')).toBeTruthy();
  });

  it('Q-UI-130 the entry is in the admin area for either capability, and for nobody else', async () => {
    const entry = () => {
      const nav = TestBed.inject(Navigation);
      return nav.area('admin')?.entries.some((e) => e.kind === 'item' && e.route === '/admin/security') ?? false;
    };
    await renderScreen(VulnerabilityReport, { capabilities: ['content:metadata-read'] });
    expect(entry()).toBe(true);
    await renderScreen(VulnerabilityReport, { capabilities: ['vulnerabilities:triage'] });
    expect(entry()).toBe(true);
    await renderScreen(VulnerabilityReport, { capabilities: ['audit:read'] });
    expect(entry()).toBe(false);
  });

  it('Q-UI-130 the "?" opens the manual page of the dashboard, and the routes keep the screens under /admin/security', () => {
    expect(helpFor('/admin/security')).toEqual(['/help', 'admin', 'security-dashboard']);
    expect(helpFor('/admin/security/findings/abc')).toEqual(['/help', 'admin', 'security-dashboard']);
    const inner = SECURITY_ROUTES[0]!.children!;
    expect(inner.map((c) => c.path ?? 'matcher')).toEqual(['', 'findings/:id', 'matcher']);
    expect(SECURITY_ROUTES[0]!.providers).toEqual([{ provide: VULN_CONTEXT, useValue: SECURITY_CONTEXT }]);
  });

  it('Q-UI-130 the dashboard is usable after the list changes: a filter change restarts the list', async () => {
    const r = await openDashboard();
    await r.fixture.componentRef.setInput('project', 'web');
    await settle(r.fixture);
    expect(rows(r).every((tr) => text(tr).includes('web'))).toBe(true);
    expect(r.fake.listed.at(-1)?.project).toEqual(['web']);
  });
});

describe('canonicalAdvisoryId (Q-UI-130)', () => {
  it('writes the prefix of a CVE, MAL and OSV identifier in upper case, a GHSA identifier in its own case, and leaves the rest', () => {
    expect(canonicalAdvisoryId(' cve-2021-23337 ')).toBe('CVE-2021-23337');
    expect(canonicalAdvisoryId('mal-2026-1234')).toBe('MAL-2026-1234');
    expect(canonicalAdvisoryId('ghsa-35JH-R3H4-6JHM')).toBe('GHSA-35jh-r3h4-6jhm');
    expect(canonicalAdvisoryId('GHSA-35jh-r3h4-6jhm')).toBe('GHSA-35jh-r3h4-6jhm');
    expect(canonicalAdvisoryId('lodash')).toBe('lodash');
    expect(canonicalAdvisoryId('')).toBe('');
  });
});
