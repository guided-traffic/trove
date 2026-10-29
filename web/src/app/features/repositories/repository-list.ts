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
import { Router, RouterLink } from '@angular/router';
import { newIdempotencyKey } from '../../api/client';
import { JobReference, RepositoriesApi, RepositorySummary } from '../../api/repositories-api';
import { ApiError } from '../../api/problem';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { Confirm } from '../../shared/confirm-dialog';
import { CopyButton } from '../../shared/copy-button';
import { ErrorNotice } from '../../shared/error-notice';
import { JobProgress } from '../../shared/job-progress';
import { PagedResource, asApiError, valueOf } from '../../shared/paged-resource';
import { RecentRepositories } from './recent-repositories';
import { classLabel } from './repository-wizard';

/**
 * The repository list (R-UI-020): key, format, class, description, client address with a copy
 * button, last activity. Filters live in the query parameters (R-UI-003) and restart the cursor
 * walk; "Load more" continues it. Delete asks for the key, may offer to detach the repository from
 * the virtual repositories that include it, and follows the deletion job until the row is gone.
 */
@Component({
  selector: 'trove-repository-list',
  imports: [DatePipe, MatButtonModule, RouterLink, CopyButton, ErrorNotice, JobProgress],
  templateUrl: './repository-list.html',
  styleUrl: './repository-list.scss',
})
export class RepositoryList {
  private readonly api = inject(RepositoriesApi);
  private readonly router = inject(Router);
  private readonly session = inject(SessionStore);
  private readonly notifier = inject(Notifier);
  private readonly confirm = inject(Confirm);
  private readonly dom = inject(DOCUMENT);
  protected readonly recent = inject(RecentRepositories);

  /** Query parameters, bound by the router's component input binding. */
  readonly q = input<string | undefined>();
  readonly class = input<string | undefined>();
  readonly format = input<string | undefined>();
  readonly state = input<string | undefined>();

  protected readonly formats = resource({ loader: () => this.api.formats() });
  protected readonly list = new PagedResource<RepositorySummary>();
  /** Repositories whose deletion job is being followed, by key. */
  protected readonly deleting = signal<ReadonlyMap<string, JobReference>>(new Map());
  protected readonly deleteFailure = signal<ApiError | null>(null);
  protected readonly search = signal('');

  protected readonly canCreate = computed(() => this.session.has('repositories:write-config'));
  protected readonly canDelete = this.canCreate;
  protected readonly formatOptions = computed(() =>
    (valueOf(this.formats) ?? []).map((f) => f.name),
  );
  protected readonly filtered = computed(
    () => !!(this.q() || this.class() || this.format() || this.state()),
  );

  constructor() {
    effect(() => {
      const filters = {
        q: this.q(),
        class: this.class(),
        format: this.format(),
        state: this.state(),
      };
      untracked(() => {
        this.search.set(filters.q ?? '');
        void this.list.start(this.api.list(filters));
      });
    });
  }

  protected classText = classLabel;
  protected readonly copyLabel = $localize`:@@repo.copy-address:Copy client address`;
  protected readonly deleteLabel = $localize`:@@repo.delete.progress:Repository deletion`;

  protected address(row: RepositorySummary): string {
    const path = row.addresses[0];
    return path ? this.dom.location.origin + path : '';
  }

  protected applyFilter(change: Readonly<Record<string, string | null>>): void {
    void this.router.navigate([], { queryParams: change, queryParamsHandling: 'merge' });
  }

  protected submitSearch(event: Event): void {
    event.preventDefault();
    this.applyFilter({ q: this.search().trim() || null });
  }

  protected clearFilters(): void {
    this.applyFilter({ q: null, class: null, format: null, state: null });
  }

  protected async remove(row: RepositorySummary): Promise<void> {
    this.deleteFailure.set(null);
    const idempotencyKey = newIdempotencyKey();
    let offerDetach = false;
    for (;;) {
      const answer = await this.confirm.ask({
        title: $localize`:@@repo.delete.title:Delete repository ${row.key}:key:?`,
        lines: [
          $localize`:@@repo.delete.text:The repository and everything in it are deleted for good. Deletion runs as a background job; the key stays taken until it has finished.`,
        ],
        confirmLabel: $localize`:@@repo.delete.confirm:Delete repository`,
        typeToConfirm: row.key,
        choice: offerDetach
          ? $localize`:@@repo.delete.detach:Also remove it from the virtual repositories that include it`
          : undefined,
      });
      if (answer === null) return;
      try {
        const outcome = await this.api.delete(row.key, idempotencyKey, answer.checked);
        if (outcome.done) this.gone(row.key);
        else this.deleting.update((m) => new Map(m).set(row.key, outcome.job));
        return;
      } catch (e) {
        const error = asApiError(e);
        if (error.code === 'repository_referenced' && !offerDetach) {
          // The choice of R-UI-020: ask again, this time with the detach option.
          offerDetach = true;
          continue;
        }
        this.deleteFailure.set(error);
        return;
      }
    }
  }

  protected gone(key: string): void {
    this.deleting.update((m) => {
      const next = new Map(m);
      next.delete(key);
      return next;
    });
    this.list.remove((r) => r.key === key);
    this.notifier.status($localize`:@@repo.delete.done:Repository ${key}:key: deleted.`);
  }

  protected deletionFailed(key: string): void {
    this.deleting.update((m) => {
      const next = new Map(m);
      next.delete(key);
      return next;
    });
    this.notifier.alert(
      $localize`:@@repo.delete.failed:Deleting repository ${key}:key: failed. Its row is kept; try again.`,
    );
    void this.list.start(
      this.api.list({
        q: this.q(),
        class: this.class(),
        format: this.format(),
        state: this.state(),
      }),
    );
  }

  protected jobEnded(key: string, state: string): void {
    if (state === 'succeeded') this.gone(key);
    else this.deletionFailed(key);
  }

  protected visit(key: string): void {
    this.recent.add(key);
  }
}
