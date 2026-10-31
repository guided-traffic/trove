import {
  Component,
  DestroyRef,
  computed,
  effect,
  inject,
  resource,
  signal,
  untracked,
} from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { Router, RouterLink } from '@angular/router';
import { newIdempotencyKey } from '../../api/client';
import { ApiError, FieldProblem } from '../../api/problem';
import {
  FormatDescriptor,
  RepositoriesApi,
  Repository,
  RepositoryClass,
} from '../../api/repositories-api';
import { Notifier } from '../../core/notifier';
import { ServerAddress } from '../../core/server-address';
import { SessionStore } from '../../core/session-store';
import { ErrorNotice } from '../../shared/error-notice';
import { ErrorSummary, SummaryItem, fieldMessage, problemsAt } from '../../shared/field-errors';
import { DraftStore } from '../../shared/drafts';
import { asApiError, valueOf } from '../../shared/paged-resource';
import { HasUnsavedChanges } from '../../shared/unsaved-guard';
import { FormatSettings } from './format-settings';
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
  settingsOf,
} from './repository-model';
import { SettingSchema, errorTarget, settingEntries, settingNodes } from './settings-schema';

type KeyCheck = 'idle' | 'checking' | 'free' | 'taken' | 'unknown';

/** The pointer prefixes that belong to each step, to bring the user to the field a server error names. */
const STEP_OF_POINTER: readonly (readonly [prefix: string, step: number])[] = [
  ['/class', 0],
  ['/format', 1],
  ['/key', 2],
  ['/description', 2],
  ['/adminNotes', 2],
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
 * Repository creation wizard (R-UI-021): class, format, basic settings, class-specific settings,
 * review. The classes and the formats come only from `GET /api/v1/formats`; the UI holds no list of
 * its own. The `Idempotency-Key` is generated when the wizard opens, so a double click or a retry
 * after a lost answer creates one repository (R-UI-042). Server field errors appear at their
 * fields, in a summary, and the wizard returns to the step that holds the field.
 */
@Component({
  selector: 'trove-repository-wizard',
  imports: [
    MatButtonModule,
    RouterLink,
    ErrorNotice,
    ErrorSummary,
    FormatSettings,
    RepositoryFields,
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
  private keyTimer: ReturnType<typeof setTimeout> | undefined;

  protected readonly steps = computed(() => [
    $localize`:@@repo.wizard.step.class:Class`,
    $localize`:@@repo.wizard.step.format:Format`,
    $localize`:@@repo.wizard.step.basic:Basic settings`,
    this.settingNodes().length > 0
      ? $localize`:@@repo.wizard.step.access-format:Access and ${this.formatLabel()}:format: settings`
      : $localize`:@@repo.wizard.step.access:Access settings`,
    $localize`:@@repo.wizard.step.review:Review`,
  ]);

  protected readonly classes = computed<readonly RepositoryClass[]>(() => {
    const all = new Set<RepositoryClass>();
    for (const f of valueOf(this.formats) ?? []) for (const c of f.classes) all.add(c);
    return (['local', 'remote', 'virtual'] as const).filter((c) => all.has(c));
  });
  protected readonly formatsOfClass = computed<readonly FormatDescriptor[]>(() => {
    const cls = this.cls();
    return cls ? (valueOf(this.formats) ?? []).filter((f) => f.classes.includes(cls)) : [];
  });
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
      ? { name, schema: this.format()?.settingsSchema as SettingSchema | null | undefined }
      : null;
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
    return cls && member ? documentOf(cls, member, this.form()) : null;
  });
  protected readonly requestJson = computed(() => JSON.stringify(this.body(), null, 2));
  protected readonly curl = computed(() => {
    const doc = this.body();
    return doc
      ? curlOf('POST', apiAddress(valueOf(this.baseUrl), '/api/v1/repositories'), doc, [
          'Idempotency-Key: <a fresh UUID>',
        ])
      : '';
  });
  protected readonly address = computed(() =>
    clientAddress(valueOf(this.baseUrl), this.format() ?? undefined, this.form().key),
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
      this.form.set(draft.form);
      this.step.set(draft.step);
    }
    // A form that asks for a recent sign-in keeps its non-secret draft across the redirect.
    effect(() => {
      if (!this.session.reauthRequired()) return;
      untracked(() =>
        this.drafts.keep({
          cls: this.cls(),
          format: this.formatName(),
          form: this.form(),
          step: this.step(),
        }),
      );
    });
    // Live check of the key (R-UI-021): the shape at once, availability after a pause.
    effect(() => {
      const key = this.form().key;
      const valid = this.keyValid();
      untracked(() => this.scheduleKeyCheck(key, valid));
    });
  }

  hasUnsavedChanges(): boolean {
    return !this.created && (this.cls() !== null || this.form() !== EMPTY_FORM);
  }

  protected chooseClass(cls: RepositoryClass): void {
    if (this.cls() !== cls) {
      this.cls.set(cls);
      this.formatName.set(null);
      this.resetFormatMember();
    }
  }

  protected chooseFormat(name: string): void {
    if (this.formatName() !== name) {
      this.formatName.set(name);
      this.resetFormatMember();
    }
  }

  protected setFormatMember(formatMember: Record<string, unknown>): void {
    this.form.update((f) => ({ ...f, formatMember }));
  }

  /** The format settings belong to one class and one format: another choice starts them afresh. */
  private resetFormatMember(): void {
    if (Object.keys(this.form().formatMember).length > 0) this.setFormatMember({});
  }

  protected canNext(): boolean {
    switch (this.step()) {
      case 0:
        return this.cls() !== null;
      case 1:
        return this.formatName() !== null;
      case 2:
        return this.keyValid() && this.keyCheck() !== 'taken';
      default:
        return true;
    }
  }

  protected next(): void {
    if (this.step() === 2) this.showErrors.set(true);
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
      const created = await this.api.create(doc, this.idempotencyKey);
      this.created = true;
      this.notifier.status(
        $localize`:@@repo.wizard.created:Repository ${created.value.key}:key: created.`,
      );
      await this.router.navigate(['/admin/repositories', created.value.key]);
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
      )?.[1] ?? 3
    );
  }

  protected pointerLabel(pointer: string): string {
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

  private scheduleKeyCheck(key: string, valid: boolean): void {
    clearTimeout(this.keyTimer);
    if (!valid) {
      this.keyCheck.set('idle');
      return;
    }
    this.keyCheck.set('checking');
    this.keyTimer = setTimeout(() => void this.checkKey(key), 400);
  }

  /** A `GET` of the key that answers 404 means the key is free (R-UI-021). */
  private async checkKey(key: string): Promise<void> {
    try {
      await this.api.get(key);
      if (this.form().key === key) this.keyCheck.set('taken');
    } catch (e) {
      if (this.form().key !== key) return;
      this.keyCheck.set(e instanceof ApiError && e.status === 404 ? 'free' : 'unknown');
    }
  }
}
