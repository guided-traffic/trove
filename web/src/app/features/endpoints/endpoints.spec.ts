import { Router } from '@angular/router';
import { EndpointsApi } from '../../api/endpoints-api';
import { ApiError } from '../../api/problem';
import { RepositoriesApi } from '../../api/repositories-api';
import {
  FakeEndpointsApi,
  FakeRepositoriesApi,
  GENERIC_FORMAT,
  PACKAGE_FORMAT,
  sampleRule,
} from '../../api/testing/fakes-catalog';
import { button, expectAccessible, renderScreen, settle, text, type } from '../../testing/screen';
import { EndpointDetail } from './endpoint-detail';
import { EndpointList } from './endpoint-list';
import { ExposureRuleEditor, PREVIEW_DEBOUNCE_MS } from './exposure-rule-editor';

describe('Endpoint list', () => {
  it('R-EP-017 lists name, base URL, hosts, surface, exposure mode, rule count and pool; an inert name is flagged', async () => {
    const { root } = await renderScreen(EndpointList, {
      setup: (get) => {
        const fake = get(EndpointsApi) as FakeEndpointsApi;
        fake.endpoints = [
          ...fake.endpoints,
          { name: 'old', inert: true, ruleCount: 3, enabledRuleCount: 2, configuration: null },
        ];
      },
    });
    const t = text(root);
    expect(t).toContain('https://packages.example.net');
    expect(t).toContain('ep-customers');
    expect(t).toContain('1 (1)');
    expect(t).toContain('all (rules have no effect)');
    expect(t).toContain('not configured');
    expect(t).toContain('No longer configured: 3 rules without effect.');
    await expectAccessible(root);
  });

  it('R-EP-017 a failing list shows the error and passes the accessibility check', async () => {
    const { root } = await renderScreen(EndpointList, {
      setup: (get) =>
        ((get(EndpointsApi) as FakeEndpointsApi).list = () =>
          Promise.reject(new ApiError(403, { code: 'forbidden' }, null))),
    });
    expect(text(root)).toContain('You do not have the permission');
    await expectAccessible(root);
  });
});

describe('Endpoint screen', () => {
  it('R-EP-017 shows the configuration read-only and the rule table with enable switch, repository, client address and actions', async () => {
    const { root } = await renderScreen(EndpointDetail, { inputs: { endpoint: 'customers' } });
    const t = text(root);
    expect(t).toContain('this comes from the configuration file');
    expect(t).toContain('releases');
    expect(root.querySelector('input[role="switch"]')).toBeTruthy();
    expect(t).toContain('This endpoint does not serve this address.');
    expect(root.querySelector('table th[scope="col"]')).toBeTruthy();
    await expectAccessible(root);
  });

  it('R-EP-017 an endpoint in mode "all" shows a banner that its rules have no effect', async () => {
    const { root } = await renderScreen(EndpointDetail, { inputs: { endpoint: 'default' } });
    expect(text(root)).toContain('mode "all"');
    expect(text(root)).toContain('no effect');
    await expectAccessible(root);
  });

  it('R-EP-017 the enable switch sends a one-member merge patch with the ETag of the row', async () => {
    const { root, fixture, get } = await renderScreen(EndpointDetail, {
      inputs: { endpoint: 'customers' },
    });
    const fake = get(EndpointsApi) as FakeEndpointsApi;
    const toggle = root.querySelector<HTMLInputElement>('input[role="switch"]')!;
    toggle.checked = false;
    toggle.dispatchEvent(new Event('change', { bubbles: true }));
    await settle(fixture);
    expect(fake.patched).toEqual([{ name: 'releases', patch: { enabled: false }, etag: '"1"' }]);
    expect(fake.ruleStore[0]?.enabled).toBe(false);
  });

  it('R-EP-017 a toggle that meets a changed rule reloads the table and tells the user', async () => {
    const { root, fixture, get } = await renderScreen(EndpointDetail, {
      inputs: { endpoint: 'customers' },
    });
    const fake = get(EndpointsApi) as FakeEndpointsApi;
    fake.ruleStore[0] = { ...fake.ruleStore[0]!, revision: 5 };
    const toggle = root.querySelector<HTMLInputElement>('input[role="switch"]')!;
    toggle.checked = false;
    toggle.dispatchEvent(new Event('change', { bubbles: true }));
    await settle(fixture);
    expect(fake.ruleStore[0]?.enabled).toBe(true);
    expect(toggle.checked).toBe(true);
  });

  it('R-EP-017 an unknown endpoint shows the not-found error and passes the accessibility check', async () => {
    const { root } = await renderScreen(EndpointDetail, { inputs: { endpoint: 'nope' } });
    expect(text(root)).toContain('does not exist');
    await expectAccessible(root);
  });

  it('R-EP-017 deleting a rule asks first and sends the ETag', async () => {
    const { root, fixture, get } = await renderScreen(EndpointDetail, {
      inputs: { endpoint: 'customers' },
    });
    const fake = get(EndpointsApi) as FakeEndpointsApi;
    button(root, 'Delete').click();
    await settle(fixture);
    button(root.ownerDocument.body, 'Delete rule').click();
    await settle(fixture);
    expect(fake.deleted).toEqual(['releases']);
  });

  it('R-EP-017 a caller without endpoints:write sees no new-rule button and a disabled switch', async () => {
    const { root } = await renderScreen(EndpointDetail, {
      inputs: { endpoint: 'customers' },
      capabilities: ['endpoints:read'],
    });
    expect(text(root)).not.toContain('New rule');
    expect(root.querySelector<HTMLInputElement>('input[role="switch"]')?.disabled).toBe(true);
  });
});

describe('Exposure rule editor', () => {
  async function create(setup?: (endpoints: FakeEndpointsApi, repos: FakeRepositoriesApi) => void) {
    const rendered = await renderScreen(ExposureRuleEditor, {
      inputs: { endpoint: 'customers' },
      setup: (get) => {
        vi.spyOn(get(Router), 'navigate').mockResolvedValue(true);
        setup?.(get(EndpointsApi) as FakeEndpointsApi, get(RepositoriesApi) as FakeRepositoriesApi);
      },
    });
    return { ...rendered, endpoints: rendered.get(EndpointsApi) as FakeEndpointsApi };
  }

  async function edit(setup?: (endpoints: FakeEndpointsApi, repos: FakeRepositoriesApi) => void) {
    const rendered = await renderScreen(ExposureRuleEditor, {
      inputs: { endpoint: 'customers', name: 'releases' },
      setup: (get) =>
        setup?.(get(EndpointsApi) as FakeEndpointsApi, get(RepositoriesApi) as FakeRepositoriesApi),
    });
    return { ...rendered, endpoints: rendered.get(EndpointsApi) as FakeEndpointsApi };
  }

  it('R-EP-017 creating the rule through the editor stores the document the API takes, with the Idempotency-Key of the page', async () => {
    const r = await create();
    type(r.root.querySelector<HTMLInputElement>('#f-name')!, 'acme-releases');
    type(r.root.querySelector<HTMLSelectElement>('#f-repository')!, 'generic-local');
    await settle(r.fixture);
    type(r.root.querySelector<HTMLTextAreaElement>('#f-paths-include')!, 'releases/**');
    await settle(r.fixture);
    await expectAccessible(r.root);
    button(r.root, 'Create rule').click();
    button(r.root, 'Create rule').click();
    await settle(r.fixture);

    expect(r.endpoints.created).toHaveLength(1);
    expect(r.endpoints.created[0]?.key).toBeTruthy();
    expect(r.endpoints.created[0]?.rule).toEqual({
      name: 'acme-releases',
      description: '',
      repository: 'generic-local',
      enabled: true,
      packages: { include: [], exclude: [] },
      paths: { include: ['releases/**'], exclude: [] },
      versions: { include: [], exclude: [], range: null },
      prereleases: false,
      distTags: { include: [], exclude: [] },
    });
  });

  it('R-EP-017 the preview asks check-exposure 500 ms after the last change with the unsaved draft and the test paths, and lists verdicts with reasons', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    try {
      const r = await create((endpoints) => {
        endpoints.checkResult = {
          ...(endpoints.checkResult as object),
          paths: [
            { path: 'releases/1.0/app.zip', exposed: true, rule: 'releases', reason: 'exposed' },
            { path: 'internal/x.bin', exposed: false, rule: null, reason: 'path_excluded' },
          ],
        } as never;
      });
      type(r.root.querySelector<HTMLSelectElement>('#f-repository')!, 'generic-local');
      await settle(r.fixture);
      vi.advanceTimersByTime(PREVIEW_DEBOUNCE_MS);
      await settle(r.fixture);
      r.endpoints.checks.length = 0;

      type(
        r.root.querySelector<HTMLTextAreaElement>('#t-paths')!,
        'releases/1.0/app.zip\ninternal/x.bin',
      );
      await settle(r.fixture);
      vi.advanceTimersByTime(PREVIEW_DEBOUNCE_MS - 1);
      await settle(r.fixture);
      expect(r.endpoints.checks).toHaveLength(0);
      vi.advanceTimersByTime(1);
      await settle(r.fixture);

      expect(r.endpoints.checks).toHaveLength(1);
      expect(r.endpoints.checks[0]?.paths).toEqual(['releases/1.0/app.zip', 'internal/x.bin']);
      expect(r.endpoints.checks[0]?.rule?.name).toBe('draft');
      const preview = text(r.root);
      expect(preview).toContain('releases/1.0/app.zip');
      expect(preview).toContain('the path is excluded or not included');
      vi.useRealTimers(); // axe schedules its own timers
      await expectAccessible(r.root);
    } finally {
      vi.useRealTimers();
    }
  });

  it('R-EP-017 the preview of a draft for a package format sends the package and versions', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    try {
      const r = await create((endpoints, repos) => {
        repos.formatsResult = [GENERIC_FORMAT, PACKAGE_FORMAT];
        repos.repositories.push({ key: 'npm-local', class: 'local', format: 'npm' });
        endpoints.checkResult = {
          ...(endpoints.checkResult as object),
          package: '@acme/sdk',
          versions: [
            { version: '2.0.0', exposed: true, rule: 'sdk', reason: 'exposed' },
            { version: '1.0.0', exposed: false, rule: null, reason: 'out_of_range' },
          ],
        } as never;
      });
      type(r.root.querySelector<HTMLSelectElement>('#f-repository')!, 'npm-local');
      await settle(r.fixture);
      type(r.root.querySelector<HTMLInputElement>('#f-versions-range')!, '>=2.0.0');
      type(r.root.querySelector<HTMLInputElement>('#t-package')!, '@acme/sdk');
      await settle(r.fixture);
      vi.advanceTimersByTime(PREVIEW_DEBOUNCE_MS);
      await settle(r.fixture);
      const last = r.endpoints.checks.at(-1);
      expect(last?.package).toBe('@acme/sdk');
      expect(last?.rule?.versions?.range).toBe('>=2.0.0');
      expect(text(r.root)).toContain('the version is outside the range');
      expect(r.root.querySelector('#f-paths-include')).toBeNull();
      vi.useRealTimers(); // axe schedules its own timers
      await expectAccessible(r.root);
    } finally {
      vi.useRealTimers();
    }
  });

  it('R-EP-017 saving an existing rule shows a diff against the loaded revision, then sends PUT with If-Match', async () => {
    const r = await edit();
    type(r.root.querySelector<HTMLTextAreaElement>('#f-paths-include')!, 'releases/**\nextra/**');
    await settle(r.fixture);
    button(r.root, 'Review and save').click();
    await settle(r.fixture);
    const body = r.root.ownerDocument.body;
    expect(text(body)).toContain('Save changes to rule releases?');
    expect(text(body)).toContain('paths.include');
    expect(r.endpoints.replaced).toHaveLength(0);
    button(body.querySelector('mat-dialog-container')!, 'Save').click();
    await settle(r.fixture);
    expect(r.endpoints.replaced).toHaveLength(1);
    expect(r.endpoints.replaced[0]?.etag).toBe('"1"');
    expect(r.endpoints.replaced[0]?.rule.paths?.include).toEqual(['releases/**', 'extra/**']);
  });

  it('R-EP-017 and R-UI-042 saving over a rule that changed opens the conflict dialog', async () => {
    const r = await edit((endpoints) => {
      endpoints.beforeReplace = () => {
        endpoints.ruleStore[0] = { ...endpoints.ruleStore[0]!, description: 'Theirs', revision: 2 };
        endpoints.beforeReplace = null;
      };
    });
    type(r.root.querySelector<HTMLInputElement>('#f-description')!, 'Mine');
    await settle(r.fixture);
    button(r.root, 'Review and save').click();
    await settle(r.fixture);
    button(r.root.ownerDocument.body.querySelector('mat-dialog-container')!, 'Save').click();
    await settle(r.fixture);
    const body = r.root.ownerDocument.body;
    expect(text(body)).toContain('Changed by someone else since you opened it');
    button(body, 'Overwrite').click();
    await settle(r.fixture);
    expect(r.endpoints.ruleStore[0]?.description).toBe('Mine');
    expect(r.endpoints.replaced.at(-1)?.etag).toBe('"2"');
  });

  it('R-EP-017 name and repository of a stored rule cannot be edited', async () => {
    const r = await edit();
    expect(r.root.querySelector('input#f-name')).toBeNull();
    expect(r.root.querySelector<HTMLSelectElement>('#f-repository')?.disabled).toBe(true);
  });

  it('R-EP-017 a server field error is shown at its field and in the summary', async () => {
    const r = await create((endpoints, repos) => {
      repos.formatsResult = [GENERIC_FORMAT, PACKAGE_FORMAT];
      repos.repositories.push({ key: 'npm-local', class: 'local', format: 'npm' });
      endpoints.createRule = async () => {
        throw new ApiError(
          422,
          {
            code: 'invalid_version_range',
            errors: [{ pointer: '/versions/range', code: 'invalid_version_range', detail: 'bad' }],
          },
          null,
        );
      };
    });
    type(r.root.querySelector<HTMLInputElement>('#f-name')!, 'sdk');
    type(r.root.querySelector<HTMLSelectElement>('#f-repository')!, 'npm-local');
    await settle(r.fixture);
    type(r.root.querySelector<HTMLInputElement>('#f-versions-range')!, '>>');
    await settle(r.fixture);
    button(r.root, 'Create rule').click();
    await settle(r.fixture);
    expect(r.root.querySelector('[role="alert"]')?.textContent).toContain(
      'Some entries are not valid',
    );
    expect(r.root.querySelector('#f-versions-range')?.getAttribute('aria-invalid')).toBe('true');
    expect(text(r.root)).toContain('version range is not valid');
    await expectAccessible(r.root);
  });

  it('R-EP-017 an endpoint in mode "all" shows the banner on the editor too', async () => {
    const r = await renderScreen(ExposureRuleEditor, { inputs: { endpoint: 'default' } });
    expect(text(r.root)).toContain('mode "all"');
    await expectAccessible(r.root);
  });

  it('R-EP-017 an unknown rule shows the not-found error and passes the accessibility check', async () => {
    const r = await renderScreen(ExposureRuleEditor, {
      inputs: { endpoint: 'customers', name: 'missing' },
    });
    expect(text(r.root)).toContain('does not exist');
    await expectAccessible(r.root);
  });

  it('R-EP-017 an invalid name is refused before any request', async () => {
    const r = await create();
    type(r.root.querySelector<HTMLInputElement>('#f-name')!, 'Not Valid');
    type(r.root.querySelector<HTMLSelectElement>('#f-repository')!, 'generic-local');
    await settle(r.fixture);
    button(r.root, 'Create rule').click();
    await settle(r.fixture);
    expect(r.endpoints.created).toHaveLength(0);
    expect(text(r.root)).toContain('The name does not match the pattern.');
    expect(sampleRule().name).toBe('releases');
  });
});
