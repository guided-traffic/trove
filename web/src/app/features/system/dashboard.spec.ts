import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import { ApiError } from '../../api/problem';
import { FakeSystemApi, provideFakeApis, sampleReplica, sampleRuntime } from '../../api/testing/fakes';
import { SystemApi } from '../../api/system-api';
import { SessionStore } from '../../core/session-store';
import { seriousViolations } from '../../testing/axe';
import { Dashboard } from './dashboard';

async function render(setup?: (system: FakeSystemApi) => void) {
  await TestBed.configureTestingModule({
    imports: [Dashboard],
    providers: [provideRouter([]), ...provideFakeApis()],
  }).compileComponents();
  setup?.(TestBed.inject(SystemApi) as FakeSystemApi);
  await TestBed.inject(SessionStore).load();
  const fixture = TestBed.createComponent(Dashboard);
  for (let i = 0; i < 4; i++) await fixture.whenStable();
  return { fixture, root: fixture.nativeElement as HTMLElement };
}

const text = (root: HTMLElement): string => root.textContent?.replace(/\s+/g, ' ') ?? '';

describe('Admin dashboard (Q-OPS-175)', () => {
  it('R-UI-038 shows the instance, each role with its load, the dependencies with their checks, and the counts', async () => {
    const { root } = await render((s) => {
      s.replicasResult = [
        sampleReplica({
          runtime: sampleRuntime({ requests: 200, windowSeconds: 10, latencyMs: { p50: 4, p95: 33, p99: 50 } }),
          checks: { database: { status: 'pass', durationMs: 2.4 }, valkey: { status: 'pass', durationMs: 0.8 }, objectStore: { status: 'pass', durationMs: 7 } },
        }),
      ];
    });
    const t = text(root);
    expect(root.querySelector('h1')?.textContent).toContain('Dashboard');
    expect(t).toContain('All roles ready');
    expect(t).toContain('20 per second');
    expect(t).toContain('33 ms');
    expect(t).toContain('PostgreSQL');
    expect(t).toContain('Connected');
    expect(t).toContain('2.4 ms');
    expect(t).toContain('8.5 GiB');
    expect(t).toContain('Repositories');
    expect(root.querySelector('a[href="/admin/repositories/new"]')).toBeTruthy();
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-038 a failing overview leaves the rest of the screen readable and offers a retry', async () => {
    const { root } = await render((s) => (s.overviewResult = new ApiError(503, { code: 'database_unavailable' }, null)));
    const t = text(root);
    expect(t).toContain('All roles ready');
    expect(t).toContain('PostgreSQL');
    expect(root.querySelectorAll('trove-error-notice').length).toBe(1);
    expect(t).not.toContain('Active tokens');
  });

  it('R-UI-038 a role without a replica says so instead of showing zeros', async () => {
    const { root } = await render((s) => (s.replicasResult = [sampleReplica()]));
    expect(text(root)).toContain('No replica of this role is running.');
  });

  it('R-UI-038 next to a live replica of the development role all, the roles it covers are not shown as missing', async () => {
    const { root } = await render((s) => {
      s.replicasResult = [sampleReplica({ roles: ['all'] })];
      s.infoResult = { ...s.infoResult, replicas: { live: 1, ready: 1, versions: { dev: 1 }, roles: { all: { live: 1, ready: 1, degraded: 0, nodes: 0 } } } };
    });
    expect(text(root)).not.toContain('No replica of this role is running.');
    expect([...root.querySelectorAll('.card-head h3')].map((h) => h.textContent?.trim())).toContain('all');
  });

  it('R-UI-038 WCAG 2.2.2: the automatic refresh can be paused', async () => {
    const { fixture, root } = await render();
    const pause = [...root.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.includes('Pause'))!;
    expect(pause.getAttribute('aria-pressed')).toBe('false');
    pause.click();
    await fixture.whenStable();
    expect(pause.getAttribute('aria-pressed')).toBe('true');
    expect(pause.textContent).toContain('Resume automatic refresh');
  });
});
