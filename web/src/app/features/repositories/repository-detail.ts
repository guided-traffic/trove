import { MemberChange, VirtualMembers } from './virtual-members';
import { DatePipe } from '@angular/common';
import {
  Component,
  computed,
  effect,
  inject,
  input,
  resource,
  signal,
  untracked,
} from '@angular/core';
import { TabContent, TabGroup, TabPage } from '../../shared/ui/tabs';
import { Router, RouterLink } from '@angular/router';
import { newIdempotencyKey } from '../../api/client';
import { ApiError, FieldProblem } from '../../api/problem';
import { collectAll } from '../../api/pagination';
import { RepositoryVisibility } from '../../api/repositories-api';
import { VisibilityIcon, VisibilityLevel } from '../../shared/ui/visibility-level';
import { ProjectsApi } from '../../api/projects-api';
import { SelectField, SelectOption } from '../../shared/access/fields';
import {
  JobReference,
  RepositoriesApi,
  Repository,
  RepositoryConsumerView,
  Versioned,
  isConsumerView,
  repoRef,
  repositoryUrl,
} from '../../api/repositories-api';
import { Notifier } from '../../core/notifier';
import { ServerAddress } from '../../core/server-address';
import { SessionStore } from '../../core/session-store';
import { Confirm } from '../../shared/confirm-dialog';
import { ConflictResolver } from '../../shared/conflict-dialog';
import { AddressField } from '../../shared/address-field';
import { ErrorNotice } from '../../shared/error-notice';
import { ErrorSummary, SummaryItem, fieldMessage } from '../../shared/field-errors';
import { DraftStore } from '../../shared/drafts';
import { JobProgress } from '../../shared/job-progress';
import { JsonObject, mergePatchOf } from '../../shared/merge-patch';
import { asApiError, valueOf } from '../../shared/paged-resource';
import { HasUnsavedChanges } from '../../shared/unsaved-guard';
import { FormatClient } from '../../api/guides-api';
import { AnsibleImportTasks } from './ansible-import-tasks';
import { AnsibleTombstones } from './ansible-tombstones';
import { DebIndexStatePanel } from './deb-index-state';
import { DebSigningKeys } from './deb-signing-keys';
import { DebUpstreamKeys } from './deb-upstream-keys';
import { FormatSettings } from './format-settings';
import { MirrorSettings, mirrorTarget } from './mirror-settings';
import { MirrorStatusPanel } from './mirror-status';
import { FrequentPlaces } from '../../core/frequent-places';
import { RepositoryFields } from './repository-fields';
import { Disclosure } from '../../shared/ui/disclosure';
import { RepositoryGuide } from './repository-guide';
import { RepositoryVisibilityBlock } from './repository-visibility';
import {
  FormatMemberOf,
  RepositoryForm,
  apiAddress,
  clientAddress,
  curlOf,
  formOf,
  maskSecrets,
  settingsOf,
} from './repository-model';
import { upstreamDefaultsOf } from './upstream-model';
import { classLabel } from './repository-class';
import { SettingSchema, errorTarget, settingNodes } from './settings-schema';
import { UpstreamSettings, upstreamTarget } from './upstream-settings';

/**
 * The edit form of a repository (R-UI-021): key, class and format are read-only; the settings are
 * sections in tabs; save sends a JSON merge patch with the loaded `ETag`. A 412 opens the conflict
 * dialog; a retried write that answers 412 because its first attempt committed is recognised by
 * comparing the stored settings with the draft (R-UI-042). Leaving with unsaved changes asks first.
 */
@Component({
  selector: 'trove-repository-detail',
  imports: [
    VirtualMembers,
    DatePipe,
    TabContent, TabGroup, TabPage,
    RouterLink,
    AddressField,
    Disclosure,
    ErrorNotice,
    ErrorSummary,
    JobProgress,
    SelectField,
    AnsibleImportTasks,
    AnsibleTombstones,
    DebIndexStatePanel,
    DebSigningKeys,
    DebUpstreamKeys,
    FormatSettings,
    RepositoryFields,
    RepositoryGuide,
    RepositoryVisibilityBlock,
    VisibilityIcon,
    MirrorSettings,
    MirrorStatusPanel,
    UpstreamSettings,
  ],
  templateUrl: './repository-detail.html',
  styleUrl: './repository-detail.scss',
})
export class RepositoryDetail implements HasUnsavedChanges {
  private readonly api = inject(RepositoriesApi);
  private readonly projectsApi = inject(ProjectsApi);
  private readonly router = inject(Router);
  private readonly session = inject(SessionStore);
  private readonly notifier = inject(Notifier);
  private readonly confirm = inject(Confirm);
  private readonly conflicts = inject(ConflictResolver);
  private readonly drafts = inject(DraftStore);
  private readonly frequent = inject(FrequentPlaces);
  private readonly serverAddress = inject(ServerAddress);

  /** The route parameters `:project` and `:key`, bound by the router: a repository is addressed inside its project. */
  readonly project = input.required<string>();
  readonly key = input.required<string>();
  /** The qualified name `<project>/<key>` every API call of the screen uses. */
  protected readonly ref = computed(() => repoRef(this.project(), this.key()));

  protected readonly formats = resource({ loader: () => this.api.formats() });
  private readonly baseUrl = resource({ loader: () => this.serverAddress.baseUrl() });
  protected readonly loaded = resource({
    params: () => this.ref(),
    loader: ({ params }) => this.api.get(params),
  });

  /** The settings as stored, in form shape; the baseline of the dirty check and of the patch. */
  protected readonly baseline = signal<RepositoryForm | null>(null);
  protected readonly form = signal<RepositoryForm | null>(null);
  private etag = '';
  protected readonly stored = signal<Repository | null>(null);
  protected readonly consumer = signal<RepositoryConsumerView | null>(null);
  protected readonly saving = signal(false);
  /** The artifacts level, shown as an icon before the name (Q-UI-230). */
  protected readonly artifactsLevel = computed<VisibilityLevel | null>(
    () => (this.stored() ?? this.consumer())?.visibility?.artifacts ?? null,
  );
  protected readonly failure = signal<ApiError | null>(null);
  protected readonly warnings = signal<readonly string[]>([]);
  protected readonly deletion = signal<JobReference | null>(null);

  /** The capability, or the Owner role in the repository's project (R-AUTHZ-026). */
  protected readonly canWrite = computed(() => this.session.canWriteRepositories(this.stored()?.project));
  protected readonly loadFailure = computed(() => {
    const e = this.loaded.error();
    return e ? asApiError(e) : null;
  });
  protected readonly fieldErrors = computed<ReadonlyMap<string, FieldProblem>>(
    () => this.failure()?.fieldErrors ?? new Map(),
  );
  protected readonly summary = computed<readonly SummaryItem[]>(() =>
    [...this.fieldErrors()].map(([pointer, problem]) => ({
      label: pointer,
      message: fieldMessage(problem),
      targetId: this.pointerTarget(pointer),
    })),
  );
  protected readonly format = computed(
    () => (valueOf(this.formats) ?? []).find((f) => f.name === this.stored()?.format) ?? null,
  );
  /** The format member of the document and its schema; the schema is unknown until the formats load. */
  private readonly member = computed<FormatMemberOf>(() => ({
    name: this.stored()?.format ?? '',
    schema: this.format()?.settingsSchema as SettingSchema | null | undefined,
    ...(this.upstreamSection()
      ? {
          remote: 'structural' as const,
          mirror: this.mirrorSection(),
          upstream: upstreamDefaultsOf(this.format()),
        }
      : {}),
  }));
  /** The upstream settings: a remote repository (R-UI-021, F-54). */
  protected readonly upstreamSection = computed(() => this.stored()?.class === 'remote');
  /** The status panel: a remote repository that is stored in mode `mirror` (Q-UI-104, Q-UI-119). */
  protected readonly mirrorStatusShown = computed(
    () => this.mirrorSection() && this.stored()?.remote?.mode === 'mirror',
  );
  /** Changes when the document changed, so the status panel reads the status again. */
  protected readonly mirrorRefresh = signal(0);
  /** A pause or a resume is being written. */
  protected readonly pausing = signal(false);
  /** The Mirror section: a remote repository of a format that can mirror (R-UI-021, Q-UI-104). */
  protected readonly mirrorSection = computed(
    () => this.stored()?.class === 'remote' && this.format()?.mirror === true,
  );
  /** An Owner without the capability keeps what is stored, pauses and turns the mirror off (Q-REPO-142). */
  protected readonly mirrorRestricted = computed(
    () => !this.session.has('repositories:write-config'),
  );
  /** The signing keys and the index state: a local or virtual Debian repository (R-DEB-014, R-DEB-015, R-UI-021). */
  protected readonly debSection = computed(() => {
    const repo = this.stored();
    return repo?.format === 'deb' && repo.class !== 'remote';
  });
  /** The import tasks: a local or virtual Ansible Galaxy repository; a remote one has none (R-ANS-009, R-ANS-026). */
  protected readonly ansibleImportsSection = computed(() => {
    const repo = this.stored();
    return repo?.format === 'ansible' && repo.class !== 'remote';
  });
  /** The tombstones: a local Ansible Galaxy repository (R-ANS-012, R-ANS-026). */
  protected readonly ansibleTombstonesSection = computed(() => {
    const repo = this.stored();
    return repo?.format === 'ansible' && repo.class === 'local';
  });
  /** The fingerprints of a remote Debian repository's upstream keys, read back beside `upstreamKeys` (R-DEB-020). */
  protected readonly debUpstreamFingerprints = computed<readonly string[]>(() => {
    const repo = this.stored();
    if (repo?.format !== 'deb' || repo.class !== 'remote') return [];
    return repo.deb?.upstreamKeyFingerprints ?? [];
  });
  protected readonly memberPointer = computed(() => `/${this.member().name}`);
  protected readonly settingNodes = computed(() => {
    const repo = this.stored();
    return repo ? settingNodes(this.member().schema, repo.class, repo.format) : [];
  });
  protected readonly patch = computed<JsonObject>(() => {
    const base = this.baseline();
    const now = this.form();
    const m = this.member();
    return base && now ? mergePatchOf(settingsOf(base, m), settingsOf(now, m)) : {};
  });
  protected readonly dirty = computed(() => Object.keys(this.patch()).length > 0);
  protected readonly address = computed(() =>
    clientAddress(valueOf(this.baseUrl), this.format() ?? undefined, this.ref()),
  );
  protected readonly documentJson = computed(() => JSON.stringify(this.stored(), null, 2));
  protected readonly patchJson = computed(() => JSON.stringify(this.patch(), null, 2));
  protected readonly curl = computed(() =>
    curlOf(
      'PATCH',
      apiAddress(valueOf(this.baseUrl), repositoryUrl(this.ref())),
      maskSecrets(this.patch()),
      [`If-Match: ${this.etag}`],
    ),
  );

  constructor() {
    effect(() => {
      const value = valueOf(this.loaded);
      if (value) untracked(() => this.adopt(value));
    });
    // A form that asks for a recent sign-in keeps its non-secret draft across the redirect.
    effect(() => {
      if (!this.session.reauthRequired()) return;
      const form = untracked(() => this.form());
      // The password is a secret: it never goes to storage (R-UI-043).
      if (form) this.drafts.keep({ ...form, upstream: { ...form.upstream, password: '' } });
    });
  }

  hasUnsavedChanges(): boolean {
    return this.dirty();
  }

  protected classText = classLabel;

  protected setForm(form: RepositoryForm): void {
    this.form.set(form);
  }

  protected setFormatMember(formatMember: JsonObject): void {
    this.form.update((f) => (f ? { ...f, formatMember } : f));
  }

  private pointerTarget(pointer: string): string {
    if (this.upstreamSection()) {
      const target = upstreamTarget(pointer);
      if (target) return target;
    }
    if (this.mirrorSection()) {
      const target = mirrorTarget(pointer);
      if (target) return target;
    }
    return errorTarget(pointer, this.settingNodes(), this.memberPointer());
  }

  /** Takes a freshly read representation as the new baseline; a saved draft is restored once. */
  private adopt(value: Versioned<Repository | RepositoryConsumerView>): void {
    this.frequent.visitRepository(this.project(), this.key());
    this.etag = value.etag;
    if (isConsumerView(value.value)) {
      this.consumer.set(value.value);
      this.stored.set(null);
      this.form.set(null);
      this.baseline.set(null);
      return;
    }
    this.consumer.set(null);
    this.stored.set(value.value);
    const stored = formOf(value.value);
    this.baseline.set(stored);
    this.form.set(this.drafts.take<RepositoryForm>() ?? stored);
    this.warnings.set([]);
  }

  /** The project's level bounds the repository's (R-UI-055); unknown when the caller cannot read the project, then the server decides. */
  protected readonly projectLevel = resource({
    params: () => this.project(),
    loader: async ({ params }): Promise<VisibilityLevel | null> => {
      try {
        return (await this.projectsApi.get(params)).value.visibility;
      } catch {
        return null;
      }
    },
  });
  protected readonly visibilitySaving = signal(false);
  protected readonly visibilityFailure = signal<ApiError | null>(null);
  protected readonly visibilityWarnings = signal<readonly string[]>([]);

  /**
   * Stores the levels of the Visibility block (R-UI-055) with the loaded `ETag`. The patch names the changed parts only and the
   * levels are not part of the form, so other unsaved edits stay; a 412 is settled like a save (R-UI-042).
   */
  protected async saveVisibility(change: Partial<RepositoryVisibility>): Promise<void> {
    if (this.visibilitySaving() || !this.stored()) return;
    this.visibilitySaving.set(true);
    this.visibilityFailure.set(null);
    try {
      await this.sendVisibility(change, this.etag);
    } catch (e) {
      await this.handleVisibilityError(asApiError(e), change);
    } finally {
      this.visibilitySaving.set(false);
    }
  }

  private async sendVisibility(change: Partial<RepositoryVisibility>, etag: string): Promise<void> {
    const saved = await this.api.patch(this.ref(), { visibility: change }, etag);
    this.takeStored(saved.value, saved.etag);
    this.visibilityWarnings.set(
      (saved.value.warnings ?? []).filter((w) => w.code === 'public_populates_remote').map((w) => w.detail),
    );
    this.notifier.status($localize`:@@repo.visibility.saved:Visibility saved.`);
  }

  private async handleVisibilityError(error: ApiError, change: Partial<RepositoryVisibility>): Promise<void> {
    if (error.status !== 412) {
      this.visibilityFailure.set(error);
      return;
    }
    let theirs: Versioned<Repository | RepositoryConsumerView>;
    try {
      theirs = await this.api.get(this.ref());
    } catch (e) {
      this.visibilityFailure.set(asApiError(e));
      return;
    }
    if (isConsumerView(theirs.value)) {
      this.visibilityFailure.set(error);
      return;
    }
    const current = theirs.value.visibility;
    const wanted = { ...current, ...change };
    if (JSON.stringify(current) === JSON.stringify(wanted)) {
      this.takeStored(theirs.value, theirs.etag);
      return;
    }
    const decision = await this.conflicts.resolve({ mine: { ...wanted }, theirs: { ...current } });
    if (decision === 'reload') {
      this.adopt(theirs);
    } else if (decision === 'overwrite') {
      try {
        await this.sendVisibility(change, theirs.etag);
      } catch (e) {
        this.visibilityFailure.set(asApiError(e));
      }
    }
  }

  protected readonly membersSaving = signal(false);
  protected readonly membersFailure = signal<ApiError | null>(null);

  /**
   * Stores the members and the default deployment target of a virtual repository (R-UI-022) with the loaded `ETag`. The patch carries
   * these two members only, so other unsaved edits of the form stay; a 412 or a refusal is shown by the member editor.
   */
  protected async saveMembers(change: MemberChange): Promise<void> {
    if (this.membersSaving() || !this.stored()) return;
    this.membersSaving.set(true);
    this.membersFailure.set(null);
    try {
      const saved = await this.api.patch(
        this.ref(),
        { virtual: { members: [...change.members], deployTarget: change.deployTarget } },
        this.etag,
      );
      this.takeStored(saved.value, saved.etag);
      this.notifier.status($localize`:@@repo.members.saved:Members saved.`);
    } catch (e) {
      this.membersFailure.set(asApiError(e));
    } finally {
      this.membersSaving.set(false);
    }
  }

  protected async save(): Promise<void> {
    const base = this.baseline();
    const mine = this.form();
    if (!base || !mine || !this.dirty() || this.saving()) return;
    this.saving.set(true);
    this.failure.set(null);
    try {
      await this.send(this.patch(), this.etag);
    } catch (e) {
      await this.handleSaveError(asApiError(e), mine);
    } finally {
      this.saving.set(false);
    }
  }

  private async send(patch: JsonObject, etag: string): Promise<void> {
    const saved = await this.api.patch(this.ref(), patch, etag);
    this.etag = saved.etag;
    this.stored.set(saved.value);
    const stored = formOf(saved.value);
    this.baseline.set(stored);
    this.form.set(stored);
    this.warnings.set((saved.value.warnings ?? []).map((w) => w.detail));
    this.mirrorRefresh.update((n) => n + 1);
    this.notifier.status($localize`:@@repo.edit.saved:Repository saved.`);
  }

  /**
   * Pause and resume are the member `remote.mirror.paused`, patched with the loaded `ETag` (R-REPO-056,
   * Q-UI-105). The patch carries that member only: other unsaved edits stay in the form. A 412 is
   * settled like a save: a stored document that already says so is a success, anything else opens
   * the conflict dialog (R-UI-042).
   */
  protected async setMirrorPaused(paused: boolean): Promise<void> {
    if (this.pausing() || !this.stored()) return;
    this.pausing.set(true);
    this.failure.set(null);
    try {
      await this.sendPause(paused, this.etag);
    } catch (e) {
      await this.handlePauseError(asApiError(e), paused);
    } finally {
      this.pausing.set(false);
    }
  }

  private async sendPause(paused: boolean, etag: string): Promise<void> {
    const saved = await this.api.patch(this.ref(), { remote: { mirror: { paused } } }, etag);
    this.takeStored(saved.value, saved.etag);
    this.notifier.status(
      paused
        ? $localize`:@@mirror.paused-notice:Mirror paused.`
        : $localize`:@@mirror.resumed-notice:Mirror resumed.`,
    );
  }

  /** The stored document is the new baseline; the user's other unsaved edits stay, and the paused member follows the store. */
  private takeStored(value: Repository, etag: string): void {
    this.etag = etag;
    this.stored.set(value);
    const stored = formOf(value);
    this.baseline.set(stored);
    this.form.update((f) => {
      if (!f) return stored;
      const mirror: JsonObject = { ...f.mirror };
      if (stored.mirror['paused'] === undefined) delete mirror['paused'];
      else mirror['paused'] = stored.mirror['paused'];
      return { ...f, mirror };
    });
    this.mirrorRefresh.update((n) => n + 1);
  }

  private async handlePauseError(error: ApiError, paused: boolean): Promise<void> {
    if (error.status !== 412) {
      this.failure.set(error);
      return;
    }
    let theirs: Versioned<Repository | RepositoryConsumerView>;
    try {
      theirs = await this.api.get(this.ref());
    } catch (e) {
      this.failure.set(asApiError(e));
      return;
    }
    if (isConsumerView(theirs.value)) {
      this.failure.set(error);
      return;
    }
    const theirForm = formOf(theirs.value);
    const m = this.member();
    const wanted = settingsOf({ ...theirForm, mirror: { ...theirForm.mirror, paused } }, m);
    if (JSON.stringify(settingsOf(theirForm, m)) === JSON.stringify(wanted)) {
      // The stored document says so already (another administrator, or a retried write that did commit).
      this.takeStored(theirs.value, theirs.etag);
      return;
    }
    const decision = await this.conflicts.resolve({ mine: wanted, theirs: settingsOf(theirForm, m) });
    if (decision === 'reload') {
      this.adopt(theirs);
    } else if (decision === 'overwrite') {
      try {
        await this.sendPause(paused, theirs.etag);
      } catch (e) {
        this.failure.set(asApiError(e));
      }
    }
  }

  private async handleSaveError(error: ApiError, mine: RepositoryForm): Promise<void> {
    if (error.status !== 412) {
      this.failure.set(error);
      return;
    }
    // The stored version changed (R-UI-042): read it, then decide.
    let theirs: Versioned<Repository | RepositoryConsumerView>;
    try {
      theirs = await this.api.get(this.ref());
    } catch (e) {
      this.failure.set(asApiError(e));
      return;
    }
    if (isConsumerView(theirs.value)) {
      this.failure.set(error);
      return;
    }
    const theirForm = formOf(theirs.value);
    const m = this.member();
    if (
      error.afterRetry &&
      JSON.stringify(settingsOf(theirForm, m)) === JSON.stringify(settingsOf(mine, m))
    ) {
      // The first attempt of a retried write did commit: that is a success, not a conflict.
      this.adopt(theirs);
      this.notifier.status($localize`:@@repo.edit.saved:Repository saved.`);
      return;
    }
    const decision = await this.conflicts.resolve({
      mine: settingsOf(mine, m),
      theirs: settingsOf(theirForm, m),
    });
    if (decision === 'reload') {
      this.adopt(theirs);
    } else if (decision === 'overwrite') {
      try {
        await this.send(mergePatchOf(settingsOf(theirForm, m), settingsOf(mine, m)), theirs.etag);
      } catch (e) {
        this.failure.set(asApiError(e));
      }
    }
  }

  protected reset(): void {
    this.form.set(this.baseline());
    this.failure.set(null);
  }

  protected async remove(): Promise<void> {
    const answer = await this.confirm.ask({
      title: $localize`:@@repo.delete.title:Delete repository ${this.ref()}:key:?`,
      lines: [
        $localize`:@@repo.delete.text:The repository and everything in it are deleted for good. Deletion runs as a background job; the key stays taken until it has finished.`,
      ],
      confirmLabel: $localize`:@@repo.delete.confirm:Delete repository`,
      typeToConfirm: this.key(),
    });
    if (answer === null) return;
    try {
      const outcome = await this.api.delete(this.ref(), newIdempotencyKey());
      if (outcome.done) await this.deleted();
      else this.deletion.set(outcome.job);
    } catch (e) {
      this.failure.set(asApiError(e));
    }
  }

  /** The target projects of a move: where the caller is an Owner, every project for an administrator (R-REPO-060). */
  protected readonly moveTargets = resource({
    loader: async () => {
      const own = this.session.projects().filter((p) => p.role === 'owner').map((p) => p.key);
      if (this.session.has('projects:read')) {
        return (await collectAll(this.projectsApi.list({}))).map((p) => p.key);
      }
      return own;
    },
  });
  protected readonly moveOptions = computed<readonly SelectOption[]>(() => [
    { value: '', label: $localize`:@@repo.move.choose:Choose a project` },
    ...(valueOf(this.moveTargets) ?? [])
      .filter((k) => k !== this.project())
      .map((k) => ({ value: k, label: k })),
  ]);
  /** Moving takes `repositories:write-config`, or the Owner role in both projects (R-REPO-060). */
  protected readonly canMove = computed(
    () => this.session.has('repositories:write-config') || this.session.isOwnerOf(this.project()),
  );
  protected readonly moveTarget = signal('');
  protected readonly moving = signal(false);

  /** Moves the repository to another project: its address changes, its key stays (R-REPO-060). */
  protected async move(): Promise<void> {
    const target = this.moveTarget();
    if (!target) return;
    const answer = await this.confirm.ask({
      title: $localize`:@@repo.move.title:Move repository ${this.ref()}:key: to project ${target}:project:?`,
      lines: [
        $localize`:@@repo.move.text:The repository gets a new address; clients must switch to it. Policies, pins, virtual memberships and CI mappings that name it in this project stop applying, and the move is refused while any of them exists.`,
      ],
      confirmLabel: $localize`:@@repo.move.confirm:Move repository`,
    });
    if (answer === null) return;
    this.moving.set(true);
    this.failure.set(null);
    try {
      const moved = await this.api.move(this.ref(), target, newIdempotencyKey());
      this.notifier.status($localize`:@@repo.move.done:Repository moved to project ${target}:project:.`);
      this.baseline.set(this.form());
      await this.router.navigate(['/projects', target, 'repositories', moved.value.key, 'settings']);
    } catch (e) {
      this.failure.set(asApiError(e));
    } finally {
      this.moving.set(false);
    }
  }

  protected async jobEnded(state: string): Promise<void> {
    if (state === 'succeeded') await this.deleted();
    else
      this.notifier.alert(
        $localize`:@@repo.delete.failed:Deleting repository ${this.ref()}:key: failed. Its row is kept; try again.`,
      );
  }

  private async deleted(): Promise<void> {
    this.notifier.status($localize`:@@repo.delete.done:Repository ${this.ref()}:key: deleted.`);
    this.baseline.set(this.form());
    await this.router.navigate(['/projects', this.project(), 'repositories']);
  }

  protected readonly deleteLabel = $localize`:@@repo.delete.progress:Repository deletion`;
  protected readonly tabGeneral = $localize`:@@repo.tab.general:General`;
  protected readonly tabAccess = $localize`:@@repo.tab.access:Access`;
  protected readonly tabMembers = $localize`:@@repo.tab.members:Members`;
  protected readonly tabFormat = computed(
    () =>
      $localize`:@@repo.tab.format:${this.format()?.displayName ?? this.member().name}:format: settings`,
  );
  protected readonly tabUpstream = $localize`:@@repo.tab.upstream:Upstream`;
  protected readonly tabMirror = $localize`:@@repo.tab.mirror:Mirror`;
  protected readonly tabAnsibleImports = $localize`:@@repo.tab.ansible-imports:Import tasks`;
  protected readonly tabAnsibleTombstones = $localize`:@@repo.tab.ansible-tombstones:Tombstones`;
  protected readonly tabDebKeys = $localize`:@@repo.tab.deb-keys:Signing keys`;
  protected readonly tabDebIndex = $localize`:@@repo.tab.deb-index:Index`;
  protected readonly tabApi = $localize`:@@repo.tab.api:API`;
  protected readonly tabGuide = $localize`:@@repo.tab.guide:How to use`;

  /** The format's clients; a format with clients has a guide (R-UI-049). */
  protected clientsOf(format: string): readonly FormatClient[] {
    return (valueOf(this.formats) ?? []).find((f) => f.name === format)?.clients ?? [];
  }
}
