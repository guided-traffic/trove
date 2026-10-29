import { Injectable } from '@angular/core';

export type StorageArea = 'local' | 'session';

/**
 * The only place that touches `localStorage` and `sessionStorage` (R-UI-046, lint-enforced).
 * Access is wrapped so that blocked storage (private mode, policy, quota) only makes the
 * features that need it disappear: every method answers as if the key were absent. Secrets never
 * go through here (R-UI-043).
 */
@Injectable({ providedIn: 'root' })
export class BrowserStorage {
  /** False when the area cannot be read or written; callers hide the feature then. */
  available(area: StorageArea): boolean {
    try {
      const s = this.area(area);
      const probe = 'trove.probe';
      s.setItem(probe, '1');
      s.removeItem(probe);
      return true;
    } catch {
      return false;
    }
  }

  get(area: StorageArea, key: string): string | null {
    try {
      return this.area(area).getItem(key);
    } catch {
      return null;
    }
  }

  /** Returns false when the value could not be stored. */
  set(area: StorageArea, key: string, value: string): boolean {
    try {
      this.area(area).setItem(key, value);
      return true;
    } catch {
      return false;
    }
  }

  remove(area: StorageArea, key: string): void {
    try {
      this.area(area).removeItem(key);
    } catch {
      // Blocked storage: nothing to remove.
    }
  }

  // >>> M1 ui-b
  /** Every key and value of the area; for the specs that prove no secret was stored (R-UI-043). */
  entries(area: StorageArea): [string, string][] {
    try {
      const s = this.area(area);
      return Array.from({ length: s.length }, (_, i) => s.key(i) ?? '').map((k) => [k, s.getItem(k) ?? '']);
    } catch {
      return [];
    }
  }

  /** Removes everything in the area; for the specs. */
  clear(area: StorageArea): void {
    try {
      this.area(area).clear();
    } catch {
      // Blocked storage: nothing to clear.
    }
  }
  // <<< M1 ui-b

  private area(area: StorageArea): Storage {
    return area === 'local' ? localStorage : sessionStorage;
  }
}
