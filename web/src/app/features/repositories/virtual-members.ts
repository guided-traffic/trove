import { Listbox, Option } from '@angular/aria/listbox';
import { Component, computed, inject, input, linkedSignal, output, resource, signal } from '@angular/core';
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
 * The member editor of a virtual repository (R-UI-022, Q-UI-210): two listboxes of `@angular/aria`, the repositories that can be
 * added and the members in resolution order, with the buttons Add, Remove, Move up, Move down and Move to top (a pointer or the
 * keyboard, no drag needed: WCAG 2.5.7; Alt with the arrow keys or Home does the same on the member list). Every change is
 * announced in a live region ("npm-remote moved to position 2 of 3"). The default deployment target is chosen among the members that
 * are local. A server refusal (422 with a pointer such as `/virtual/members/1`) is shown on the member it names. The effective
 * resolution order is the saved list, and a sample path is explained member by member by `GET …/resolution`.
 *
 * Only the repositories of the virtual repository's own project and format that are no virtual repository and not the repository itself
 * are offered (R-REPO-017, Q-REPO-212); virtual members arrive with R-REPO-018.
 */
@Component({
  selector: 'trove-virtual-members',
  imports: [ErrorNotice, Listbox, Option, SelectField, TextField],
  host: { '(keydown)': 'onMemberKey($event)' },
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

  private readonly storedMembers = computed<readonly string[]>(() => this.repo().virtual?.members ?? []);
  private readonly storedTarget = computed<string | null>(() => this.repo().virtual?.deployTarget ?? null);

  protected readonly members = linkedSignal<readonly string[]>(() => this.storedMembers());
  protected readonly deployTarget = linkedSignal<string | null>(() => this.storedTarget());

  /** The listbox values: the options that are selected, one list each. */
  protected readonly pickedAvailable = signal<string[]>([]);
  protected readonly pickedMember = signal<string[]>([]);
  /** The text of the live region. */
  protected readonly announcement = signal('');

  protected readonly candidates = resource({ loader: () => this.api.listAll() });

  private readonly byKey = computed(() => {
    const map = new Map<string, RepositorySummary>();
    for (const s of this.candidates.value() ?? []) {
      if (s.project === (this.repo().project ?? '')) map.set(s.key, s);
    }
    return map;
  });

  /** The repositories that may be added: same project and format, local or remote, not this one, not a member yet, not being deleted. */
  protected readonly available = computed<readonly RepositorySummary[]>(() => {
    const r = this.repo();
    const taken = new Set(this.members());
    return [...this.byKey().values()]
      .filter(
        (s) => s.format === r.format && s.class !== 'virtual' && s.key !== r.key && !taken.has(s.key) && s.lifecycle !== 'deleting',
      )
      .sort((a, b) => a.key.localeCompare(b.key));
  });

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

  protected readonly selectedIndex = computed(() => {
    const picked = this.pickedMember()[0];
    return picked === undefined ? -1 : this.members().indexOf(picked);
  });
  protected readonly canMoveUp = computed(() => this.canWrite() && this.selectedIndex() > 0);
  protected readonly canMoveDown = computed(() => this.canWrite() && this.selectedIndex() >= 0 && this.selectedIndex() < this.members().length - 1);

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
    const c = this.byKey().get(key)?.class;
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
  protected memberError(index: number): string | null {
    return fieldErrorAt(this.failure(), `/virtual/members/${index}`);
  }

  protected targetError(): string | null {
    return fieldErrorAt(this.failure(), '/virtual/deployTarget');
  }

  protected add(): void {
    const keys = this.available()
      .map((s) => s.key)
      .filter((k) => this.pickedAvailable().includes(k));
    if (!this.canWrite() || keys.length === 0) return;
    this.members.update((m) => [...m, ...keys]);
    this.pickedAvailable.set([]);
    const last = keys[keys.length - 1]!;
    this.pickedMember.set([last]);
    const total = this.members().length;
    this.announcement.set(
      keys.length === 1
        ? $localize`:@@virtual.announce.added:${last}:key: added at position ${total}:position: of ${total}:total:`
        : $localize`:@@virtual.announce.added-many:${keys.length}:count: repositories added, ${total}:total: members in all`,
    );
  }

  protected remove(): void {
    const at = this.selectedIndex();
    if (!this.canWrite() || at < 0) return;
    const key = this.members()[at]!;
    this.members.update((m) => m.filter((k) => k !== key));
    this.pickedMember.set([]);
    let text = $localize`:@@virtual.announce.removed:${key}:key: removed, ${this.members().length}:total: members left`;
    if (this.deployTarget() === key) {
      this.deployTarget.set(null);
      text += ' ' + $localize`:@@virtual.announce.target-cleared:The default deployment target is now none.`;
    }
    this.announcement.set(text);
  }

  protected moveUp(): void {
    this.moveTo(this.selectedIndex() - 1);
  }

  protected moveDown(): void {
    this.moveTo(this.selectedIndex() + 1);
  }

  protected moveToTop(): void {
    this.moveTo(0);
  }

  private moveTo(target: number): void {
    const from = this.selectedIndex();
    if (!this.canWrite() || from < 0 || target < 0 || target >= this.members().length || target === from) return;
    const next = [...this.members()];
    const [key] = next.splice(from, 1);
    next.splice(target, 0, key!);
    this.members.set(next);
    const position = target + 1;
    const total = next.length;
    this.announcement.set($localize`:@@virtual.announce.moved:${key!}:key: moved to position ${position}:position: of ${total}:total:`);
  }

  /** Alt with ArrowUp, ArrowDown or Home on the member list does what the buttons do. */
  protected onMemberKey(event: KeyboardEvent): void {
    if (!event.altKey || !(event.target as Element | null)?.closest('[data-testid="vm-members"]')) return;
    const actions: Record<string, () => void> = {
      ArrowUp: () => this.moveUp(),
      ArrowDown: () => this.moveDown(),
      Home: () => this.moveToTop(),
    };
    const action = actions[event.key];
    if (!action) return;
    event.preventDefault();
    action();
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
    this.pickedMember.set([]);
    this.announcement.set($localize`:@@virtual.announce.discarded:Changes discarded.`);
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
