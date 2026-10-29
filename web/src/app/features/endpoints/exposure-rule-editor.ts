import {
  Component,
  DestroyRef,
  computed,
  effect,
  inject,
  input,
  resource,
  signal,
  untracked,
} from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { Router, RouterLink } from '@angular/router';
import { newIdempotencyKey } from '../../api/client';
import {
  EndpointsApi,
  ExposureCheck,
  ExposureCheckRequest,
  ExposureRule,
} from '../../api/endpoints-api';
import { ApiError, FieldProblem } from '../../api/problem';
import { RepositoriesApi } from '../../api/repositories-api';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { ConflictResolver } from '../../shared/conflict-dialog';
import { Confirm } from '../../shared/confirm-dialog';
import { CopyButton } from '../../shared/copy-button';
import { DiffReview } from '../../shared/diff-dialog';
import { ErrorNotice } from '../../shared/error-notice';
import { ErrorSummary, SummaryItem, fieldMessage, problemsAt } from '../../shared/field-errors';
import { DraftStore } from '../../shared/drafts';
import { asApiError, valueOf } from '../../shared/paged-resource';
import { HasUnsavedChanges } from '../../shared/unsaved-guard';
import { lines } from '../repositories/repository-model';
import {
  EMPTY_RULE_FORM,
  RULE_NAME_PATTERN,
  RuleForm,
  endpointAddress,
  flatRule,
  ruleFormOf,
  ruleOf,
} from './endpoint-model';

/** The preview waits this long after the last change before it asks the server (R-EP-017). */
export const PREVIEW_DEBOUNCE_MS = 500;

/** The translated reason of a `check-exposure` verdict. */
export function reasonText(reason: string): string {
  switch (reason) {
    case 'exposed':
      return $localize`:@@ep.reason.exposed:exposed`;
    case 'no_rule':
      return $localize`:@@ep.reason.no_rule:no rule admits it`;
    case 'rule_disabled':
      return $localize`:@@ep.reason.rule_disabled:the rule is disabled`;
    case 'package_excluded':
      return $localize`:@@ep.reason.package_excluded:the package is excluded`;
    case 'path_excluded':
      return $localize`:@@ep.reason.path_excluded:the path is excluded or not included`;
    case 'version_excluded':
      return $localize`:@@ep.reason.version_excluded:the version is excluded or not included`;
    case 'out_of_range':
      return $localize`:@@ep.reason.out_of_range:the version is outside the range`;
    case 'prerelease':
      return $localize`:@@ep.reason.prerelease:pre-releases are not exposed`;
    case 'tag_excluded':
      return $localize`:@@ep.reason.tag_excluded:the tag is excluded or not included`;
    default:
      return reason;
  }
}

function warningText(code: string): string {
  switch (code) {
    case 'path_patterns_on_package_format':
      return $localize`:@@ep.warning.path-patterns:Path patterns on a package format can admit a tarball but not its metadata. Prefer package-name patterns.`;
    case 'endpoint_not_in_rules_mode':
      return $localize`:@@ep.warning.not-rules-mode:This endpoint is in mode "all", so this rule has no effect.`;
    default:
      return code;
  }
}

/** The pointer prefixes of the rule document and the control they belong to. */
const FIELD_LABELS: readonly (readonly [prefix: string, label: () => string])[] = [
  ['/name', () => $localize`:@@ep.field.name:Name`],
  ['/repository', () => $localize`:@@ep.field.repository:Repository`],
  ['/description', () => $localize`:@@ep.field.description:Description`],
  ['/packages', () => $localize`:@@ep.field.packages:Package patterns`],
  ['/paths', () => $localize`:@@ep.field.paths:Path patterns`],
  ['/versions/range', () => $localize`:@@ep.field.range:Version range`],
  ['/versions', () => $localize`:@@ep.field.versions:Versions`],
  ['/distTags', () => $localize`:@@ep.field.tags:Tags`],
];

/**
 * The rule editor (R-EP-017), one page like the policy editor: a repository picker of keys, include
 * and exclude lists with syntax help, the version range, the pre-release switch, tag lists, and
 * `enabled`. The preview panel asks `check-exposure` with the unsaved draft 500 ms after the last
 * change. Saving an existing rule shows a diff against the loaded revision and sends `PUT` with
 * `If-Match`; a create sends the `Idempotency-Key` generated when the page opened (R-UI-042).
 */
@Component({
  selector: 'trove-exposure-rule-editor',
  imports: [MatButtonModule, RouterLink, CopyButton, ErrorNotice, ErrorSummary],
  templateUrl: './exposure-rule-editor.html',
  styleUrl: './exposure-rule-editor.scss',
})
export class ExposureRuleEditor implements HasUnsavedChanges {
  protected readonly valueOf = valueOf;
  private readonly api = inject(EndpointsApi);
  private readonly repositories = inject(RepositoriesApi);
  private readonly router = inject(Router);
  private readonly session = inject(SessionStore);
  private readonly notifier = inject(Notifier);
  private readonly confirm = inject(Confirm);
  private readonly conflicts = inject(ConflictResolver);
  private readonly diff = inject(DiffReview);
  private readonly drafts = inject(DraftStore);
  private readonly destroyed = inject(DestroyRef);

  /** Route parameters; `name` is absent on `rules/new`. */
  readonly endpoint = input.required<string>();
  readonly name = input<string | undefined>();

  /** Generated when the page opens and kept until it closes (R-UI-042). */
  private readonly idempotencyKey = newIdempotencyKey();

  protected readonly creating = computed(() => this.name() === undefined);
  protected readonly info = resource({
    params: () => this.endpoint(),
    loader: ({ params }) => this.api.get(params),
  });
  protected readonly formats = resource({ loader: () => this.repositories.formats() });
  protected readonly repos = resource({ loader: () => this.repositories.listAll() });
  protected readonly loaded = resource({
    params: () => ({ endpoint: this.endpoint(), name: this.name() }),
    loader: ({ params }) =>
      params.name === undefined
        ? Promise.resolve(null)
        : this.api.rule(params.endpoint, params.name),
  });

  protected readonly baseline = signal<RuleForm>(EMPTY_RULE_FORM);
  protected readonly form = signal<RuleForm>(EMPTY_RULE_FORM);
  protected readonly stored = signal<ExposureRule | null>(null);
  private etag = '';
  protected readonly saving = signal(false);
  protected readonly failure = signal<ApiError | null>(null);
  protected readonly warnings = signal<readonly string[]>([]);
  protected readonly showErrors = signal(false);
  private saved = false;

  // The tester of the preview panel.
  protected readonly testPaths = signal('');
  protected readonly testPackage = signal('');
  protected readonly testVersions = signal('');
  protected readonly preview = signal<ExposureCheck | null>(null);
  protected readonly previewFailure = signal<ApiError | null>(null);
  protected readonly previewing = signal(false);
  private previewTimer: ReturnType<typeof setTimeout> | undefined;
  private previewRun = 0;

  protected readonly canWrite = computed(() => this.session.has('endpoints:write'));
  protected readonly loadFailure = computed(() => {
    const e = this.info.error() ?? this.loaded.error();
    return e ? asApiError(e) : null;
  });
  protected readonly configuration = computed(() => valueOf(this.info)?.configuration ?? null);
  protected readonly inert = computed(() => valueOf(this.info)?.inert ?? false);
  protected readonly modeAll = computed(() => this.configuration()?.exposure === 'all');
  protected readonly repository = computed(
    () => (valueOf(this.repos) ?? []).find((r) => r.key === this.form().repository) ?? null,
  );
  protected readonly format = computed(
    () => (valueOf(this.formats) ?? []).find((f) => f.name === this.repository()?.format) ?? null,
  );
  /** `name`: package names, versions and tags; `path`: paths only (R-EP-012). */
  protected readonly byName = computed(() => this.format()?.patternSubject === 'name');
  protected readonly address = computed(() => {
    const c = this.configuration();
    const key = this.form().repository;
    return c && key ? endpointAddress(c, this.format() ?? undefined, key) : null;
  });

  protected readonly dirty = computed(
    () => JSON.stringify(this.form()) !== JSON.stringify(this.baseline()),
  );
  protected readonly nameValid = computed(() => RULE_NAME_PATTERN.test(this.form().name));
  protected readonly fieldErrors = computed<ReadonlyMap<string, FieldProblem>>(
    () => this.failure()?.fieldErrors ?? new Map(),
  );
  protected readonly summary = computed<readonly SummaryItem[]>(() =>
    [...this.fieldErrors()].map(([pointer, problem]) => ({
      label:
        FIELD_LABELS.find(
          ([prefix]) => pointer === prefix || pointer.startsWith(`${prefix}/`),
        )?.[1]() ?? pointer,
      message: fieldMessage(problem),
      targetId: `f-${pointer.split('/').slice(1, 3).join('-')}`,
    })),
  );
  protected readonly previewRequest = computed<ExposureCheckRequest | null>(() => {
    const f = this.form();
    if (!f.repository) return null;
    const request: { -readonly [K in keyof ExposureCheckRequest]: ExposureCheckRequest[K] } = {
      repository: f.repository,
      rule: ruleOf({ ...f, name: f.name || 'draft' }),
    };
    if (this.byName()) {
      const pkg = this.testPackage().trim();
      if (pkg) request.package = pkg;
      const versions = lines(this.testVersions());
      if (versions.length > 0) request.versions = versions;
    } else {
      const paths = lines(this.testPaths());
      if (paths.length > 0) request.paths = paths;
    }
    return request;
  });
  protected readonly previewMode = computed(
    () => this.preview()?.mode ?? this.configuration()?.exposure ?? null,
  );

  constructor() {
    this.destroyed.onDestroy(() => clearTimeout(this.previewTimer));
    effect(() => {
      const value = valueOf(this.loaded);
      if (value) untracked(() => this.adopt(value.value, value.etag));
    });
    effect(() => {
      // Create mode: restore a draft kept across a re-authentication.
      if (this.creating()) {
        const draft = untracked(() => this.drafts.take<RuleForm>());
        if (draft) this.form.set(draft);
      }
    });
    effect(() => {
      if (!this.session.reauthRequired()) return;
      const form = untracked(() => this.form());
      this.drafts.keep(form);
    });
    effect(() => {
      const request = this.previewRequest();
      untracked(() => this.schedulePreview(request));
    });
  }

  hasUnsavedChanges(): boolean {
    return !this.saved && this.dirty();
  }

  protected reasonText = reasonText;

  protected verdictText(check: ExposureCheck): string {
    const rule = check.rule ? `, ${check.rule}` : '';
    return `(${reasonText(check.reason)}${rule})`;
  }

  protected warningText = warningText;

  protected set(change: Partial<{ -readonly [K in keyof RuleForm]: RuleForm[K] }>): void {
    this.form.update((f) => ({ ...f, ...change }));
  }

  protected errors(pointer: string): readonly FieldProblem[] {
    return problemsAt(this.fieldErrors(), pointer);
  }

  protected invalid(pointer: string): 'true' | null {
    return this.errors(pointer).length > 0 ? 'true' : null;
  }

  protected message(problem: FieldProblem): string {
    return fieldMessage(problem);
  }

  private adopt(rule: ExposureRule, etag: string): void {
    this.etag = etag;
    this.stored.set(rule);
    const form = ruleFormOf(rule);
    this.baseline.set(form);
    this.form.set(this.drafts.take<RuleForm>() ?? form);
    this.warnings.set((rule.warnings ?? []).map(warningText));
  }

  // --- preview ------------------------------------------------------------------------------

  private schedulePreview(request: ExposureCheckRequest | null): void {
    clearTimeout(this.previewTimer);
    const run = ++this.previewRun;
    if (!request) {
      this.preview.set(null);
      this.previewing.set(false);
      return;
    }
    this.previewing.set(true);
    this.previewTimer = setTimeout(async () => {
      try {
        const result = await this.api.check(this.endpoint(), request);
        if (run !== this.previewRun) return;
        this.preview.set(result);
        this.previewFailure.set(null);
      } catch (e) {
        if (run === this.previewRun) this.previewFailure.set(asApiError(e));
      } finally {
        if (run === this.previewRun) this.previewing.set(false);
      }
    }, PREVIEW_DEBOUNCE_MS);
  }

  // --- save ---------------------------------------------------------------------------------

  protected async save(): Promise<void> {
    this.showErrors.set(true);
    const form = this.form();
    if (this.saving() || (this.creating() && (!this.nameValid() || !form.repository))) return;
    this.failure.set(null);
    const rule = ruleOf(form);
    const stored = this.stored();
    if (!this.creating() && stored) {
      const ok = await this.diff.confirm({
        title: $localize`:@@ep.save.diff.title:Save changes to rule ${form.name}:name:?`,
        mine: flatRule(rule),
        theirs: flatRule(stored),
        confirmLabel: $localize`:@@ep.save.confirm:Save`,
      });
      if (!ok) return;
    }
    this.saving.set(true);
    try {
      if (this.creating()) {
        const created = await this.api.createRule(this.endpoint(), rule, this.idempotencyKey);
        this.saved = true;
        this.notifier.status(
          $localize`:@@ep.save.created:Rule ${created.value.name}:name: created.`,
        );
        await this.router.navigate([
          '/admin/endpoints',
          this.endpoint(),
          'rules',
          created.value.name,
        ]);
      } else {
        await this.replace(rule, this.etag);
      }
    } catch (e) {
      await this.handleSaveError(asApiError(e), rule);
    } finally {
      this.saving.set(false);
    }
  }

  private async replace(rule: ExposureRule, etag: string): Promise<void> {
    const saved = await this.api.replaceRule(this.endpoint(), rule.name, rule, etag);
    this.adopt(saved.value, saved.etag);
    this.notifier.status($localize`:@@ep.save.saved:Rule saved.`);
  }

  private async handleSaveError(error: ApiError, mine: ExposureRule): Promise<void> {
    if (error.status !== 412 || this.creating()) {
      this.failure.set(error);
      return;
    }
    let theirs;
    try {
      theirs = await this.api.rule(this.endpoint(), mine.name);
    } catch (e) {
      this.failure.set(asApiError(e));
      return;
    }
    if (
      error.afterRetry &&
      JSON.stringify(flatRule(theirs.value)) === JSON.stringify(flatRule(mine))
    ) {
      // The first attempt of a retried write did commit (R-UI-042).
      this.adopt(theirs.value, theirs.etag);
      this.notifier.status($localize`:@@ep.save.saved:Rule saved.`);
      return;
    }
    const decision = await this.conflicts.resolve({
      mine: flatRule(mine),
      theirs: flatRule(theirs.value),
    });
    if (decision === 'reload') {
      this.adopt(theirs.value, theirs.etag);
    } else if (decision === 'overwrite') {
      try {
        await this.replace(mine, theirs.etag);
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
    const rule = this.stored();
    if (!rule) return;
    const answer = await this.confirm.ask({
      title: $localize`:@@ep.rule.delete.title:Delete rule ${rule.name}:name:?`,
      lines: [
        $localize`:@@ep.rule.delete.text:Content that only this rule exposes stops being retrievable on this endpoint.`,
      ],
      confirmLabel: $localize`:@@ep.rule.delete.confirm:Delete rule`,
    });
    if (answer === null) return;
    try {
      await this.api.deleteRule(this.endpoint(), rule.name, this.etag);
      this.saved = true;
      this.notifier.status($localize`:@@ep.rule.deleted:Rule ${rule.name}:name: deleted.`);
      await this.router.navigate(['/admin/endpoints', this.endpoint()]);
    } catch (e) {
      this.failure.set(asApiError(e));
    }
  }

  protected readonly copyAddress = $localize`:@@ep.copy-address:Copy client address`;
}
