import { Component, computed, inject, resource, signal } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatCheckboxModule } from '@angular/material/checkbox';
import { MAT_DIALOG_DATA, MatDialogModule, MatDialogRef } from '@angular/material/dialog';
import { newIdempotencyKey } from '../../api/client';
import { IdentityApi } from '../../api/identity-api';
import { ApiError } from '../../api/problem';
import { PROJECT_KEY_PATTERN, Project, ProjectInput, ProjectsApi } from '../../api/projects-api';
import { RepositoriesApi, RepositorySummary, Versioned } from '../../api/repositories-api';
import { ProjectFilter } from '../../core/project-filter';
import { ConflictResolver } from '../../shared/conflict-dialog';
import { Confirm } from '../../shared/confirm-dialog';
import { ErrorNotice } from '../../shared/error-notice';
import { fieldErrorAt } from '../../shared/field-errors';
import { asApiError, valueOf } from '../../shared/paged-resource';
import { SelectField, SelectOption, TextField } from '../../shared/access/fields';
import { PickablePrincipal, PrincipalPicker } from './principal-picker';

/** What the dialog is opened with: nothing to create a project, the loaded project to edit it. */
export interface ProjectDialogData {
  readonly project?: Versioned<Project>;
}

/**
 * Create and edit a project (R-UI-041, R-AUTHZ-026; the caller holds `projects:write`). The key is
 * fixed at creation. Saving is a sequence of the API's operations: the project itself (`POST`, or `PUT`
 * with `If-Match`; a 412 opens the conflict dialog of R-UI-042), then the administrators it gained or
 * lost, then the repositories it gained or lost. Each step that succeeded moves the baseline, so a
 * failure in the middle leaves the dialog open on the rest and a second click repeats only that rest;
 * a project that was just created is no longer created again. A repository that belongs to another
 * project is listed with that project and is moved only because the person chose it (`?move=true`);
 * the owners are read again right before the assignments, and a repository whose owner is not the one
 * the dialog showed when it was chosen is asked about first (`move=true` only for a seen owner), and every
 * assignment names the owner it read (`from`), so a move in between answers 409 and asks again.
 * The display name and the key are checked before the first request; the idempotency key of the create
 * is replaced after a refusal, because the server stores a 4xx outcome under it.
 */
@Component({
  selector: 'trove-project-dialog',
  imports: [MatButtonModule, MatCheckboxModule, MatDialogModule, ErrorNotice, PrincipalPicker, SelectField, TextField],
  template: `
    <h2 mat-dialog-title>
      @if (existing(); as p) {
        <ng-container i18n="@@project-dialog.title-edit">Edit project {{ p.value.key }}</ng-container>
      } @else {
        <ng-container i18n="@@project-dialog.title-new">New project</ng-container>
      }
    </h2>
    <form (submit)="submit($event)" novalidate>
      <mat-dialog-content>
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
          hint="Lower-case letters, digits and -, two to 32 characters, starting with a letter. Cannot be changed later."
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

        <fieldset class="group">
          <legend i18n="@@project-dialog.switches">What the administrators may do</legend>
          <mat-checkbox [checked]="managePolicies()" (change)="managePolicies.set($event.checked)">
            <ng-container i18n="@@project-dialog.manage-policies">Manage policies and service accounts</ng-container>
          </mat-checkbox>
          <p class="hint" i18n="@@project-dialog.manage-policies-hint">
            Create and change the project's permission policies, create and enable its service accounts, issue their tokens and look up users and groups.
          </p>
          <mat-checkbox [checked]="manageRepositories()" (change)="manageRepositories.set($event.checked)">
            <ng-container i18n="@@project-dialog.manage-repositories">Manage repositories</ng-container>
          </mat-checkbox>
          <p class="hint" i18n="@@project-dialog.manage-repositories-hint">
            Create, change and delete the project's repositories, also remote ones that reach other servers. Assigning repositories stays with you.
          </p>
        </fieldset>

        <fieldset class="group">
          <legend i18n="@@project-dialog.admins">Administrators</legend>
          @if (admins().length === 0) {
            <p class="hint" i18n="@@project-dialog.no-admins">No administrator yet. Users and groups can administer a project.</p>
          } @else {
            <ul class="chips" [attr.aria-label]="adminsLabel">
              @for (a of admins(); track a) {
                <li>
                  <span translate="no">{{ a }}</span>
                  <button mat-icon-button type="button" (click)="removeAdmin(a)">
                    <span class="material-icons" aria-hidden="true">close</span>
                    <span class="visually-hidden"><ng-container i18n="@@project-dialog.remove-admin">Remove administrator</ng-container> {{ a }}</span>
                  </button>
                </li>
              }
            </ul>
          }
          <trove-principal-picker
            i18n-label="@@project-dialog.find-admin"
            label="Find a user or group"
            name="admin-search"
            [search]="searchIdentities"
            [exclude]="admins()"
            (picked)="addAdmin($event)"
          />
        </fieldset>

        <fieldset class="group">
          <legend i18n="@@project-dialog.repositories">Repositories</legend>
          @if (repositories().length === 0) {
            <p class="hint" i18n="@@project-dialog.no-repositories">No repository is assigned.</p>
          } @else {
            <ul class="chips" [attr.aria-label]="repositoriesLabel">
              @for (r of repositories(); track r) {
                <li>
                  <span translate="no">{{ r }}</span>
                  @if (movedFrom(r); as from) {
                    <span class="move" i18n="@@project-dialog.moves">moves from {{ from }}</span>
                  }
                  <button mat-icon-button type="button" (click)="removeRepository(r)">
                    <span class="material-icons" aria-hidden="true">close</span>
                    <span class="visually-hidden"><ng-container i18n="@@project-dialog.remove-repository">Remove repository</ng-container> {{ r }}</span>
                  </button>
                </li>
              }
            </ul>
          }
          <trove-select-field
            i18n-label="@@project-dialog.add-repository"
            label="Add a repository"
            name="add-repository"
            [options]="repositoryOptions()"
            [value]="''"
            (valueChange)="addRepository($event)"
          />
        </fieldset>
      </mat-dialog-content>
      <mat-dialog-actions align="end">
        <button mat-button type="button" mat-dialog-close i18n="@@project-dialog.cancel">Cancel</button>
        <button mat-flat-button type="submit" [disabled]="busy()">
          @if (existing()) {
            <ng-container i18n="@@project-dialog.save">Save project</ng-container>
          } @else {
            <ng-container i18n="@@project-dialog.create">Create project</ng-container>
          }
        </button>
      </mat-dialog-actions>
    </form>
  `,
  styleUrl: '../../shared/access/access.scss',
  styles: `
    .group {
      margin: 0 0 var(--trove-space-3);
      padding: var(--trove-space-2) var(--trove-space-3);
      border: 1px solid var(--mat-sys-outline-variant);
      border-radius: var(--mat-sys-corner-small);
    }
    .hint {
      margin: 0 0 var(--trove-space-2);
      color: var(--mat-sys-on-surface-variant);
      font: var(--mat-sys-body-small);
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
      border: 1px solid var(--mat-sys-outline);
      border-radius: var(--mat-sys-corner-full);
    }
    .move {
      color: var(--mat-sys-on-surface-variant);
      font: var(--mat-sys-label-small);
    }
  `,
})
export class ProjectDialog {
  private readonly api = inject(ProjectsApi);
  private readonly identity = inject(IdentityApi);
  private readonly repositoriesApi = inject(RepositoriesApi);
  private readonly conflicts = inject(ConflictResolver);
  private readonly confirm = inject(Confirm);
  private readonly filter = inject(ProjectFilter);
  private readonly ref = inject<MatDialogRef<ProjectDialog, Project>>(MatDialogRef);
  private readonly data = inject<ProjectDialogData>(MAT_DIALOG_DATA);
  /** Replaced after every refusal of the create: the server stores a 4xx outcome under its key (R-API-014). */
  private idempotencyKey = newIdempotencyKey();

  /** The project once it exists: the one being edited, or the one this dialog just created. */
  protected readonly existing = signal<Versioned<Project> | null>(this.data.project ?? null);
  protected readonly key = signal(this.data.project?.value.key ?? '');
  protected readonly displayName = signal(this.data.project?.value.displayName ?? '');
  protected readonly description = signal(this.data.project?.value.description ?? '');
  protected readonly managePolicies = signal(this.data.project?.value.managePolicies ?? true);
  protected readonly manageRepositories = signal(this.data.project?.value.manageRepositories ?? false);
  protected readonly admins = signal<readonly string[]>(this.data.project?.value.admins ?? []);
  protected readonly repositories = signal<readonly string[]>([]);
  protected readonly busy = signal(false);
  protected readonly error = signal<ApiError | null>(null);
  private readonly attempted = signal(false);

  /** What the server holds of the administrators and the repositories: the baseline of the diff. */
  private storedAdmins: readonly string[] = this.data.project?.value.admins ?? [];
  private storedRepositories: readonly string[] = [];
  /** The owner each added repository showed when it was chosen; null when it was unassigned. */
  private readonly shownOwner = new Map<string, string | null>();

  protected readonly adminsLabel = $localize`:@@project-dialog.admins-label:Administrators of the project`;
  protected readonly repositoriesLabel = $localize`:@@project-dialog.repositories-label:Repositories of the project`;

  /** Every repository, with the project it belongs to: the choices and the "moves from" notes. */
  private readonly all = resource({ loader: () => this.repositoriesApi.listAll() });
  /** The summaries read again after a 409 `repository_assigned`: they replace the ones loaded when the dialog opened. */
  private readonly fresh = signal<readonly RepositorySummary[] | null>(null);
  private readonly summaries = computed<readonly RepositorySummary[]>(() => this.fresh() ?? valueOf(this.all) ?? []);

  protected readonly repositoryOptions = computed<readonly SelectOption[]>(() => {
    const chosen = new Set(this.repositories());
    return [
      { value: '', label: $localize`:@@project-dialog.choose-repository:Choose a repository` },
      ...this.summaries()
        .filter((r) => !chosen.has(r.key))
        .map((r) => ({
          value: r.key,
          label:
            r.project && r.project !== this.key()
              ? $localize`:@@project-dialog.repository-of:${r.key}:key: (belongs to ${r.project}:project:)`
              : r.key,
        })),
    ];
  });

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

  constructor() {
    const project = this.data.project?.value;
    if (project) {
      // The repositories already assigned: the baseline of the repository diff.
      void this.repositoriesApi
        .list({ project: project.key })(null, 500)
        .then((page) => {
          const keys = page.items.map((r) => r.key);
          this.storedRepositories = keys;
          // A repository the person added before the baseline arrived stays in the list.
          this.repositories.update((list) => [...keys, ...list.filter((r) => !keys.includes(r))]);
        })
        .catch((e: unknown) => this.error.set(asApiError(e)));
    }
  }

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

  /** The project a repository is taken from when this dialog is saved, or null. */
  protected movedFrom(repository: string): string | null {
    const from = this.summaries().find((r) => r.key === repository)?.project ?? null;
    return from !== null && from !== this.key() && !this.storedRepositories.includes(repository) ? from : null;
  }

  protected addAdmin(ref: string): void {
    this.admins.update((list) => (list.includes(ref) ? list : [...list, ref]));
  }

  protected removeAdmin(ref: string): void {
    this.admins.update((list) => list.filter((a) => a !== ref));
  }

  protected addRepository(key: string): void {
    if (!key || this.repositories().includes(key)) return;
    // What the person is shown at this moment ("moves from"): the only owner a move is agreed for.
    this.shownOwner.set(key, this.ownerOf(key));
    this.repositories.update((list) => [...list, key]);
  }

  protected removeRepository(key: string): void {
    this.repositories.update((list) => list.filter((r) => r !== key));
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
      await this.saveAdmins();
      await this.saveRepositories();
      const key = this.existing()?.value.key ?? this.key().trim();
      this.filter.refresh().catch(() => undefined);
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
      managePolicies: this.managePolicies(),
      manageRepositories: this.manageRepositories(),
    };
  }

  /** Creates or replaces the project; false when the person left the conflict dialog without a choice. */
  private async saveProject(): Promise<boolean> {
    const current = this.existing();
    if (!current) {
      let created: Versioned<Project>;
      try {
        created = await this.api.create({ key: this.key().trim(), ...this.body() }, this.idempotencyKey);
      } catch (e) {
        // The refusal is stored under the key; a corrected retry has another body and needs another key.
        // A request still in progress is the same intent: its key stays.
        if (e instanceof ApiError && e.status >= 400 && e.status < 500 && e.code !== 'idempotency_in_progress') {
          this.idempotencyKey = newIdempotencyKey();
        }
        throw e;
      }
      this.existing.set(created);
      this.storedAdmins = created.value.admins;
      return true;
    }
    const body = this.body();
    const stored = current.value;
    const patch: Record<string, unknown> = {};
    if (body.displayName !== stored.displayName) patch['displayName'] = body.displayName;
    if (body.description !== stored.description) patch['description'] = body.description;
    if (body.managePolicies !== stored.managePolicies) patch['managePolicies'] = body.managePolicies;
    if (body.manageRepositories !== stored.manageRepositories) patch['manageRepositories'] = body.manageRepositories;
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
      this.managePolicies.set(p.managePolicies);
      this.manageRepositories.set(p.manageRepositories);
      return false;
    }
    return true;
  }

  private async saveAdmins(): Promise<void> {
    const key = this.existing()?.value.key ?? '';
    const wanted = this.admins();
    for (const a of wanted.filter((x) => !this.storedAdmins.includes(x))) {
      await this.api.addAdmin(key, a);
      this.storedAdmins = [...this.storedAdmins, a];
    }
    for (const a of this.storedAdmins.filter((x) => !wanted.includes(x))) {
      await settleGone(() => this.api.removeAdmin(key, a));
      this.storedAdmins = this.storedAdmins.filter((x) => x !== a);
    }
  }

  private async saveRepositories(): Promise<void> {
    const key = this.existing()?.value.key ?? '';
    const wanted = this.repositories();
    const added = wanted.filter((x) => !this.storedRepositories.includes(x));
    // The owners are read again right before the assignments: what the dialog showed may be old.
    if (added.length > 0) this.fresh.set(await this.repositoriesApi.listAll());
    for (const r of added) {
      await this.assign(key, r);
      this.storedRepositories = [...this.storedRepositories, r];
    }
    for (const r of this.storedRepositories.filter((x) => !wanted.includes(x))) {
      await settleGone(() => this.api.unassignRepository(key, r));
      this.storedRepositories = this.storedRepositories.filter((x) => x !== r);
    }
  }

  private ownerOf(repository: string): string | null {
    return this.summaries().find((s) => s.key === repository)?.project ?? null;
  }

  /**
   * Assigns one repository. `move=true` is only ever sent for an owner the person saw: the one the
   * dialog showed when the repository was chosen ("moves from"). A different owner now reads the owner
   * again and asks first. The request names the owner that was read (`from`, empty for none); a 409
   * `project_owner_changed` (the owner changed in the window after the read) or `repository_assigned`
   * reads the owner again and asks about the one that holds it now (Q-AUTHZ-205).
   */
  private async assign(key: string, repository: string): Promise<void> {
    const owner = this.ownerOf(repository);
    if (owner === key) return;
    if (owner !== null && owner !== (this.shownOwner.get(repository) ?? null)) {
      await this.askToMove(key, repository, owner);
    }
    try {
      await this.api.assignRepository(key, repository, owner !== null, owner);
    } catch (e) {
      if (!(e instanceof ApiError) || (e.code !== 'repository_assigned' && e.code !== 'project_owner_changed')) throw e;
      this.fresh.set(await this.repositoriesApi.listAll());
      const now = this.ownerOf(repository);
      if (now === key) return;
      if (now !== null) await this.askToMove(key, repository, now);
      await this.api.assignRepository(key, repository, now !== null, now);
    }
  }

  /** Asks whether to take the repository from `owner`; declining stops the save with the 409 notice. */
  private async askToMove(key: string, repository: string, owner: string): Promise<void> {
    const answer = await this.confirm.ask({
      title: $localize`:@@project-dialog.move-title:Move ${repository}:repository: to ${key}:project:?`,
      lines: [
        $localize`:@@project-dialog.move-line:The repository now belongs to project ${owner}:owner:. Moving it takes it away from that project.`,
      ],
      confirmLabel: $localize`:@@project-dialog.move-confirm:Move repository`,
    });
    if (!answer) throw new ApiError(409, { code: 'repository_assigned' }, null);
  }
}

/** Runs a removal; a 404 means it is already gone, which is what the step wants. */
async function settleGone(step: () => Promise<void>): Promise<void> {
  try {
    await step();
  } catch (e) {
    if (!(e instanceof ApiError && e.status === 404)) throw e;
  }
}
