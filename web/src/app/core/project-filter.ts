import { Injectable, computed, effect, inject, signal, untracked } from '@angular/core';
import { collectAll } from '../api/pagination';
import { Project, ProjectsApi } from '../api/projects-api';
import { PreferencesStore } from './preferences-store';
import { SessionStore } from './session-store';

/**
 * The project filter of the top bar (R-UI-041): the projects the caller may see, and the selected one,
 * stored as the `projectFilter` preference (R-API-021). The repository and policy lists send the
 * selection as `project=`. The filter is offered when projects exist and the caller administers at
 * least one or holds `projects:read` (an administrator). A stored key that no longer names a visible
 * project (deleted, or access lost) filters nothing: `active` is null then, and the preference stays
 * until the person picks again.
 */
@Injectable({ providedIn: 'root' })
export class ProjectFilter {
  private readonly api = inject(ProjectsApi);
  private readonly session = inject(SessionStore);
  private readonly preferences = inject(PreferencesStore);

  private readonly _projects = signal<readonly Project[]>([]);

  /** The visible projects, ordered by key. */
  readonly projects = this._projects.asReadonly();
  readonly canList = computed(() => this.session.has('projects:read') || this.session.isProjectOwner());
  /** The header offers the filter. */
  readonly visible = computed(() => this.canList() && this._projects().length > 0);
  /** The key lists filter by, or null for none. */
  readonly active = computed<string | null>(() => {
    const key = this.preferences.projectFilter();
    return key !== null && this._projects().some((p) => p.key === key) ? key : null;
  });

  constructor() {
    effect(() => {
      const ready = this.session.authenticated() && this.canList();
      untracked(() => {
        if (ready) void this.refresh();
        else this._projects.set([]);
      });
    });
  }

  /** Reads the projects again; the project screens call it after they create or delete one. */
  async refresh(): Promise<void> {
    try {
      this._projects.set(await collectAll(this.api.list({})));
    } catch {
      // A convenience: without the list the header offers no filter and the lists stay unfiltered.
      this._projects.set([]);
    }
  }

  async select(key: string | null): Promise<void> {
    await this.preferences.setProjectFilter(key);
  }
}
