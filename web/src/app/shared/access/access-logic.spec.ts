import { TestBed } from '@angular/core/testing';
import { DestroyRef } from '@angular/core';
import { provideRouter, Router } from '@angular/router';
import { ApiError } from '../../api/problem';
import { arrayPages } from '../../api/testing/access-fakes';
import { cleanQuery } from '../../api/query';
import { formatDuration, fromDays, toDays } from './duration';
import { DraftStore } from './drafts';
import { PagedSignal } from './paged-signal';
import { browserStorage } from './testing';
import { expiresSoon, scopeSummary } from './token-table';

describe('PagedSignal', () => {
  it('R-API-010 accumulates pages and stops at the last cursor', async () => {
    const all = Array.from({ length: 5 }, (_, i) => i);
    const list = new PagedSignal<number>(2);
    await list.start(arrayPages(() => all));
    expect(list.items()).toEqual([0, 1]);
    expect(list.hasMore()).toBe(true);
    await list.more();
    await list.more();
    expect(list.items()).toEqual([0, 1, 2, 3, 4]);
    expect(list.hasMore()).toBe(false);
  });

  it('R-API-010 a new traversal replaces the old one and drops its late answer', async () => {
    const list = new PagedSignal<string>(10);
    const held: { release?: () => void } = {};
    const slow = (): Promise<{ items: string[]; nextCursor: null }> =>
      new Promise((resolve) => {
        held.release = () => resolve({ items: ['old'], nextCursor: null });
      });
    const first = list.start(slow);
    await list.start(async () => ({ items: ['new'], nextCursor: null }));
    held.release?.();
    await first;
    expect(list.items()).toEqual(['new']);
  });

  it('R-UI-042 a failing page leaves the items and exposes the error', async () => {
    const list = new PagedSignal<number>(10);
    await list.start(async () => {
      throw new ApiError(503, { code: 'unavailable' }, 'r1');
    });
    expect(list.error()?.status).toBe(503);
    expect(list.loaded()).toBe(false);
  });
});

describe('query and duration helpers', () => {
  it('R-API-011 cleanQuery drops empty filters', () => {
    expect(cleanQuery({ q: '', status: 'active', admin: false, group: undefined })).toEqual({
      status: 'active',
      admin: false,
    });
  });

  it('R-UI-045 formats durations in the largest even unit with Intl', () => {
    expect(formatDuration(7_776_000, 'en')).toBe('90 days');
    expect(formatDuration(3600, 'en')).toBe('1 hour');
    expect(formatDuration(7_776_000, 'de')).toBe('90 Tage');
    expect(formatDuration(90, 'en')).toBe('90 seconds');
  });

  it('R-UI-031 converts between a day field and seconds', () => {
    expect(toDays(7_776_000)).toBe('90');
    expect(fromDays('0.5')).toBe(43_200);
    expect(fromDays('')).toBeNaN();
    expect(fromDays('x')).toBeNaN();
  });
});

describe('token table helpers', () => {
  const now = Date.parse('2026-10-29T10:00:00Z');

  it('R-UI-029 marks tokens that expire within seven days', () => {
    expect(expiresSoon({ state: 'active', expiresAt: '2026-11-02T10:00:00Z' }, now)).toBe(true);
    expect(expiresSoon({ state: 'active', expiresAt: '2026-12-02T10:00:00Z' }, now)).toBe(false);
    expect(expiresSoon({ state: 'active', expiresAt: null }, now)).toBe(false);
    expect(expiresSoon({ state: 'revoked', expiresAt: '2026-11-02T10:00:00Z' }, now)).toBe(false);
    expect(expiresSoon({ state: 'active', expiresAt: '2026-10-01T10:00:00Z' }, now)).toBe(false);
  });

  it('R-UI-029 summarizes a scope in one line', () => {
    expect(scopeSummary({ inherit: true })).toContain('All permissions');
    expect(scopeSummary({ repositories: [{ repositories: ['npm-*'], actions: ['read'] }] })).toContain('1');
    expect(scopeSummary({ inherit: false, system: ['audit:read'], admin: true })).toContain('audit:read');
  });
});

describe('DraftStore', () => {
  beforeEach(() => {
    browserStorage.clear('session');
    TestBed.configureTestingModule({ providers: [provideRouter([{ path: '**', children: [] }])] });
  });

  it('R-UI-042 keeps a draft per route and hands it back once', async () => {
    const drafts = TestBed.inject(DraftStore);
    await TestBed.inject(Router).navigateByUrl('/admin/permissions/new?x=1');
    expect(drafts.keep({ description: 'draft' })).toBe(true);
    expect(drafts.take<{ description: string }>()).toEqual({ description: 'draft' });
    expect(drafts.take()).toBeNull();
  });

  it('R-UI-004 offers the unsaved drafts of open editors as JSON until they close', () => {
    const drafts = TestBed.inject(DraftStore);
    let destroy: (() => void) | undefined;
    const ref = { onDestroy: (fn: () => void) => ((destroy = fn), () => undefined) } as unknown as DestroyRef;
    let current: unknown = null;
    drafts.track(() => current, ref);
    expect(drafts.snapshot()).toBeNull();
    current = { name: 'team-a' };
    expect(JSON.parse(drafts.snapshot() ?? 'null')).toEqual({ name: 'team-a' });
    destroy?.();
    expect(drafts.snapshot()).toBeNull();
  });
});
