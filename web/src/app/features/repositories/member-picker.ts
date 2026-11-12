import { Listbox, Option } from '@angular/aria/listbox';
import { Component, computed, input, model, output, signal } from '@angular/core';
import { RepositorySummary } from '../../api/repositories-api';

/** A member's class as the picker names it: a virtual repository is never offered (R-REPO-017). */
export type MemberClass = 'local' | 'remote' | 'unknown';

/**
 * The member picker of a virtual repository (R-UI-022, R-UI-081, Q-UI-210): two listboxes of `@angular/aria`, the repositories
 * that can be added and the members in resolution order, with Add, Remove, Move up, Move down and Move to top (a pointer or the
 * keyboard, no drag needed: WCAG 2.5.7; Alt with the arrow keys or Home does the same on the member list). Every change is
 * announced in a live region ("npm-remote moved to position 2 of 3"). The creation form's group **Members** and the settings
 * page's section **Members** use it; each screen keeps the deployment target and the save.
 *
 * `pool` is every repository that may be a member (the screen filters project, format, class and lifecycle); what is not a
 * member yet is offered. A refusal of the server at `/virtual/members/<i>` is shown on that member (`errorAt`).
 */
@Component({
  selector: 'trove-member-picker',
  imports: [Listbox, Option],
  host: { '(keydown)': 'onMemberKey($event)' },
  template: `
    <div class="lists">
      <div class="list">
        <h3 [id]="ids.available" i18n="@@virtual.available">Available repositories</h3>
        <div
          ngListbox
          class="listbox"
          [multi]="true"
          selectionMode="explicit"
          [attr.aria-labelledby]="ids.available"
          [value]="pickedAvailable()"
          (valueChange)="pickedAvailable.set($event)"
          data-testid="vm-available"
        >
          @for (s of available(); track s.key) {
            <div ngOption class="option" [value]="s.key" [label]="s.key">
              <span class="member-key" translate="no">{{ s.key }}</span>&ngsp;
              <span class="class-tag">{{ classText(s.key) }}</span>
            </div>
          }
        </div>
        @if (available().length === 0) {
          <p class="hint" i18n="@@virtual.available-empty">No other local or remote repository of this project and format is left to add.</p>
        }
      </div>

      <div class="buttons" role="group" [attr.aria-labelledby]="ids.actions">
        <h3 [id]="ids.actions" class="visually-hidden" i18n="@@virtual.actions">Change the members</h3>
        <button class="btn btn-default" type="button" [disabled]="!canWrite() || pickedAvailable().length === 0" (click)="add()" i18n="@@virtual.add">Add</button>
        <button class="btn btn-default" type="button" [disabled]="!canWrite() || selectedIndex() < 0" (click)="remove()" i18n="@@virtual.remove">Remove</button>
        <button class="btn btn-default" type="button" [disabled]="!canMoveUp()" (click)="moveUp()" i18n="@@virtual.move-up">Move up</button>
        <button class="btn btn-default" type="button" [disabled]="!canMoveDown()" (click)="moveDown()" i18n="@@virtual.move-down">Move down</button>
        <button class="btn btn-default" type="button" [disabled]="!canMoveUp()" (click)="moveToTop()" i18n="@@virtual.move-top">Move to top</button>
      </div>

      <div class="list">
        <h3 [id]="ids.members" i18n="@@virtual.selected">Members in resolution order</h3>
        <div
          ngListbox
          class="listbox"
          selectionMode="explicit"
          [attr.aria-labelledby]="ids.members"
          [attr.aria-describedby]="ids.keys"
          [value]="pickedMember()"
          (valueChange)="pickedMember.set($event)"
          data-testid="vm-members"
        >
          @for (key of members(); track key; let i = $index) {
            <div ngOption class="option" [value]="key" [label]="key" [attr.aria-invalid]="errorAt()(i) ? 'true' : null">
              <span class="position">{{ i + 1 }}.</span>&ngsp;
              <span class="member-key" translate="no">{{ key }}</span>&ngsp;
              <span class="class-tag">{{ classText(key) }}</span>
              @if (errorAt()(i); as message) {
                <span class="field-error" role="alert" data-testid="vm-member-error">{{ message }}</span>
              }
            </div>
          }
        </div>
        @if (members().length === 0) {
          <p class="hint">{{ emptyText() }}</p>
        }
        <p [id]="ids.keys" class="hint" i18n="@@virtual.keys-hint">
          Alt with the arrow up or down key moves the selected member, Alt with Home moves it to the top.
        </p>
      </div>
    </div>

    <p class="visually-hidden" role="status" aria-live="polite" data-testid="vm-live">{{ announcement() }}</p>
  `,
  styles: `
    :host {
      display: block;
    }
    h3 {
      margin-block: 0 var(--trove-space-1);
      font: var(--trove-type-title-small);
    }
    .lists {
      display: flex;
      flex-wrap: wrap;
      align-items: flex-start;
      gap: var(--trove-space-4);
    }
    .list {
      flex: 1 1 16rem;
      min-inline-size: 0;
    }
    .listbox {
      min-block-size: 8rem;
      max-block-size: 20rem;
      overflow-y: auto;
      border: 1px solid var(--trove-border);
      border-radius: var(--trove-radius-m);
    }
    .option {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: var(--trove-space-2);
      padding: var(--trove-space-1) var(--trove-space-3);
      cursor: pointer;
    }
    .option[aria-selected='true'] {
      background: var(--trove-btn-tertiary-bg-hover);
      font-weight: 600;
    }
    .option:focus-visible,
    .option[data-active='true'] {
      outline: 2px solid var(--trove-focus-ring, currentColor);
      outline-offset: -2px;
    }
    .member-key {
      overflow-wrap: anywhere;
    }
    .position {
      min-inline-size: 2ch;
      color: var(--trove-text-muted);
    }
    .class-tag {
      color: var(--trove-text-muted);
      font: var(--trove-type-body-small);
    }
    .buttons {
      display: flex;
      flex-direction: column;
      gap: var(--trove-space-2);
      align-self: center;
    }
    .field-error {
      flex-basis: 100%;
      color: var(--trove-status-bad);
      font: var(--trove-type-body-small);
    }
    .hint {
      margin-block: var(--trove-space-1) 0;
    }
    @media (max-width: 40rem) {
      .buttons {
        flex-direction: row;
        flex-wrap: wrap;
        align-self: stretch;
      }
    }
  `,
})
export class MemberPicker {
  /** Every repository that may be a member; those that are not members yet are offered. */
  readonly pool = input.required<readonly RepositorySummary[]>();
  /** The members in resolution order. */
  readonly members = model.required<readonly string[]>();
  readonly canWrite = input(true);
  /** The error text of the member at an index, from a refusal of the server. */
  readonly errorAt = input<(index: number) => string | null>(() => null);
  /** What the member list says while it is empty. */
  readonly emptyText = input($localize`:@@virtual.selected-empty:The repository has no member. Every request for a package answers not found.`);
  /** What the screen adds to the announcement of a removal (the deployment target it clears), asked before the member goes. */
  readonly removalNote = input<(key: string) => string>(() => '');
  /** A member was removed; the screen clears what named it. */
  readonly removed = output<string>();
  /** Members were added; the screen may preset what follows from them (the creation form's deployment target, Q-UI-347). */
  readonly added = output<readonly string[]>();

  private static next = 1;
  protected readonly ids = (() => {
    const n = MemberPicker.next++;
    return {
      available: `mp-${n}-available`,
      actions: `mp-${n}-actions`,
      members: `mp-${n}-members`,
      keys: `mp-${n}-keys`,
    };
  })();

  /** The listbox values: the options that are selected, one list each. */
  protected readonly pickedAvailable = signal<string[]>([]);
  protected readonly pickedMember = signal<string[]>([]);
  /** The text of the live region. */
  protected readonly announcement = signal('');

  private readonly byKey = computed(() => new Map(this.pool().map((s) => [s.key, s] as const)));

  protected readonly available = computed<readonly RepositorySummary[]>(() => {
    const taken = new Set(this.members());
    return this.pool()
      .filter((s) => !taken.has(s.key))
      .sort((a, b) => a.key.localeCompare(b.key));
  });

  protected readonly selectedIndex = computed(() => {
    const picked = this.pickedMember()[0];
    return picked === undefined ? -1 : this.members().indexOf(picked);
  });
  protected readonly canMoveUp = computed(() => this.canWrite() && this.selectedIndex() > 0);
  protected readonly canMoveDown = computed(
    () => this.canWrite() && this.selectedIndex() >= 0 && this.selectedIndex() < this.members().length - 1,
  );

  classOf(key: string): MemberClass {
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

  /** Says something in the live region (a discard of the screen). */
  announce(text: string): void {
    this.pickedMember.set([]);
    this.announcement.set(text);
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
    this.added.emit(keys);
  }

  protected remove(): void {
    const at = this.selectedIndex();
    if (!this.canWrite() || at < 0) return;
    const key = this.members()[at]!;
    const note = this.removalNote()(key);
    this.members.update((m) => m.filter((k) => k !== key));
    this.pickedMember.set([]);
    const text = $localize`:@@virtual.announce.removed:${key}:key: removed, ${this.members().length}:total: members left`;
    this.announcement.set(note ? `${text} ${note}` : text);
    this.removed.emit(key);
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
}
