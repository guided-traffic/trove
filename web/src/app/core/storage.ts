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

  private area(area: StorageArea): Storage {
    return area === 'local' ? localStorage : sessionStorage;
  }
}
