import { DOCUMENT, DatePipe } from '@angular/common';
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
import { MatButtonModule } from '@angular/material/button';
import { MatTabsModule } from '@angular/material/tabs';
import { Router, RouterLink } from '@angular/router';
import { newIdempotencyKey } from '../../api/client';
import { ApiError, FieldProblem } from '../../api/problem';
import {
  JobReference,
  RepositoriesApi,
  Repository,
  RepositoryConsumerView,
  Versioned,
  isConsumerView,
} from '../../api/repositories-api';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { Confirm } from '../../shared/confirm-dialog';
import { ConflictResolver } from '../../shared/conflict-dialog';
import { CopyButton } from '../../shared/copy-button';
import { ErrorNotice } from '../../shared/error-notice';
import { ErrorSummary, SummaryItem, fieldMessage } from '../../shared/field-errors';
import { DraftStore } from '../../shared/drafts';
import { JobProgress } from '../../shared/job-progress';
import { JsonObject, mergePatchOf } from '../../shared/merge-patch';
import { asApiError, valueOf } from '../../shared/paged-resource';
import { HasUnsavedChanges } from '../../shared/unsaved-guard';
import { RecentRepositories } from './recent-repositories';
import { RepositoryFields } from './repository-fields';
import { RepositoryForm, clientAddress, curlOf, formOf, settingsOf } from './repository-model';
import { classLabel } from './repository-wizard';

/**
 * The edit form of a repository (R-UI-021): key, class and format are read-only; the settings are
 * sections in tabs; save sends a JSON merge patch with the loaded `ETag`. A 412 opens the conflict
 * dialog; a retried write that answers 412 because its first attempt committed is recognised by
 * comparing the stored settings with the draft (R-UI-042). Leaving with unsaved changes asks first.
 */
@Component({
  selector: 'trove-repository-detail',
  imports: [
    DatePipe,
    MatButtonModule,
    MatTabsModule,
    RouterLink,
    CopyButton,
    ErrorNotice,
    ErrorSummary,
    JobProgress,
    RepositoryFields,
  ],
  templateUrl: './repository-detail.html',
  styleUrl: './repository-detail.scss',
})
export class RepositoryDetail implements HasUnsavedChanges {
  private readonly api = inject(RepositoriesApi);
  private readonly router = inject(Router);
  private readonly session = inject(SessionStore);
  private readonly notifier = inject(Notifier);
  private readonly confirm = inject(Confirm);
  private readonly conflicts = inject(ConflictResolver);
  private readonly drafts = inject(DraftStore);
  private readonly recent = inject(RecentRepositories);
  private readonly dom = inject(DOCUMENT);

  /** The route parameter `:key`, bound by the router. */
  readonly key = input.required<string>();

  protected readonly formats = resource({ loader: () => this.api.formats() });
  protected readonly loaded = resource({
    params: () => this.key(),
    loader: ({ params }) => this.api.get(params),
  });

  /** The settings as stored, in form shape; the baseline of the dirty check and of the patch. */
  protected readonly baseline = signal<RepositoryForm | null>(null);
  protected readonly form = signal<RepositoryForm | null>(null);
  private etag = '';
  protected readonly stored = signal<Repository | null>(null);
  protected readonly consumer = signal<RepositoryConsumerView | null>(null);
  protected readonly saving = signal(false);
  protected readonly failure = signal<ApiError | null>(null);
  protected readonly warnings = signal<readonly string[]>([]);
  protected readonly deletion = signal<JobReference | null>(null);

  protected readonly canWrite = computed(() => this.session.has('repositories:write-config'));
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
      targetId: `f-${pointer.split('/')[1] ?? ''}`,
    })),
  );
  protected readonly patch = computed<JsonObject>(() => {
    const base = this.baseline();
    const now = this.form();
    return base && now ? mergePatchOf(settingsOf(base), settingsOf(now)) : {};
  });
  protected readonly dirty = computed(() => Object.keys(this.patch()).length > 0);
  protected readonly format = computed(
    () => (valueOf(this.formats) ?? []).find((f) => f.name === this.stored()?.format) ?? null,
  );
  protected readonly address = computed(() =>
    clientAddress(this.dom.location.origin, this.format() ?? undefined, this.key()),
  );
  protected readonly documentJson = computed(() => JSON.stringify(this.stored(), null, 2));
  protected readonly patchJson = computed(() => JSON.stringify(this.patch(), null, 2));
  protected readonly curl = computed(() =>
    curlOf(
      'PATCH',
      `${this.dom.location.origin}/api/v1/repositories/${encodeURIComponent(this.key())}`,
      this.patch(),
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
      if (form) this.drafts.keep(form);
    });
  }

  hasUnsavedChanges(): boolean {
    return this.dirty();
  }

  protected classText = classLabel;

  protected setForm(form: RepositoryForm): void {
    this.form.set(form);
  }

  /** Takes a freshly read representation as the new baseline; a saved draft is restored once. */
  private adopt(value: Versioned<Repository | RepositoryConsumerView>): void {
    this.recent.add(this.key());
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
    const saved = await this.api.patch(this.key(), patch, etag);
    this.etag = saved.etag;
    this.stored.set(saved.value);
    const stored = formOf(saved.value);
    this.baseline.set(stored);
    this.form.set(stored);
    this.warnings.set((saved.value.warnings ?? []).map((w) => w.detail));
    this.notifier.status($localize`:@@repo.edit.saved:Repository saved.`);
  }

  private async handleSaveError(error: ApiError, mine: RepositoryForm): Promise<void> {
    if (error.status !== 412) {
      this.failure.set(error);
      return;
    }
    // The stored version changed (R-UI-042): read it, then decide.
    let theirs: Versioned<Repository | RepositoryConsumerView>;
    try {
      theirs = await this.api.get(this.key());
    } catch (e) {
      this.failure.set(asApiError(e));
      return;
    }
    if (isConsumerView(theirs.value)) {
      this.failure.set(error);
      return;
    }
    const theirForm = formOf(theirs.value);
    if (
      error.afterRetry &&
      JSON.stringify(settingsOf(theirForm)) === JSON.stringify(settingsOf(mine))
    ) {
      // The first attempt of a retried write did commit: that is a success, not a conflict.
      this.adopt(theirs);
      this.notifier.status($localize`:@@repo.edit.saved:Repository saved.`);
      return;
    }
    const decision = await this.conflicts.resolve({
      mine: settingsOf(mine),
      theirs: settingsOf(theirForm),
    });
    if (decision === 'reload') {
      this.adopt(theirs);
    } else if (decision === 'overwrite') {
      try {
        await this.send(mergePatchOf(settingsOf(theirForm), settingsOf(mine)), theirs.etag);
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
      title: $localize`:@@repo.delete.title:Delete repository ${this.key()}:key:?`,
      lines: [
        $localize`:@@repo.delete.text:The repository and everything in it are deleted for good. Deletion runs as a background job; the key stays taken until it has finished.`,
      ],
      confirmLabel: $localize`:@@repo.delete.confirm:Delete repository`,
      typeToConfirm: this.key(),
    });
    if (answer === null) return;
    try {
      const outcome = await this.api.delete(this.key(), newIdempotencyKey());
      if (outcome.done) await this.deleted();
      else this.deletion.set(outcome.job);
    } catch (e) {
      this.failure.set(asApiError(e));
    }
  }

  protected async jobEnded(state: string): Promise<void> {
    if (state === 'succeeded') await this.deleted();
    else
      this.notifier.alert(
        $localize`:@@repo.delete.failed:Deleting repository ${this.key()}:key: failed. Its row is kept; try again.`,
      );
  }

  private async deleted(): Promise<void> {
    this.notifier.status($localize`:@@repo.delete.done:Repository ${this.key()}:key: deleted.`);
    this.baseline.set(this.form());
    await this.router.navigate(['/admin/repositories']);
  }

  protected readonly deleteLabel = $localize`:@@repo.delete.progress:Repository deletion`;
  protected readonly copyAddress = $localize`:@@repo.copy-address:Copy client address`;
  protected readonly tabGeneral = $localize`:@@repo.tab.general:General`;
  protected readonly tabAccess = $localize`:@@repo.tab.access:Access`;
  protected readonly tabApi = $localize`:@@repo.tab.api:API`;
}
