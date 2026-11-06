import { TestBed } from '@angular/core/testing';
import { Router, UrlTree, provideRouter } from '@angular/router';
import { FakeSessionApi, provideFakeApis } from '../api/testing/fakes';
import { SessionApi } from '../api/session-api';
import { adminHomeGuard } from './auth-guard';

async function startOf(capabilities: string[]): Promise<string> {
  TestBed.resetTestingModule();
  TestBed.configureTestingModule({ providers: [provideRouter([]), ...provideFakeApis()] });
  (TestBed.inject(SessionApi) as FakeSessionApi).meResult = { capabilities };
  const tree = (await TestBed.runInInjectionContext(() => adminHomeGuard({} as never, {} as never))) as UrlTree;
  return TestBed.inject(Router).serializeUrl(tree);
}

describe('The start of the admin area (R-UI-002, Q-OPS-175, Q-UI-130)', () => {
  it('Q-OPS-175 starts at the dashboard for a holder of settings:read', async () => {
    expect(await startOf(['settings:read', 'content:metadata-read'])).toBe('/admin/dashboard');
  });

  it('Q-UI-130 starts an auditor or a security administrator without the configuration capabilities at the security dashboard', async () => {
    expect(await startOf(['content:metadata-read'])).toBe('/admin/security');
    expect(await startOf(['vulnerabilities:triage'])).toBe('/admin/security');
  });

  it('R-UI-041 keeps the projects as the start of a caller who reads projects but not repositories', async () => {
    expect(await startOf(['projects:read', 'content:metadata-read'])).toBe('/admin/projects');
  });

  it('R-UI-002 keeps the repositories as the start of everyone else', async () => {
    expect(await startOf(['repositories:read-config', 'content:metadata-read'])).toBe('/admin/repositories');
    expect(await startOf(['audit:read'])).toBe('/admin/repositories');
  });
});
