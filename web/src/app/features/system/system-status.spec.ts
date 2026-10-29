import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import { ApiError } from '../../api/problem';
import { SessionApi } from '../../api/session-api';
import {
  FakeEncryptionApi,
  FakeSessionApi,
  FakeSystemApi,
  provideFakeApis,
  sampleInfo,
  sampleReplica,
} from '../../api/testing/fakes';
import { EncryptionApi, SystemApi } from '../../api/system-api';
import { SessionStore } from '../../core/session-store';
import { SystemStatus } from './system-status';

async function render(setup?: (system: FakeSystemApi, enc: FakeEncryptionApi, session: FakeSessionApi) => void) {
  await TestBed.configureTestingModule({
    imports: [SystemStatus],
    providers: [provideRouter([]), ...provideFakeApis()],
  }).compileComponents();
  setup?.(
    TestBed.inject(SystemApi) as FakeSystemApi,
    TestBed.inject(EncryptionApi) as FakeEncryptionApi,
    TestBed.inject(SessionApi) as FakeSessionApi,
  );
  await TestBed.inject(SessionStore).load();
  const fixture = TestBed.createComponent(SystemStatus);
  for (let i = 0; i < 4; i++) await fixture.whenStable();
  return { fixture, root: fixture.nativeElement as HTMLElement };
}

const text = (root: HTMLElement): string => root.textContent?.replace(/\s+/g, ' ') ?? '';

describe('System status screen', () => {
  it('R-UI-038 shows the summary, the installation facts and the replicas per role', async () => {
    const { root } = await render();
    const t = text(root);
    expect(t).toContain('All ready');
    expect(t).toContain('0.1.1');
    expect(t).toContain('https://trove.example.net');
    expect(t).toContain('trove-web-0');
    expect(t).toContain('No live worker replica');
    expect(root.querySelectorAll('table th[scope="row"]').length).toBeGreaterThan(1);
  });

  it('R-UI-038 a degraded Valkey check is a warning that names what it means, not an error', async () => {
    const { root } = await render((system) => {
      system.replicasResult = [
        sampleReplica({ degraded: ['valkey'], valkey: { ...sampleReplica().valkey!, state: 'degraded', reason: 'timeout' } }),
        sampleReplica({ name: 'b', instanceUid: 'b', node: 'node-b', degraded: ['valkey'], valkey: { ...sampleReplica().valkey!, state: 'degraded', reason: 'timeout' } }),
      ];
      system.infoResult = sampleInfo({ valkey: { ...sampleInfo().valkey, degradedReplicas: 2 } });
    });
    const t = text(root);
    expect(t).toContain('Degraded');
    expect(t).not.toContain('Replicas unready');
    expect(t).toContain('Degraded on 2 of 2 replicas');
    expect(t).toContain('change-log poll');
  });

  it('R-UI-038 two replicas on one node make the role not drain-safe', async () => {
    const { root } = await render((system) => {
      system.replicasResult = [sampleReplica({ node: 'n1' }), sampleReplica({ name: 'b', instanceUid: 'b', node: 'n1' })];
    });
    expect(text(root)).toContain('All ready replicas on one node');
    expect(text(root)).toContain('Not drain-safe');
  });

  it('R-UI-038 the key card marks a key with sealed values as re-encryption pending and offers Re-encrypt now', async () => {
    const { root, fixture } = await render((_s, _e, session) => {
      session.meResult = { capabilities: ['content:bypass', 'settings:read'] };
    });
    expect(text(root)).toContain('re-encryption pending');
    const button = [...root.querySelectorAll('button')].find((b) => b.textContent?.includes('Re-encrypt now'));
    expect(button).toBeTruthy();
    button?.click();
    await fixture.whenStable();
    expect((TestBed.inject(EncryptionApi) as FakeEncryptionApi).reencryptKeys.length).toBe(1);
  });

  it('R-UI-038 Re-encrypt now is disabled with its reason while the rings are incompatible', async () => {
    const { root } = await render((_s, enc) => {
      enc.statusResult = { ...(enc.statusResult as object), ringsCompatible: false } as never;
    });
    const button = [...root.querySelectorAll('button')].find((b) => b.textContent?.includes('Re-encrypt now'));
    expect(button?.disabled).toBe(true);
    expect(text(root)).toContain('Disabled while the key rings are not compatible');
  });

  it('R-UI-038 a user without the admin role does not get Re-encrypt now', async () => {
    const { root } = await render((_s, _e, session) => {
      session.meResult = { capabilities: ['settings:read'] };
    });
    expect([...root.querySelectorAll('button')].some((b) => b.textContent?.includes('Re-encrypt now'))).toBe(false);
  });

  it('R-UI-038 a failing key-ring endpoint shows its own error and leaves the rest of the screen alone', async () => {
    const { root } = await render((_s, enc) => {
      enc.statusResult = new ApiError(404, { code: 'key_ring_unreadable', detail: 'no such endpoint', requestId: 'rid-9' }, 'rid-9');
    });
    expect(text(root)).toContain('All ready');
    expect(text(root)).toContain('rid-9');
  });

  it('R-UI-038 the pause control stops the automatic refresh (WCAG 2.2.2)', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
    const { root, fixture } = await render();
    const system = TestBed.inject(SystemApi) as FakeSystemApi;
    const spy = vi.spyOn(system, 'info');
    vi.advanceTimersByTime(15_000);
    await fixture.whenStable();
    expect(spy).toHaveBeenCalledTimes(1);

    const pause = root.querySelector<HTMLButtonElement>('header button[aria-pressed]');
    pause?.click();
    await fixture.whenStable();
    expect(pause?.getAttribute('aria-pressed')).toBe('true');
    vi.advanceTimersByTime(60_000);
    await fixture.whenStable();
    expect(spy).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });
});
