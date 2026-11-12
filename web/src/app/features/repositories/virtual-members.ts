import { Component, computed, inject, input, linkedSignal, output, resource, signal, viewChild } from '@angular/core';
import { ApiError } from '../../api/problem';
import {
  Repository,
  RepositoriesApi,
  RepositoryResolution,
  RepositorySummary,
} from '../../api/repositories-api';
import { SelectField, SelectOption, TextField } from '../../shared/access/fields';
import { ErrorNotice } from '../../shared/error-notice';
import { fieldErrorAt } from '../../shared/field-errors';
import { asApiError } from '../../shared/paged-resource';
import { MemberPicker } from './member-picker';

/** What the editor asks its screen to store: the ordered member keys and the default deployment target (`null`: none). */
export interface MemberChange {
  readonly members: readonly string[];
  readonly deployTarget: string | null;
}

/** The words of a decision of the resolution explanation (R-REPO-019); an unknown decision is shown as the server wrote it. */
export function decisionText(decision: string): string {
  switch (decision) {
    case 'hit':
      return $localize`:@@virtual.decision.hit:Holds it and would serve it`;
    case 'miss':
      return $localize`:@@virtual.decision.miss:Does not hold it`;
    case 'negative':
      return $localize`:@@virtual.decision.negative:Remembered as not having it`;
    case 'disabled':
      return $localize`:@@virtual.decision.disabled:Disabled, so not asked`;
    case 'not_admitted':
      return $localize`:@@virtual.decision.not-admitted:Its include and exclude patterns do not admit the path`;
    case 'pinned-out':
      return $localize`:@@virtual.decision.pinned-out:Not asked: a pin rule names other members for this package`;
    case 'claimed-elsewhere':
      return $localize`:@@virtual.decision.claimed-elsewhere:Not asked: another repository claims this name`;
    case 'refused-claimed':
      return $localize`:@@virtual.decision.refused-claimed:Refused: a local repository claims this name`;
    case 'no-read':
      return $localize`:@@virtual.decision.no-read:You may not read it`;
    case 'no-cache-fill':
      return $localize`:@@virtual.decision.no-cache-fill:Not asked: you may not fill its cache`;
    case 'offline':
      return $localize`:@@virtual.decision.offline:Offline, so not asked`;
    case 'circuit-open':
      return $localize`:@@virtual.decision.circuit-open:Not asked: its upstream circuit is open`;
    default:
      return decision;
  }
}

/**
 * The member editor of a virtual repository (R-UI-022, Q-UI-210): the member picker (`trove-member-picker`, the two listboxes and
 * their buttons, shared with the creation form), the default deployment target among the members that are local, and a save of its
 * own. A server refusal (422 with a pointer such as `/virtual/members/1`) is shown on the member it names. The effective resolution
 * order is the saved list, and a sample path is explained member by member by `GET …/resolution`.
 *
 * Only the repositories of the virtual repository's own project and format that are no virtual repository and not the repository itself
 * are offered (R-REPO-017, Q-REPO-212); virtual members arrive with R-REPO-018.
 */
@Component({
  selector: 'trove-virtual-members',
  imports: [ErrorNotice, MemberPicker, SelectField, TextField],
  templateUrl: './virtual-members.html',
  styleUrl: './virtual-members.scss',
})
export class VirtualMembers {
  private readonly api = inject(RepositoriesApi);

  /** The stored repository; a new one (after a save or a reload) is the new baseline. */
  readonly repo = input.required<Repository>();
  /** The caller may change the configuration. */
  readonly canWrite = input(false);
  readonly saving = input(false);
  /** The failure of the last save. */
  readonly failure = input<ApiError | null>(null);
  readonly save = output<MemberChange>();
  /** The stored document changed meanwhile (412): the screen reads it again. */
  readonly reload = output<void>();

  private readonly picker = viewChild(MemberPicker);

  private readonly storedMembers = computed<readonly string[]>(() => this.repo().virtual?.members ?? []);
  private readonly storedTarget = computed<string | null>(() => this.repo().virtual?.deployTarget ?? null);

  protected readonly members = linkedSignal<readonly string[]>(() => this.storedMembers());
  protected readonly deployTarget = linkedSignal<string | null>(() => this.storedTarget());

  protected readonly candidates = resource({ loader: () => this.api.listAll() });

  /** The repositories that may be members: same project and format, local or remote, not this one, not being deleted. */
  protected readonly pool = computed<readonly RepositorySummary[]>(() => {
    const r = this.repo();
    return (this.candidates.value() ?? []).filter(
      (s) =>
        s.project === (r.project ?? '') &&
        s.format === r.format &&
        s.class !== 'virtual' &&
        s.key !== r.key &&
        s.lifecycle !== 'deleting',
    );
  });

  private readonly classes = computed(() => new Map(this.pool().map((s) => [s.key, s.class] as const)));

  protected readonly dirty = computed(
    () =>
      this.members().length !== this.storedMembers().length ||
      this.members().some((k, i) => k !== this.storedMembers()[i]) ||
      this.deployTarget() !== this.storedTarget(),
  );

  protected readonly targetOptions = computed<readonly SelectOption[]>(() => [
    { value: '', label: $localize`:@@virtual.target.none:None: the repository is read-only` },
    ...this.members()
      .filter((k) => this.classOf(k) === 'local')
      .map((k) => ({ value: k, label: k })),
  ]);

  /** A refusal of the server that does not belong to a member or the target. */
  protected readonly generalFailure = computed(() => {
    const f = this.failure();
    if (!f) return null;
    if (f.status === 412) return f;
    return f.fieldErrors.size === 0 ? f : null;
  });
  protected readonly stale = computed(() => this.failure()?.status === 412);

  // The effective resolution order is the saved list; the explanation of a path is asked for on demand.
  protected readonly effective = computed(() =>
    this.storedMembers().map((key, i) => ({ key, position: i + 1, cls: this.classOf(key), target: key === this.storedTarget() })),
  );
  protected readonly samplePath = signal('');
  protected readonly explaining = signal(false);
  protected readonly explanation = signal<RepositoryResolution | null>(null);
  protected readonly explainError = signal<ApiError | null>(null);

  protected classOf(key: string): 'local' | 'remote' | 'unknown' {
    const c = this.classes().get(key);
    return c === 'local' || c === 'remote' ? c : 'unknown';
  }

  protected classText(key: string): string {
    const c = this.classOf(key);
    return c === 'local'
      ? $localize`:@@virtual.class.local:Local`
      : c === 'remote'
        ? $localize`:@@virtual.class.remote:Remote`
        : '';
  }

  /** The error text of the member at `index`: a cycle or any other refusal the server pointed at it. */
  protected readonly memberErrorAt = (index: number): string | null =>
    fieldErrorAt(this.failure(), `/virtual/members/${index}`);

  /** What a removal adds to its announcement: the deployment target it clears. */
  protected readonly removalNote = (key: string): string =>
    this.deployTarget() === key
      ? $localize`:@@virtual.announce.target-cleared:The default deployment target is now none.`
      : '';

  protected onRemoved(key: string): void {
    if (this.deployTarget() === key) this.deployTarget.set(null);
  }

  protected targetError(): string | null {
    return fieldErrorAt(this.failure(), '/virtual/deployTarget');
  }

  protected setTarget(value: string): void {
    this.deployTarget.set(value === '' ? null : value);
  }

  protected submit(event: Event): void {
    event.preventDefault();
    if (!this.canWrite() || !this.dirty() || this.saving()) return;
    this.save.emit({ members: [...this.members()], deployTarget: this.deployTarget() });
  }

  protected discard(): void {
    this.members.set(this.storedMembers());
    this.deployTarget.set(this.storedTarget());
    this.picker()?.announce($localize`:@@virtual.announce.discarded:Changes discarded.`);
  }

  protected async explain(event: Event): Promise<void> {
    event.preventDefault();
    const path = this.samplePath().trim();
    if (path === '' || this.explaining()) return;
    this.explaining.set(true);
    this.explainError.set(null);
    try {
      const r = this.repo();
      this.explanation.set(await this.api.resolution(`${r.project ?? ''}/${r.key}`, path));
    } catch (e) {
      this.explanation.set(null);
      this.explainError.set(asApiError(e));
    } finally {
      this.explaining.set(false);
    }
  }

  protected decision(d: string): string {
    return decisionText(d);
  }
}
