import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import { ApiError } from '../../api/problem';
import { SessionApi } from '../../api/session-api';
import { FakeSessionApi, provideFakeApis } from '../../api/testing/fakes';
import { SessionStore } from '../../core/session-store';
import { BrowserStorage } from '../../core/storage';
import { PackageMemory, RECENT_PACKAGES_LIMIT, SAVED_VIEWS_LIMIT } from './package-memory';

async function memory(): Promise<PackageMemory> {
  TestBed.resetTestingModule();
  new BrowserStorage().clear('local');
  TestBed.configureTestingModule({ providers: [provideRouter([]), ...provideFakeApis()] });
  await TestBed.inject(SessionStore).load();
  return TestBed.inject(PackageMemory);
}

describe('PackageMemory', () => {
  it('R-UI-006 keeps the last ten packages opened, newest first, without duplicates', async () => {
    const m = await memory();
    for (let i = 0; i < 12; i++) m.addRecent({ format: 'npm', name: `pkg-${i}` });
    m.addRecent({ format: 'npm', name: 'pkg-5' });
    expect(m.recent()).toHaveLength(RECENT_PACKAGES_LIMIT);
    expect(m.recent()[0]?.name).toBe('pkg-5');
    expect(m.recent().filter((r) => r.name === 'pkg-5')).toHaveLength(1);
  });

  it('R-UI-006 the list is kept per signed-in principal and survives a new session of the service', async () => {
    const m = await memory();
    m.addRecent({ format: 'npm', name: 'left-pad' });
    expect(new BrowserStorage().get('local', 'trove.packages.alice')).toContain('left-pad');
    expect(TestBed.inject(PackageMemory).recent()[0]?.name).toBe('left-pad');
  });

  it('R-UI-006 saves at most eight views; rename, make default and delete work; deleting the default clears it', async () => {
    const m = await memory();
    for (let i = 0; i < SAVED_VIEWS_LIMIT; i++) expect(m.saveView(`view ${i}`, { format: 'npm' })).toBe(true);
    expect(m.saveView('one too many', {})).toBe(false);
    expect(m.saveView('   ', {})).toBe(false);
    const first = m.views()[0]!;
    m.renameView(first.id, ' renamed ');
    expect(m.views()[0]?.name).toBe('renamed');
    m.makeDefault(first.id);
    expect(m.defaultView()?.id).toBe(first.id);
    m.deleteView(first.id);
    expect(m.defaultView()).toBeNull();
    expect(m.views()).toHaveLength(SAVED_VIEWS_LIMIT - 1);
  });

  it('R-UI-006 stored data of a wrong shape is ignored', async () => {
    new BrowserStorage().set('local', 'trove.packages.alice', '{"recent":[1,{"format":"npm"}],"views":"x"}');
    TestBed.resetTestingModule();
    TestBed.configureTestingModule({ providers: [provideRouter([]), ...provideFakeApis()] });
    await TestBed.inject(SessionStore).load();
    const m = TestBed.inject(PackageMemory);
    expect(m.recent()).toEqual([]);
    expect(m.views()).toEqual([]);
  });

  it('R-UI-006 blocked storage hides the feature instead of failing', async () => {
    const spy = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('blocked', 'SecurityError');
    });
    try {
      const m = await memory();
      expect(m.available()).toBe(false);
      m.addRecent({ format: 'npm', name: 'x' });
    } finally {
      spy.mockRestore();
    }
  });

  it('R-UI-006 nobody signed in means no remembered data', async () => {
    TestBed.resetTestingModule();
    TestBed.configureTestingModule({ providers: [provideRouter([]), ...provideFakeApis()] });
    const api = TestBed.inject(SessionApi) as FakeSessionApi;
    api.whoamiResult = new ApiError(401, { code: 'unauthenticated' }, null);
    await TestBed.inject(SessionStore).load();
    expect(TestBed.inject(SessionStore).state()).toBe('anonymous');
    const m = TestBed.inject(PackageMemory);
    expect(m.available()).toBe(false);
  });
});
