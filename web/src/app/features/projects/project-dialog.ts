import { Component, inject, signal } from '@angular/core';
import { DIALOG_DATA, DialogClose, DialogRef, DialogTitle } from '../../shared/ui/dialog';
import { newIdempotencyKey } from '../../api/client';
import { IdentityApi } from '../../api/identity-api';
import { ApiError } from '../../api/problem';
import { PROJECT_KEY_PATTERN, Project, ProjectInput, ProjectsApi } from '../../api/projects-api';
import { Versioned } from '../../api/repositories-api';
import { ProjectContext } from '../../core/project-context';
import { ConflictResolver } from '../../shared/conflict-dialog';
import { ErrorNotice } from '../../shared/error-notice';
import { fieldErrorAt } from '../../shared/field-errors';
import { asApiError } from '../../shared/paged-resource';
import { TextField } from '../../shared/access/fields';
import { PickablePrincipal, PrincipalPicker } from './principal-picker';

/** What the dialog is opened with: nothing to create a project, the loaded project to edit it. */
export interface ProjectDialogData {
  readonly project?: Versioned<Project>;
}

/**
 * Create and edit a project (R-UI-041, R-AUTHZ-026). The key is fixed at creation. A new project names its first
 * Owners (`owners`, Q-AUTHZ-229; the caller holds `projects:write`); an edit changes the display name and the
 * description (`PUT` with `If-Match`; a 412 opens the conflict dialog of R-UI-042), by an Owner or an administrator of
 * trove. The members and their roles are changed on the project's screen, the repositories are created in the
 * project. The display name and the key are checked before the first request; the idempotency key of the create is
 * replaced after a refusal, because the server stores a 4xx outcome under it.
 */
@Component({
  selector: 'trove-project-dialog',
  imports: [DialogTitle, DialogClose, ErrorNotice, PrincipalPicker, TextField],
  template: `
    <h2 troveDialogTitle>
      @if (existing(); as p) {
        <ng-container i18n="@@project-dialog.title-edit">Edit project {{ p.value.key }}</ng-container>
      } @else {
        <ng-container i18n="@@project-dialog.title-new">New project</ng-container>
      }
    </h2>
    <form (submit)="submit($event)" novalidate>
      <div class="dialog-body">
        @if (error(); as e) {
          <trove-error-notice [error]="e" />
        }
        <trove-text-field
          i18n-label="@@project-dialog.key"
          label="Key"
          name="key"
          [(value)]="key"
          [required]="true"
          [readonly]="existing() !== null"
          [error]="keyError()"
          i18n-hint="@@project-dialog.key-hint"
          hint="Lower-case letters, digits and -, two to 32 characters, starting with a letter and ending with a letter or digit. Cannot be changed later."
        />
        <trove-text-field
          i18n-label="@@project-dialog.display-name"
          label="Display name"
          name="displayName"
          [(value)]="displayName"
          [required]="true"
          [error]="displayNameError()"
        />
        <trove-text-field
          i18n-label="@@project-dialog.description"
          label="Description"
          name="description"
          type="textarea"
          [(value)]="description"
          [error]="fieldError('/description')"
        />

        @if (!existing()) {
          <fieldset class="group">
            <legend i18n="@@project-dialog.owners">First Owners</legend>
            @if (owners().length === 0) {
              <p class="hint" i18n="@@project-dialog.no-owners">
                No Owner yet. Users and groups can own a project; members are added on the project's screen.
              </p>
            } @else {
              <ul class="chips" [attr.aria-label]="ownersLabel">
                @for (a of owners(); track a) {
                  <li>
                    <span translate="no">{{ a }}</span>
                    <button class="btn btn-icon" type="button" (click)="removeOwner(a)">
                      <span class="material-icons" aria-hidden="true">close</span>
                      <span class="visually-hidden"><ng-container i18n="@@project-dialog.remove-owner">Remove owner</ng-container> {{ a }}</span>
                    </button>
                  </li>
                }
              </ul>
            }
            <trove-principal-picker
              i18n-label="@@project-dialog.find-owner"
              label="Find a user or group"
              name="owner-search"
              [search]="searchIdentities"
              [exclude]="owners()"
              (picked)="addOwner($event)"
            />
          </fieldset>
        }
      </div>
      <div class="dialog-actions">
        <button class="btn btn-tertiary" type="button" troveDialogClose i18n="@@project-dialog.cancel">Cancel</button>
        <button class="btn btn-confirm" type="submit" [disabled]="busy()">
          @if (existing()) {
            <ng-container i18n="@@project-dialog.save">Save project</ng-container>
          } @else {
            <ng-container i18n="@@project-dialog.create">Create project</ng-container>
          }
        </button>
      </div>
    </form>
  `,
  styleUrl: '../../shared/access/access.scss',
  styles: `
    .group {
      margin: 0 0 var(--trove-space-3);
      padding: var(--trove-space-2) var(--trove-space-3);
      border: 1px solid var(--trove-border);
      border-radius: var(--trove-radius-m);
    }
    .hint {
      margin: 0 0 var(--trove-space-2);
      color: var(--trove-text-muted);
      font: var(--trove-type-body-small);
    }
    .chips {
      display: flex;
      flex-wrap: wrap;
      gap: var(--trove-space-2);
      margin: 0 0 var(--trove-space-2);
      padding: 0;
      list-style: none;
    }
    .chips li {
      display: inline-flex;
      align-items: center;
      gap: var(--trove-space-1);
      padding-inline: var(--trove-space-3) var(--trove-space-1);
      border: 1px solid var(--trove-border-strong);
      border-radius: var(--trove-radius-full);
    }
    .move {
      color: var(--trove-text-muted);
      font: var(--trove-type-label-small);
    }
  `,
})
export class ProjectDialog {
  private readonly api = inject(ProjectsApi);
  private readonly identity = inject(IdentityApi);
  private readonly conflicts = inject(ConflictResolver);
  private readonly context = inject(ProjectContext);
  private readonly ref = inject<DialogRef<Project, ProjectDialog>>(DialogRef);
  private readonly data = inject<ProjectDialogData>(DIALOG_DATA);
  /** Replaced after every refusal of the create: the server stores a 4xx outcome under its key (R-API-014). */
  private idempotencyKey = newIdempotencyKey();

  /** The project once it exists: the one being edited, or the one this dialog just created. */
  protected readonly existing = signal<Versioned<Project> | null>(this.data.project ?? null);
  protected readonly key = signal(this.data.project?.value.key ?? '');
  protected readonly displayName = signal(this.data.project?.value.displayName ?? '');
  protected readonly description = signal(this.data.project?.value.description ?? '');
  protected readonly owners = signal<readonly string[]>([]);
  protected readonly busy = signal(false);
  protected readonly error = signal<ApiError | null>(null);
  private readonly attempted = signal(false);

  protected readonly ownersLabel = $localize`:@@project-dialog.owners-label:First Owners of the project`;

  protected readonly searchIdentities = async (text: string): Promise<readonly PickablePrincipal[]> => {
    const [users, groups] = await Promise.all([
      this.identity.users({ q: text, status: 'active' })(null, 10),
      this.identity.groups({ q: text })(null, 10),
    ]);
    return [
      ...users.items.map((u): PickablePrincipal => ({ ref: `user:${u.username}`, kind: 'user' })),
      ...groups.items.map((g): PickablePrincipal => ({ ref: `group:${g.name}`, kind: 'group' })),
    ];
  };

  protected keyError(): string | null {
    const server = fieldErrorAt(this.error(), '/key');
    if (server) return server;
    if (this.existing() || !this.attempted()) return null;
    return PROJECT_KEY_PATTERN.test(this.key().trim())
      ? null
      : $localize`:@@project-dialog.key-invalid:The key does not match the pattern.`;
  }

  /** The display name is required and at most 128 characters (R-UI-041); the server's word wins when it refused. */
  protected displayNameError(): string | null {
    const server = fieldErrorAt(this.error(), '/displayName');
    if (server) return server;
    return this.attempted() && !this.displayNameValid()
      ? $localize`:@@project-dialog.display-name-invalid:The display name is required, 1 to 128 characters.`
      : null;
  }

  private displayNameValid(): boolean {
    const length = this.displayName().trim().length;
    return length >= 1 && length <= 128;
  }

  protected fieldError(pointer: string): string | null {
    return fieldErrorAt(this.error(), pointer);
  }

  protected addOwner(ref: string): void {
    this.owners.update((list) => (list.includes(ref) ? list : [...list, ref]));
  }

  protected removeOwner(ref: string): void {
    this.owners.update((list) => list.filter((a) => a !== ref));
  }

  protected async submit(event: Event): Promise<void> {
    event.preventDefault();
    this.attempted.set(true);
    const keyValid = this.existing() !== null || PROJECT_KEY_PATTERN.test(this.key().trim());
    if (!keyValid || !this.displayNameValid()) return;
    this.busy.set(true);
    this.error.set(null);
    try {
      if (!(await this.saveProject())) return;
      const key = this.existing()?.value.key ?? this.key().trim();
      this.context.refresh().catch(() => undefined);
      this.ref.close((await this.api.get(key)).value);
    } catch (e) {
      this.error.set(asApiError(e));
    } finally {
      this.busy.set(false);
    }
  }

  private body(): ProjectInput {
    return {
      displayName: this.displayName().trim(),
      description: this.description().trim(),
    };
  }

  /** Creates or replaces the project; false when the person left the conflict dialog without a choice. */
  private async saveProject(): Promise<boolean> {
    const current = this.existing();
    if (!current) {
      let created: Versioned<Project>;
      try {
        created = await this.api.create(
          { key: this.key().trim(), ...this.body(), ...(this.owners().length > 0 ? { owners: [...this.owners()] } : {}) },
          this.idempotencyKey,
        );
      } catch (e) {
        // The refusal is stored under the key; a corrected retry has another body and needs another key.
        // A request still in progress is the same intent: its key stays.
        if (e instanceof ApiError && e.status >= 400 && e.status < 500 && e.code !== 'idempotency_in_progress') {
          this.idempotencyKey = newIdempotencyKey();
        }
        throw e;
      }
      this.existing.set(created);
      return true;
    }
    const body = this.body();
    const stored = current.value;
    const patch: Record<string, unknown> = {};
    if (body.displayName !== stored.displayName) patch['displayName'] = body.displayName;
    if (body.description !== stored.description) patch['description'] = body.description;
    if (Object.keys(patch).length === 0) return true;
    const result = await this.conflicts.save<Project>({
      etag: current.etag,
      patch,
      send: (etag) => this.api.replace(stored.key, { key: stored.key, ...body }, etag),
      reload: () => this.api.get(stored.key),
    });
    if (result.outcome === 'dismissed') return false;
    this.existing.set(result.current);
    if (result.outcome === 'reloaded') {
      // "Reload (discard mine)": the fields show what is stored now; the person looks and saves again.
      const p = result.current.value;
      this.displayName.set(p.displayName);
      this.description.set(p.description);
      return false;
    }
    return true;
  }
}
