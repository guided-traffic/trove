import { Injectable, computed, inject, signal } from '@angular/core';
import { SessionStore } from '../../core/session-store';
import { BrowserStorage } from '../../core/storage';

/** "Recently viewed" holds this many packages (R-UI-006). */
export const RECENT_PACKAGES_LIMIT = 10;
/** At most this many saved views (R-UI-006). */
export const SAVED_VIEWS_LIMIT = 8;

export interface RecentPackage {
  readonly format: string;
  readonly name: string;
}

/** A saved view is a name and the query parameters of the list. */
export interface SavedView {
  readonly id: string;
  readonly name: string;
  readonly query: Readonly<Record<string, string | readonly string[]>>;
}

interface Stored {
  readonly recent: readonly RecentPackage[];
  readonly views: readonly SavedView[];
  readonly defaultView: string | null;
}

const EMPTY: Stored = { recent: [], views: [], defaultView: null };

/**
 * What the packages list remembers per signed-in principal in browser storage: the last packages
 * opened and the saved views (R-UI-006). Non-secret convenience data (R-UI-043). Where storage is
 * blocked, or nobody is signed in, `available` is false and the screens hide the two tabs.
 */
@Injectable({ providedIn: 'root' })
export class PackageMemory {
  private readonly storage = inject(BrowserStorage);
  private readonly session = inject(SessionStore);
  private readonly usable = this.storage.available('local');
  private readonly state = signal<{ key: string | null; value: Stored }>({ key: null, value: EMPTY });

  private readonly key = computed(() => {
    const name = this.session.whoami()?.principal?.name;
    return name ? `trove.packages.${name}` : null;
  });
  private readonly current = computed<Stored>(() => {
    const key = this.key();
    const s = this.state();
    return s.key === key && key !== null ? s.value : key !== null ? this.read(key) : EMPTY;
  });

  /** False hides the "Recently viewed" and "Saved views" tabs. */
  readonly available = computed(() => this.usable && this.key() !== null);
  readonly recent = computed(() => this.current().recent);
  readonly views = computed(() => this.current().views);
  readonly defaultView = computed(() => {
    const s = this.current();
    return s.views.find((v) => v.id === s.defaultView) ?? null;
  });

  /** Records an opened package: newest first, no duplicate. */
  addRecent(item: RecentPackage): void {
    const s = this.current();
    this.write({
      ...s,
      recent: [item, ...s.recent.filter((r) => r.format !== item.format || r.name !== item.name)].slice(
        0,
        RECENT_PACKAGES_LIMIT,
      ),
    });
  }

  /** Saves the current query under a name; false when the limit is reached or the name is empty. */
  saveView(name: string, query: SavedView['query']): boolean {
    const s = this.current();
    const trimmed = name.trim();
    if (!trimmed || s.views.length >= SAVED_VIEWS_LIMIT) return false;
    this.write({ ...s, views: [...s.views, { id: crypto.randomUUID(), name: trimmed, query }] });
    return true;
  }

  renameView(id: string, name: string): void {
    const trimmed = name.trim();
    if (!trimmed) return;
    const s = this.current();
    this.write({ ...s, views: s.views.map((v) => (v.id === id ? { ...v, name: trimmed } : v)) });
  }

  deleteView(id: string): void {
    const s = this.current();
    this.write({
      ...s,
      views: s.views.filter((v) => v.id !== id),
      defaultView: s.defaultView === id ? null : s.defaultView,
    });
  }

  /** `null` removes the default. */
  makeDefault(id: string | null): void {
    this.write({ ...this.current(), defaultView: id });
  }

  private write(next: Stored): void {
    const key = this.key();
    if (key === null) return;
    this.state.set({ key, value: next });
    this.storage.set('local', key, JSON.stringify(next));
  }

  private read(key: string): Stored {
    try {
      const parsed = JSON.parse(this.storage.get('local', key) ?? 'null') as Partial<Stored> | null;
      if (!parsed || typeof parsed !== 'object') return EMPTY;
      const recent = (Array.isArray(parsed.recent) ? parsed.recent : []).filter(
        (r): r is RecentPackage => typeof r?.format === 'string' && typeof r?.name === 'string',
      );
      const views = (Array.isArray(parsed.views) ? parsed.views : []).filter(
        (v): v is SavedView => typeof v?.id === 'string' && typeof v?.name === 'string' && typeof v?.query === 'object',
      );
      return {
        recent: recent.slice(0, RECENT_PACKAGES_LIMIT),
        views: views.slice(0, SAVED_VIEWS_LIMIT),
        defaultView: typeof parsed.defaultView === 'string' ? parsed.defaultView : null,
      };
    } catch {
      return EMPTY;
    }
  }
}
