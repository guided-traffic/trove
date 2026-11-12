import { TestBed } from '@angular/core/testing';
import { Router } from '@angular/router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '../api/problem';
import { SessionApi } from '../api/session-api';
import { FakeSessionApi } from '../api/testing/fakes';
import { renderScreen } from '../testing/screen';
import { Component } from '@angular/core';
import { FREQUENT_LIMIT, FrequentPlaces, VISIT_WINDOW_MS, byFrequency } from './frequent-places';
import { SessionStore } from './session-store';
import { BrowserStorage } from './storage';

@Component({ template: '' })
class Empty {}

const DAY = 24 * 60 * 60 * 1000;

async function places(principal = '00000000-0000-0000-0000-000000000001', name = 'alice') {
  new BrowserStorage().clear('local');
  await renderScreen(Empty, {
    setup: (get) => {
      (get(SessionApi) as FakeSessionApi).whoamiResult = { principal: { kind: 'user', name, id: principal } } as never;
    },
  });
  return TestBed.inject(FrequentPlaces);
}

afterEach(() => vi.useRealTimers());

describe('Frequently visited places (R-UI-096, Q-UI-405)', () => {
  it('R-UI-096 the store is one key per principal ID; a second user of the browser sees none of the first one\'s entries', async () => {
    const first = await places('id-sam', 'sam');
    first.visitRepository('web-frontend', 'npm-vendor');
    first.visitProject('web-frontend');
    expect(new BrowserStorage().get('local', 'trove.frequent.id-sam')).toContain('web-frontend/npm-vendor');
    const second = await places('id-bob', 'bob');
    new BrowserStorage().set('local', 'trove.frequent.id-sam', JSON.stringify({ repositories: [{ id: 'web-frontend/npm-vendor', visits: 3, last: Date.now() }], projects: [] }));
    expect(second.frequentRepositories()).toEqual([]);
  });

  it('R-UI-096 the order is the visits, halved for every 14 days since the last one, then the last visit', () => {
    const now = 100 * DAY;
    const order = byFrequency(
      [
        { id: 'platform/collections', visits: 1, last: now - 1000 },
        { id: 'web-frontend/npm-vendor', visits: 3, last: now - 2000 },
        { id: 'web/old', visits: 3, last: now - 28 * DAY },
        { id: 'web/tie', visits: 1, last: now - 500 },
      ],
      now,
    ).map((v) => v.id);
    // 3 visits halved twice (0.75) fall behind one recent visit; of two single visits the later one comes first.
    expect(order).toEqual(['web-frontend/npm-vendor', 'web/tie', 'platform/collections', 'web/old']);
  });

  it('R-UI-096 a visit counts once per entry in 15 minutes', async () => {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date('2026-11-12T10:00:00Z'));
    const store = await places();
    store.visitRepository('web', 'npm-local');
    vi.setSystemTime(Date.now() + VISIT_WINDOW_MS - 1000);
    store.visitRepository('web', 'npm-local');
    expect(store.frequentRepositories()[0]).toMatchObject({ id: 'web/npm-local', visits: 1 });
    vi.setSystemTime(Date.now() + 2000);
    store.visitRepository('web', 'npm-local');
    expect(store.frequentRepositories()[0]).toMatchObject({ id: 'web/npm-local', visits: 2 });
  });

  it('R-UI-096 at most 20 per kind are kept; the one just visited always stays', async () => {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date('2026-11-12T10:00:00Z'));
    const store = await places();
    for (let i = 0; i < FREQUENT_LIMIT + 5; i++) {
      store.visitRepository('web', `repo-${i}`);
      vi.setSystemTime(Date.now() + 1000);
    }
    expect(store.frequentRepositories()).toHaveLength(FREQUENT_LIMIT);
    expect(store.recentRepositories()[0]!.id).toBe(`web/repo-${FREQUENT_LIMIT + 4}`);
  });

  it('R-UI-096 a route under /admin is never recorded', async () => {
    const store = await places();
    await TestBed.inject(Router).navigateByUrl('/admin/repositories');
    store.visitRepository('web', 'npm-local');
    store.visitProject('web');
    expect(store.frequentRepositories()).toEqual([]);
    expect(store.frequentProjects()).toEqual([]);
  });

  it('R-UI-096 with storage blocked nothing is kept and the store says it is not available', async () => {
    const store = await places();
    const storage = TestBed.inject(BrowserStorage);
    vi.spyOn(storage, 'available').mockReturnValue(false);
    vi.spyOn(storage, 'set').mockReturnValue(false);
    vi.spyOn(storage, 'get').mockReturnValue(null);
    store.visitRepository('web', 'npm-local');
    expect(TestBed.inject(FrequentPlaces).available()).toBe(false);
    expect(store.frequentRepositories()).toEqual([]);
  });

  it('R-UI-096 an anonymous caller keeps nothing', async () => {
    new BrowserStorage().clear('local');
    await renderScreen(Empty, {
      setup: (get) => {
        (get(SessionApi) as FakeSessionApi).whoamiResult = new ApiError(401, null, null);
      },
    });
    expect(TestBed.inject(SessionStore).state()).toBe('anonymous');
    const store = TestBed.inject(FrequentPlaces);
    store.visitRepository('web', 'npm-local');
    expect(store.available()).toBe(false);
    expect(new BrowserStorage().entries('local').filter(([k]) => k.startsWith('trove.frequent.'))).toEqual([]);
  });
});
