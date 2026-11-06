import { Injectable, computed, effect, inject, signal, untracked } from '@angular/core';
import { NavigationCancel, NavigationEnd, NavigationError, Router, RoutesRecognized } from '@angular/router';
import { collectAll } from '../api/pagination';
import { Project, ProjectsApi } from '../api/projects-api';
import { SessionStore } from './session-store';
import { BrowserStorage } from './storage';

/** How many recently visited projects the switcher remembers. */
export const RECENT_PROJECTS = 5;

/** The project key of a URL below `/projects/<key>`, or null outside a project (Q-UI-129). */
export function projectKeyOf(url: string): string | null {
  const match = /^\/projects\/([^/?#]+)/.exec(url);
  if (!match?.[1]) return null;
  try {
    return decodeURIComponent(match[1]);
  } catch {
    return null;
  }
}

/**
 * The project the screens are in (Q-UI-129): the key is the URL (`/projects/<key>/…`), the projects are the ones the
 * caller may see (`GET /api/v1/projects`: the memberships, every project for `projects:read`), and the recently visited
 * ones are a convenience of this browser, kept per signed-in principal (R-UI-043, R-UI-046). A key in the URL that names no
 * visible project is still the context: the screen under it reports what the API answers.
 */
@Injectable({ providedIn: 'root' })
export class ProjectContext {
  private readonly api = inject(ProjectsApi);
  private readonly session = inject(SessionStore);
  private readonly storage = inject(BrowserStorage);
  private readonly router = inject(Router);

  private readonly _projects = signal<readonly Project[]>([]);
  private readonly _loaded = signal(false);
  private readonly _recent = signal<readonly string[]>([]);
  private pending: Promise<void> | null = null;

  /**
   * The URL the screens are in. It moves as soon as the router has recognised the target, before the screens of the target
   * are created, so a screen that asks for the project in its constructor gets the new one; a navigation that is cancelled
   * or fails puts the old URL back.
   */
  private readonly url = signal(this.router.url);

  /** The key of the project the current URL is in, or null (the start page, the admin area, the profile …). */
  readonly key = computed(() => projectKeyOf(this.url()));
  /** The projects the caller may see, ordered by key. */
  readonly projects = this._projects.asReadonly();
  /** The list has been read (successfully or not); the start page waits for it. */
  readonly loaded = this._loaded.asReadonly();
  /** The record of the current project, when the caller sees it in the list. */
  readonly current = computed<Project | null>(() => {
    const key = this.key();
    return key === null ? null : (this._projects().find((p) => p.key === key) ?? null);
  });
  /** The keys of the recently visited projects, newest first. */
  readonly recent = this._recent.asReadonly();

  private readonly storageKey = computed(() => {
    const name = this.session.whoami()?.principal?.name;
    return name ? `trove.recent-projects.${name}` : null;
  });

  constructor() {
    this.router.events.subscribe((e) => {
      if (e instanceof RoutesRecognized || e instanceof NavigationEnd) this.url.set(e.urlAfterRedirects);
      else if (e instanceof NavigationCancel || e instanceof NavigationError) this.url.set(this.router.url);
    });
    effect(() => {
      const ready = this.session.authenticated();
      untracked(() => {
        if (ready) void this.refresh();
        else {
          this._projects.set([]);
          this._loaded.set(false);
        }
      });
    });
    effect(() => {
      const storageKey = this.storageKey();
      untracked(() => this._recent.set(storageKey ? this.stored(storageKey) : []));
    });
    effect(() => {
      const key = this.key();
      // Reads the stored list first, so that the first visit after a reload does not overwrite it.
      this.storageKey();
      if (key !== null) untracked(() => this.visit(key));
    });
  }

  /** Reads the projects again; the screens call it after they create, change or delete one. */
  refresh(): Promise<void> {
    this.pending ??= this.read().finally(() => {
      this.pending = null;
    });
    return this.pending;
  }

  /** The projects, read once: the start guard waits for them. */
  async ready(): Promise<readonly Project[]> {
    if (!this._loaded()) await this.refresh();
    return this._projects();
  }

  private async read(): Promise<void> {
    try {
      this._projects.set(await collectAll(this.api.list({})));
    } catch {
      // A convenience: without the list the switcher offers the recent keys only.
      this._projects.set([]);
    } finally {
      this._loaded.set(true);
    }
  }

  /** Remembers the project as the most recent one. */
  visit(key: string): void {
    const current = this._recent();
    const next = [key, ...current.filter((k) => k !== key)].slice(0, RECENT_PROJECTS);
    if (next.length === current.length && next.every((k, i) => k === current[i])) return;
    this.keep(next);
  }

  /** Forgets a project that no longer exists. */
  forget(key: string): void {
    this.keep(this._recent().filter((k) => k !== key));
  }

  private keep(next: readonly string[]): void {
    this._recent.set(next);
    const storageKey = this.storageKey();
    if (storageKey) this.storage.set('local', storageKey, JSON.stringify(next));
  }

  private stored(storageKey: string): readonly string[] {
    try {
      const parsed: unknown = JSON.parse(this.storage.get('local', storageKey) ?? 'null');
      return Array.isArray(parsed)
        ? parsed.filter((k): k is string => typeof k === 'string').slice(0, RECENT_PROJECTS)
        : [];
    } catch {
      return [];
    }
  }
}
