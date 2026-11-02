import { TestBed } from '@angular/core/testing';
import { Router } from '@angular/router';
import { describe, expect, it, vi } from 'vitest';
import { CiApi } from '../../api/ci-api';
import { ApiError } from '../../api/problem';
import { FakeCiApi, sampleIssuance } from '../../api/testing/fakes-ci';
import { helpFor } from '../../core/help-topics';
import { Navigation } from '../../core/navigation';
import { SessionStore } from '../../core/session-store';
import { buttonByText, expectAccessible, openFilters, renderScreen, settle, sleep, text, type, typeInto } from '../../testing/screen';
import { ClaimTester } from './claim-tester';
import { IssuancesPage } from './issuances-page';
import { IssuerCreate, IssuerDetail } from './issuer-detail';
import { IssuerList } from './issuer-list';

type Get = <A>(token: abstract new (...args: never[]) => A) => A;
const ci = (get: Get): FakeCiApi => get(CiApi) as FakeCiApi;
const overlay = (): HTMLElement => document.querySelector('.cdk-overlay-container') as HTMLElement;

describe('The CI issuer list (R-UI-035)', () => {
  it('R-UI-035 lists name, kind, URL, audiences, mapping count and key status, and marks the configuration source', async () => {
    const { root } = await renderScreen(IssuerList);
    const rows = [...root.querySelectorAll('tbody tr')].map((r) => text(r));
    expect(rows).toHaveLength(2);
    const [corp, gitlab] = rows as [string, string];
    expect(gitlab).toContain('gitlab-com');
    expect(gitlab).toContain('gitlab');
    expect(gitlab).toContain('https://gitlab.example.com');
    expect(gitlab).toContain('https://trove.example.comnpm:trove.example.com');
    expect([...root.querySelectorAll('tbody tr')[1]!.querySelectorAll('td')[2]!.querySelectorAll('div')].map((d) => text(d))).toEqual(['https://trove.example.com', 'npm:trove.example.com']);
    expect(text(root.querySelectorAll('tbody tr')[1]!.querySelectorAll('td')[3]!)).toBe('1');
    expect(gitlab).toContain('2 usable');
    expect(gitlab).toContain('api');
    // The issuer of the Helm values says so, and its failed fetch shows with its error, as text and not as colour alone.
    expect(corp).toContain('configuration');
    expect(corp).toContain('Fetch failed');
    expect(corp).toContain('HTTP 502 from git.corp.example.com');
    expect(root.querySelector('a[href="/admin/ci/gitlab-com"]')).toBeTruthy();
    await expectAccessible(root);
  });

  it('R-UI-035 a server whose federation is not built answers 501 and the screen says "not available yet" instead of failing', async () => {
    const { root } = await renderScreen(IssuerList, { setup: (get) => (ci(get).notImplemented = true) });
    expect(text(root)).toContain('Not available yet');
    expect(root.querySelector('[role="alert"]')).toBeNull();
    expect(root.querySelector('table')).toBeNull();
    await expectAccessible(root);
  });

  it('R-UI-035 "New issuer" is offered to the holder of ci:write only, the auditor reads', async () => {
    const admin = await renderScreen(IssuerList);
    expect(admin.root.querySelector('a[href="/admin/ci/new"]')).toBeTruthy();
    const auditor = await renderScreen(IssuerList, { capabilities: ['ci:read'] });
    expect(auditor.root.querySelector('a[href="/admin/ci/new"]')).toBeNull();
    expect(text(auditor.root)).toContain('Read only: changing this needs the permission ci:write');
  });

  it('R-UI-035 the navigation lists CI identity for ci:read and the help opens the CI page', async () => {
    await renderScreen(IssuerList, { capabilities: ['ci:read', 'identities:read'] });
    const identity = TestBed.inject(Navigation)
      .areas()
      .flatMap((a) => a.entries)
      .flatMap((e) => (e.kind === 'group' ? e.items : [e]))
      .map((i) => i.route);
    expect(identity).toContain('/admin/ci');
    TestBed.inject(SessionStore);
    expect(helpFor('/admin/ci')).toEqual(['/help', 'admin', 'gitlab-ci']);
    expect(helpFor('/admin/ci/gitlab-com/mappings/x')).toEqual(['/help', 'admin', 'gitlab-ci']);
    expect(helpFor('/admin/projects/web/ci')).toEqual(['/help', 'admin', 'gitlab-ci']);
    expect(helpFor('/admin/projects/web/ci/mappings/new?issuer=a')).toEqual(['/help', 'admin', 'gitlab-ci']);
    expect(helpFor('/admin/projects/web')).toEqual(['/help', 'admin', 'projects']);
  });
});

describe('A CI issuer (R-UI-035, R-CI-001, R-CI-002, Q-CI-16)', () => {
  it('R-UI-035 opens on the mappings and offers settings, keys, mappings and issuances as tabs', async () => {
    const { root } = await renderScreen(IssuerDetail, { inputs: { issuer: 'gitlab-com' } });
    const labels = [...root.querySelectorAll('[role="tab"]')].map((t) => text(t));
    expect(labels).toEqual(['Settings', 'Keys', 'Mappings', 'Issuances']);
    expect(text(root)).toContain('team-a-release');
    expect(text(root)).toContain('namespace_id in [4711] and ref_protected = true');
    expect(text(root)).toContain('Policy ci-read-internal');
    expect(text(root)).toContain('30 minutes');
    await expectAccessible(root);
  });

  it('Q-CI-16 an issuer of the configuration is read-only and names the configuration as its source', async () => {
    const { root, fixture } = await renderScreen(IssuerDetail, { inputs: { issuer: 'corp-gitlab', tab: 'settings' } });
    await settle(fixture);
    expect(text(root)).toContain('declared in the installation');
    expect(text(root)).toContain('ci.issuers');
    expect(root.querySelector('form')).toBeNull();
    expect(root.querySelector('dl.facts')).toBeTruthy();
    // Not deletable while it is still declared.
    expect(root.textContent).not.toContain('Delete issuer');
  });

  it('R-CI-001 an issuer that left the configuration says it is disabled and can be deleted', async () => {
    const { root, fixture } = await renderScreen(IssuerDetail, {
      inputs: { issuer: 'corp-gitlab', tab: 'settings' },
      setup: (get) => {
        const api = ci(get);
        api.issuers = api.issuers.map((i) => (i.name === 'corp-gitlab' ? { ...i, removedFromConfiguration: true, enabled: false } : i));
      },
    });
    await settle(fixture);
    expect(text(root)).toContain('no longer declared');
    expect(text(root)).toContain('Delete issuer');
  });

  it('R-UI-035 the settings of an API issuer are edited and saved with the loaded ETag', async () => {
    const { root, fixture, get } = await renderScreen(IssuerDetail, { inputs: { issuer: 'gitlab-com', tab: 'settings' } });
    await settle(fixture);
    expect(root.querySelector('form')).toBeTruthy();
    typeInto(root, 'Description', 'Changed');
    buttonByText(root, 'Save settings').click();
    await sleep(fixture, 100);
    expect(ci(get).calls).toContain('replaceIssuer gitlab-com');
    expect(ci(get).ifMatch).toEqual(['"1"']);
    expect(text(root)).not.toContain('Changed by someone else');
  });

  it('R-UI-035 the keys tab shows the key status and "Refresh keys" fetches again', async () => {
    const { root, fixture, get } = await renderScreen(IssuerDetail, {
      inputs: { issuer: 'corp-gitlab', tab: 'keys' },
    });
    await settle(fixture);
    expect(text(root)).toContain('HTTP 502 from git.corp.example.com');
    expect(text(root)).toContain('Ignored');
    expect(text(root)).toContain('key_too_small');
    buttonByText(root, 'Refresh keys').click();
    await sleep(fixture, 100);
    expect(ci(get).calls).toContain('refreshKeys corp-gitlab');
    expect(text(root)).toContain('In use');
    expect(text(root)).not.toContain('HTTP 502 from git.corp.example.com');
  });

  it('R-CI-003 a refresh that cannot reach the issuer says so', async () => {
    const { root, fixture, get } = await renderScreen(IssuerDetail, { inputs: { issuer: 'gitlab-com', tab: 'keys' } });
    await settle(fixture);
    ci(get).discoveryFailures['https://gitlab.example.com'] = 'x';
    buttonByText(root, 'Refresh keys').click();
    await sleep(fixture, 100);
    expect(text(root)).toContain('did not answer correctly');
  });

  it('R-CI-002 deleting an issuer with mappings names them and cascades after the name is typed', async () => {
    const { root, fixture, get } = await renderScreen(IssuerDetail, { inputs: { issuer: 'gitlab-com' } });
    buttonByText(root, 'Delete issuer').click();
    await settle(fixture);
    const ask = overlay().querySelector('trove-confirm-dialog') as HTMLElement;
    expect(text(ask)).toContain('Its 1 mappings and their principals are deleted with it.');
    type(ask.querySelector('input') as HTMLInputElement, 'gitlab-com');
    await settle(fixture);
    buttonByText(ask, 'Delete issuer').click();
    await sleep(fixture, 200);
    expect(ci(get).calls).toContain('deleteIssuer gitlab-com cascade');
  });

  it('R-CI-002 a missing issuer and a 501 show their states', async () => {
    const missing = await renderScreen(IssuerDetail, { inputs: { issuer: 'nope' } });
    expect(text(missing.root)).toContain('does not exist');
    const off = await renderScreen(IssuerDetail, { inputs: { issuer: 'gitlab-com' }, setup: (get) => (ci(get).notImplemented = true) });
    expect(text(off.root)).toContain('Not available yet');
  });
});

describe('Creating a CI issuer (R-UI-035, R-CI-002)', () => {
  it('R-CI-002 a discovery that fails shows the reason of the 422 code next to the URL and nothing is stored', async () => {
    const { root, fixture, get } = await renderScreen(IssuerCreate, {
      setup: (g) => (ci(g).discoveryFailures['https://gitlab.broken.example.com'] = 'discovery_unreachable'),
    });
    typeInto(root, 'Name', 'broken');
    typeInto(root, 'Issuer URL', 'https://gitlab.broken.example.com');
    buttonByText(root, 'Create issuer').click();
    await sleep(fixture, 100);
    expect(text(root)).toContain("discovery document or key set could not be fetched");
    expect(ci(get).issuers.some((i) => i.name === 'broken')).toBe(false);
    await expectAccessible(root);
  });

  it('R-CI-002 the other validation codes have their own sentence', async () => {
    const { root, fixture, get } = await renderScreen(IssuerCreate);
    for (const [code, sentence] of [
      ['issuer_mismatch', 'names a different issuer'],
      ['no_usable_keys', 'publishes no signing key'],
      ['algorithms_not_offered', 'does not offer any of the allowed signing algorithms'],
    ] as const) {
      ci(get).failure = new ApiError(422, { code }, 'r');
      typeInto(root, 'Name', 'x1');
      typeInto(root, 'Issuer URL', 'https://gitlab.example.org');
      buttonByText(root, 'Create issuer').click();
      await sleep(fixture, 60);
      expect(text(root), code).toContain(sentence);
    }
  });

  it('R-CI-001 the name and the URL are checked before the request, and a created issuer opens its keys', async () => {
    const { root, fixture, get } = await renderScreen(IssuerCreate);
    const navigate = vi.spyOn(TestBed.inject(Router), 'navigate').mockResolvedValue(true);
    typeInto(root, 'Name', 'Bad Name');
    typeInto(root, 'Issuer URL', 'http://insecure');
    buttonByText(root, 'Create issuer').click();
    await settle(fixture);
    expect(text(root)).toContain('Use two to 64 characters');
    expect(text(root)).toContain('must be an https URL');
    expect(ci(get).calls).toEqual([]);
    typeInto(root, 'Name', 'new-gitlab');
    typeInto(root, 'Issuer URL', 'https://gitlab.new.example.com');
    buttonByText(root, 'Create issuer').click();
    await sleep(fixture, 100);
    expect(ci(get).calls).toEqual(['createIssuer new-gitlab']);
    expect(navigate).toHaveBeenCalledWith(['/admin/ci', 'new-gitlab'], { queryParams: { tab: 'keys' } });
  });

  it('R-CI-002 a name or URL that exists is refused with the existing-object message', async () => {
    const { root, fixture } = await renderScreen(IssuerCreate);
    typeInto(root, 'Name', 'gitlab-com');
    typeInto(root, 'Issuer URL', 'https://other.example.com');
    buttonByText(root, 'Create issuer').click();
    await sleep(fixture, 100);
    expect(text(root)).toContain('already exists');
  });
});

describe('The issued CI tokens (R-UI-035, R-CI-020)', () => {
  const second = sampleIssuance({
    tokenId: '22222222-2222-4222-8222-222222222222',
    jobId: '555',
    pipelineId: '900',
    projectId: '7',
    projectPath: 'team-b/other',
    mappingNames: ['team-b-read'],
  });

  it('R-CI-020 lists the records and filters by the fields of the API', async () => {
    const { root, fixture, get } = await renderScreen(IssuancesPage, { setup: (g) => (ci(g).issuances = [sampleIssuance(), second]) });
    expect([...root.querySelectorAll('tbody tr')]).toHaveLength(2);
    await openFilters(fixture, root);
    typeInto(root, 'Job ID', '555');
    (root.querySelector('trove-filter-panel button[type="submit"]') as HTMLButtonElement).click();
    await sleep(fixture, 80);
    const rows = [...root.querySelectorAll('tbody tr')];
    expect(rows).toHaveLength(1);
    expect(text(rows[0]!)).toContain('team-b/other');
    expect(ci(get).issuances).toHaveLength(2);
  });

  it('R-CI-020 the detail shows the claims snapshot, the grants, the scope and the usage summary', async () => {
    const { root, fixture } = await renderScreen(IssuancesPage);
    buttonByText(root, 'Details').click();
    await sleep(fixture, 100);
    const dialog = overlay().querySelector('trove-ci-issuance-detail') as HTMLElement;
    expect(text(dialog)).toContain('team-a/app');
    expect(text(dialog)).toContain('read deploy:npm-internal');
    expect(text(dialog)).toContain('"project_path": "team-a/app"');
    expect(text(dialog)).toContain('"repositories"');
    expect(text(dialog)).toContain('12 requests');
    expect(text(dialog)).toContain('@team-a/ui@1.0.0');
    await expectAccessible(dialog);
  });

  it('R-CI-020 the bulk revoke needs an issuer and a narrowing filter, and the confirmation states how many tokens it revokes', async () => {
    const { root, fixture, get } = await renderScreen(IssuancesPage, {
      setup: (g) => (ci(g).issuances = [sampleIssuance(), sampleIssuance({ tokenId: '33333333-3333-4333-8333-333333333333' }), sampleIssuance({ tokenId: '44444444-4444-4444-8444-444444444444', active: false })]),
    });
    expect((buttonByText(root, 'Revoke matching tokens') as HTMLButtonElement).disabled).toBe(true);
    expect(text(root)).toContain('filter by one issuer');
    await openFilters(fixture, root);
    typeInto(root, 'Issuer', 'gitlab-com');
    typeInto(root, 'Job ID', '98765');
    (root.querySelector('trove-filter-panel button[type="submit"]') as HTMLButtonElement).click();
    await sleep(fixture, 80);
    const revoke = buttonByText(root, 'Revoke matching tokens') as HTMLButtonElement;
    expect(revoke.disabled).toBe(false);
    revoke.click();
    await sleep(fixture, 100);
    const ask = overlay().querySelector('trove-confirm-dialog') as HTMLElement;
    // Three records match, two of them are active: the number is the one the request revokes.
    expect(text(ask)).toContain('Revoke 2 tokens?');
    expect(text(ask)).toContain('The 2 active tokens that match');
    buttonByText(ask, 'Revoke 2 tokens').click();
    await sleep(fixture, 150);
    expect(ci(get).calls).toContain('revoke {"issuer":"gitlab-com","jobId":"98765"}');
    expect(ci(get).issuances.filter((i) => i.active)).toHaveLength(0);
  });

  it('R-CI-020 without a matching active token nothing is asked and nothing is revoked', async () => {
    const { root, fixture, get } = await renderScreen(IssuancesPage, { setup: (g) => (ci(g).issuances = [sampleIssuance({ active: false })]) });
    await openFilters(fixture, root);
    typeInto(root, 'Issuer', 'gitlab-com');
    typeInto(root, 'Job ID', '98765');
    (root.querySelector('trove-filter-panel button[type="submit"]') as HTMLButtonElement).click();
    await sleep(fixture, 80);
    buttonByText(root, 'Revoke matching tokens').click();
    await sleep(fixture, 100);
    expect(overlay()?.querySelector('trove-confirm-dialog') ?? null).toBeNull();
    expect(ci(get).calls.filter((c) => c.startsWith('revoke'))).toEqual([]);
  });

  it('R-CI-020 a caller without tokens:revoke sees the list and no revoke control', async () => {
    const { root } = await renderScreen(IssuancesPage, { capabilities: ['ci:read', 'tokens:read'] });
    expect(root.textContent).not.toContain('Revoke matching tokens');
  });

  it('R-UI-035 the screen shows "not available yet" while the backend answers 501', async () => {
    const { root } = await renderScreen(IssuancesPage, { setup: (g) => (ci(g).notImplemented = true) });
    expect(text(root)).toContain('Not available yet');
  });
});

describe('The claim tester route is reachable from the issuer (R-UI-036)', () => {
  it('R-UI-036 a standalone tester for an issuer renders its heading and no result', async () => {
    const { root } = await renderScreen(ClaimTester, { inputs: { issuer: 'gitlab-com' } });
    expect(text(root)).toContain('Claim tester');
    expect(root.querySelector('#ci-t-result')).toBeNull();
  });
});
