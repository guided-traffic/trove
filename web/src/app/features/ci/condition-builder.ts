import { Component, computed, input, output } from '@angular/core';
import { CondNode, GITLAB_CLAIMS, OPERATORS, Operator, depthOf, editAt, newGroup, newLeaf } from './ci-model';

/** What every node of the tree shares: the tree, whether it can be edited and the problems the server reported. */
export interface BuilderContext {
  readonly root: () => CondNode;
  readonly readonly: () => boolean;
  /** The message of a problem at an RFC 6901 pointer into the request body (`/match/all/1`), or null. */
  readonly errorAt: (pointer: string) => string | null;
  readonly edit: (path: readonly number[], fn: (n: CondNode) => CondNode | null) => void;
}

/** The pointer into the mapping body of the node at `path`. */
export function pointerOf(root: CondNode, path: readonly number[]): string {
  let pointer = '/match';
  let n: CondNode = root;
  for (const i of path) {
    if (n.kind === 'not') {
      pointer += '/not';
      n = n.child;
    } else if (n.kind === 'leaf') {
      break;
    } else {
      pointer += `/${n.kind}/${i}`;
      n = n.children[i] ?? n;
    }
  }
  return pointer;
}

const OPERATOR_LABELS: Record<Operator, string> = {
  equals: $localize`:@@ci.op.equals:equals`,
  in: $localize`:@@ci.op.in:is one of`,
  glob: $localize`:@@ci.op.glob:matches glob`,
  regex: $localize`:@@ci.op.regex:matches regex`,
  present: $localize`:@@ci.op.present:presence`,
};

/**
 * One node of a condition tree (R-CI-010) in the mapping editor: a combinator (all, any), a negation or
 * a leaf with a claim name, an operator and its value. It calls itself for its children. Every control
 * has a visible label, the claim names of GitLab are offered as suggestions and any claim may be typed.
 * The server's problems appear under the node they point at.
 */
@Component({
  selector: 'trove-ci-condition-node',
  imports: [],
  template: `
    @let n = node();
    @let c = ctx();
    @switch (n.kind) {
      @case ('leaf') {
        <div class="leaf" [attr.data-pointer]="pointer()">
          <div class="leaf-row">
            <div class="cell claim">
              <label [for]="idOf('claim')" i18n="@@ci.cond.claim">Claim</label>
              @if (n.path) {
                <input class="text-input" [id]="idOf('claim')" readonly [value]="n.path.join(' › ')" />
              } @else {
                <input
                  class="text-input"
                  [id]="idOf('claim')"
                  [attr.list]="'ci-claims'"
                  autocomplete="off"
                  spellcheck="false"
                  [readOnly]="c.readonly()"
                  [value]="n.claim"
                  [attr.aria-invalid]="problem() ? 'true' : null"
                  [attr.aria-describedby]="problem() ? idOf('error') : null"
                  (input)="update({ claim: $any($event.target).value })"
                />
              }
            </div>
            <div class="cell op">
              <label [for]="idOf('op')" i18n="@@ci.cond.operator">Operator</label>
              <select class="select-input" [id]="idOf('op')" [disabled]="c.readonly()" (change)="update({ operator: $any($event.target).value })">
                @for (o of operators; track o) {
                  <option [value]="o" [selected]="o === n.operator">{{ operatorLabel(o) }}</option>
                }
              </select>
            </div>
            <div class="cell value">
              @if (n.operator === 'present') {
                <label [for]="idOf('value')" i18n="@@ci.cond.presence">The claim</label>
                <select class="select-input" [id]="idOf('value')" [disabled]="c.readonly()" (change)="update({ present: $any($event.target).value === 'true' })">
                  <option value="true" [selected]="n.present" i18n="@@ci.cond.is-present">is present</option>
                  <option value="false" [selected]="!n.present" i18n="@@ci.cond.is-absent">is absent</option>
                </select>
              } @else if (n.operator === 'in') {
                <label [for]="idOf('value')" i18n="@@ci.cond.values">Values (one per line)</label>
                <textarea
                  class="text-input"
                  rows="2"
                  [id]="idOf('value')"
                  spellcheck="false"
                  [readOnly]="c.readonly()"
                  [value]="n.value"
                  [attr.aria-invalid]="problem() ? 'true' : null"
                  [attr.aria-describedby]="problem() ? idOf('error') : null"
                  (input)="update({ value: $any($event.target).value })"
                ></textarea>
              } @else {
                <label [for]="idOf('value')" i18n="@@ci.cond.value">Value</label>
                <input
                  class="text-input"
                  [id]="idOf('value')"
                  autocomplete="off"
                  spellcheck="false"
                  [readOnly]="c.readonly()"
                  [value]="n.value"
                  [attr.aria-invalid]="problem() ? 'true' : null"
                  [attr.aria-describedby]="problem() ? idOf('error') : null"
                  (input)="update({ value: $any($event.target).value })"
                />
              }
            </div>
            @if (n.operator !== 'present') {
              <label class="fold">
                <input type="checkbox" [disabled]="c.readonly()" [checked]="n.ignoreCase" (change)="update({ ignoreCase: $any($event.target).checked })" />
                <span i18n="@@ci.cond.ignore-case">Ignore case</span>
              </label>
            }
            @if (!c.readonly() && path().length > 0) {
              <button type="button" class="btn btn-tertiary remove" (click)="remove()" [attr.aria-label]="removeLabel">
                <span class="material-icons" aria-hidden="true">delete</span>
                <ng-container i18n="@@ci.cond.remove">Remove</ng-container>
              </button>
            }
          </div>
          @if (problem(); as text) {
            <p class="field-error" [id]="idOf('error')">{{ text }}</p>
          }
        </div>
      }
      @case ('not') {
        <div class="group not" role="group" [attr.aria-label]="notLabel">
          <div class="group-head">
            <strong i18n="@@ci.cond.not">Not: this must be false</strong>
            @if (!c.readonly() && path().length > 0) {
              <button class="btn btn-tertiary" type="button" (click)="remove()" i18n="@@ci.cond.remove-group">Remove group</button>
            }
          </div>
          @if (problem(); as text) {
            <p class="field-error">{{ text }}</p>
          }
          <trove-ci-condition-node [node]="n.child" [path]="[...path(), 0]" [ctx]="c" />
        </div>
      }
      @default {
        <div class="group" role="group" [attr.aria-label]="groupLabel(n.kind)" [attr.data-pointer]="pointer()">
          <div class="group-head">
            <label [for]="idOf('kind')" i18n="@@ci.cond.group">Match</label>
            <select class="select-input" [id]="idOf('kind')" [disabled]="c.readonly()" (change)="update({ kind: $any($event.target).value })">
              <option value="all" [selected]="n.kind === 'all'" i18n="@@ci.cond.all">all of these</option>
              <option value="any" [selected]="n.kind === 'any'" i18n="@@ci.cond.any">any of these</option>
            </select>
            @if (!c.readonly()) {
              <button class="btn btn-default" type="button" (click)="addLeaf()" i18n="@@ci.cond.add-leaf">Add condition</button>
              <button class="btn btn-default" type="button" [disabled]="!canNest()" (click)="addGroup('all')" i18n="@@ci.cond.add-group">Add group</button>
              <button class="btn btn-default" type="button" [disabled]="!canNest()" (click)="addNot()" i18n="@@ci.cond.add-not">Add negation</button>
              @if (path().length > 0) {
                <button class="btn btn-tertiary" type="button" (click)="remove()" i18n="@@ci.cond.remove-group">Remove group</button>
              }
            }
          </div>
          @if (problem(); as text) {
            <p class="field-error" [id]="idOf('error')">{{ text }}</p>
          }
          @for (child of children(); track child.id; let i = $index) {
            <trove-ci-condition-node [node]="child" [path]="[...path(), i]" [ctx]="c" />
          }
          @if (children().length === 0) {
            <p class="muted" i18n="@@ci.cond.empty-group">This group is empty. A condition needs at least one entry in every group.</p>
          }
        </div>
      }
    }
  `,
  styles: `
    :host {
      display: block;
    }
    .group {
      margin-block: var(--trove-space-2);
      padding: var(--trove-space-2) var(--trove-space-3);
      border: 1px solid var(--trove-border);
      border-radius: var(--trove-radius-m);
      background: var(--trove-surface-raised);
    }
    .group.not {
      border-style: dashed;
    }
    .group-head {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: var(--trove-space-2);
      margin-block-end: var(--trove-space-2);
    }
    .leaf {
      margin-block: var(--trove-space-2);
      padding: var(--trove-space-2);
      border: 1px solid var(--trove-border);
      border-radius: var(--trove-radius-s);
    }
    .leaf-row {
      display: flex;
      flex-wrap: wrap;
      gap: var(--trove-space-3);
      align-items: flex-end;
    }
    .cell {
      display: flex;
      flex-direction: column;
      gap: var(--trove-space-1);
    }
    .cell.claim {
      flex: 1 1 12rem;
    }
    .cell.value {
      flex: 2 1 14rem;
    }
    label {
      font: var(--trove-type-label-medium);
    }
    .text-input,
    .select-input {
      box-sizing: border-box;
      inline-size: 100%;
      padding: var(--trove-space-2);
      border: 1px solid var(--trove-border-strong);
      border-radius: var(--trove-radius-s);
      background: var(--trove-surface-raised);
      color: var(--trove-on-surface);
      font: inherit;
    }
    .fold {
      display: flex;
      align-items: center;
      gap: var(--trove-space-1);
    }
    .field-error {
      margin: var(--trove-space-1) 0 0;
      color: var(--trove-status-bad);
      font: var(--trove-type-body-small);
    }
    .muted {
      color: var(--trove-text-muted);
    }
    .material-icons {
      font-size: 1.125rem;
      vertical-align: middle;
    }
  `,
})
export class ConditionNode {
  readonly node = input.required<CondNode>();
  readonly path = input.required<readonly number[]>();
  readonly ctx = input.required<BuilderContext>();

  protected readonly operators = OPERATORS;
  protected readonly removeLabel = $localize`:@@ci.cond.remove-label:Remove this condition`;
  protected readonly notLabel = $localize`:@@ci.cond.not-label:Negation`;

  protected readonly pointer = computed(() => pointerOf(this.ctx().root(), this.path()));
  protected readonly problem = computed(() => this.ctx().errorAt(this.pointer()));
  protected readonly children = computed(() => {
    const n = this.node();
    return n.kind === 'all' || n.kind === 'any' ? n.children : [];
  });
  /** The server refuses more than four levels (R-CI-010); the buttons stop at the same depth. */
  protected readonly canNest = computed(() => depthOf(this.ctx().root()) < 4);

  protected idOf(part: string): string {
    return `ci-cond-${this.node().id}-${part}`;
  }
  protected operatorLabel(o: Operator): string {
    return OPERATOR_LABELS[o];
  }
  protected groupLabel(kind: string): string {
    return kind === 'any' ? $localize`:@@ci.cond.any-label:Any of these` : $localize`:@@ci.cond.all-label:All of these`;
  }

  protected update(change: Record<string, unknown>): void {
    this.ctx().edit(this.path(), (n) => ({ ...n, ...change }) as CondNode);
  }
  protected remove(): void {
    this.ctx().edit(this.path(), () => null);
  }
  protected addLeaf(): void {
    this.ctx().edit(this.path(), (n) => (n.kind === 'all' || n.kind === 'any' ? { ...n, children: [...n.children, newLeaf()] } : n));
  }
  protected addGroup(kind: 'all' | 'any'): void {
    this.ctx().edit(this.path(), (n) =>
      n.kind === 'all' || n.kind === 'any' ? { ...n, children: [...n.children, newGroup(kind, [newLeaf()])] } : n,
    );
  }
  protected addNot(): void {
    this.ctx().edit(this.path(), (n) =>
      n.kind === 'all' || n.kind === 'any' ? { ...n, children: [...n.children, { kind: 'not', id: newLeaf().id, child: newLeaf() }] } : n,
    );
  }
}

/**
 * The condition of a mapping as a tree of groups and conditions (R-UI-035, R-CI-010). It owns the list of
 * claim names the leaves suggest and applies the edits of its nodes to the tree it is given.
 */
@Component({
  selector: 'trove-ci-condition-builder',
  imports: [ConditionNode],
  template: `
    <datalist id="ci-claims">
      @for (c of claims; track c) {
        <option [value]="c"></option>
      }
    </datalist>
    <trove-ci-condition-node [node]="value()" [path]="[]" [ctx]="context" />
  `,
})
export class ConditionBuilder {
  readonly value = input.required<CondNode>();
  readonly readonly = input(false);
  readonly errorAt = input<(pointer: string) => string | null>(() => null);
  readonly valueChange = output<CondNode>();

  protected readonly claims = GITLAB_CLAIMS;

  protected readonly context: BuilderContext = {
    root: () => this.value(),
    readonly: () => this.readonly(),
    errorAt: (p) => this.errorAt()(p),
    edit: (path, fn) => {
      const next = editAt(this.value(), path, fn);
      if (next !== null) this.valueChange.emit(next);
    },
  };
}
