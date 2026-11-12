import { Component, DestroyRef, computed, effect, inject, input, linkedSignal, resource, signal, untracked, viewChild } from '@angular/core';
import { ActivatedRoute, ActivatedRouteSnapshot, ResolveFn, Router, RouterLink } from '@angular/router';
import { newIdempotencyKey } from '../../api/client';
import { collectAll } from '../../api/pagination';
import { ApiError, FieldProblem } from '../../api/problem';
import { ProjectsApi } from '../../api/projects-api';
import {
  FormatDescriptor,
  RepositoriesApi,
  Repository,
  RepositoryClass,
  RepositoryDefaults,
  RepositorySummary,
  repoRef,
} from '../../api/repositories-api';
import { Notifier } from '../../core/notifier';
import { ServerAddress } from '../../core/server-address';
import { SessionStore } from '../../core/session-store';
import { SelectOption } from '../../shared/access/fields';
import { DraftStore } from '../../shared/drafts';
import { ErrorNotice } from '../../shared/error-notice';
import { ErrorSummary, SummaryItem, fieldMessage, problemsAt } from '../../shared/field-errors';
import { asApiError, valueOf } from '../../shared/paged-resource';
import { HasUnsavedChanges } from '../../shared/unsaved-guard';
import { Disclosure } from '../../shared/ui/disclosure';
import { PageHeader } from '../../shared/ui/page-header';
import { VisibilityLevel, VisibilityLevelControl, aboveProjectReasons } from '../../shared/ui/visibility-level';
import { FormatSettings, formatPartNodes } from './format-settings';
import { MemberPicker } from './member-picker';
import { MirrorSettings, mirrorPointerLabel, mirrorTarget } from './mirror-settings';
import { CLASSES, classHelp, classLabel } from './repository-class';
import { RepositoryFields } from './repository-fields';
import { RepositoryFormatChooser } from './repository-format-chooser';
import {
  EMPTY_FORM,
  FormatMemberOf,
  RepositoryForm,
  apiAddress,
  createBodyOf,
  curlOf,
  keyShapeValid,
  maskSecrets,
  mirrorDefaultsOf,
} from './repository-model';
import { SettingSchema, errorTarget, settingNodes } from './settings-schema';
import { initialUpstream, upstreamDefaultsOf } from './upstream-model';
import { UpstreamSettings, upstreamPointerLabel, upstreamTarget } from './upstream-settings';

type KeyCheck = 'idle' | 'checking' | 'free' | 'taken' | 'unknown';

/** The pointers whose fields are in **Advanced settings**: an error there opens it (R-UI-083). */
const ADVANCED_POINTERS = ['/adminNotes', '/includePatterns', '/excludePatterns', '/hideExistence', '/remote/mode', '/remote/mirror'];

/** The title of the form's route: "New OCI (Docker) repository" (R-UI-080, Q-UI-353); the chooser's title for an unknown format. */
export const newRepositoryTitle: ResolveFn<string> = async (route: ActivatedRouteSnapshot) => {
  const name = route.paramMap.get('format') ?? '';
  let format: FormatDescriptor | undefined;
  try {
    format = (await inject(RepositoriesApi).formats()).find((f) => f.name === name);
  } catch {
    format = undefined;
  }
  return format ? createHeading(format) : $localize`:@@route.repositories.new.title:New repository`;
};

/** "New <display name> repository". */
export function createHeading(format: FormatDescriptor): string {
  return $localize`:@@repo.create.heading:New ${format.displayName ?? format.name}:format: repository`;
}

/** The class a new repository of the format starts with: local where the format has it (Q-UI-341). */
function presetClass(format: FormatDescriptor | null): RepositoryClass {
  return CLASSES.find((c) => format?.classes.includes(c)) ?? 'local';
}

/** What a form that asks for a recent sign-in keeps across the redirect; never a password (R-UI-043). */
interface CreateDraft {
  readonly format: string;
  readonly cls: RepositoryClass;
  readonly project: string;
  readonly form: RepositoryForm;
  readonly keyEdited: boolean;
  readonly artifacts: VisibilityLevel | null;
  readonly members: readonly string[];
  readonly deployTarget: string | null;
  readonly targetChosen: boolean;
}

/**
 * The form of a new repository of one format (R-UI-080 to R-UI-088, Q-UI-340 to Q-UI-353): one page titled after the format, with
 * the class, the key (the server's free suggestion, R-REPO-063), the description, the visibility (preset from the same answer; left
 * out when it is not known, so the server applies its default), the class's own group (the upstream of a remote, the members and the
 * deployment target of a virtual repository), and every other setting in one closed **Advanced settings** section. Nothing is red
 * before it was touched or the form was sent (Q-UI-350). The create writes only what differs from a default (Q-UI-80), with the
 * `Idempotency-Key` generated when the form opened (R-UI-042), and opens the new repository's content page.
 */
@Component({
  selector: 'trove-repository-create',
  imports: [
    RouterLink,
    Disclosure,
    ErrorNotice,
    ErrorSummary,
    FormatSettings,
    MemberPicker,
    MirrorSettings,
    PageHeader,
    RepositoryFields,
    RepositoryFormatChooser,
    UpstreamSettings,
    VisibilityLevelControl,
  ],
  templateUrl: './repository-create.html',
  styleUrl: './repository-create.scss',
})
export class RepositoryCreate implements HasUnsavedChanges {
  private readonly api = inject(RepositoriesApi);
  private readonly projectsApi = inject(ProjectsApi);
  private readonly router = inject(Router);
  private readonly route = inject(ActivatedRoute);
  private readonly notifier = inject(Notifier);
  private readonly session = inject(SessionStore);
  private readonly drafts = inject(DraftStore);
  private readonly serverAddress = inject(ServerAddress);
  private readonly destroyed = inject(DestroyRef);

  /** Route parameter `:project` inside a project; in the admin area the query parameter `project`, the project the form starts with. */
  readonly project = input<string | undefined>();
  /** Route parameter `:format`: the format the form is for. */
  readonly format = input<string | undefined>();

  private readonly summaryView = viewChild(ErrorSummary);

  /** Inside a project the project is given, not chosen (Q-UI-129). */
  protected readonly inProject = (): boolean => this.route.snapshot.paramMap.has('project');

  /** Generated when the form opens and kept until it closes (R-UI-042). */
  private readonly idempotencyKey = newIdempotencyKey();

  protected readonly formats = resource({ loader: () => this.api.formats() });
  private readonly baseUrl = resource({ loader: () => this.serverAddress.baseUrl() });
  protected readonly descriptor = computed<FormatDescriptor | null>(
    () => (valueOf(this.formats) ?? []).find((f) => f.name === this.format()) ?? null,
  );
  /** The format of the address when the installation does not have it: the chooser is shown with a notice (R-UI-080). */
  protected readonly unknownFormat = computed(() =>
    valueOf(this.formats) !== undefined && this.descriptor() === null ? (this.format() ?? '') : null,
  );
  protected readonly loadFailure = computed(() => {
    const e = this.formats.error();
    return e ? asApiError(e) : null;
  });
  protected readonly formatLabel = computed(() => this.descriptor()?.displayName ?? this.format() ?? '');
  protected readonly heading = computed(() => {
    const f = this.descriptor();
    return f ? createHeading(f) : '';
  });
  protected readonly projectLine = computed(() =>
    this.inProject() ? $localize`:@@repo.create.in-project:In the project ${this.chosenProject()}:project:.` : '',
  );
  /** The chooser, with the admin area's project carried back. */
  protected readonly chooserLink = computed(() =>
    this.inProject() ? ['/projects', this.chosenProject(), 'repositories', 'new'] : ['/admin/repositories/new'],
  );
  protected readonly chooserQuery = computed(() =>
    !this.inProject() && this.chosenProject() ? { project: this.chosenProject() } : {},
  );
  protected readonly cancelLink = computed(() =>
    this.inProject() ? ['/projects', this.chosenProject(), 'repositories'] : ['/admin/repositories'],
  );

  // ---- Project (admin area only) ----

  /**
   * The project the repository is created in. It follows the address until the user chooses one; after that only the user
   * changes it, so an input bound late by the router never drops a choice made right after the page opened.
   */
  private readonly projectChoice = signal<string | null>(null);
  protected readonly chosenProject = computed(() => this.projectChoice() ?? this.project() ?? '');
  /** The projects the caller may create in: every project for `projects:read`, the projects it owns otherwise; the server decides. */
  protected readonly projects = resource({
    // The session's projects and capabilities are the parameters: a session that arrives after the page opened lists again,
    // and the choice, which is the user's (`projectChoice`), stays.
    params: () => ({
      owned: this.session
        .projects()
        .filter((p) => p.role === 'owner')
        .map((p) => p.key),
      all: !this.inProject() && this.session.has('projects:read'),
    }),
    loader: async ({ params: { owned, all } }): Promise<readonly string[]> => {
      if (!all) return owned;
      try {
        return (await collectAll(this.projectsApi.list({}))).map((p) => p.key);
      } catch {
        return owned;
      }
    },
  });
  protected readonly loadingProjects = $localize`:@@repo.create.project-loading:Loading projects…`;
  protected readonly projectOptions = computed<readonly SelectOption[]>(() => [
    { value: '', label: $localize`:@@repo.create.project-choose:Choose a project` },
    ...(valueOf(this.projects) ?? []).map((k) => ({ value: k, label: k })),
  ]);

  // ---- Class and the defaults of the class (R-REPO-063) ----

  protected readonly classes = CLASSES;
  /** The class of a draft kept across a sign-in; it wins over the preset once the format is known. */
  private restoredClass: RepositoryClass | null = null;
  protected readonly cls = linkedSignal<RepositoryClass>(() => {
    const f = this.descriptor();
    return this.restoredClass !== null && f?.classes.includes(this.restoredClass) ? this.restoredClass : presetClass(f);
  });
  protected readonly defaults = resource({
    params: () => {
      const format = this.descriptor()?.name;
      const project = this.chosenProject();
      return format && project ? { project, format, cls: this.cls() } : undefined;
    },
    // A caller the server refuses, or a server without the operation, gets the form without the values it would have given:
    // the key is typed, and the visibility is left to the server.
    loader: async ({ params }): Promise<RepositoryDefaults | null> => {
      try {
        return await this.api.defaults(params.project, params.format, params.cls);
      } catch {
        return null;
      }
    },
  });
  /** The artifacts level a create without `visibility` stores; null when not known, and then the field is left out. */
  protected readonly defaultLevel = computed<VisibilityLevel | null>(
    () => valueOf(this.defaults)?.repository.visibility?.artifacts ?? null,
  );
  /** The document of the defaults, for the upstream section's periods; null while not known. */
  protected readonly defaultDocument = computed<Readonly<Record<string, unknown>> | null>(
    () => (valueOf(this.defaults)?.repository as Readonly<Record<string, unknown>> | undefined) ?? null,
  );
  /** The server's defaults of `remote.mirror` (R-REPO-063): the values of the Mirror group's fields. */
  protected readonly mirrorDefaults = computed(() => mirrorDefaultsOf(this.defaultDocument()));
  private readonly remoteDefaults = computed(() => {
    const r = this.defaultDocument()?.['remote'];
    return typeof r === 'object' && r !== null ? (r as Readonly<Record<string, unknown>>) : null;
  });

  // ---- The form ----

  protected readonly form = signal<RepositoryForm>(EMPTY_FORM);
  /** The form as the screen last set it; a difference is the user's (the unsaved guard). */
  private readonly pristine = signal<RepositoryForm>(EMPTY_FORM);
  private readonly keyEdited = signal(false);
  protected readonly keyTouched = signal(false);
  protected readonly keyCheck = signal<KeyCheck>('idle');
  private keyTimer: ReturnType<typeof setTimeout> | undefined;
  /** **Create repository** was pressed: every field shows its error (R-UI-086). */
  protected readonly submitted = signal(false);
  protected readonly creating = signal(false);
  protected readonly failure = signal<ApiError | null>(null);
  protected readonly advancedOpen = signal(false);
  private created = false;

  /** What the creator chose; null while the preset of R-REPO-063 stands. */
  protected readonly artifactsChoice = signal<VisibilityLevel | null>(null);
  protected readonly artifacts = computed(() => this.artifactsChoice() ?? this.defaultLevel());
  private readonly projectLevel = resource({
    params: () => this.chosenProject() || undefined,
    loader: async ({ params }): Promise<VisibilityLevel | null> => {
      try {
        return (await this.projectsApi.get(params)).value.visibility;
      } catch {
        return null;
      }
    },
  });
  protected readonly levelReasons = computed(() => aboveProjectReasons(valueOf(this.projectLevel) ?? null));

  // ---- Members of a virtual repository (R-UI-081, Q-UI-347) ----

  protected readonly members = signal<readonly string[]>([]);
  protected readonly deployTarget = signal<string | null>(null);
  private readonly targetChosen = signal(false);
  private readonly candidates = resource({
    params: () => (this.cls() === 'virtual' ? this.chosenProject() || undefined : undefined),
    loader: () => this.api.listAll(),
  });
  protected readonly pool = computed<readonly RepositorySummary[]>(() =>
    (valueOf(this.candidates) ?? []).filter(
      (s) =>
        s.project === this.chosenProject() &&
        s.format === this.descriptor()?.name &&
        s.class !== 'virtual' &&
        s.lifecycle !== 'deleting',
    ),
  );
  private readonly poolClasses = computed(() => new Map(this.pool().map((s) => [s.key, s.class] as const)));
  protected readonly targetOptions = computed<readonly SelectOption[]>(() => [
    { value: '', label: $localize`:@@virtual.target.none:None: the repository is read-only` },
    ...this.members()
      .filter((k) => this.poolClasses().get(k) === 'local')
      .map((k) => ({ value: k, label: k })),
  ]);
  protected readonly membersEmpty = $localize`:@@repo.create.members-empty:No member yet. Add the repositories this one serves from.`;

  // ---- Format settings and mirror ----

  protected readonly mirrorGroup = computed(() => this.cls() === 'remote' && this.descriptor()?.mirror === true);
  private readonly member = computed<FormatMemberOf | null>(() => {
    const f = this.descriptor();
    if (!f) return null;
    return {
      name: f.name,
      schema: f.settingsSchema as SettingSchema | null | undefined,
      ...(this.cls() === 'remote'
        ? {
            remote: 'sparse' as const,
            mirror: this.mirrorGroup(),
            mirrorDefaults: this.mirrorDefaults(),
            upstream: upstreamDefaultsOf(f, this.remoteDefaults()),
          }
        : {}),
    };
  });
  protected readonly memberPointer = computed(() => `/${this.descriptor()?.name ?? ''}`);
  protected readonly nodes = computed(() => {
    const m = this.member();
    return m ? settingNodes(m.schema, this.cls(), m.name) : [];
  });
  protected readonly formatNodes = computed(() => formatPartNodes(this.nodes(), 'settings'));
  protected readonly formatMirrorNodes = computed(() => formatPartNodes(this.nodes(), 'mirror'));
  /** Without `repositories:write-config` the mirror is read-only (Q-REPO-142). */
  protected readonly mirrorRestricted = computed(() => !this.session.has('repositories:write-config'));

  // ---- Validation (R-UI-086) ----

  protected readonly keyValid = computed(() => keyShapeValid(this.form().key));
  protected readonly keyError = computed<string | null>(() => {
    const server = problemsAt(this.fieldErrors(), '/key')[0];
    if (server) return fieldMessage(server);
    const key = this.form().key;
    const shapeShown = this.submitted() || (this.keyTouched() && key !== '');
    if (shapeShown && !this.keyValid()) return keyShapeText();
    if (this.keyCheck() === 'taken') return $localize`:@@repo.key.taken:A repository with this key already exists in the project.`;
    return null;
  });
  protected readonly projectError = computed(() =>
    this.submitted() && this.chosenProject() === '' ? $localize`:@@repo.create.project-missing:Choose a project.` : null,
  );
  protected readonly fieldErrors = computed<ReadonlyMap<string, FieldProblem>>(() => this.failure()?.fieldErrors ?? new Map());
  protected readonly descriptionError = computed(() => {
    const problem = problemsAt(this.fieldErrors(), '/description')[0];
    return problem ? fieldMessage(problem) : null;
  });

  /** The form's own errors, as the summary lists them once **Create repository** was pressed. */
  private readonly ownProblems = computed<readonly SummaryItem[]>(() => {
    const items: SummaryItem[] = [];
    const f = this.descriptor();
    if (!this.inProject() && this.chosenProject() === '') {
      items.push({ label: $localize`:@@repo.create.project:Project`, message: this.projectError() ?? '', targetId: 'f-project' });
    }
    if (!this.keyValid() || this.keyCheck() === 'taken') {
      items.push({ label: $localize`:@@repo.field.key:Key`, message: this.keyError() ?? keyShapeText(), targetId: 'f-key' });
    }
    if (f && this.cls() === 'remote' && f.upstreamPresets.length === 0 && this.form().upstream.url.trim() === '') {
      items.push({ label: $localize`:@@upstream.url:Upstream URL`, message: missingUrlText(), targetId: 'f-remote-url' });
    }
    return items;
  });
  protected readonly summary = computed<readonly SummaryItem[]>(() => {
    const server = [...this.fieldErrors()].map(([pointer, problem]) => ({
      label: this.pointerLabel(pointer),
      message: fieldMessage(problem),
      targetId: this.pointerTarget(pointer),
    }));
    if (server.length > 0) return server;
    return this.submitted() ? this.ownProblems() : [];
  });

  // ---- The request ----

  protected readonly body = computed<Repository | null>(() => {
    const m = this.member();
    if (!m) return null;
    const level = this.artifactsChoice();
    const visibility = level !== null && level !== this.defaultLevel() ? { artifacts: level, wiki: level } : undefined;
    return createBodyOf(this.cls(), m, this.form(), {
      ...(visibility ? { visibility } : {}),
      ...(this.cls() === 'virtual' ? { virtual: { members: this.members(), deployTarget: this.deployTarget() } } : {}),
    });
  });
  /** The request as shown: a typed password is never displayed (R-UI-043). */
  protected readonly requestJson = computed(() => JSON.stringify(maskSecrets(this.body()), null, 2));
  protected readonly curl = computed(() => {
    const doc = this.body();
    const path = `/api/v1/projects/${this.chosenProject() || '<project>'}/repositories`;
    return doc
      ? curlOf('POST', apiAddress(valueOf(this.baseUrl), path), maskSecrets(doc), ['Idempotency-Key: <a fresh UUID>'])
      : '';
  });

  constructor() {
    this.destroyed.onDestroy(() => clearTimeout(this.keyTimer));
    const draft = this.drafts.take<CreateDraft>();
    if (draft) this.restore(draft);
    // A form that asks for a recent sign-in keeps its draft without secrets across the redirect.
    effect(() => {
      if (!this.session.reauthRequired()) return;
      untracked(() => this.keepDraft());
    });
    // The class's defaults arrive: the key follows the suggestion until it is edited (Q-UI-342).
    effect(() => {
      const d = valueOf(this.defaults);
      if (d === undefined) return;
      untracked(() => {
        if (this.keyEdited()) return;
        const key = d?.suggestedKey ?? '';
        this.form.update((f) => ({ ...f, key }));
        this.pristine.update((f) => ({ ...f, key }));
      });
    });
    // The upstream of a remote starts at the format's default preset (R-UI-082).
    effect(() => {
      const f = this.descriptor();
      if (!f) return;
      untracked(() => {
        if (this.form().upstream.url !== '' || draft) return;
        const upstream = initialUpstream(f);
        this.form.update((current) => ({ ...current, upstream }));
        this.pristine.update((current) => ({ ...current, upstream }));
      });
    });
    // The key's availability, asked 400 ms after the last keystroke within the chosen project (R-UI-086).
    effect(() => {
      const key = this.form().key;
      const project = this.chosenProject();
      const suggested = valueOf(this.defaults)?.suggestedKey ?? null;
      const check = this.keyValid() && project !== '' && key !== suggested;
      untracked(() => this.scheduleKeyCheck(key, project, check));
    });
  }

  hasUnsavedChanges(): boolean {
    if (this.created) return false;
    return (
      this.keyEdited() ||
      JSON.stringify(this.form()) !== JSON.stringify(this.pristine()) ||
      this.artifactsChoice() !== null ||
      this.members().length > 0
    );
  }

  protected classText = classLabel;
  protected classHelpText = classHelp;

  protected supports(cls: RepositoryClass): boolean {
    return this.descriptor()?.classes.includes(cls) ?? false;
  }

  /** The sentence at a class the format does not implement (Q-UI-113). */
  protected unsupportedReason(cls: RepositoryClass): string {
    return $localize`:@@repo.class.unsupported:${this.formatLabel()}:format: does not implement the class ${classLabel(cls)}:class:.`;
  }

  /**
   * Another class keeps the project, an edited key, the description and the visibility, and starts its own group and the Advanced
   * settings from the defaults of that class (R-UI-080, Q-UI-341).
   */
  protected chooseClass(cls: RepositoryClass): void {
    if (this.cls() === cls || !this.supports(cls)) return;
    this.cls.set(cls);
    const f = this.form();
    const next: RepositoryForm = {
      ...EMPTY_FORM,
      key: f.key,
      description: f.description,
      upstream: cls === 'remote' ? initialUpstream(this.descriptor()) : EMPTY_FORM.upstream,
    };
    this.form.set(next);
    this.pristine.set({ ...next, description: '', key: this.keyEdited() ? '' : next.key });
    this.members.set([]);
    this.deployTarget.set(null);
    this.targetChosen.set(false);
    this.failure.set(null);
  }

  protected setProject(project: string): void {
    this.projectChoice.set(project);
    this.members.set([]);
    this.deployTarget.set(null);
    this.targetChosen.set(false);
  }

  protected setKey(key: string): void {
    this.keyEdited.set(true);
    this.form.update((f) => ({ ...f, key }));
  }

  protected setDescription(description: string): void {
    this.form.update((f) => ({ ...f, description }));
  }

  protected setFormatMember(formatMember: Record<string, unknown>): void {
    this.form.update((f) => ({ ...f, formatMember }));
  }

  protected setArtifacts(level: VisibilityLevel): void {
    this.artifactsChoice.set(level);
  }

  /** The first local member becomes the deployment target until the user chooses one (Q-UI-347). */
  protected presetTarget(): void {
    if (this.targetChosen()) return;
    this.deployTarget.set(this.members().find((k) => this.poolClasses().get(k) === 'local') ?? null);
  }

  protected setTarget(value: string): void {
    this.targetChosen.set(true);
    this.deployTarget.set(value === '' ? null : value);
  }

  protected onMemberRemoved(key: string): void {
    if (this.deployTarget() === key) {
      this.deployTarget.set(null);
      this.presetTarget();
    }
  }

  /** A link of the summary names a field inside Advanced settings: it opens first (R-UI-083). */
  protected reveal(targetId: string): void {
    if (this.inAdvanced(targetId)) this.advancedOpen.set(true);
  }

  protected async create(event: Event): Promise<void> {
    event.preventDefault();
    if (this.creating()) return;
    this.submitted.set(true);
    this.failure.set(null);
    if (this.ownProblems().length > 0) {
      this.summaryView()?.focusSummary();
      return;
    }
    const doc = this.body();
    if (!doc) return;
    this.creating.set(true);
    try {
      const created = await this.api.create(this.chosenProject(), doc, this.idempotencyKey);
      this.created = true;
      this.notifier.status($localize`:@@repo.wizard.created:Repository ${created.value.key}:key: created.`);
      await this.router.navigate(['/projects', this.chosenProject(), 'repositories', created.value.key]);
    } catch (e) {
      const error = asApiError(e);
      this.failure.set(error);
      if ([...error.fieldErrors.keys()].some((p) => this.pointerInAdvanced(p))) this.advancedOpen.set(true);
      if (error.fieldErrors.size > 0) this.summaryView()?.focusSummary();
    } finally {
      this.creating.set(false);
    }
  }

  private pointerInAdvanced(pointer: string): boolean {
    const own = [...ADVANCED_POINTERS, this.memberPointer()];
    return own.some((p) => pointer === p || pointer.startsWith(`${p}/`));
  }

  private inAdvanced(targetId: string): boolean {
    return !!document.getElementById(targetId)?.closest('.advanced-settings');
  }

  protected pointerLabel(pointer: string): string {
    const upstream = this.cls() === 'remote' ? upstreamPointerLabel(pointer) : null;
    if (upstream) return upstream;
    const mirror = mirrorPointerLabel(pointer);
    if (mirror) return mirror;
    switch (pointer.split('/')[1] ?? pointer) {
      case 'key':
        return $localize`:@@repo.field.key:Key`;
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
      case 'virtual':
        return $localize`:@@repo.create.members:Members`;
      default:
        return pointer;
    }
  }

  protected pointerTarget(pointer: string): string {
    if (this.cls() === 'remote') {
      const target = upstreamTarget(pointer) ?? (this.mirrorGroup() ? mirrorTarget(pointer) : null);
      if (target) return target;
    }
    if (pointer === '/description') return 'f-description';
    if (pointer.startsWith('/virtual')) return 'f-deployTarget';
    return errorTarget(pointer, this.nodes(), this.memberPointer());
  }

  private scheduleKeyCheck(key: string, project: string, check: boolean): void {
    clearTimeout(this.keyTimer);
    if (!check) {
      this.keyCheck.set('idle');
      return;
    }
    this.keyCheck.set('checking');
    this.keyTimer = setTimeout(() => void this.checkKey(key, project), 400);
  }

  /** A `GET` of the key within the project that answers 404 means the key is free. */
  private async checkKey(key: string, project: string): Promise<void> {
    try {
      await this.api.get(repoRef(project, key));
      if (this.form().key === key && this.chosenProject() === project) this.keyCheck.set('taken');
    } catch (e) {
      if (this.form().key !== key || this.chosenProject() !== project) return;
      this.keyCheck.set(e instanceof ApiError && e.status === 404 ? 'free' : 'unknown');
    }
  }

  private keepDraft(): void {
    const draft: CreateDraft = {
      format: this.format() ?? '',
      cls: this.cls(),
      project: this.chosenProject(),
      // The password is a secret: it never goes to storage (R-UI-043).
      form: { ...this.form(), upstream: { ...this.form().upstream, password: '' } },
      keyEdited: this.keyEdited(),
      artifacts: this.artifactsChoice(),
      members: this.members(),
      deployTarget: this.deployTarget(),
      targetChosen: this.targetChosen(),
    };
    this.drafts.keep(draft);
  }

  private restore(draft: CreateDraft): void {
    this.restoredClass = draft.cls;
    if (draft.project) this.projectChoice.set(draft.project);
    this.form.set({ ...EMPTY_FORM, ...draft.form });
    this.keyEdited.set(draft.keyEdited);
    this.artifactsChoice.set(draft.artifacts);
    this.members.set(draft.members);
    this.deployTarget.set(draft.deployTarget);
    this.targetChosen.set(draft.targetChosen);
  }
}

/** What to type into the key, in words (R-UI-086). */
export function keyShapeText(): string {
  return $localize`:@@repo.key.shape:Use 2 to 64 lower-case letters and digits, with ., _, __ or - between them.`;
}

/** What a missing upstream URL asks for, the words of the upstream section's own error (R-UI-082, R-UI-086). */
function missingUrlText(): string {
  return $localize`:@@upstream.url.required:Enter the address of the registry, such as https://registry.example.com/.`;
}
