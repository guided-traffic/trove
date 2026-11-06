import { DatePipe } from '@angular/common';
import { Component, computed, effect, inject, input, signal, untracked } from '@angular/core';
import { Router } from '@angular/router';
import { firstValueFrom } from 'rxjs';
import { ApiError } from '../../api/problem';
import { Project, ProjectsApi } from '../../api/projects-api';
import { Versioned } from '../../api/repositories-api';
import { Notifier } from '../../core/notifier';
import { ProjectContext } from '../../core/project-context';
import { SessionStore } from '../../core/session-store';
import { Needs } from '../../shared/access/needs';
import { Confirm } from '../../shared/confirm-dialog';
import { ErrorNotice } from '../../shared/error-notice';
import { asApiError } from '../../shared/paged-resource';
import { TroveDialog } from '../../shared/ui/dialog';
import { ProjectDialog } from './project-dialog';
import { projectRoleLabel } from './project-roles';

/**
 * The settings of one project (R-UI-041, Q-UI-129): the facts of the project with its Owners, editing for an Owner and
 * `projects:write`, and delete for `projects:write`. Delete is offered only for a project without repositories and
 * service accounts; its policies go with it, and a 409 `project_not_empty` that races it is explained. The members,
 * repositories, policies and service accounts have screens of their own in the project's sidebar.
 */
@Component({
  selector: 'trove-project-settings',
  imports: [DatePipe, ErrorNotice, Needs],
  template: `
    <header class="page-header">
      <h1 tabindex="-1" i18n="@@project.settings">Settings</h1>
    </header>

    @if (loadError(); as e) {
      <trove-error-notice [error]="e" (retry)="load()" />
    }
    @if (actionError(); as e) {
      <trove-error-notice [error]="e" />
    }

    @if (project(); as p) {
      <section class="card" aria-labelledby="project-general">
        <h2 id="project-general" i18n="@@project.general">General</h2>
        <dl class="facts">
          <dt i18n="@@project.key">Key</dt>
          <dd translate="no">{{ p.key }}</dd>
          <dt i18n="@@project.display-name">Display name</dt>
          <dd>{{ p.displayName }}</dd>
          <dt i18n="@@project.description">Description</dt>
          <dd>{{ p.description || '–' }}</dd>
          <dt i18n="@@project.owners">Owners</dt>
          <dd>
            @if (p.owners.length === 0) {
              <span i18n="@@project.no-owners">None yet.</span>
            } @else {
              <ul class="plain">
                @for (a of p.owners; track a) {
                  <li translate="no">{{ a }}</li>
                }
              </ul>
            }
          </dd>
          <dt i18n="@@project.your-role">Your role</dt>
          <dd>{{ p.role ? roleText(p.role) : '–' }}</dd>
          <dt i18n="@@project.counts">Contents</dt>
          <dd>
            <ng-container i18n="@@project.counts-text">{{ p.members }} members, {{ p.repositories }} repositories, {{ p.policies }} policies, {{ p.serviceAccounts }} service accounts</ng-container>
          </dd>
          <dt i18n="@@project.updated">Last changed</dt>
          <dd>{{ p.updatedAt | date: 'medium' }}</dd>
        </dl>

        @if (canEdit()) {
          <div class="row-actions">
            <button class="btn btn-confirm" type="button" [disabled]="busy()" (click)="edit()" i18n="@@project.edit">Edit project</button>
            @if (canWrite()) {
            <button class="btn btn-tertiary"
              type="button"
              [disabled]="busy() || !empty()"
              aria-describedby="project-delete-note"
              (click)="remove()"
              i18n="@@project.delete"
            >
              Delete project
            </button>
            }
          </div>
          @if (canWrite()) {
            <p id="project-delete-note" class="muted">
              @if (empty()) {
                <ng-container i18n="@@project.delete-note-empty">The project's policies are deleted with it.</ng-container>
              } @else {
                <ng-container i18n="@@project.delete-note"
                  >A project can be deleted when no repository and no service account belongs to it. Move or delete the repositories and delete the service accounts first.</ng-container
                >
              }
            </p>
          }
        } @else {
          <trove-needs capability="projects:write" />
        }
      </section>
    }
  `,
  styleUrl: '../../shared/access/access.scss',
  styles: `
    .plain {
      margin: 0;
      padding: 0;
      list-style: none;
    }
  `,
})
export class ProjectSettings {
  /** Route parameter `:project`. */
  readonly key = input.required<string>({ alias: 'project' });

  private readonly api = inject(ProjectsApi);
  private readonly dialog = inject(TroveDialog);
  private readonly confirmer = inject(Confirm);
  private readonly notifier = inject(Notifier);
  private readonly router = inject(Router);
  private readonly context = inject(ProjectContext);
  private readonly session = inject(SessionStore);

  protected readonly project = signal<Project | null>(null);
  protected readonly loadError = signal<ApiError | null>(null);
  protected readonly actionError = signal<ApiError | null>(null);
  protected readonly busy = signal(false);

  protected readonly canWrite = computed(() => this.session.has('projects:write'));
  /** Changing the display name and the description: `projects:write`, or the Owner role of the project (R-AUTHZ-026). */
  protected readonly canEdit = computed(() => this.canWrite() || this.session.isOwnerOf(this.key()));
  protected readonly roleText = projectRoleLabel;
  protected readonly empty = computed(() => {
    const p = this.project();
    return p !== null && p.repositories === 0 && p.serviceAccounts === 0;
  });

  constructor() {
    effect(() => {
      this.key();
      untracked(() => void this.load());
    });
  }

  protected async load(): Promise<void> {
    this.loadError.set(null);
    try {
      this.project.set((await this.api.get(this.key())).value);
    } catch (e) {
      this.project.set(null);
      this.loadError.set(asApiError(e));
    }
  }

  protected async edit(): Promise<void> {
    this.actionError.set(null);
    try {
      const loaded: Versioned<Project> = await this.api.get(this.key());
      const ref = this.dialog.open<ProjectDialog, { project: Versioned<Project> }, Project>(ProjectDialog, {
        data: { project: loaded },
        maxWidth: '40rem',
      });
      const saved = await firstValueFrom(ref.closed);
      if (saved) {
        this.project.set(saved);
        void this.context.refresh();
        this.notifier.status($localize`:@@project.saved:Project saved.`);
      }
    } catch (e) {
      this.actionError.set(asApiError(e));
    }
  }

  protected async remove(): Promise<void> {
    const p = this.project();
    if (!p) return;
    const answer = await this.confirmer.ask({
      title: $localize`:@@project.delete-title:Delete project ${p.key}:key:?`,
      lines: [
        $localize`:@@project.delete-text:The project, its permission policies and its member entries are deleted. The policies stop granting at once on every replica. This cannot be undone.`,
      ],
      confirmLabel: $localize`:@@project.delete-confirm:Delete project`,
      typeToConfirm: p.key,
    });
    if (answer === null) return;
    this.busy.set(true);
    this.actionError.set(null);
    try {
      await this.api.delete(p.key);
      this.notifier.status($localize`:@@project.deleted:Project deleted.`);
      this.context.forget(p.key);
      void this.context.refresh();
      await this.router.navigate(['/projects']);
    } catch (e) {
      // 409 project_not_empty when a repository or an account arrived since the page was read.
      this.actionError.set(asApiError(e));
      void this.load();
    } finally {
      this.busy.set(false);
    }
  }
}
