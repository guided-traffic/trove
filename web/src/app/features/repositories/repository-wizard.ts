import {
  Component,
  DestroyRef,
  computed,
  effect,
  linkedSignal,
  inject,
  input,
  resource,
  signal,
  untracked,
} from '@angular/core';
import { Listbox, Option } from '@angular/aria/listbox';
import { ActivatedRoute, Router, RouterLink } from '@angular/router';
import { newIdempotencyKey } from '../../api/client';
import { ApiError, FieldProblem } from '../../api/problem';
import {
  FormatDescriptor,
  RepositoriesApi,
  Repository,
  RepositoryClass,
  repoRef,
} from '../../api/repositories-api';
import { collectAll } from '../../api/pagination';
import { ProjectsApi } from '../../api/projects-api';
import { VisibilityApi } from '../../api/visibility-api';
import {
  VisibilityIcon,
  VisibilityLevel,
  VisibilityLevelControl,
  aboveProjectReasons,
  levelName,
  lowerLevel,
} from '../../shared/ui/visibility-level';
import { SelectField, SelectOption } from '../../shared/access/fields';
import { Notifier } from '../../core/notifier';
import { AddressField } from '../../shared/address-field';
import { ServerAddress } from '../../core/server-address';
import { SessionStore } from '../../core/session-store';
import { ErrorNotice } from '../../shared/error-notice';
import { ErrorSummary, SummaryItem, fieldMessage, problemsAt } from '../../shared/field-errors';
import { DraftStore } from '../../shared/drafts';
import { FormatIconView } from '../../shared/format-icon';
import { asApiError, valueOf } from '../../shared/paged-resource';
import { HasUnsavedChanges } from '../../shared/unsaved-guard';
import { FormatSettings } from './format-settings';
import { MirrorSettings, mirrorPointerLabel, mirrorTarget } from './mirror-settings';
import { RepositoryFields } from './repository-fields';
import {
  EMPTY_FORM,
  FormatMemberOf,
  KEY_PATTERN,
  RepositoryForm,
  apiAddress,
  clientAddress,
  curlOf,
  documentOf,
  lines,
  maskSecrets,
  mirrorOf,
  sameMirror,
  settingsOf,
} from './repository-model';
import { effectiveUrl, initialUpstream, upstreamDefaultsOf, upstreamOf } from './upstream-model';
import { SettingSchema, errorTarget, settingEntries, settingNodes } from './settings-schema';
import { UpstreamSettings, upstreamPointerLabel, upstreamTarget } from './upstream-settings';

type KeyCheck = 'idle' | 'checking' | 'free' | 'taken' | 'unknown';

/** The steps of the wizard (R-UI-021, Q-UI-112): the format comes first, because it limits everything after it. */
const STEP_FORMAT = 0;
const STEP_CLASS = 1;
const STEP_BASIC = 2;
const STEP_SPECIFIC = 3;

/** The classes of the repository document (`Repository.class` of the API), in the order they are offered. */
const CLASSES: readonly RepositoryClass[] = ['local', 'remote', 'virtual'];

/** The pointer prefixes that belong to each step, to bring the user to the field a server error names. */
const STEP_OF_POINTER: readonly (readonly [prefix: string, step: number])[] = [
  ['/format', STEP_FORMAT],
  ['/class', STEP_CLASS],
  ['/key', STEP_BASIC],
  ['/description', STEP_BASIC],
  ['/adminNotes', STEP_BASIC],
];

export function classLabel(cls: string): string {
  switch (cls) {
    case 'local':
      return $localize`:@@repo.class.local:Local`;
    case 'remote':
      return $localize`:@@repo.class.remote:Remote`;
    case 'virtual':
      return $localize`:@@repo.class.virtual:Virtual`;
    default:
      return cls;
  }
}

export function classHelp(cls: string): string {
  switch (cls) {
    case 'local':
      return $localize`:@@repo.class.local.help:Holds content that is deployed to trove.`;
    case 'remote':
      return $localize`:@@repo.class.remote.help:Caches content from an upstream registry.`;
    case 'virtual':
      return $localize`:@@repo.class.virtual.help:Presents several repositories under one address.`;
    default:
      return '';
  }
}

/**
 * Repository creation wizard (R-UI-021, Q-UI-112): format, class, basic settings, class-specific
 * settings, review. The format is the first choice because some features are impossible for some
 * formats; the class step then offers the classes of the chosen format and shows the others as
 * unavailable, with the reason (Q-UI-113). The formats and their classes come only from
 * `GET /api/v1/formats`; the UI holds no list of formats of its own. The `Idempotency-Key` is generated when the wizard opens, so a double click or a retry
 * after a lost answer creates one repository (R-UI-042). Server field errors appear at their
 * fields, in a summary, and the wizard returns to the step that holds the field.
 */
@Component({
  selector: 'trove-repository-wizard',
  imports: [
    RouterLink,
    ErrorNotice,
    ErrorSummary,
    FormatSettings,
    RepositoryFields,
    MirrorSettings,
    UpstreamSettings,
    Listbox,
    Option,
    SelectField,
    FormatIconView,
    AddressField,
    VisibilityIcon,
    VisibilityLevelControl,
  ],
  templateUrl: './repository-wizard.html',
  styleUrl: './repository-wizard.scss',
})
export class RepositoryWizard implements HasUnsavedChanges {
  private readonly api = inject(RepositoriesApi);
  private readonly router = inject(Router);
  private readonly notifier = inject(Notifier);
  private readonly session = inject(SessionStore);
  private readonly drafts = inject(DraftStore);
  private readonly serverAddress = inject(ServerAddress);
  private readonly destroyed = inject(DestroyRef);

  /**
   * Route parameter `:project` below `/projects/:project/repositories`: the project the repository is created in, fixed
   * (Q-UI-129). In the admin area it is the query parameter `project`, the project the form starts with.
   */
  readonly project = input<string | undefined>();
  private readonly route = inject(ActivatedRoute);
  /** The wizard runs inside a project: the project is not chosen but given. */
  protected readonly inProject = (): boolean => this.route.snapshot.paramMap.has('project');
  protected readonly cancelLink = computed(() =>
    this.inProject() ? ['/projects', this.chosenProject(), 'repositories'] : ['/admin/repositories'],
  );
  /** The project the repository is created in (R-REPO-001); chosen in the basic step. */
  protected readonly chosenProject = linkedSignal(() => this.project() ?? '');
  private readonly projectsApi = inject(ProjectsApi);
  /**
   * The projects the caller may create in: every project for a holder of `projects:read` (an administrator), the
   * projects it is an Owner of otherwise (R-REPO-001). The server decides.
   */
  protected readonly projects = resource({
    loader: async (): Promise<readonly string[]> => {
      const owned = this.session
        .projects()
        .filter((p) => p.role === 'owner')
        .map((p) => p.key);
      if (!this.session.has('projects:read')) return owned;
      try {
        return (await collectAll(this.projectsApi.list({}))).map((p) => p.key);
      } catch {
        return owned;
      }
    },
  });
  protected readonly loadingProjects = $localize`:@@repo.wizard.project-loading:Loading projects…`;
  protected readonly projectOptions = computed<readonly SelectOption[]>(() => [
    { value: '', label: $localize`:@@repo.wizard.project-choose:Choose a project` },
    ...(valueOf(this.projects) ?? []).map((k) => ({ value: k, label: k })),
  ]);

  private readonly visibilityApi = inject(VisibilityApi);
  /** The installation's default level; `null` for a caller without `settings:read` (a project Owner), whose creates then leave the field out. */
  private readonly defaultLevel = resource({
    loader: async (): Promise<VisibilityLevel | null> => {
      // Asking without the permission is a 403 the screen already knows; the create leaves the level out then.
      if (!this.session.has('settings:read')) return null;
      try {
        return (await this.visibilityApi.load()).settings.defaultLevel;
      } catch {
        return null;
      }
    },
  });
  /** The chosen project's level bounds the repository's; unknown when the caller cannot read it, then the server decides. */
  private readonly projectLevel = resource({
    params: () => this.chosenProject(),
    loader: async ({ params }): Promise<VisibilityLevel | null> => {
      if (params === '') return null;
      try {
        return (await this.projectsApi.get(params)).value.visibility;
      } catch {
        return null;
      }
    },
  });
  /** What the creator chose; `null` while the preset stands. */
  private readonly artifactsChoice = signal<VisibilityLevel | null>(null);
  /**
   * The artifacts level of the new repository: the choice, else the lower of the default and the project's level (R-UI-055). `null`
   * while the default is not known and nothing is chosen: the create then leaves `visibility` out and the server applies its default.
   */
  protected readonly artifacts = computed<VisibilityLevel | null>(() => {
    const project = valueOf(this.projectLevel) ?? null;
    const known = valueOf(this.defaultLevel);
    const choice = this.artifactsChoice();
    const level = choice ?? (known ? lowerLevel(known, project ?? 'public') : null);
    return level === null || project === null ? level : lowerLevel(level, project);
  });
  protected readonly unsetNote = $localize`:@@repo.visibility.unset:Not chosen: the repository takes the default of the installation and of its project.`;
  protected readonly artifactReasons = computed(() => aboveProjectReasons(valueOf(this.projectLevel) ?? null));
  protected readonly artifactsLabel = computed(() =>
    this.format()?.wiki ? $localize`:@@repo.visibility.artifacts:Artifacts` : $localize`:@@repo.visibility.single:Visibility`,
  );
  protected readonly artifactsName = computed(() => {
    const level = this.artifacts();
    return level === null ? $localize`:@@repo.review.visibility-default:Default of the installation and of the project` : levelName(level);
  });
  protected setArtifacts(level: VisibilityLevel): void {
    this.artifactsChoice.set(level);
  }

  /** Generated when the wizard opens and kept until it closes (R-UI-042). */
  private readonly idempotencyKey = newIdempotencyKey();

  protected readonly formats = resource({ loader: () => this.api.formats() });
  private readonly baseUrl = resource({ loader: () => this.serverAddress.baseUrl() });
  protected readonly step = signal(0);
  protected readonly cls = signal<RepositoryClass | null>(null);
  protected readonly formatName = signal<string | null>(null);
  protected readonly form = signal<RepositoryForm>(EMPTY_FORM);
  protected readonly creating = signal(false);
  protected readonly failure = signal<ApiError | null>(null);
  protected readonly keyCheck = signal<KeyCheck>('idle');
  protected readonly showErrors = signal(false);
  private created = false;
  /** The form as the wizard opened it. */
  private initialForm: RepositoryForm = EMPTY_FORM;
  private keyTimer: ReturnType<typeof setTimeout> | undefined;

  /** The filter text of the format step (Q-UI-115). */
  protected readonly formatFilter = signal('');

  protected readonly steps = computed(() => [
    $localize`:@@repo.wizard.step.format:Format`,
    $localize`:@@repo.wizard.step.class:Class`,
    $localize`:@@repo.wizard.step.basic:Basic settings`,
    this.specificStepLabel(),
    $localize`:@@repo.wizard.step.review:Review`,
  ]);

  private specificStepLabel(): string {
    const nodes = this.settingNodes().length > 0;
    if (this.mirrorSection()) {
      return nodes
        ? $localize`:@@repo.wizard.step.access-format-mirror:Access, upstream, ${this.formatLabel()}:format: and mirror settings`
        : $localize`:@@repo.wizard.step.access-mirror:Access, upstream and mirror settings`;
    }
    if (this.cls() === 'remote') {
      return nodes
        ? $localize`:@@repo.wizard.step.access-upstream-format:Access, upstream and ${this.formatLabel()}:format: settings`
        : $localize`:@@repo.wizard.step.access-upstream:Access and upstream settings`;
    }
    return nodes
      ? $localize`:@@repo.wizard.step.access-format:Access and ${this.formatLabel()}:format: settings`
      : $localize`:@@repo.wizard.step.access:Access settings`;
  }

  /** Every class of the document; one the chosen format does not implement is shown unavailable (Q-UI-113). */
  protected readonly classes = CLASSES;
  /** The installed formats that match the filter, by display name or name, case-insensitive (Q-UI-115). */
  protected readonly shownFormats = computed<readonly FormatDescriptor[]>(() => {
    const q = this.formatFilter().trim().toLowerCase();
    return (valueOf(this.formats) ?? []).filter(
      (f) => q === '' || (f.displayName ?? f.name).toLowerCase().includes(q) || f.name.includes(q),
    );
  });
  protected readonly formatCount = computed(() => (valueOf(this.formats) ?? []).length);
  /** The value of the tile listbox: follows the chosen format, and is set again when a click clears it. */
  protected readonly chosenFormat = linkedSignal<string[]>(() => {
    const name = this.formatName();
    return name ? [name] : [];
  });
  /** The Mirror section applies to a remote repository of a format that can mirror (R-UI-021). */
  protected readonly mirrorSection = computed(
    () => this.cls() === 'remote' && this.format()?.mirror === true,
  );
  /** Without `repositories:write-config` (a project Owner) the section is read-only (Q-REPO-142). */
  protected readonly mirrorRestricted = computed(
    () => !this.session.has('repositories:write-config'),
  );
  protected readonly format = computed(
    () => (valueOf(this.formats) ?? []).find((f) => f.name === this.formatName()) ?? null,
  );
  protected readonly formatLabel = computed(
    () => this.format()?.displayName ?? this.formatName() ?? '',
  );
  /** The format member of the document and the schema it is rendered from (R-UI-021). */
  private readonly member = computed<FormatMemberOf | null>(() => {
    const name = this.formatName();
    return name
      ? {
          name,
          schema: this.format()?.settingsSchema as SettingSchema | null | undefined,
          ...(this.cls() === 'remote'
            ? {
                remote: 'sparse' as const,
                mirror: this.mirrorSection(),
                upstream: upstreamDefaultsOf(this.format()),
              }
            : {}),
        }
      : null;
  });
  /** The upstream settings the create writes, for the review; a password is not shown. */
  protected readonly upstreamEntries = computed(() => {
    const defaults = upstreamDefaultsOf(this.format());
    const upstream = this.form().upstream;
    const url = effectiveUrl(upstream, defaults);
    return settingEntries(
      maskSecrets({ ...(url ? { url } : {}), ...upstreamOf(upstream, defaults, 'sparse') }),
    );
  });
  protected readonly memberPointer = computed(() => `/${this.formatName() ?? ''}`);
  protected readonly settingNodes = computed(() => {
    const m = this.member();
    const cls = this.cls();
    return m && cls ? settingNodes(m.schema, cls, m.name) : [];
  });
  /** The format settings the create writes, for the review. */
  protected readonly formatEntries = computed(() => {
    const m = this.member();
    const written = m ? settingsOf(this.form(), m)[m.name] : undefined;
    return written ? settingEntries(written as Record<string, unknown>) : [];
  });
  /** The mirror members the create writes, for the review. */
  protected readonly mirrorEntries = computed(() => settingEntries(mirrorOf(this.form())));
  protected readonly loadFailure = computed(() => {
    const e = this.formats.error();
    return e ? asApiError(e) : null;
  });
  protected readonly keyValid = computed(
    () =>
      KEY_PATTERN.test(this.form().key) &&
      !this.form().key.includes('..') &&
      !this.form().key.endsWith('.'),
  );
  protected readonly fieldErrors = computed<ReadonlyMap<string, FieldProblem>>(
    () => this.failure()?.fieldErrors ?? new Map(),
  );
  protected readonly summary = computed<readonly SummaryItem[]>(() => {
    const items: SummaryItem[] = [];
    for (const [pointer, problem] of this.fieldErrors()) {
      items.push({
        label: this.pointerLabel(pointer),
        message: fieldMessage(problem),
        targetId: this.pointerTarget(pointer),
      });
    }
    return items;
  });
  protected readonly body = computed<Repository | null>(() => {
    const cls = this.cls();
    const member = this.member();
    return cls && member
      ? documentOf(cls, member, this.form(), this.artifacts() === null ? undefined : { artifacts: this.artifacts()!, wiki: this.artifacts()! })
      : null;
  });
  /** The request as shown: a typed password is never displayed (R-UI-043). */
  protected readonly requestJson = computed(() => JSON.stringify(maskSecrets(this.body()), null, 2));
  protected readonly curl = computed(() => {
    const doc = this.body();
    return doc
      ? curlOf('POST', apiAddress(valueOf(this.baseUrl), `/api/v1/projects/${this.chosenProject() || '<project>'}/repositories`), maskSecrets(doc), [
          'Idempotency-Key: <a fresh UUID>',
        ])
      : '';
  });
  protected readonly address = computed(() =>
    clientAddress(valueOf(this.baseUrl), this.format() ?? undefined, repoRef(this.chosenProject(), this.form().key)),
  );
  protected readonly includes = computed(() => lines(this.form().includePatterns));
  protected readonly excludes = computed(() => lines(this.form().excludePatterns));

  constructor() {
    this.destroyed.onDestroy(() => clearTimeout(this.keyTimer));
    const draft = this.drafts.take<{
      cls: RepositoryClass | null;
      format: string | null;
      form: RepositoryForm;
      step: number;
    }>();
    if (draft) {
      this.cls.set(draft.cls);
      this.formatName.set(draft.format);
      this.form.set({ ...EMPTY_FORM, ...draft.form });
      this.step.set(draft.step);
    }
    // A form that asks for a recent sign-in keeps its non-secret draft across the redirect.
    effect(() => {
      if (!this.session.reauthRequired()) return;
      untracked(() =>
        this.drafts.keep({
          cls: this.cls(),
          format: this.formatName(),
          // The password is a secret: it never goes to storage (R-UI-043).
          form: { ...this.form(), upstream: { ...this.form().upstream, password: '' } },
          step: this.step(),
        }),
      );
    });
    // Live check of the key (R-UI-021): the shape at once, availability after a pause, within the chosen project.
    effect(() => {
      const key = this.form().key;
      const valid = this.keyValid() && this.chosenProject() !== '';
      const project = this.chosenProject();
      untracked(() => this.scheduleKeyCheck(key, project, valid));
    });
  }

  hasUnsavedChanges(): boolean {
    return !this.created && (this.formatName() !== null || this.form() !== this.initialForm);
  }

  /** Whether the chosen format implements the class (`classes` of the format descriptor). */
  protected supports(cls: RepositoryClass): boolean {
    return this.format()?.classes.includes(cls) ?? false;
  }

  /** The sentence at a class the chosen format does not implement (Q-UI-113). */
  protected unsupportedReason(cls: RepositoryClass): string {
    return $localize`:@@repo.class.unsupported:${this.formatLabel()}:format: does not implement the class ${classLabel(cls)}:class:.`;
  }

  protected chooseClass(cls: RepositoryClass): void {
    if (this.cls() !== cls && this.supports(cls)) {
      this.cls.set(cls);
      this.resetDependent();
    }
  }

  /**
   * Another format keeps the class only when the new format implements it, and always starts the
   * format settings and the mirror section afresh (R-UI-021).
   */
  protected chooseFormat(name: string): void {
    if (this.formatName() === name) return;
    this.formatName.set(name);
    const next = (valueOf(this.formats) ?? []).find((f) => f.name === name);
    const cls = this.cls();
    if (cls !== null && !(next?.classes.includes(cls) ?? false)) this.cls.set(null);
    this.resetDependent();
  }

  /** The tile the listbox selected; a click on the chosen tile does not clear the choice. */
  protected onFormatSelected(values: readonly string[]): void {
    const name = values[0];
    if (name !== undefined) this.chooseFormat(name);
    else this.chosenFormat.set(this.formatName() ? [this.formatName()!] : []);
  }

  protected setFormatFilter(text: string): void {
    this.formatFilter.set(text);
  }

  /** Enter in the filter field chooses the one format that is left. */
  protected chooseOnlyMatch(event: Event): void {
    const only = this.shownFormats();
    if (only.length === 1) {
      event.preventDefault();
      this.chooseFormat(only[0]!.name);
    }
  }

  protected setFormatMember(formatMember: Record<string, unknown>): void {
    this.form.update((f) => ({ ...f, formatMember }));
  }

  /**
   * The format settings, the upstream settings and the mirror section belong to one format and one
   * class: another choice starts them afresh (a remote with the format's preset URL). A form that
   * holds none of them keeps its identity.
   */
  private resetDependent(): void {
    const f = this.form();
    const upstream = this.cls() === 'remote' ? initialUpstream(this.format()) : initialUpstream(null);
    if (
      Object.keys(f.formatMember).length === 0 &&
      sameMirror(f) &&
      JSON.stringify(f.upstream) === JSON.stringify(upstream)
    ) {
      return;
    }
    this.form.set({ ...f, formatMember: {}, remoteMode: 'pullThrough', mirror: {}, upstream });
  }

  protected canNext(): boolean {
    switch (this.step()) {
      case STEP_FORMAT:
        return this.formatName() !== null;
      case STEP_CLASS:
        return this.cls() !== null;
      case STEP_BASIC:
        return this.chosenProject() !== '' && this.keyValid() && this.keyCheck() !== 'taken';
      default:
        return true;
    }
  }

  protected next(): void {
    if (this.step() === STEP_BASIC) this.showErrors.set(true);
    if (this.canNext() && this.step() < this.steps().length - 1) this.step.update((s) => s + 1);
  }

  protected back(): void {
    if (this.step() > 0) this.step.update((s) => s - 1);
  }

  protected goTo(step: number): void {
    if (step < this.step()) this.step.set(step);
  }

  protected setKey(key: string): void {
    this.form.update((f) => ({ ...f, key }));
  }

  protected async create(): Promise<void> {
    const doc = this.body();
    if (!doc || this.creating()) return;
    this.creating.set(true);
    this.failure.set(null);
    try {
      const created = await this.api.create(this.chosenProject(), doc, this.idempotencyKey);
      this.created = true;
      this.notifier.status(
        $localize`:@@repo.wizard.created:Repository ${created.value.key}:key: created.`,
      );
      await this.router.navigate(['/projects', this.chosenProject(), 'repositories', created.value.key]);
    } catch (e) {
      const error = asApiError(e);
      this.failure.set(error);
      const first = [...error.fieldErrors.keys()][0];
      if (first !== undefined) this.step.set(this.stepOf(first));
    } finally {
      this.creating.set(false);
    }
  }

  private stepOf(pointer: string): number {
    return (
      STEP_OF_POINTER.find(
        ([prefix]) => pointer === prefix || pointer.startsWith(`${prefix}/`),
      )?.[1] ?? STEP_SPECIFIC
    );
  }

  protected pointerLabel(pointer: string): string {
    const upstream = this.cls() === 'remote' ? upstreamPointerLabel(pointer) : null;
    if (upstream) return upstream;
    const mirror = mirrorPointerLabel(pointer);
    if (mirror) return mirror;
    const name = pointer.split('/')[1] ?? pointer;
    switch (name) {
      case 'key':
        return $localize`:@@repo.field.key:Key`;
      case 'description':
        return $localize`:@@repo.field.description:Description`;
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

  protected pointerTarget(pointer: string): string {
    if (this.cls() === 'remote') {
      const target = upstreamTarget(pointer);
      if (target) return target;
    }
    if (this.mirrorSection()) {
      const target = mirrorTarget(pointer);
      if (target) return target;
    }
    return errorTarget(pointer, this.settingNodes(), this.memberPointer());
  }

  protected keyErrors(): readonly FieldProblem[] {
    return problemsAt(this.fieldErrors(), '/key');
  }

  protected message(problem: FieldProblem): string {
    return fieldMessage(problem);
  }

  protected classText = classLabel;
  protected classHelpText = classHelp;

  private scheduleKeyCheck(key: string, project: string, valid: boolean): void {
    clearTimeout(this.keyTimer);
    if (!valid) {
      this.keyCheck.set('idle');
      return;
    }
    this.keyCheck.set('checking');
    this.keyTimer = setTimeout(() => void this.checkKey(key, project), 400);
  }

  /** A `GET` of the key within the project that answers 404 means the key is free (R-UI-021). */
  private async checkKey(key: string, project: string): Promise<void> {
    try {
      await this.api.get(repoRef(project, key));
      if (this.form().key === key && this.chosenProject() === project) this.keyCheck.set('taken');
    } catch (e) {
      if (this.form().key !== key || this.chosenProject() !== project) return;
      this.keyCheck.set(e instanceof ApiError && e.status === 404 ? 'free' : 'unknown');
    }
  }
}
