import { DestroyRef } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { vi } from 'vitest';
import { ApiError } from '../api/problem';
import { SystemApi } from '../api/system-api';
import { FakeSystemApi, provideFakeApis } from '../api/testing/fakes';
import { BANNER_POLL_MS, BannerStore } from './banner-store';
import { SessionStore } from './session-store';

function setup() {
  TestBed.configureTestingModule({ providers: [...provideFakeApis()] });
  return { store: TestBed.inject(BannerStore), api: TestBed.inject(SystemApi) as FakeSystemApi, session: TestBed.inject(SessionStore) };
}

/** A DestroyRef a spec can end by hand. */
function destroyRef() {
  const callbacks: (() => void)[] = [];
  return {
    ref: { onDestroy: (fn: () => void) => callbacks.push(fn) } as unknown as DestroyRef,
    destroy: () => callbacks.forEach((fn) => fn()),
  };
}

describe('BannerStore', () => {
  afterEach(() => vi.useRealTimers());

  it('R-UI-033 shows no notice while no banner is stored and the mode is normal', async () => {
    const { store } = setup();
    await store.refresh();
    expect(store.notices()).toEqual([]);
  });

  it('R-UI-033 an enabled banner is a notice with its severity, title and message; a disabled one or one without text is none', async () => {
    const { store, api } = setup();
    api.bannerResult = { enabled: true, severity: 'critical', title: 'Outage', message: 'Pushes fail.' };
    await store.refresh();
    expect(store.notices()).toEqual([{ kind: 'banner', severity: 'critical', title: 'Outage', message: 'Pushes fail.' }]);
    api.bannerResult = { enabled: false, severity: 'info', message: 'kept text' };
    await store.refresh();
    expect(store.notices()).toEqual([]);
    api.bannerResult = { enabled: true, message: '   ' };
    await store.refresh();
    expect(store.notices()).toEqual([]);
  });

  it('R-UI-033 R-OPS-024 the read-only mode is a warning notice before the banner, for a signed-in caller only', async () => {
    const { store, api, session } = setup();
    api.bannerResult = { enabled: true, message: 'Planned work' };
    api.maintenanceResult = { mode: 'read_only', cacheFill: true, message: 'Storage migration until 14:00 UTC', retryAfterSeconds: 300 };
    await session.load();
    await store.refresh();
    expect(store.notices().map((n) => [n.kind, n.severity, n.message])).toEqual([
      ['maintenance', 'warning', 'Storage migration until 14:00 UTC'],
      ['banner', 'info', 'Planned work'],
    ]);
    expect(api.maintenanceReads).toBe(1);
  });

  it('R-UI-033 an anonymous caller reads the banner and never asks for the mode', async () => {
    const { store, api } = setup();
    api.bannerResult = { enabled: true, message: 'Planned work' };
    await store.refresh();
    expect(store.notices().map((n) => n.kind)).toEqual(['banner']);
    expect(api.maintenanceReads).toBe(0);
  });

  it('R-UI-033 a failed read keeps what was shown', async () => {
    const { store, api } = setup();
    api.bannerResult = { enabled: true, message: 'Planned work' };
    await store.refresh();
    api.bannerResult = new ApiError(503, { code: 'unavailable' }, null);
    await store.refresh();
    expect(store.notices().map((n) => n.message)).toEqual(['Planned work']);
  });

  it('R-UI-033 a saved banner shows within a minute: a watcher reads again every poll interval and stops with the last screen', async () => {
    vi.useFakeTimers();
    const { store, api } = setup();
    const first = destroyRef();
    const second = destroyRef();
    store.watch(first.ref);
    store.watch(second.ref);
    await vi.advanceTimersByTimeAsync(0);
    expect(api.bannerReads).toBe(2);
    expect(BANNER_POLL_MS).toBeLessThanOrEqual(60_000);

    api.bannerResult = { enabled: true, message: 'Saved by another administrator' };
    await vi.advanceTimersByTimeAsync(BANNER_POLL_MS);
    expect(store.notices().map((n) => n.message)).toEqual(['Saved by another administrator']);

    first.destroy();
    await vi.advanceTimersByTimeAsync(BANNER_POLL_MS);
    const whileOneWatcher = api.bannerReads;
    expect(whileOneWatcher).toBeGreaterThan(3);
    second.destroy();
    await vi.advanceTimersByTimeAsync(3 * BANNER_POLL_MS);
    expect(api.bannerReads).toBe(whileOneWatcher);
  });

  it('R-UI-033 adopt shows what a save answered without waiting for the poll', () => {
    const { store } = setup();
    store.adopt({ enabled: true, message: 'Now' }, { mode: 'read_only', cacheFill: false, message: 'Frozen' });
    expect(store.notices().map((n) => n.kind)).toEqual(['maintenance', 'banner']);
    store.adopt(null, { mode: 'normal', cacheFill: true });
    expect(store.notices().map((n) => n.kind)).toEqual(['banner']);
  });
});
