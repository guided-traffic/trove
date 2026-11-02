import { DatePipe } from '@angular/common';
import { Component, computed, effect, inject, input, signal, untracked } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatDialog } from '@angular/material/dialog';
import { Router, RouterLink } from '@angular/router';
import { firstValueFrom } from 'rxjs';
import { ApiError } from '../../api/problem';
import { Project, ProjectsApi } from '../../api/projects-api';
import { Versioned } from '../../api/repositories-api';
import { Notifier } from '../../core/notifier';
import { ProjectFilter } from '../../core/project-filter';
import { SessionStore } from '../../core/session-store';
import { Needs } from '../../shared/access/needs';
import { Confirm } from '../../shared/confirm-dialog';
import { ErrorNotice } from '../../shared/error-notice';
import { asApiError } from '../../shared/paged-resource';
import { ProjectDialog } from './project-dialog';
import { ProjectPolicies } from './project-policies';
import { ProjectRepositories } from './project-repositories';
import { ProjectServiceAccounts } from './project-service-accounts';

/**
 * One project (R-UI-041): the overview with its switches and administrators (read-only for a project
 * administrator; edit and delete for `projects:write`), then the project's repositories, permission
 * policies and service accounts. Delete is offered only for a project without repositories and service
 * accounts; its policies go with it, and a 409 `project_not_empty` that races it is explained.
 */
@Component({
  selector: 'trove-project-detail',
  imports: [
    DatePipe,
    MatButtonModule,
    RouterLink,
    ErrorNotice,
    Needs,
    ProjectPolicies,
    ProjectRepositories,
    ProjectServiceAccounts,
  ],
  template: `
    <header class="page-header">
      <h1 tabindex="-1" i18n="@@project.heading">Project</h1>
      <a mat-button routerLink="/admin/projects" i18n="@@project.back">All projects</a>
    </header>
    <p class="subject" translate="no">{{ key() }}</p>

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
          <dt i18n="@@project.display-name">Display name</dt>
          <dd>{{ p.displayName }}</dd>
          <dt i18n="@@project.description">Description</dt>
          <dd>{{ p.description || '–' }}</dd>
          <dt i18n="@@project.switches">Administrators may</dt>
          <dd>
            <ul class="plain">
              <li>
                <span class="material-icons" aria-hidden="true">{{ p.managePolicies ? 'check_circle' : 'block' }}</span>
                <ng-container i18n="@@project.switch.policies">manage policies, service accounts and their tokens</ng-container>:
                <strong>{{ p.managePolicies ? on : off }}</strong>
              </li>
              <li>
                <span class="material-icons" aria-hidden="true">{{ p.manageRepositories ? 'check_circle' : 'block' }}</span>
                <ng-container i18n="@@project.switch.repositories">create, change and delete repositories</ng-container>:
                <strong>{{ p.manageRepositories ? on : off }}</strong>
              </li>
            </ul>
          </dd>
          <dt i18n="@@project.admins">Administrators</dt>
          <dd>
            @if (p.admins.length === 0) {
              <span i18n="@@project.no-admins">None yet.</span>
            } @else {
              <ul class="plain">
                @for (a of p.admins; track a) {
                  <li translate="no">{{ a }}</li>
                }
              </ul>
            }
          </dd>
          <dt i18n="@@project.counts">Contents</dt>
          <dd>
            <ng-container i18n="@@project.counts-text">{{ p.repositories }} repositories, {{ p.policies }} policies, {{ p.serviceAccounts }} service accounts</ng-container>
          </dd>
          <dt i18n="@@project.updated">Last changed</dt>
          <dd>{{ p.updatedAt | date: 'medium' }}</dd>
        </dl>

        @if (canWrite()) {
          <div class="row-actions">
            <button mat-flat-button type="button" [disabled]="busy()" (click)="edit()" i18n="@@project.edit">Edit project</button>
            <button
              mat-button
              type="button"
              [disabled]="busy() || !empty()"
              aria-describedby="project-delete-note"
              (click)="remove()"
              i18n="@@project.delete"
            >
              Delete project
            </button>
          </div>
          <p id="project-delete-note" class="muted">
            @if (empty()) {
              <ng-container i18n="@@project.delete-note-empty">The project's policies are deleted with it.</ng-container>
            } @else {
              <ng-container i18n="@@project.delete-note"
                >A project can be deleted when no repository and no service account belongs to it. Unassign the repositories and delete the service accounts first.</ng-container
              >
            }
          </p>
        } @else {
          <trove-needs capability="projects:write" />
        }
      </section>

      <section class="card" aria-labelledby="project-repositories">
        <h2 id="project-repositories" i18n="@@project.repositories">Repositories</h2>
        <trove-project-repositories [project]="p.key" />
      </section>

      <section class="card" aria-labelledby="project-policies">
        <h2 id="project-policies" i18n="@@project.policies">Permission policies</h2>
        <trove-project-policies [project]="p.key" />
      </section>

      <!-- >>> M4 ci -->
      @if (canCi()) {
        <section class="card" aria-labelledby="project-ci">
          <h2 id="project-ci" i18n="@@project.ci">CI identity</h2>
          <p class="muted" i18n="@@project.ci-hint">
            Let the GitLab jobs of your projects publish to and read from this project's repositories without a stored secret. A mapping states which jobs get which permissions.
          </p>
          <a mat-stroked-button [routerLink]="['/admin/projects', p.key, 'ci']" i18n="@@project.ci-open">Open CI identity</a>
        </section>
      }
      <!-- <<< M4 ci -->

      <section class="card" aria-labelledby="project-sas">
        <h2 id="project-sas" i18n="@@project.service-accounts">Service accounts</h2>
        <p class="muted" i18n="@@project.service-accounts-hint">
          A service account of the project acts for a pipeline or a partner. Only the project's own policies grant to it; open one to issue its tokens.
        </p>
        <trove-project-service-accounts [project]="p.key" />
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
export class ProjectDetail {
  /** Route parameter `:key`. */
  readonly key = input.required<string>();

  private readonly api = inject(ProjectsApi);
  private readonly dialog = inject(MatDialog);
  private readonly confirmer = inject(Confirm);
  private readonly notifier = inject(Notifier);
  private readonly router = inject(Router);
  private readonly filter = inject(ProjectFilter);
  private readonly session = inject(SessionStore);

  protected readonly project = signal<Project | null>(null);
  protected readonly loadError = signal<ApiError | null>(null);
  protected readonly actionError = signal<ApiError | null>(null);
  protected readonly busy = signal(false);
  protected readonly on = $localize`:@@project.on:on`;
  protected readonly off = $localize`:@@project.off:off`;

  protected readonly canWrite = computed(() => this.session.has('projects:write'));
  /** The CI screens of the project: the capability, or administration of the project (Q-CI-26). */
  protected readonly canCi = computed(() => this.session.has('ci:read') || this.session.projectRights(this.key()) !== null);
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
      const saved = await firstValueFrom(ref.afterClosed());
      if (saved) {
        this.project.set(saved);
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
        $localize`:@@project.delete-text:The project, its permission policies and its administrator entries are deleted. The policies stop granting at once on every replica. This cannot be undone.`,
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
      void this.filter.refresh();
      await this.router.navigate(['/admin/projects']);
    } catch (e) {
      // 409 project_not_empty when a repository or an account arrived since the page was read.
      this.actionError.set(asApiError(e));
      void this.load();
    } finally {
      this.busy.set(false);
    }
  }
}
