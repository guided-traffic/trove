import { TestBed } from '@angular/core/testing';
import { provideRouter, withComponentInputBinding } from '@angular/router';
import { RouterTestingHarness } from '@angular/router/testing';
import { beforeEach, describe, expect, it } from 'vitest';
import { SessionApi } from '../../api/session-api';
import { FakeSessionApi, provideFakeApis } from '../../api/testing/fakes';
import { SessionStore } from '../../core/session-store';
import { text } from '../../testing/screen';
import { CI_ROUTES, PROJECT_CI_ROUTES } from './ci.routes';

/**
 * The routes bind path and query parameters to the inputs of the screens (`withComponentInputBinding`).
 * A screen tested with inputs set by hand cannot catch a parameter that is named differently in the
 * route, so every route of the CI area is navigated here as the router would.
 */
async function harness(routes: typeof CI_ROUTES, capabilities: string[] = ['ci:read', 'ci:write', 'tokens:read', 'tokens:revoke', 'policies:read', 'identities:read']) {
  TestBed.resetTestingModule();
  TestBed.configureTestingModule({
    providers: [
      provideRouter(routes, withComponentInputBinding()),
      ...provideFakeApis(),
    ],
  });
  (TestBed.inject(SessionApi) as FakeSessionApi).meResult = {
    roles: [],
    capabilities,
    principals: ['user:alice'],
    projects: [{ key: 'web', managePolicies: true, manageRepositories: false }],
  };
  await TestBed.inject(SessionStore).load();
  return RouterTestingHarness.create();
}

async function open(h: RouterTestingHarness, url: string): Promise<HTMLElement> {
  await h.navigateByUrl(url);
  for (let i = 0; i < 6; i++) {
    await h.fixture.whenStable();
    h.detectChanges();
  }
  return h.routeNativeElement as HTMLElement;
}

describe('The routes of the CI area (R-UI-035, R-UI-036, Q-CI-26)', () => {
  let h: RouterTestingHarness;

  describe('of the administrators', () => {
    beforeEach(async () => {
      h = await harness(CI_ROUTES);
    });

    it('R-UI-035 the list, the new issuer, the issuances and the issuer open', async () => {
      expect(text(await open(h, '/'))).toContain('A CI issuer is a GitLab instance');
      expect(text(await open(h, '/new'))).toContain('New CI issuer');
      expect(text(await open(h, '/issuances'))).toContain('Every token a job received');
      const detail = await open(h, '/gitlab-com');
      expect(text(detail)).toContain('team-a-release');
      expect(detail.querySelector('.subject')!.textContent).toContain('gitlab-com');
    });

    it('R-UI-035 the tab of the issuer comes from the query', async () => {
      expect(text(await open(h, '/gitlab-com?tab=keys'))).toContain('Refresh keys');
      expect(text(await open(h, '/gitlab-com?tab=settings'))).toContain('Save settings');
    });

    it('R-UI-036 the claim tester takes the issuer from the path and the mapping from the query', async () => {
      const tester = await open(h, '/gitlab-com/test?mapping=team-a-release');
      expect(tester.querySelector('.subject')!.textContent).toContain('gitlab-com');
      expect((tester.querySelector('#ci-t-claims') as HTMLTextAreaElement).value).toContain('"namespace_id": "4711"');
    });

    it('R-UI-035 the mapping editor takes the issuer and the mapping from the path', async () => {
      const editor = await open(h, '/gitlab-com/mappings/team-a-release');
      expect(editor.querySelector('.subject')!.textContent).toContain('gitlab-com/team-a-release');
      const created = await open(h, '/gitlab-com/mappings/new');
      expect(text(created)).toContain('New mapping');
      expect(created.querySelector<HTMLInputElement>('input[name="priority"]')!.value).toBe('110');
    });
  });

  describe('under a project', () => {
    beforeEach(async () => {
      h = await harness(PROJECT_CI_ROUTES, []);
    });

    it('Q-CI-26 the overview, the dry run and the editor take the project from the path and the issuer from the query', async () => {
      expect(text(await open(h, '/web/ci'))).toContain('Issuers you can use');
      const tester = await open(h, '/web/ci/test?issuer=gitlab-com');
      expect(tester.querySelector('.subject')!.textContent).toContain('web');
      const created = await open(h, '/web/ci/mappings/new?issuer=gitlab-com');
      expect(created.querySelector<HTMLInputElement>('input[name="name"]')!.value).toBe('web-');
      expect(created.querySelector('.subject')!.textContent).toContain('web');
      expect(text(created)).not.toContain('Add a group');
    });

    it('Q-CI-26 an existing project mapping opens by its name, with the issuer in the query', async () => {
      const editor = await open(h, '/web/ci/mappings/web-x?issuer=gitlab-com');
      // The mapping does not exist in the fake: the load error shows, which proves the parameters were bound.
      expect(text(editor)).toContain('does not exist');
    });
  });
});
