import { DestroyRef, Component, computed, effect, inject, input, signal, untracked } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatCheckboxModule } from '@angular/material/checkbox';
import { MatDialog } from '@angular/material/dialog';
import { Router, RouterLink } from '@angular/router';
import { firstValueFrom } from 'rxjs';
import { newIdempotencyKey } from '../../api/client';
import { LoadedPolicy, PermissionPolicyInput, PolicyApi, PolicyProblem, PolicyValidation } from '../../api/authz-api';
import { ApiError } from '../../api/problem';
import { ExplainDraft, Explanation, PolicyExplainer } from '../../api/policy-explainer';
import { describeAccessProblem } from '../../core/http/problem-messages-access';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { Confirmer } from '../../shared/access/confirm-dialog';
import { DraftStore } from '../../shared/access/drafts';
import { SelectField, SelectOption, TextField } from '../../shared/access/fields';
import { Needs } from '../../shared/access/needs';
import { toApiError } from '../../shared/access/paged-signal';
import { ConflictResolver } from '../../shared/conflict-dialog';
import { ErrorNotice } from '../../shared/error-notice';
import { SetVerdict, verdict } from './glob';
import { PolicyDiffDialog } from './policy-diff-dialog';
import {
  EMPTY_FORM,
  EntryRow,
  POLICY_ACTIONS,
  PRESETS,
  PolicyForm,
  actionHelp,
  diffPolicy,
  expandActions,
  formFromPolicy,
  inputFromForm,
  lines,
  newRow,
  warningText,
} from './policy-model';

/** Debounce of the live validation (R-UI-027). */
export const PREVIEW_DEBOUNCE_MS = 500;

/**
 * Create and edit a permission policy on one page (R-UI-027, Q-UI-4): general, principals,
 * resources with patterns and a tester, actions. The preview fed by `POST /authz/validate-policy`
 * (debounced 500 ms) shows the covered repositories, errors next to the fields and warnings
 * before anything is saved; "Try it" asks why a principal may or may not do an action on a
 * repository through `PolicyExplainer`, which reads the draft today and the explain operation
 * once it exists. Saving an existing policy shows the diff against the loaded revision, then
 * sends `PUT` with `If-Match`. The `Trove-Authz-Epoch` of the write is kept by the HTTP layer and
 * sent as `Trove-Authz-Min-Epoch` on the reads that follow (R-AUTHZ-032, R-UI-042).
 */
@Component({
  selector: 'trove-policy-editor',
  imports: [MatButtonModule, MatCheckboxModule, RouterLink, ErrorNotice, Needs, SelectField, TextField],
  templateUrl: './policy-editor.html',
  styleUrls: ['../../shared/access/access.scss', './policy-editor.scss'],
})
export class PolicyEditor {
  /** Route parameter `:name`; absent on `/new`. */
  readonly name = input<string | undefined>();

  private readonly api = inject(PolicyApi);
  private readonly explainer = inject(PolicyExplainer);
  private readonly dialog = inject(MatDialog);
  private readonly confirmer = inject(Confirmer);
  private readonly conflicts = inject(ConflictResolver);
  private readonly notifier = inject(Notifier);
  private readonly drafts = inject(DraftStore);
  private readonly router = inject(Router);
  protected readonly session = inject(SessionStore);

  private idempotencyKey = newIdempotencyKey();
  protected readonly actions = POLICY_ACTIONS;
  protected readonly actionHelp = actionHelp;

  protected readonly loaded = signal<LoadedPolicy | null>(null);
  protected readonly loadError = signal<ApiError | null>(null);
  protected readonly saveError = signal<ApiError | null>(null);
  protected readonly busy = signal(false);
  protected readonly attempted = signal(false);

  protected readonly policyName = signal('');
  protected readonly description = signal('');
  protected readonly repositories = signal('');
  protected readonly packagesInclude = signal('');
  protected readonly packagesExclude = signal('');
  protected readonly pathsInclude = signal('');
  protected readonly pathsExclude = signal('');
  protected readonly entries = signal<readonly EntryRow[]>([]);

  protected readonly isNew = computed(() => this.name() === undefined);
  protected readonly canWrite = computed(() => this.session.has('policies:write'));
  protected readonly canValidate = computed(() => this.session.has('policies:read'));
  protected readonly form = computed<PolicyForm>(() => ({
    name: this.policyName(),
    description: this.description(),
    repositories: this.repositories(),
    packagesInclude: this.packagesInclude(),
    packagesExclude: this.packagesExclude(),
    pathsInclude: this.pathsInclude(),
    pathsExclude: this.pathsExclude(),
    entries: this.entries(),
  }));
  protected readonly body = computed(() => inputFromForm(this.form(), this.isNew()));
  /** The stored policy as an input, to tell whether anything changed. */
  private readonly baseline = computed(() => {
    const l = this.loaded();
    return l ? inputFromForm(formFromPolicy(l.policy), false) : inputFromForm(EMPTY_FORM, false);
  });
  protected readonly dirty = computed(
    () => JSON.stringify(inputFromForm(this.form(), false)) !== JSON.stringify(this.baseline()),
  );

  // Preview.
  protected readonly validation = signal<PolicyValidation | null>(null);
  protected readonly validating = signal(false);
  protected readonly previewOpen = signal(false);
  private validationGeneration = 0;
  protected readonly serverProblems = computed<readonly PolicyProblem[]>(() => {
    const live = this.validation()?.errors ?? [];
    const fromSave = [...(this.saveError()?.fieldErrors.values() ?? [])].map((f) => ({
      pointer: f.pointer,
      code: f.code,
      detail: f.detail ?? '',
    }));
    const seen = new Set(live.map((p) => `${p.pointer}|${p.code}`));
    return [...live, ...fromSave.filter((p) => !seen.has(`${p.pointer}|${p.code}`))];
  });

  // Pattern tester.
  protected readonly testDimension = signal('packages');
  protected readonly testValue = signal('');
  protected readonly dimensionOptions: readonly SelectOption[] = [
    { value: 'packages', label: $localize`:@@policy.test.packages:Package name` },
    { value: 'paths', label: $localize`:@@policy.test.paths:Path` },
  ];
  protected readonly testVerdict = computed<SetVerdict | null>(() => {
    const value = this.testValue().trim();
    if (!value) return null;
    const dimension = this.testDimension() === 'paths' ? 'paths' : 'packages';
    const [inc, exc] =
      dimension === 'paths'
        ? [lines(this.pathsInclude()), lines(this.pathsExclude())]
        : [lines(this.packagesInclude()), lines(this.packagesExclude())];
    return verdict(dimension, inc, exc, value);
  });

  // "Try it".
  protected readonly askPrincipal = signal('');
  protected readonly askRepository = signal('');
  protected readonly askAction = signal('read');
  protected readonly explanation = signal<Explanation | null>(null);
  protected readonly explainError = signal<ApiError | null>(null);
  protected readonly mine = signal<boolean | null>(null);
  protected readonly actionOptions: readonly SelectOption[] = POLICY_ACTIONS.map((a) => ({ value: a, label: a }));
  protected readonly presetNames = Object.keys(PRESETS);
  protected readonly presetGroupLabel = $localize`:@@policy.preset:Preset`;

  constructor() {
    this.drafts.track(() => (this.dirty() ? this.form() : null), inject(DestroyRef));
    effect(() => {
      const name = this.name();
      untracked(() => void this.init(name));
    });
    // Live validation, debounced.
    effect((onCleanup) => {
      const body = this.body();
      if (!this.canValidate()) return;
      const timer = setTimeout(() => void this.validate(body), PREVIEW_DEBOUNCE_MS);
      onCleanup(() => clearTimeout(timer));
    });
  }

  private async init(name: string | undefined): Promise<void> {
    this.loadError.set(null);
    this.loaded.set(null);
    this.idempotencyKey = newIdempotencyKey();
    if (name === undefined) {
      this.setForm(EMPTY_FORM);
    } else {
      try {
        const l = await this.api.load(name);
        this.loaded.set(l);
        this.setForm(formFromPolicy(l.policy));
      } catch (e) {
        this.loadError.set(toApiError(e));
        return;
      }
    }
    // A draft kept across re-authentication (R-UI-042).
    const draft = this.drafts.take<PolicyForm>();
    if (draft) {
      this.setForm({ ...draft, entries: draft.entries.map((e) => newRow(e.principal, e.actions)) });
      this.notifier.status($localize`:@@policy.draft-restored:Your entries were restored. Save the policy again.`);
    }
  }

  private setForm(f: PolicyForm): void {
    this.policyName.set(f.name);
    this.description.set(f.description);
    this.repositories.set(f.repositories);
    this.packagesInclude.set(f.packagesInclude);
    this.packagesExclude.set(f.packagesExclude);
    this.pathsInclude.set(f.pathsInclude);
    this.pathsExclude.set(f.pathsExclude);
    this.entries.set(f.entries);
  }

  // Entries.

  protected addEntry(): void {
    this.entries.update((list) => [...list, newRow()]);
  }

  protected removeEntry(id: number): void {
    this.entries.update((list) => list.filter((e) => e.id !== id));
  }

  protected setPrincipal(id: number, principal: string): void {
    this.entries.update((list) => list.map((e) => (e.id === id ? { ...e, principal } : e)));
  }

  protected toggleAction(id: number, action: string, on: boolean): void {
    this.entries.update((list) =>
      list.map((e) => {
        if (e.id !== id) return e;
        const set = new Set(e.actions);
        if (on) set.add(action);
        else set.delete(action);
        return { ...e, actions: POLICY_ACTIONS.filter((a) => set.has(a)) };
      }),
    );
  }

  protected presetTitle(preset: string): string {
    return (PRESETS[preset] ?? []).join(', ');
  }

  protected reload(): void {
    void this.init(this.name());
  }

  protected applyPreset(id: number, preset: string): void {
    const actions = PRESETS[preset];
    if (!actions) return;
    this.entries.update((list) => list.map((e) => (e.id === id ? { ...e, actions } : e)));
  }

  // Validation and its problems.

  protected async validate(body: PermissionPolicyInput = this.body()): Promise<void> {
    const generation = ++this.validationGeneration;
    this.validating.set(true);
    try {
      const result = await this.api.validate(body);
      if (generation === this.validationGeneration) this.validation.set(result);
    } catch {
      if (generation === this.validationGeneration) this.validation.set(null);
    } finally {
      if (generation === this.validationGeneration) this.validating.set(false);
    }
  }

  /** Problems whose JSON pointer starts with `prefix`, as sentences (R-UI-044: text next to the field). */
  protected problems(prefix: string): readonly string[] {
    return this.serverProblems()
      .filter((p) => p.pointer === prefix || p.pointer.startsWith(prefix + '/'))
      .map((p) => describeAccessProblem(p.code) ?? p.detail ?? p.code);
  }

  protected problemText(p: PolicyProblem): string {
    return describeAccessProblem(p.code) ?? p.detail ?? p.code;
  }

  protected warningText = warningText;

  protected readonly covers = computed(() => this.validation()?.covers ?? []);

  // Pattern tester text.

  protected verdictText(v: SetVerdict): string {
    switch (v.kind) {
      case 'unrestricted':
        return $localize`:@@policy.test.unrestricted:Matches: this list has no include pattern, so it is not restricted.`;
      case 'included':
        return $localize`:@@policy.test.included:Matches: included by ${v.pattern}:pattern:.`;
      case 'excluded':
        return $localize`:@@policy.test.excluded:Does not match: excluded by ${v.pattern}:pattern:.`;
      case 'not-included':
        return $localize`:@@policy.test.not-included:Does not match: no include pattern matches.`;
      default:
        return $localize`:@@policy.test.invalid:The pattern ${v.pattern}:pattern: is not valid.`;
    }
  }

  protected verdictOk(v: SetVerdict): boolean {
    return v.kind === 'unrestricted' || v.kind === 'included';
  }

  // Try it.

  protected async explain(event: Event): Promise<void> {
    event.preventDefault();
    const v = this.validation();
    const draft: ExplainDraft = {
      input: this.body(),
      covers: v?.covers ?? [],
      invalid: v === null || !v.valid,
    };
    const question = {
      principal: this.askPrincipal().trim(),
      repository: this.askRepository().trim(),
      action: this.askAction(),
    };
    this.explainError.set(null);
    this.mine.set(null);
    try {
      this.explanation.set(await this.explainer.explain(question, draft));
      const [allowed] = await this.session.check([
        { repository: question.repository, action: question.action },
      ]);
      this.mine.set(allowed ?? false);
    } catch (e) {
      this.explanation.set(null);
      this.explainError.set(toApiError(e));
    }
  }

  // Saving.

  protected async save(event: Event): Promise<void> {
    event.preventDefault();
    this.attempted.set(true);
    this.saveError.set(null);
    this.busy.set(true);
    try {
      await this.validate();
      if ((this.validation()?.errors.length ?? 0) > 0) {
        this.previewOpen.set(true);
        return;
      }
      const l = this.loaded();
      if (l === null) {
        await this.create();
      } else {
        const diff = diffPolicy(this.baseline(), inputFromForm(this.form(), false));
        const ref = this.dialog.open(PolicyDiffDialog, {
          data: { name: l.policy.name, lines: diff },
          maxWidth: '40rem',
        });
        if ((await firstValueFrom(ref.afterClosed())) === true) await this.replace(l);
      }
    } catch (e) {
      await this.failed(toApiError(e));
    } finally {
      this.busy.set(false);
    }
  }

  private async create(): Promise<void> {
    const saved = await this.api.create(this.body(), this.idempotencyKey);
    this.afterWrite(saved, $localize`:@@policy.created:Policy created.`);
    await this.router.navigate(['/admin/permissions', saved.policy.name]);
  }

  private async replace(l: LoadedPolicy): Promise<void> {
    const saved = await this.api.replace(l.policy.name, inputFromForm(this.form(), false), l.etag);
    this.afterWrite(saved, $localize`:@@policy.saved:Policy saved.`);
  }

  private afterWrite(saved: LoadedPolicy, done: string): void {
    this.loaded.set(saved);
    this.setForm(formFromPolicy(saved.policy));
    this.notifier.status(done);
    // The grants changed; the decisions the tab cached and its own capabilities may differ.
    void this.session.refreshCapabilities().catch(() => undefined);
  }

  private async failed(error: ApiError): Promise<void> {
    if (error.code === 'reauthentication_required') {
      this.drafts.keep(this.form());
      this.saveError.set(error);
    } else if (error.status === 412 && error.code === 'authz_epoch_unknown') {
      await this.reconcile();
    } else if (error.status === 412) {
      await this.conflict();
    } else {
      this.saveError.set(error);
    }
  }

  /**
   * 412 `authz_epoch_unknown` (R-UI-042): the epoch the tab holds is not known to the replica, so
   * the HTTP layer dropped it. Read the policy again and say whether the change is stored.
   */
  private async reconcile(): Promise<void> {
    const name = this.loaded()?.policy.name ?? this.policyName().trim();
    if (!name) return;
    try {
      const current = await this.api.load(name);
      const mineBody = JSON.stringify(inputFromForm(this.form(), false));
      const stored = JSON.stringify(inputFromForm(formFromPolicy(current.policy), false));
      this.loaded.set(current);
      if (mineBody === stored) {
        this.notifier.status($localize`:@@policy.epoch-stored:The change is stored.`);
      } else {
        this.setForm(formFromPolicy(current.policy));
        this.notifier.alert(
          $localize`:@@policy.epoch-maybe:Your change may not have been saved. The policy is shown as it is stored now.`,
        );
      }
    } catch (e) {
      this.saveError.set(toApiError(e));
    }
  }

  /** 412 `precondition_failed`: their version against mine; "Overwrite" sends mine with the new ETag. */
  private async conflict(): Promise<void> {
    const l = this.loaded();
    if (!l) return;
    const current = await this.api.load(l.policy.name);
    const mine = inputFromForm(this.form(), false);
    const theirs = inputFromForm(formFromPolicy(current.policy), false);
    if (JSON.stringify(mine) === JSON.stringify(theirs)) {
      // A retried write that did commit: report success instead of a conflict.
      this.afterWrite(current, $localize`:@@policy.saved:Policy saved.`);
      return;
    }
    const decision = await this.conflicts.resolve({ mine: { ...mine }, theirs: { ...theirs } });
    if (decision === 'overwrite') {
      this.afterWrite(await this.api.replace(l.policy.name, mine, current.etag), $localize`:@@policy.saved:Policy saved.`);
    } else {
      this.loaded.set(current);
      this.setForm(formFromPolicy(current.policy));
    }
  }

  protected async remove(): Promise<void> {
    const l = this.loaded();
    if (!l) return;
    const answer = await this.confirmer.ask({
      title: $localize`:@@policy.delete-title:Delete this policy?`,
      text: $localize`:@@policy.delete-text:${l.policy.name}:policy: stops granting access at once on every replica.`,
      confirm: $localize`:@@policy.delete-confirm:Delete`,
    });
    if (!answer.confirmed) return;
    try {
      await this.api.delete(l.policy.name, l.etag);
      this.notifier.status($localize`:@@policy.deleted:Policy deleted.`);
      void this.session.refreshCapabilities().catch(() => undefined);
      await this.router.navigate(['/admin/permissions']);
    } catch (e) {
      this.saveError.set(toApiError(e));
    }
  }

  protected jump(id: string): void {
    const el = document.getElementById(id);
    el?.scrollIntoView?.({ block: 'start' });
    el?.focus();
  }

  protected expand = expandActions;
}
