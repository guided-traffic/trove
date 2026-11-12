import { DatePipe, NgTemplateOutlet } from '@angular/common';
import { Component, computed, effect, inject, input, resource, signal, untracked } from '@angular/core';
import { Router, RouterLink } from '@angular/router';
import { newIdempotencyKey } from '../../api/client';
import { FormatClient } from '../../api/guides-api';
import { collectAll } from '../../api/pagination';
import { ApiError, FieldProblem } from '../../api/problem';
import { ProjectsApi } from '../../api/projects-api';
import {
  JobReference,
  RepositoriesApi,
  Repository,
  RepositoryConsumerView,
  RepositoryVisibility,
  Versioned,
  isConsumerView,
  repoRef,
  repositoryUrl,
} from '../../api/repositories-api';
import { Notifier } from '../../core/notifier';
import { ServerAddress } from '../../core/server-address';
import { SessionStore } from '../../core/session-store';
import { SelectField, SelectOption } from '../../shared/access/fields';
import { AddressField } from '../../shared/address-field';
import { Confirm } from '../../shared/confirm-dialog';
import { ConflictResolver } from '../../shared/conflict-dialog';
import { DraftStore } from '../../shared/drafts';
import { ErrorNotice } from '../../shared/error-notice';
import { ErrorSummary, SummaryItem, fieldMessage } from '../../shared/field-errors';
import { JobProgress } from '../../shared/job-progress';
import { JsonObject } from '../../shared/merge-patch';
import { asApiError, valueOf } from '../../shared/paged-resource';
import { HasUnsavedChanges } from '../../shared/unsaved-guard';
import { Disclosure } from '../../shared/ui/disclosure';
import { PageHeader } from '../../shared/ui/page-header';
import { VisibilityIcon, VisibilityLevel } from '../../shared/ui/visibility-level';
import { AnsibleImportTasks } from './ansible-import-tasks';
import { AnsibleTombstones } from './ansible-tombstones';
import { DebIndexStatePanel } from './deb-index-state';
import { DebSigningKeys } from './deb-signing-keys';
import { DebUpstreamKeys } from './deb-upstream-keys';
import { FormatSettings, formatPartNodes } from './format-settings';
import { MirrorSettings, mirrorPointerLabel, mirrorTarget } from './mirror-settings';
import { MirrorStatusPanel } from './mirror-status';
import { FrequentPlaces } from '../../core/frequent-places';
import { classLabel } from './repository-class';
import { RepositoryFields } from './repository-fields';
import { RepositoryGuide } from './repository-guide';
import {
  FormatMemberOf,
  RepositoryForm,
  apiAddress,
  clientAddress,
  curlOf,
  formOf,
  maskSecrets,
  mirrorDefaultsOf,
} from './repository-model';
import { RepositoryVisibilityBlock } from './repository-visibility';
import { SETTINGS_SECTIONS, SettingsSection, rebaseForm, sectionMembers, sectionPatch, withSection } from './settings-sections';
import { SettingSchema, errorTarget, settingNodes } from './settings-schema';
import { upstreamDefaultsOf } from './upstream-model';
import { UpstreamSettings, upstreamPointerLabel, upstreamTarget } from './upstream-settings';
import { MemberChange, VirtualMembers } from './virtual-members';

/** The closed sections whose content loads when it is first opened (a list read from the server, a guide). */
type LazySection = 'deb-keys' | 'deb-index' | 'ansible-imports' | 'ansible-tombstones' | 'guide';

/**
 * The settings page of a repository (R-UI-087, Q-UI-351, Q-UI-352): one column of sections, as the style guide's settings pages.
 * The key and the description at the top, the visibility, the upstream of a remote and the members of a virtual repository open
 * below, and every detail a closed section: the mirror, the format's settings, the access details, the administrator notes, the
 * format's operations, How to use, the facts and the API. Each section that edits the document saves only its members, a merge
 * patch with the loaded `ETag` (`sectionPatch`); a 412 opens the conflict dialog for that section, and every other section keeps its
 * unsaved edits (`rebaseForm`). **Advanced** disables or enables, moves and deletes. A caller without `repositories:read-config`
 * sees the reduced consumer view.
 */
@Component({
  selector: 'trove-repository-detail',
  imports: [
    AddressField,
    AnsibleImportTasks,
    AnsibleTombstones,
    DatePipe,
    DebIndexStatePanel,
    DebSigningKeys,
    DebUpstreamKeys,
    Disclosure,
    ErrorNotice,
    ErrorSummary,
    FormatSettings,
    JobProgress,
    MirrorSettings,
    MirrorStatusPanel,
    NgTemplateOutlet,
    PageHeader,
    RepositoryFields,
    RepositoryGuide,
    RepositoryVisibilityBlock,
    RouterLink,
    SelectField,
    UpstreamSettings,
    VirtualMembers,
    VisibilityIcon,
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

  /** The settings as stored, in form shape; the baseline of the dirty checks and of every section's patch. */
  protected readonly baseline = signal<RepositoryForm | null>(null);
  protected readonly form = signal<RepositoryForm | null>(null);
  private etag = '';
  protected readonly stored = signal<Repository | null>(null);
  protected readonly consumer = signal<RepositoryConsumerView | null>(null);
  /** The artifacts level, shown as an icon before the key (Q-UI-230). */
  protected readonly artifactsLevel = computed<VisibilityLevel | null>(
    () => (this.stored() ?? this.consumer())?.visibility?.artifacts ?? null,
  );
  protected readonly deletion = signal<JobReference | null>(null);

  /** The capability, or the Owner role in the repository's project (R-AUTHZ-026). */
  protected readonly canWrite = computed(() => this.session.canWriteRepositories(this.stored()?.project));
  /** The caller may change it and it is not being deleted: the sections save, else they are read-only (R-UI-087). */
  protected readonly canChange = computed(() => this.canWrite() && this.stored()?.lifecycle !== 'deleting');
  protected readonly loadFailure = computed(() => {
    const e = this.loaded.error();
    return e ? asApiError(e) : null;
  });
  protected readonly format = computed(
    () => (valueOf(this.formats) ?? []).find((f) => f.name === this.stored()?.format) ?? null,
  );
  protected readonly formatLabel = computed(() => this.format()?.displayName ?? this.stored()?.format ?? '');
  /** The repository's view of the vulnerability report (R-VULN-022): a scanned format, not a virtual repository. */
  protected readonly vulnerabilitiesShown = computed(() => {
    const repo = this.stored();
    return !!repo && repo.format !== 'generic' && repo.class !== 'virtual';
  });
  protected readonly classText = classLabel;

  /** The upstream settings: a remote repository (R-UI-082). */
  protected readonly upstreamSection = computed(() => this.stored()?.class === 'remote');
  /** The section Mirror: a remote repository of a format that can mirror (R-UI-021, Q-UI-104). */
  protected readonly mirrorSection = computed(() => this.stored()?.class === 'remote' && this.format()?.mirror === true);
  /** The status panel: a remote repository stored in mode `mirror` (Q-UI-119), while the section is open. */
  protected readonly mirrorStatusShown = computed(() => this.mirrorSection() && this.stored()?.remote?.mode === 'mirror');
  protected readonly mirrorRefresh = signal(0);
  protected readonly pausing = signal(false);
  /** An Owner without the capability keeps what is stored, pauses and turns the mirror off (Q-REPO-142). */
  protected readonly mirrorRestricted = computed(() => !this.session.has('repositories:write-config'));
  /**
   * The server's defaults of `remote.mirror` (R-REPO-063), read for a remote with a mirror: the values of the fields that are not set
   * and what a save leaves unwritten. A caller the server refuses them to changes nothing and gets none.
   */
  private readonly mirrorDefaultsResource = resource({
    params: () => {
      const repo = this.stored();
      return this.mirrorSection() && repo ? { project: this.project(), format: repo.format } : undefined;
    },
    loader: async ({ params }) => {
      try {
        return mirrorDefaultsOf((await this.api.defaults(params.project, params.format, 'remote')).repository as Record<string, unknown>);
      } catch {
        return null;
      }
    },
  });
  protected readonly mirrorDefaults = computed(() => valueOf(this.mirrorDefaultsResource) ?? null);
  /** The signing keys and the indexes: a local or virtual Debian repository (R-DEB-014, R-DEB-015). */
  protected readonly debSection = computed(() => {
    const repo = this.stored();
    return repo?.format === 'deb' && repo.class !== 'remote';
  });
  /** The import tasks: a local or virtual Ansible Galaxy repository (R-ANS-009, R-ANS-026). */
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

  protected readonly settingNodes = computed(() => {
    const repo = this.stored();
    return repo ? settingNodes(this.format()?.settingsSchema as SettingSchema | null | undefined, repo.class, repo.format) : [];
  });
  protected readonly formatNodes = computed(() => formatPartNodes(this.settingNodes(), 'settings'));
  protected readonly formatMirrorNodes = computed(() => formatPartNodes(this.settingNodes(), 'mirror'));
  /** The format member of the document and what of `remote` the page edits (R-UI-084: written as the merge patch's structure). */
  private readonly member = computed<FormatMemberOf>(() => ({
    name: this.stored()?.format ?? '',
    schema: this.format()?.settingsSchema as SettingSchema | null | undefined,
    formatMirror: this.formatMirrorNodes().length > 0,
    mirrorDefaults: this.mirrorDefaults(),
    ...(this.upstreamSection()
      ? { remote: 'structural' as const, mirror: this.mirrorSection(), upstream: upstreamDefaultsOf(this.format()) }
      : {}),
  }));
  protected readonly memberPointer = computed(() => `/${this.member().name}`);

  /** The patch of each section; empty where nothing changed. */
  protected readonly patches = computed<ReadonlyMap<SettingsSection, JsonObject>>(() => {
    const base = this.baseline();
    const now = this.form();
    const m = this.member();
    const out = new Map<SettingsSection, JsonObject>();
    if (!base || !now) return out;
    for (const s of SETTINGS_SECTIONS) {
      const patch = sectionPatch(s, base, now, m);
      if (Object.keys(patch).length > 0) out.set(s, patch);
    }
    return out;
  });
  protected dirty(section: SettingsSection): boolean {
    return this.patches().has(section);
  }
  /** "Unsaved changes" after the title of a section that holds one (R-UI-087). */
  protected unsaved(section: SettingsSection): string {
    return this.dirty(section) ? $localize`:@@repo.settings.unsaved:Unsaved changes` : '';
  }

  protected readonly saving = signal<SettingsSection | null>(null);
  protected readonly failure = signal<{ readonly section: SettingsSection; readonly error: ApiError } | null>(null);
  protected readonly warnings = signal<readonly string[]>([]);
  /** The field errors of the section whose save the server refused; the other sections show none. */
  protected errorsOf(section: SettingsSection): ReadonlyMap<string, FieldProblem> {
    const f = this.failure();
    return f && f.section === section ? f.error.fieldErrors : new Map();
  }
  protected noticeOf(section: SettingsSection): ApiError | null {
    const f = this.failure();
    return f && f.section === section && f.error.fieldErrors.size === 0 ? f.error : null;
  }
  protected summaryOf(section: SettingsSection): readonly SummaryItem[] {
    return [...this.errorsOf(section)].map(([pointer, problem]) => ({
      label: this.pointerLabel(pointer),
      message: fieldMessage(problem),
      targetId: this.pointerTarget(pointer),
    }));
  }

  /** The sections that open their content once (`LazySection`), and the open state of the section Mirror. */
  protected readonly opened = signal<ReadonlySet<LazySection>>(new Set());
  protected readonly mirrorOpen = signal(false);
  protected open(section: LazySection, expanded: boolean): void {
    if (expanded && !this.opened().has(section)) this.opened.update((s) => new Set([...s, section]));
  }
  protected isOpened(section: LazySection): boolean {
    return this.opened().has(section);
  }

  protected readonly address = computed(() => clientAddress(valueOf(this.baseUrl), this.format() ?? undefined, this.ref()));
  protected readonly documentJson = computed(() => JSON.stringify(this.stored(), null, 2));
  /** Each section's unsaved change as the request that saves it (R-UI-087). */
  protected readonly requests = computed(() =>
    [...this.patches()].map(([section, patch]) => ({
      section,
      label: this.sectionLabel(section),
      curl: curlOf('PATCH', apiAddress(valueOf(this.baseUrl), repositoryUrl(this.ref())), maskSecrets(patch), [`If-Match: ${this.etag}`]),
    })),
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
    return this.patches().size > 0;
  }

  protected setForm(form: RepositoryForm): void {
    this.form.set(form);
  }

  protected setFormatMember(formatMember: JsonObject): void {
    this.form.update((f) => (f ? { ...f, formatMember } : f));
  }

  protected setDescription(description: string): void {
    this.form.update((f) => (f ? { ...f, description } : f));
  }

  protected sectionLabel(section: SettingsSection): string {
    switch (section) {
      case 'general':
        return $localize`:@@repo.field.description-optional:Description (optional)`;
      case 'upstream':
        return $localize`:@@upstream.heading:Upstream`;
      case 'mirror':
        return $localize`:@@mirror.heading:Mirror`;
      case 'format':
        return this.formatHeading();
      case 'access':
        return $localize`:@@repo.group.access:Access details`;
      case 'notes':
        return $localize`:@@repo.field.notes:Administrator notes`;
    }
  }

  protected readonly formatHeading = computed(
    () => $localize`:@@repo.settings.format-heading:${this.formatLabel()}:format: settings`,
  );

  private pointerLabel(pointer: string): string {
    const upstream = this.upstreamSection() ? upstreamPointerLabel(pointer) : null;
    if (upstream) return upstream;
    const mirror = mirrorPointerLabel(pointer);
    if (mirror) return mirror;
    switch (pointer.split('/')[1] ?? pointer) {
      case 'description':
        return $localize`:@@repo.field.description-optional:Description (optional)`;
      case 'adminNotes':
        return $localize`:@@repo.field.notes:Administrator notes`;
      case 'includePatterns':
        return $localize`:@@repo.field.include:Include patterns`;
      case 'excludePatterns':
        return $localize`:@@repo.field.exclude:Exclude patterns`;
      case 'hideExistence':
        return $localize`:@@repo.field.hide:Hide existence from callers without access`;
      default:
        return pointer;
    }
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

  /**
   * The stored document after a write is the new baseline: the section that was saved shows what is stored, and every other
   * section keeps its unsaved edits (R-UI-087).
   */
  private takeStored(value: Repository, etag: string, saved: SettingsSection | null = null): void {
    const before = this.baseline();
    const current = this.form();
    const next = formOf(value);
    this.etag = etag;
    this.stored.set(value);
    this.baseline.set(next);
    this.form.set(before && current ? rebaseForm(before, current, next, this.member(), saved) : next);
    this.mirrorRefresh.update((n) => n + 1);
  }

  /** Saves one section: its members only, as a merge patch with the loaded `ETag` (R-UI-087). */
  protected async saveSection(section: SettingsSection, event?: Event): Promise<void> {
    event?.preventDefault();
    const patch = this.patches().get(section);
    const mine = this.form();
    if (!patch || !mine || this.saving() !== null || !this.canChange()) return;
    this.saving.set(section);
    this.failure.set(null);
    try {
      await this.send(section, patch, this.etag);
    } catch (e) {
      await this.handleSaveError(section, asApiError(e), mine);
    } finally {
      this.saving.set(null);
    }
  }

  private async send(section: SettingsSection, patch: JsonObject, etag: string): Promise<void> {
    const saved = await this.api.patch(this.ref(), patch, etag);
    this.takeStored(saved.value, saved.etag, section);
    this.warnings.set((saved.value.warnings ?? []).map((w) => w.detail));
    this.notifier.status($localize`:@@repo.edit.saved:Repository saved.`);
  }

  /** A 412 is settled for the section alone: a retried write that did commit is a success, anything else the conflict dialog (R-UI-042). */
  private async handleSaveError(section: SettingsSection, error: ApiError, mine: RepositoryForm): Promise<void> {
    if (error.status !== 412) {
      this.failure.set({ section, error });
      return;
    }
    let theirs: Versioned<Repository | RepositoryConsumerView>;
    try {
      theirs = await this.api.get(this.ref());
    } catch (e) {
      this.failure.set({ section, error: asApiError(e) });
      return;
    }
    if (isConsumerView(theirs.value)) {
      this.failure.set({ section, error });
      return;
    }
    const m = this.member();
    const theirForm = formOf(theirs.value);
    const theirPart = sectionMembers(section, theirForm, m);
    const myPart = sectionMembers(section, mine, m);
    if (error.afterRetry && JSON.stringify(theirPart) === JSON.stringify(myPart)) {
      this.takeStored(theirs.value, theirs.etag, section);
      this.notifier.status($localize`:@@repo.edit.saved:Repository saved.`);
      return;
    }
    const decision = await this.conflicts.resolve({ mine: myPart, theirs: theirPart });
    if (decision === 'reload') {
      this.takeStored(theirs.value, theirs.etag, section);
    } else if (decision === 'overwrite') {
      const patch = sectionPatch(section, theirForm, withSection(section, mine, theirForm), m);
      try {
        this.etag = theirs.etag;
        await this.send(section, patch, theirs.etag);
      } catch (e) {
        this.failure.set({ section, error: asApiError(e) });
      }
    }
  }

  protected discard(section: SettingsSection): void {
    const base = this.baseline();
    const now = this.form();
    if (base && now) this.form.set(withSection(section, base, now));
    if (this.failure()?.section === section) this.failure.set(null);
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
      this.takeStored(theirs.value, theirs.etag);
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

  /**
   * Pause and resume are the member `remote.mirror.paused`, patched with the loaded `ETag` (R-REPO-056, Q-UI-105). The patch
   * carries that member only: other unsaved edits stay. A 412 is settled like a save (R-UI-042).
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
    this.takeStoredPaused(saved.value, saved.etag);
    this.notifier.status(
      paused ? $localize`:@@mirror.paused-notice:Mirror paused.` : $localize`:@@mirror.resumed-notice:Mirror resumed.`,
    );
  }

  /** After a pause or a resume the paused member follows the store, and every unsaved edit stays. */
  private takeStoredPaused(value: Repository, etag: string): void {
    this.takeStored(value, etag);
    const paused = formOf(value).mirror['paused'];
    this.form.update((f) => {
      if (!f) return f;
      const mirror: JsonObject = { ...f.mirror };
      if (paused === undefined) delete mirror['paused'];
      else mirror['paused'] = paused;
      return { ...f, mirror };
    });
  }

  private async handlePauseError(error: ApiError, paused: boolean): Promise<void> {
    if (error.status !== 412) {
      this.failure.set({ section: 'mirror', error });
      return;
    }
    let theirs: Versioned<Repository | RepositoryConsumerView>;
    try {
      theirs = await this.api.get(this.ref());
    } catch (e) {
      this.failure.set({ section: 'mirror', error: asApiError(e) });
      return;
    }
    if (isConsumerView(theirs.value)) {
      this.failure.set({ section: 'mirror', error });
      return;
    }
    const m = this.member();
    const theirForm = formOf(theirs.value);
    const wanted = sectionMembers('mirror', { ...theirForm, mirror: { ...theirForm.mirror, paused } }, m);
    const stored = sectionMembers('mirror', theirForm, m);
    if (JSON.stringify(stored) === JSON.stringify(wanted)) {
      // The stored document says so already (another administrator, or a retried write that did commit).
      this.takeStoredPaused(theirs.value, theirs.etag);
      return;
    }
    const decision = await this.conflicts.resolve({ mine: wanted, theirs: stored });
    if (decision === 'reload') {
      this.adopt(theirs);
    } else if (decision === 'overwrite') {
      try {
        await this.sendPause(paused, theirs.etag);
      } catch (e) {
        this.failure.set({ section: 'mirror', error: asApiError(e) });
      }
    }
  }

  // ---- Advanced (Q-UI-352) ----

  protected readonly advancedBusy = signal(false);
  protected readonly advancedFailure = signal<ApiError | null>(null);

  /** Disables or enables the repository after a confirmation that says what stops or starts again (Q-UI-352). */
  protected async toggleDisabled(): Promise<void> {
    const repo = this.stored();
    if (!repo || this.advancedBusy()) return;
    const disable = !(repo.disabled ?? false);
    const answer = await this.confirm.ask(
      disable
        ? {
            title: $localize`:@@repo.disable.title:Disable repository ${this.ref()}:key:?`,
            lines: [
              $localize`:@@repo.disable.text:Clients can no longer download from it or publish to it, and virtual repositories skip it, until it is enabled again. Its content stays.`,
            ],
            confirmLabel: $localize`:@@repo.disable.confirm:Disable repository`,
          }
        : {
            title: $localize`:@@repo.enable.title:Enable repository ${this.ref()}:key:?`,
            lines: [$localize`:@@repo.enable.text:Clients can download from it and publish to it again.`],
            confirmLabel: $localize`:@@repo.enable.confirm:Enable repository`,
          },
    );
    if (answer === null) return;
    this.advancedBusy.set(true);
    this.advancedFailure.set(null);
    try {
      await this.sendDisabled(disable, this.etag);
    } catch (e) {
      const error = asApiError(e);
      if (error.status !== 412) {
        this.advancedFailure.set(error);
      } else {
        // The stored document changed meanwhile: the confirmed state is written over it, or kept when it is there already.
        try {
          const theirs = await this.api.get(this.ref());
          if (isConsumerView(theirs.value)) throw error;
          if ((theirs.value.disabled ?? false) === disable) this.takeStored(theirs.value, theirs.etag);
          else await this.sendDisabled(disable, theirs.etag);
        } catch (again) {
          this.advancedFailure.set(asApiError(again));
        }
      }
    } finally {
      this.advancedBusy.set(false);
    }
  }

  private async sendDisabled(disabled: boolean, etag: string): Promise<void> {
    const saved = await this.api.patch(this.ref(), { disabled }, etag);
    this.takeStored(saved.value, saved.etag);
    this.notifier.status(
      disabled
        ? $localize`:@@repo.disable.done:Repository ${this.ref()}:key: disabled.`
        : $localize`:@@repo.enable.done:Repository ${this.ref()}:key: enabled.`,
    );
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
      this.advancedFailure.set(asApiError(e));
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
    ...(valueOf(this.moveTargets) ?? []).filter((k) => k !== this.project()).map((k) => ({ value: k, label: k })),
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
    this.advancedFailure.set(null);
    try {
      const moved = await this.api.move(this.ref(), target, newIdempotencyKey());
      this.notifier.status($localize`:@@repo.move.done:Repository moved to project ${target}:project:.`);
      this.baseline.set(this.form());
      await this.router.navigate(['/projects', target, 'repositories', moved.value.key, 'settings']);
    } catch (e) {
      this.advancedFailure.set(asApiError(e));
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

  /** The format's clients; a format with clients has a guide (R-UI-049). */
  protected clientsOf(format: string): readonly FormatClient[] {
    return (valueOf(this.formats) ?? []).find((f) => f.name === format)?.clients ?? [];
  }
}
