import { Injectable, computed, effect, inject, signal, untracked } from '@angular/core';
import { SessionStore } from './session-store';
import { BrowserStorage } from './storage';

interface StoredNavState {
  /** True when the sidebar is the narrow icon rail. */
  readonly rail: boolean;
  /** Ids of the groups the viewer folded. */
  readonly closed: readonly string[];
}

const EMPTY: StoredNavState = { rail: false, closed: [] };

/**
 * How the viewer left the navigation sidebar (R-UI-002, Q-UI-68): compact rail or full width, and
 * which groups are folded. Non-secret convenience data in browser storage, keyed by the signed-in
 * principal so that two people on one browser keep their own layout (R-UI-043); when storage is
 * blocked the state lasts for the tab only.
 */
@Injectable({ providedIn: 'root' })
export class NavState {
  private readonly storage = inject(BrowserStorage);
  private readonly session = inject(SessionStore);
  private readonly state = signal<StoredNavState>(EMPTY);

  private readonly key = computed(() => {
    const name = this.session.whoami()?.principal?.name;
    return name ? `trove.nav.${name}` : null;
  });

  /** The sidebar is the icon rail. Only honoured where the sidebar is pinned. */
  readonly rail = computed(() => this.state().rail);

  constructor() {
    effect(() => {
      const key = this.key();
      untracked(() => this.state.set(key ? this.read(key) : EMPTY));
    });
  }

  setRail(rail: boolean): void {
    this.update({ ...this.state(), rail });
  }

  isOpen(groupId: string): boolean {
    return !this.state().closed.includes(groupId);
  }

  toggleGroup(groupId: string): void {
    const s = this.state();
    const closed = s.closed.includes(groupId) ? s.closed.filter((id) => id !== groupId) : [...s.closed, groupId];
    this.update({ ...s, closed });
  }

  private update(next: StoredNavState): void {
    this.state.set(next);
    const key = this.key();
    if (key) this.storage.set('local', key, JSON.stringify(next));
  }

  private read(key: string): StoredNavState {
    try {
      const parsed: unknown = JSON.parse(this.storage.get('local', key) ?? 'null');
      if (typeof parsed !== 'object' || parsed === null) return EMPTY;
      const { rail, closed } = parsed as Record<string, unknown>;
      return {
        rail: rail === true,
        closed: Array.isArray(closed) ? closed.filter((c): c is string => typeof c === 'string') : [],
      };
    } catch {
      return EMPTY;
    }
  }
}
