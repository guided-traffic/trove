import { Component, DestroyRef, computed, effect, inject, input, signal, untracked } from '@angular/core';
import { Checkbox } from '../../shared/ui/checkbox';
import { Router, RouterLink } from '@angular/router';
import { CiApi, CiMapping, CiMappingInput, CiWarning, SnippetFormat } from '../../api/ci-api';
import { PolicyApi } from '../../api/authz-api';
import { IdentityApi } from '../../api/identity-api';
import { collectAll } from '../../api/pagination';
import { ApiError } from '../../api/problem';
import { RepositoriesApi, Versioned } from '../../api/repositories-api';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { SelectField, SelectOption, TextField } from '../../shared/access/fields';
import { ConflictResolver } from '../../shared/conflict-dialog';
import { DraftStore } from '../../shared/drafts';
import { CopyButton } from '../../shared/copy-button';
import { ErrorNotice } from '../../shared/error-notice';
import { ErrorSummary, SummaryItem, fieldErrorAt, fieldMessage, problemsAt } from '../../shared/field-errors';
import { asApiError } from '../../shared/paged-resource';
import { HasUnsavedChanges } from '../../shared/unsaved-guard';
import { ProjectNeeds, createState } from '../projects/project-access';
import { Needs } from '../../shared/access/needs';
import { CiWarnings, changedMembers, ciRoutes, overwriteBody } from './ci-common';
import {
  INLINE_ACTIONS,
  InlineRow,
  MappingForm,
  conditionFromNode,
  depthOf,
  emptyMappingForm,
  formFromMapping,
  inputFromForm,
  leafCount,
  newInlineRow,
  nextPriority,
  nodeFromCondition,
  pinsProject,
  summarizeCondition,
  unprotectedWrite,
  withProtectedRef,
  writes,
} from './ci-model';
import { ConditionBuilder } from './condition-builder';
import { NamePicker } from './name-picker';

/**
 * The mapping editor (R-UI-035, R-CI-009 to R-CI-014): a mapping of the system tier of an issuer, or, with
 * a project key, of that project's tier, bounded as the server bounds it (Q-CI-26): the project's repositories
 * and policies are offered, there are no groups, and the name is bare within the project. The
 * condition is built as a tree (all, any, not and conditions on claims) or edited as JSON; the grants
 * are policies, groups (system tier only) and inline grants; the options carry the visible warning
 * for unprotected refs. The checks here only mirror what the server refuses so that the hint comes
 * before the save; the server decides. A save shows the warnings it returns, and a refusal shows its
 * reason next to the member it names.
 */
@Component({
  selector: 'trove-ci-mapping-editor',
  imports: [
    Checkbox,
    RouterLink,
    CiWarnings,
    ConditionBuilder,
    CopyButton,
    ErrorNotice,
    ErrorSummary,
    NamePicker,
    Needs,
    ProjectNeeds,
    SelectField,
    TextField,
  ],
  templateUrl: './mapping-editor.html',
  styleUrls: ['../../shared/access/access.scss', './mapping-editor.scss'],
})
export class MappingEditor implements HasUnsavedChanges {
  /** Route parameter `:issuer`, or the query parameter of a new project mapping. */
  readonly issuer = input<string | undefined>();
  /** Route parameter `:mapping`; absent when a new mapping is made. */
  readonly mapping = input<string | undefined>();
  /** Route parameter `:key`: the project of a project's mapping. */
  readonly key = input<string | undefined>();

  private readonly api = inject(CiApi);
  private readonly policiesApi = inject(PolicyApi);
  private readonly identityApi = inject(IdentityApi);
  private readonly repositoriesApi = inject(RepositoriesApi);
  private readonly conflicts = inject(ConflictResolver);
  private readonly drafts = inject(DraftStore);
  private readonly notifier = inject(Notifier);
  private readonly router = inject(Router);
  protected readonly session = inject(SessionStore);

  protected readonly project = computed(() => this.key() ?? null);
  protected readonly issuerChoice = signal('');
  protected readonly issuerName = computed(() => this.issuer() || this.issuerChoice());
  protected readonly isNew = computed(() => this.mapping() === undefined);
  protected readonly issuerNames = signal<readonly SelectOption[]>([]);

  protected readonly form = signal<MappingForm>(emptyMappingForm(10));
  protected readonly loaded = signal<Versioned<CiMapping> | null>(null);
  protected readonly loadError = signal<ApiError | null>(null);
  protected readonly saveError = signal<ApiError | null>(null);
  protected readonly warnings = signal<readonly CiWarning[]>([]);
  protected readonly busy = signal(false);
  private baseline = '';

  protected readonly policyOptions = signal<readonly string[] | null>(null);
  protected readonly groupOptions = signal<readonly string[] | null>(null);
  protected readonly projectRepositories = signal<readonly string[]>([]);
  /** A project's mapping names bare keys, a system mapping `<project>/<key>` (R-AUTHZ-004). */
  protected readonly repositoriesHint = computed(() =>
    this.project()
      ? $localize`:@@ci.editor.repositories-hint-project:One key or pattern per line. A template needs literal text next to the claim.`
      : $localize`:@@ci.editor.repositories-hint:One project/key or pattern per line, or a class such as @any-local. A template needs literal text next to the claim.`,
  );
  protected readonly publicIssuer = signal(false);

  protected readonly view = signal<'builder' | 'json'>('builder');
  protected readonly jsonText = signal('');
  protected readonly jsonError = signal<string | null>(null);

  protected readonly snippetFormat = signal<SnippetFormat>('npm');
  protected readonly snippet = signal<string | null>(null);
  protected readonly snippetError = signal<ApiError | null>(null);
  protected readonly snippetOpen = signal(false);

  protected readonly actions = INLINE_ACTIONS;
  protected readonly kindOptions: readonly SelectOption[] = [
    { value: 'packages', label: $localize`:@@ci.editor.kind.packages:Package names` },
    { value: 'paths', label: $localize`:@@ci.editor.kind.paths:File paths` },
  ];
  protected readonly ownershipOptions: readonly SelectOption[] = [
    { value: '', label: $localize`:@@ci.editor.ownership.default:Default (bind the project when the actions write)` },
    { value: 'bind_project', label: $localize`:@@ci.editor.ownership.bind:Bind the package name to the project` },
    { value: 'none', label: $localize`:@@ci.editor.ownership.none:None (the mapping owns the names)` },
  ];
  protected readonly crossOptions: readonly SelectOption[] = [
    { value: 'deny', label: $localize`:@@ci.editor.cross.deny:Deny (a fork or merge request from another project gets nothing)` },
    { value: 'read_only', label: $localize`:@@ci.editor.cross.read-only:Read only (such a pipeline can read, never write)` },
  ];

  /** The bounds of the screen: the system administrators', or the Owners' of the project. */
  protected readonly state = computed(() => createState(this.session, this.project() ?? '', 'ci:write'));
  protected readonly writable = computed(() => (this.project() ? this.state() === 'enabled' : this.session.has('ci:write')));
  /** The snippet is read by ci:read or, in a project, by its Owner (`getProjectCiSnippet`). */
  protected readonly canSnippet = computed(
    () => !this.isNew() && (this.session.has('ci:read') || (this.project() !== null && this.state() === 'enabled')),
  );

  protected readonly dirty = computed(() => this.writable() && JSON.stringify(inputFromForm(this.form(), this.project())) !== this.baseline);
  protected readonly unprotected = computed(() => unprotectedWrite(this.form()));
  protected readonly unpinned = computed(() => this.publicIssuer() && !pinsProject(this.form().match));
  protected readonly sharedOwnership = computed(() => this.form().inline.some((r) => r.ownership === 'none' && r.actions.some((a) => a !== 'read' && a !== 'annotate')));
  protected readonly tooDeep = computed(() => depthOf(this.form().match) > 4);
  protected readonly tooMany = computed(() => leafCount(this.form().match) > 64);
  protected readonly summary = computed(() => summarizeCondition(conditionFromNode(this.form().match)));
  protected readonly writesGrants = computed(() => writes(this.form()));
  protected readonly testQuery = computed(() => ({
    mapping: this.form().name,
    ...(this.project() ? { issuer: this.issuerName() } : {}),
  }));

  constructor() {
    // The write body of an open, changed editor: kept across a re-authentication and offered as "Copy draft as JSON" (R-UI-042).
    this.drafts.track(() => (this.dirty() ? inputFromForm(this.form(), this.project()) : null), inject(DestroyRef));
    // A create answers with the warnings of the save and opens the stored mapping; they travel with the navigation.
    const state = this.router.getCurrentNavigation()?.extras.state as { warnings?: CiWarning[] } | undefined;
    if (Array.isArray(state?.warnings)) this.warnings.set(state.warnings);

    effect(() => {
      this.issuer();
      this.mapping();
      this.key();
      untracked(() => void this.load());
    });
  }

  /** The templates of R-CI-014 that a pattern may hold, as the example of the include field. */
  protected readonly includeHint = $localize`:@@ci.editor.include-hint:One pattern per line, for example ${'@{{ namespace_path | slug }}/*'}:example:`;

  protected back(): string[] {
    return ciRoutes.overview(this.project(), this.issuerName() || null);
  }
  protected testLink(): string[] {
    return ciRoutes.test(this.project(), this.issuerName());
  }
  protected rowLabel(i: number): string {
    return $localize`:@@ci.editor.grant-label:Inline grant ${i + 1}:number:`;
  }
  protected nameHint(): string {
    const key = this.project();
    return key
      ? $localize`:@@ci.editor.name-hint-project:Unique within the project ${key}:project: and cannot be changed later. Lower-case letters, digits, dot, underscore and hyphen.`
      : $localize`:@@ci.editor.name-hint:It cannot be changed later. Lower-case letters, digits, dot, underscore and hyphen.`;
  }
  protected readonly isWrite = (a: string): boolean => a === 'deploy' || a === 'overwrite' || a === 'delete';

  hasUnsavedChanges(): boolean {
    return this.dirty() && !this.busy();
  }

  /** Every problem the server pointed at, with the section it lives in (R-UI-044: an error summary at the top of a form). */
  protected readonly summaryItems = computed<readonly SummaryItem[]>(() => {
    const e = this.saveError();
    if (!e) return [];
    const section = (pointer: string): string =>
      pointer.startsWith('/match')
        ? 'ci-m-condition'
        : pointer.startsWith('/grants')
          ? 'ci-m-grants'
          : pointer.startsWith('/options') || pointer === '/maxTtlSeconds'
            ? 'ci-m-options'
            : 'ci-m-general';
    return [...e.fieldErrors].map(([pointer, problem]) => ({ label: pointer, message: fieldMessage(problem), targetId: section(pointer) }));
  });

  protected fieldError(pointer: string): string | null {
    return fieldErrorAt(this.saveError(), pointer);
  }
  /** The first problem at a pointer or below it: `/grants/policies` also reports `/grants/policies/0`. */
  protected underError(pointer: string): string | null {
    const e = this.saveError();
    const first = e ? problemsAt(e.fieldErrors, pointer)[0] : undefined;
    return first ? fieldMessage(first) : null;
  }
  protected readonly errorAt = (pointer: string): string | null => this.fieldError(pointer);

  protected nameError(): string | null {
    const e = this.saveError();
    return this.fieldError('/name') ?? (e?.status === 409 && e.code === 'already_exists' ? this.alreadyExists : null);
  }
  protected priorityError(): string | null {
    const e = this.saveError();
    return this.fieldError('/priority') ?? (e?.code === 'priority_taken' ? fieldMessage({ pointer: '/priority', code: 'priority_taken' }) : null);
  }
  protected rowProblems(i: number): readonly string[] {
    const e = this.saveError();
    return e ? problemsAt(e.fieldErrors, `/grants/inline/${i}`).map(fieldMessage) : [];
  }
  private readonly alreadyExists = $localize`:@@ci.editor.name-taken:A mapping of this issuer has this name.`;

  protected patch(change: Partial<MappingForm>): void {
    this.form.update((f) => ({ ...f, ...change }));
  }
  protected setMatch(match: MappingForm['match']): void {
    this.patch({ match });
    if (this.view() === 'json') this.jsonText.set(JSON.stringify(conditionFromNode(match), null, 2));
  }

  /* ---- loading ---- */

  protected async load(): Promise<void> {
    this.loadError.set(null);
    this.saveError.set(null);
    const project = this.project();
    try {
      void this.loadContext();
      const issuer = this.issuer() ?? '';
      if (this.mapping() !== undefined) {
        const m = await this.api.getMapping(issuer, this.mapping()!, project);
        this.loaded.set(m);
        this.form.set(formFromMapping(m.value));
      } else {
        this.loaded.set(null);
        const existing = issuer || project ? await this.api.listMappings(issuer, project).catch(() => []) : [];
        this.form.set(emptyMappingForm(nextPriority(existing), project ? `${project}-` : ''));
      }
      this.baseline = JSON.stringify(inputFromForm(this.form(), project));
      this.jsonText.set(JSON.stringify(conditionFromNode(this.form().match), null, 2));
      this.jsonError.set(null);
      this.restoreDraft();
    } catch (e) {
      this.loaded.set(null);
      this.loadError.set(asApiError(e));
    }
  }

  /** The entries kept across a re-authentication (R-UI-042) fill the form again; the baseline stays the stored mapping. */
  private restoreDraft(): void {
    if (!this.writable()) return;
    const draft = this.drafts.take<CiMappingInput>();
    if (!draft || typeof draft !== 'object') return;
    this.form.set(formFromMapping({ ...draft, description: draft.description ?? '', maxTtlSeconds: draft.maxTtlSeconds ?? null, options: draft.options ?? {} } as unknown as CiMapping));
    this.jsonText.set(JSON.stringify(conditionFromNode(this.form().match), null, 2));
    this.notifier.status($localize`:@@ci.editor.draft-restored:Your entries were restored. Save the mapping again.`);
  }

  /** The choices the editor offers: policies, groups, the project's repositories and the issuer's kind of audience. Failures leave a plain text entry. */
  private async loadContext(): Promise<void> {
    const project = this.project();
    try {
      const list = await collectAll(this.policiesApi.list({ project: project ?? undefined, sort: 'name' }));
      this.policyOptions.set(list.map((p) => p.name));
    } catch {
      this.policyOptions.set(null);
    }
    if (project) {
      try {
        this.projectRepositories.set((await this.repositoriesApi.list({ project })(null, 500)).items.map((r) => r.key));
      } catch {
        this.projectRepositories.set([]);
      }
      try {
        const issuers = await this.api.projectIssuers(project);
        this.issuerNames.set(issuers.map((i) => ({ value: i.name, label: i.name })));
        this.publicIssuer.set(issuers.find((i) => i.name === this.issuerName())?.publicIssuer ?? false);
      } catch {
        this.issuerNames.set([]);
      }
    } else {
      try {
        const names = await collectAll(this.identityApi.groups({}));
        this.groupOptions.set(names.map((g) => g.name));
      } catch {
        this.groupOptions.set(null);
      }
      try {
        this.publicIssuer.set((await this.api.getIssuer(this.issuer() ?? '')).value.publicIssuer);
      } catch {
        this.publicIssuer.set(false);
      }
    }
  }

  protected chooseIssuer(name: string): void {
    this.issuerChoice.set(name);
    void this.api.projectIssuers(this.project() ?? '').then((all) => this.publicIssuer.set(all.find((i) => i.name === name)?.publicIssuer ?? false));
  }

  /* ---- the condition: builder and JSON ---- */

  protected showJson(): void {
    this.jsonText.set(JSON.stringify(conditionFromNode(this.form().match), null, 2));
    this.jsonError.set(null);
    this.view.set('json');
  }

  protected showBuilder(): void {
    if (this.jsonError()) return;
    this.view.set('builder');
  }

  protected editJson(text: string): void {
    this.jsonText.set(text);
    try {
      const parsed: unknown = JSON.parse(text);
      if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) throw new Error('not an object');
      this.patch({ match: nodeFromCondition(parsed as never) });
      this.jsonError.set(null);
    } catch {
      this.jsonError.set($localize`:@@ci.editor.json-invalid:This is not a JSON object. The condition keeps the last valid version until it is.`);
    }
  }

  protected addProtectedRef(): void {
    this.setMatch(withProtectedRef(this.form().match));
  }

  /* ---- the grants ---- */

  protected addInline(): void {
    const first = this.project() ? this.projectRepositories()[0] : undefined;
    this.patch({ inline: [...this.form().inline, newInlineRow(first ? { repositories: first } : {})] });
  }
  protected setRow(id: number, change: Partial<InlineRow>): void {
    this.patch({ inline: this.form().inline.map((r) => (r.id === id ? { ...r, ...change } : r)) });
  }
  protected removeRow(id: number): void {
    this.patch({ inline: this.form().inline.filter((r) => r.id !== id) });
  }
  protected toggleAction(row: InlineRow, action: string, on: boolean): void {
    this.setRow(row.id, { actions: on ? [...row.actions, action] : row.actions.filter((a) => a !== action) });
  }
  protected addRepository(row: InlineRow, key: string): void {
    const have = row.repositories.split('\n').map((l) => l.trim());
    if (!have.includes(key)) this.setRow(row.id, { repositories: [row.repositories.trim(), key].filter((s) => s !== '').join('\n') });
  }

  /* ---- the snippet ---- */

  protected async toggleSnippet(): Promise<void> {
    if (this.snippetOpen()) {
      this.snippetOpen.set(false);
      return;
    }
    this.snippetOpen.set(true);
    await this.loadSnippet();
  }

  protected async setFormat(format: string): Promise<void> {
    this.snippetFormat.set(format as SnippetFormat);
    await this.loadSnippet();
  }

  private async loadSnippet(): Promise<void> {
    this.snippetError.set(null);
    try {
      this.snippet.set(await this.api.snippet(this.issuerName(), this.mapping() ?? '', this.snippetFormat(), this.project()));
    } catch (e) {
      this.snippet.set(null);
      this.snippetError.set(asApiError(e));
    }
  }

  protected readonly formatOptions: readonly SelectOption[] = [
    { value: 'npm', label: 'npm' },
    { value: 'nuget', label: 'NuGet' },
  ];

  /* ---- saving ---- */

  protected async save(event: Event): Promise<void> {
    event.preventDefault();
    if (!this.writable() || this.jsonError()) return;
    const project = this.project();
    const issuer = this.issuerName();
    if (!issuer) {
      this.saveError.set(new ApiError(422, { code: 'validation_failed', errors: [{ pointer: '/issuer', code: 'invalid_request', detail: 'Choose an issuer.' }] }, null));
      return;
    }
    const body = inputFromForm(this.form(), project);
    this.busy.set(true);
    this.saveError.set(null);
    this.warnings.set([]);
    try {
      const stored = this.loaded();
      if (stored === null) {
        const created = await this.api.createMapping(issuer, body, project);
        this.baseline = JSON.stringify(body);
        this.notifier.status($localize`:@@ci.editor.created:Mapping created.`);
        await this.router.navigate(ciRoutes.mapping(project, issuer, created.value.name), {
          replaceUrl: true,
          queryParams: ciRoutes.mappingQuery(project, issuer),
          state: { warnings: created.value.warnings ?? [] },
        });
        return;
      }
      const patch = changedMembers(body, stored.value);
      if (Object.keys(patch).length === 0) return;
      const result = await this.conflicts.save<CiMapping>({
        etag: stored.etag,
        patch,
        send: (etag, current) =>
          this.api.replaceMapping(
            issuer,
            stored.value.name,
            current === null ? body : overwriteBody(body, patch, inputFromForm(formFromMapping(current), project), stored.value),
            etag,
            project,
          ),
        reload: () => this.api.getMapping(issuer, stored.value.name, project),
      });
      if (result.outcome === 'dismissed') return;
      this.loaded.set(result.current);
      this.form.set(formFromMapping(result.current.value));
      this.baseline = JSON.stringify(inputFromForm(this.form(), project));
      this.jsonText.set(JSON.stringify(conditionFromNode(this.form().match), null, 2));
      if (result.outcome === 'saved') {
        this.warnings.set(result.current.value.warnings ?? []);
        this.notifier.status($localize`:@@ci.editor.saved:Mapping saved.`);
      }
    } catch (e) {
      const error = asApiError(e);
      if (error.code === 'reauthentication_required') this.drafts.keep(body);
      this.saveError.set(error);
    } finally {
      this.busy.set(false);
    }
  }
}
