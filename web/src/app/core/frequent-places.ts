import { Injectable, computed, inject, signal } from '@angular/core';
import { Router } from '@angular/router';
import { SessionStore } from './session-store';
import { BrowserStorage } from './storage';

/** At most this many repositories and this many projects are kept per principal (R-UI-096). */
export const FREQUENT_LIMIT = 20;
/** A visit counts once per entry within this window. */
export const VISIT_WINDOW_MS = 15 * 60 * 1000;
/** The weight of the visits halves every this many days since the last one. */
export const HALF_LIFE_DAYS = 14;

const DAY_MS = 24 * 60 * 60 * 1000;

/** One remembered place: a repository's qualified name `<project>/<key>`, or a project's key. */
export interface Visit {
  readonly id: string;
  readonly visits: number;
  /** The time of the last counted visit, in ms since the epoch. */
  readonly last: number;
}

interface Store {
  readonly repositories: readonly Visit[];
  readonly projects: readonly Visit[];
}

const EMPTY: Store = { repositories: [], projects: [] };

/** The frequency order of R-UI-096: the visits, halved for every 14 days since the last one, then the last visit. */
export function byFrequency(entries: readonly Visit[], now: number): Visit[] {
  const weight = (v: Visit): number => v.visits * Math.pow(0.5, Math.max(0, now - v.last) / DAY_MS / HALF_LIFE_DAYS);
  return [...entries].sort((a, b) => weight(b) - weight(a) || b.last - a.last || a.id.localeCompare(b.id));
}

/** The newest first: the recently opened lists of the start page and the project switcher. */
export function byRecency(entries: readonly Visit[]): Visit[] {
  return [...entries].sort((a, b) => b.last - a.last || a.id.localeCompare(b.id));
}

function isVisit(v: unknown): v is Visit {
  if (typeof v !== 'object' || v === null) return false;
  const r = v as Record<string, unknown>;
  return typeof r['id'] === 'string' && typeof r['visits'] === 'number' && typeof r['last'] === 'number';
}

/**
 * The places a signed-in user opens, kept in this browser (Q-UI-405): per principal ID one key `trove.frequent.<ID>`, per
 * repository its qualified name and per project its key, each with its number of visits and the time of the last one, at most
 * 20 per kind. A visit counts once per entry in 15 minutes; a route under `/admin` is never recorded. Non-secret convenience data
 * (R-UI-043): with storage blocked nothing is kept and the lists that read it are left out. The server keeps no record of it.
 */
@Injectable({ providedIn: 'root' })
export class FrequentPlaces {
  private readonly storage = inject(BrowserStorage);
  private readonly session = inject(SessionStore);
  private readonly router = inject(Router);

  /** The storage key of the signed-in principal, or null for an anonymous caller. */
  private readonly key = computed(() => {
    const id = this.session.authenticated() ? this.session.whoami()?.principal?.id : undefined;
    return id ? `trove.frequent.${id}` : null;
  });
  /** Bumped on every write, so that the readers below follow it. */
  private readonly version = signal(0);
  private readonly store = computed<Store>(() => {
    this.version();
    const key = this.key();
    return key ? this.read(key) : EMPTY;
  });

  /** Whether anything can be kept: a signed-in caller and storage that answers (R-UI-096: groups 2 and 4 are left out else). */
  readonly available = computed(() => this.key() !== null && this.storage.available('local'));
  /** The repositories by frequency (the search field's group 2). */
  readonly frequentRepositories = computed(() => byFrequency(this.store().repositories, Date.now()));
  /** The projects by frequency (group 4). */
  readonly frequentProjects = computed(() => byFrequency(this.store().projects, Date.now()));
  /** The repositories by the last visit (the start page's **Recently opened**). */
  readonly recentRepositories = computed(() => byRecency(this.store().repositories));
  /** The projects by the last visit (the project switcher's recent list). */
  readonly recentProjects = computed(() => byRecency(this.store().projects));

  /** Records the opening of a repository's page or content page. */
  visitRepository(project: string, key: string): void {
    this.visit('repositories', `${project}/${key}`);
  }

  /** Records the opening of a project's screen. */
  visitProject(key: string): void {
    this.visit('projects', key);
  }

  /** Forgets a project that no longer exists. */
  forgetProject(key: string): void {
    const storeKey = this.key();
    if (!storeKey) return;
    const current = this.read(storeKey);
    this.write(storeKey, { ...current, projects: current.projects.filter((v) => v.id !== key) });
  }

  private visit(kind: keyof Store, id: string): void {
    const storeKey = this.key();
    if (!storeKey || this.inAdminArea()) return;
    const now = Date.now();
    const current = this.read(storeKey);
    const entries = current[kind];
    const found = entries.find((v) => v.id === id);
    if (found && now - found.last < VISIT_WINDOW_MS) return;
    const next: Visit = found ? { id, visits: found.visits + 1, last: now } : { id, visits: 1, last: now };
    // The cap drops the entry that the frequency order puts last; the one just visited always stays.
    const others = byFrequency(
      entries.filter((v) => v.id !== id),
      now,
    ).slice(0, FREQUENT_LIMIT - 1);
    this.write(storeKey, { ...current, [kind]: [next, ...others] });
  }

  private inAdminArea(): boolean {
    const url = this.router.url;
    return url === '/admin' || url.startsWith('/admin/') || url.startsWith('/admin?');
  }

  private read(storeKey: string): Store {
    try {
      const parsed: unknown = JSON.parse(this.storage.get('local', storeKey) ?? 'null');
      if (typeof parsed !== 'object' || parsed === null) return EMPTY;
      const p = parsed as Record<string, unknown>;
      const list = (v: unknown): Visit[] => (Array.isArray(v) ? v.filter(isVisit).slice(0, FREQUENT_LIMIT) : []);
      return { repositories: list(p['repositories']), projects: list(p['projects']) };
    } catch {
      return EMPTY;
    }
  }

  private write(storeKey: string, next: Store): void {
    this.storage.set('local', storeKey, JSON.stringify(next));
    this.version.update((v) => v + 1);
  }
}
