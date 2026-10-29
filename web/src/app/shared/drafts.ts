import { DestroyRef, Injectable, inject } from '@angular/core';
import { Router } from '@angular/router';
import { BrowserStorage } from '../core/storage';

const PREFIX = 'trove.draft:';

/**
 * Drafts of non-secret forms (R-UI-042). Two jobs:
 *
 * - **Re-authentication.** An editor whose write answered 401 `reauthentication_required` calls
 *   `keep`; the draft waits in session storage under the route. After the sign-in returns to the
 *   route, the editor calls `take` and the form is filled again. Session storage dies with the tab.
 * - **Session end.** An open editor registers a getter with `track`; the "Your session has ended"
 *   dialog offers "Copy draft as JSON" from `snapshot`.
 *
 * Forms that hold a secret (a token value, a password) never use either: R-UI-043.
 */
@Injectable({ providedIn: 'root' })
export class DraftStore {
  private readonly storage = inject(BrowserStorage);
  private readonly router = inject(Router);
  private readonly getters = new Set<() => unknown>();

  /** The route without query and fragment, as the key of its draft. */
  private key(): string {
    return PREFIX + this.router.url.split(/[?#]/)[0];
  }

  /** Stores the draft of the current route; false when browser storage is blocked. */
  keep(draft: unknown): boolean {
    return this.storage.set('session', this.key(), JSON.stringify(draft));
  }

  /** Returns and removes the draft of the current route, or null. */
  take<T>(): T | null {
    const raw = this.storage.get('session', this.key());
    if (raw === null) return null;
    this.storage.remove('session', this.key());
    try {
      return JSON.parse(raw) as T;
    } catch {
      return null;
    }
  }

  /**
   * Registers the draft of an open editor for "Copy draft as JSON"; the getter returns null while
   * the form is unchanged. Unregisters when the calling component is destroyed.
   */
  track(getter: () => unknown, destroyRef: DestroyRef): void {
    this.getters.add(getter);
    destroyRef.onDestroy(() => this.getters.delete(getter));
  }

  /** The drafts of all open editors as pretty JSON, or null when none has unsaved changes. */
  snapshot(): string | null {
    const drafts = [...this.getters].map((g) => g()).filter((d) => d !== null && d !== undefined);
    if (drafts.length === 0) return null;
    return JSON.stringify(drafts.length === 1 ? drafts[0] : drafts, null, 2);
  }
}
