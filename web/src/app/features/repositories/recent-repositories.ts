import { Injectable, inject, signal } from '@angular/core';
import { BrowserStorage } from '../../core/storage';

const STORAGE_KEY = 'trove.recent-repositories';

/** "Recently viewed" holds this many repository keys (R-UI-020). */
export const RECENT_LIMIT = 20;

/**
 * The keys of the repositories the user opened last, newest first, in browser storage. Non-secret
 * convenience data (R-UI-043): when storage is blocked the list is empty and the feature disappears.
 */
@Injectable({ providedIn: 'root' })
export class RecentRepositories {
  private readonly storage = inject(BrowserStorage);
  private readonly _keys = signal<readonly string[]>(this.read());

  readonly keys = this._keys.asReadonly();

  add(key: string): void {
    const next = [key, ...this._keys().filter((k) => k !== key)].slice(0, RECENT_LIMIT);
    this._keys.set(next);
    this.storage.set('local', STORAGE_KEY, JSON.stringify(next));
  }

  private read(): readonly string[] {
    try {
      const parsed: unknown = JSON.parse(this.storage.get('local', STORAGE_KEY) ?? '[]');
      return Array.isArray(parsed)
        ? parsed.filter((k): k is string => typeof k === 'string').slice(0, RECENT_LIMIT)
        : [];
    } catch {
      return [];
    }
  }
}
