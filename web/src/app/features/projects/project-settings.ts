import { DatePipe, formatNumber } from '@angular/common';
import { Component, LOCALE_ID, computed, effect, inject, input, signal, untracked } from '@angular/core';
import { Router } from '@angular/router';
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
import { ConflictResolver } from '../../shared/conflict-dialog';
import { fieldErrorAt } from '../../shared/field-errors';
import { TextField } from '../../shared/access/fields';
import { Disclosure } from '../../shared/ui/disclosure';
import { formatSize } from '../packages/format-size';
import { projectRoleLabel } from './project-roles';
import { PageHeader } from '../../shared/ui/page-header';

/**
 * The settings of one project (R-UI-041, Q-UI-129, Q-UI-215): the display name and the description edited in place by
 * an Owner and `projects:write` (`PUT` with `If-Match`; a 412 opens the conflict dialog of R-UI-042), the facts of the project
 * in a section of their own, and delete for `projects:write` under Advanced. Delete is offered only for a project without repositories and
 * service accounts; its policies go with it, and a 409 `project_not_empty` that races it is explained. The members,
 * repositories, policies and service accounts have screens of their own in the project's sidebar.
 */
@Component({
  selector: 'trove-project-settings',
  imports: [PageHeader, DatePipe, ErrorNotice, Needs, Disclosure, TextField],
  template: `
    <trove-page-header heading="Settings" i18n-heading="@@project.settings" />

    @if (loadError(); as e) {
      <trove-error-notice [error]="e" (retry)="load()" />
    }
    @if (actionError(); as e) {
      <trove-error-notice [error]="e" />
    }

    @if (record(); as p) {
      <div class="settings">
        <form (submit)="save($event)" novalidate>
          <div class="row">
            <trove-text-field
              i18n-label="@@project-dialog.display-name"
              label="Display name"
              name="displayName"
              [(value)]="displayName"
              [required]="true"
              [readonly]="!canEdit()"
              [error]="displayNameError()"
            />
            <trove-text-field i18n-label="@@project.key" label="Key" name="key" [value]="p.key" [readonly]="true" />
          </div>
          <trove-text-field
            i18n-label="@@project.description-optional"
            label="Description (optional)"
            name="description"
            type="textarea"
            [(value)]="description"
            [readonly]="!canEdit()"
            [error]="serverError('/description')"
          />
          @if (canEdit()) {
            <button class="btn btn-confirm" type="submit" [disabled]="busy() || !dirty()" i18n="@@project.save-changes">Save changes</button>
          } @else {
            <trove-needs capability="projects:write" [project]="p.key" />
          }
        </form>

        <trove-disclosure
          i18n-heading="@@project.details"
          heading="Details"
          i18n-hint="@@project.details-hint"
          hint="Who owns the project, your role in it and what it holds."
          class="first"
        >
          <dl class="facts">
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
            <dt i18n="@@project.storage">Storage</dt>
            <dd [attr.title]="exactBytes(p)">{{ size(p.logicalBytes) }}</dd>
            <dt i18n="@@project.updated">Last changed</dt>
            <dd>{{ p.updatedAt | date: 'medium' }}</dd>
          </dl>
        </trove-disclosure>

        @if (canWrite()) {
          <trove-disclosure
            i18n-heading="@@project.advanced"
            heading="Advanced"
            i18n-hint="@@project.advanced-hint"
            hint="Delete this project."
          >
            <p id="project-delete-note" class="muted">
              @if (empty()) {
                <ng-container i18n="@@project.delete-note-empty">The project's policies are deleted with it.</ng-container>
              } @else {
                <ng-container i18n="@@project.delete-note"
                  >A project can be deleted when no repository and no service account belongs to it. Move or delete the repositories and delete the service accounts first.</ng-container
                >
              }
            </p>
            <button
              class="btn btn-danger"
              type="button"
              [disabled]="busy() || !empty()"
              aria-describedby="project-delete-note"
              (click)="remove()"
              i18n="@@project.delete"
            >
              Delete project
            </button>
          </trove-disclosure>
        }
      </div>
    }
  `,
  styleUrl: '../../shared/access/access.scss',
  styles: `
    .settings {
      max-inline-size: var(--trove-settings-measure);
    }
    form {
      padding-block-end: var(--trove-space-6);
    }
    .row {
      display: grid;
      grid-template-columns: minmax(0, 5fr) minmax(0, 4fr);
      gap: var(--trove-space-6);
    }
    @media (max-width: 40rem) {
      .row {
        grid-template-columns: minmax(0, 1fr);
      }
    }
    .plain {
      margin: 0;
      padding: 0;
      list-style: none;
    }
  `,
})
export class ProjectSettings {
  /** Route parameter `:project`. */
  readonly project = input.required<string>();

  private readonly locale = inject(LOCALE_ID);
  private readonly api = inject(ProjectsApi);
  private readonly conflicts = inject(ConflictResolver);
  private readonly confirmer = inject(Confirm);
  private readonly notifier = inject(Notifier);
  private readonly router = inject(Router);
  private readonly context = inject(ProjectContext);
  private readonly session = inject(SessionStore);

  private readonly versioned = signal<Versioned<Project> | null>(null);
  protected readonly record = computed(() => this.versioned()?.value ?? null);
  protected readonly displayName = signal('');
  protected readonly description = signal('');
  private readonly attempted = signal(false);
  private readonly saveError = signal<ApiError | null>(null);
  protected readonly dirty = computed(() => {
    const p = this.record();
    return p !== null && (this.displayName().trim() !== p.displayName || this.description().trim() !== (p.description ?? ''));
  });
  protected readonly loadError = signal<ApiError | null>(null);
  protected readonly actionError = signal<ApiError | null>(null);
  protected readonly busy = signal(false);

  protected readonly canWrite = computed(() => this.session.has('projects:write'));
  /** Changing the display name and the description: `projects:write`, or the Owner role of the project (R-AUTHZ-026). */
  protected readonly canEdit = computed(() => this.canWrite() || this.session.isOwnerOf(this.project()));
  protected readonly roleText = projectRoleLabel;
  protected readonly size = formatSize;
  protected readonly empty = computed(() => {
    const p = this.record();
    return p !== null && p.repositories === 0 && p.serviceAccounts === 0;
  });

  /** The tooltip of the size: the exact bytes the project is charged (Q-STOR-155) and what the figure counts. */
  protected exactBytes(p: Project): string {
    const bytes = formatNumber(p.logicalBytes, this.locale, '1.0-0');
    return $localize`:@@project.storage-title:${bytes}:bytes: bytes. The full size of every file in the project's repositories, also where another project holds the same content.`;
  }

  constructor() {
    effect(() => {
      this.project();
      untracked(() => void this.load());
    });
  }

  protected async load(): Promise<void> {
    this.loadError.set(null);
    try {
      const keepEdits = this.dirty();
      this.show(await this.api.get(this.project()), keepEdits);
    } catch (e) {
      this.versioned.set(null);
      this.loadError.set(asApiError(e));
    }
  }

  private show(loaded: Versioned<Project>, keepEdits = false): void {
    this.versioned.set(loaded);
    if (!keepEdits) {
      this.displayName.set(loaded.value.displayName);
      this.description.set(loaded.value.description ?? '');
    }
  }

  protected serverError(pointer: string): string | null {
    return fieldErrorAt(this.saveError(), pointer);
  }

  /** The display name is required and at most 128 characters (R-UI-041); the server's word wins when it refused. */
  protected displayNameError(): string | null {
    const server = this.serverError('/displayName');
    if (server) return server;
    return this.attempted() && !this.displayNameValid()
      ? $localize`:@@project-dialog.display-name-invalid:The display name is required, 1 to 128 characters.`
      : null;
  }

  private displayNameValid(): boolean {
    const name = this.displayName().trim();
    return name.length >= 1 && name.length <= 128;
  }

  protected async save(event: Event): Promise<void> {
    event.preventDefault();
    this.attempted.set(true);
    const current = this.versioned();
    if (!current || !this.dirty() || !this.displayNameValid()) return;
    const stored = current.value;
    const body = { displayName: this.displayName().trim(), description: this.description().trim() };
    const patch: Record<string, unknown> = {};
    if (body.displayName !== stored.displayName) patch['displayName'] = body.displayName;
    if (body.description !== (stored.description ?? '')) patch['description'] = body.description;
    this.busy.set(true);
    this.saveError.set(null);
    this.actionError.set(null);
    try {
      const result = await this.conflicts.save<Project>({
        etag: current.etag,
        patch,
        send: (etag) => this.api.replace(stored.key, { key: stored.key, ...body }, etag),
        reload: () => this.api.get(stored.key),
      });
      if (result.outcome === 'dismissed') return;
      this.show(result.current);
      if (result.outcome === 'reloaded') return;
      this.attempted.set(false);
      void this.context.refresh();
      this.notifier.status($localize`:@@project.saved:Project saved.`);
    } catch (e) {
      const failure = asApiError(e);
      this.saveError.set(failure);
      this.actionError.set(failure);
    } finally {
      this.busy.set(false);
    }
  }

  protected async remove(): Promise<void> {
    const p = this.record();
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
