import { NgTemplateOutlet } from '@angular/common';
import { Component, computed, input, model } from '@angular/core';
import { FieldProblem } from '../../api/problem';
import { fieldMessage } from '../../shared/field-errors';
import { JsonObject } from '../../shared/merge-patch';
import {
  SettingNode,
  controlIdOf,
  numberOrText,
  targetOf,
  valueAt,
  withValue,
} from './settings-schema';

type Of<K extends SettingNode['kind']> = Extract<SettingNode, { kind: K }>;

function entriesOf(text: string): string[] {
  return text
    .split('\n')
    .map((l) => l.trim())
    .filter((l) => l.length > 0);
}

function shown(value: unknown): string {
  return typeof value === 'string' ? value : JSON.stringify(value);
}

/** The part of a format's settings a group shows: its own settings, or the members of its `mirror` object (Q-UI-344). */
export type FormatPart = 'settings' | 'mirror';

function isMirrorNode(n: SettingNode): boolean {
  return n.path.length === 1 && n.path[0] === 'mirror';
}

/** The nodes of `part`: every node but the `mirror` object, or that object's members (empty when the format has none). */
export function formatPartNodes(nodes: readonly SettingNode[], part: FormatPart): readonly SettingNode[] {
  if (part === 'settings') return nodes.filter((n) => !isMirrorNode(n));
  const mirror = nodes.find(isMirrorNode);
  return mirror?.kind === 'group' ? mirror.children : [];
}

/**
 * The settings of a format, rendered from its settings schema (R-ARCH-004, R-UI-021, Q-UI-78): the
 * member named after the format, edited in place. Every member of the schema that applies to the
 * class gets a native control by its type; an object is a fieldset of its members. Labels and
 * descriptions are the schema's (API texts, English, Q-UI-79). An empty field, and a value equal to
 * the default, leave the setting to the format's default when the document is written (Q-UI-80).
 */
@Component({
  selector: 'trove-format-settings',
  imports: [NgTemplateOutlet],
  template: `
    <div class="format-settings" [id]="sectionId()" tabindex="-1">
      @for (e of errors(sectionId()); track $index) {
        <p class="field-error">{{ message(e) }}</p>
      }
      <ng-container *ngTemplateOutlet="list; context: { $implicit: shownNodes() }" />
    </div>

    <ng-template #list let-items>
      <div class="form-grid">
        @for (n of asNodes(items); track n.pointer) {
          @switch (n.kind) {
            @case ('group') {
              <fieldset
                class="settings-group"
                [id]="n.id"
                tabindex="-1"
                [attr.aria-describedby]="describedBy(n)"
              >
                <legend translate="no" lang="en">{{ n.label }}</legend>
                <ng-container *ngTemplateOutlet="notes; context: { $implicit: n }" />
                <ng-container *ngTemplateOutlet="list; context: { $implicit: group(n).children }" />
              </fieldset>
            }
            @case ('boolean') {
              <div class="field">
                <label class="check-row" [for]="n.id">
                  <input
                    type="checkbox"
                    [id]="n.id"
                    [checked]="checked(n)"
                    [attr.aria-invalid]="invalid(n)"
                    [attr.aria-describedby]="describedBy(n)"
                    (change)="set(n, $any($event.target).checked)"
                  />
                  <span translate="no" lang="en">{{ n.label }}</span>
                </label>
                <ng-container *ngTemplateOutlet="notes; context: { $implicit: n }" />
              </div>
            }
            @case ('choice') {
              <div class="field">
                <label class="field-label" [for]="n.id" translate="no" lang="en">{{
                  n.label
                }}</label>
                <select
                  class="select-input field-w-name"
                  [id]="n.id"
                  [attr.aria-invalid]="invalid(n)"
                  [attr.aria-describedby]="describedBy(n)"
                  (change)="choose(n, $any($event.target).value)"
                >
                  @if (choice(n).fallback === undefined) {
                    <option value="" [selected]="selected(n) < 0" i18n="@@repo.format.automatic">Automatic</option>
                  }
                  @for (o of choice(n).options; track $index) {
                    <option [value]="$index" [selected]="$index === selected(n)" translate="no">
                      {{ show(o) }}
                    </option>
                  }
                </select>
                <ng-container *ngTemplateOutlet="notes; context: { $implicit: n }" />
              </div>
            }
            @case ('list') {
              <div class="field">
                @if (listNode(n).options; as options) {
                  <fieldset class="settings-group" [id]="n.id" tabindex="-1" [attr.aria-describedby]="describedBy(n)">
                    <legend translate="no" lang="en">{{ n.label }}</legend>
                    @for (o of options; track o) {
                      <label class="check-row">
                        <input
                          type="checkbox"
                          [id]="n.id + '-' + o"
                          [checked]="entries(n).includes(o)"
                          (change)="toggle(n, o, $any($event.target).checked)"
                        />
                        <span translate="no">{{ o }}</span>
                      </label>
                    }
                    <ng-container *ngTemplateOutlet="notes; context: { $implicit: n }" />
                  </fieldset>
                } @else {
                  <label class="field-label" [for]="n.id" translate="no" lang="en">{{ n.label }}</label>
                  <textarea
                    class="area-input field-w-full"
                    [id]="n.id"
                    rows="3"
                    autocomplete="off"
                    spellcheck="false"
                    [value]="listText(n)"
                    [attr.aria-invalid]="invalid(n)"
                    [attr.aria-describedby]="listDescribedBy(n)"
                    (input)="typeList(n, $any($event.target).value)"
                    (blur)="showDefaultAgain(n, $event)"
                  ></textarea>
                  <p class="hint" [id]="n.id + '-l'" i18n="@@repo.format.list">One entry per line.</p>
                  <ng-container *ngTemplateOutlet="notes; context: { $implicit: n }" />
                }
              </div>
            }
            @case ('other') {
              <div class="field">
                <span class="field-label" [id]="n.id" tabindex="-1" translate="no" lang="en">{{
                  n.label
                }}</span>
                <ng-container *ngTemplateOutlet="notes; context: { $implicit: n }" />
                <p class="hint" i18n="@@repo.format.other">
                  This setting cannot be changed here. Set it through the API.
                </p>
                @if (current(n) !== undefined) {
                  <pre class="code-block" translate="no" tabindex="0">{{ show(current(n)) }}</pre>
                }
              </div>
            }
            @default {
              <div class="field">
                <label class="field-label" [for]="n.id" translate="no" lang="en">{{
                  n.label
                }}</label>
                @if (isMultiline(n)) {
                  <textarea
                    class="area-input field-w-full"
                    [id]="n.id"
                    rows="8"
                    autocomplete="off"
                    spellcheck="false"
                    [value]="text(n)"
                    [attr.aria-invalid]="invalid(n)"
                    [attr.aria-describedby]="describedBy(n)"
                    (input)="type(n, $any($event.target).value)"
                    (blur)="showDefaultAgain(n, $event)"
                  ></textarea>
                } @else {
                  <input
                    class="text-input"
                    [class]="'field-w-' + widthOf(n)"
                    [id]="n.id"
                    [type]="isUrl(n) ? 'url' : 'text'"
                    [attr.inputmode]="inputMode(n)"
                    autocomplete="off"
                    spellcheck="false"
                    [value]="text(n)"
                    [attr.aria-invalid]="invalid(n)"
                    [attr.aria-describedby]="describedBy(n)"
                    (input)="type(n, $any($event.target).value)"
                    (blur)="showDefaultAgain(n, $event)"
                  />
                }
                <ng-container *ngTemplateOutlet="notes; context: { $implicit: n }" />
              </div>
            }
          }
        }
      </div>
    </ng-template>

    <ng-template #notes let-item>
      @let n = asNode(item);
      @if (n.description) {
        <p class="hint" [id]="n.id + '-d'" lang="en">{{ n.description }}</p>
      }
      @for (e of errors(n.id); track $index) {
        <p class="field-error" [id]="n.id + '-e'">{{ message(e) }}</p>
      }
    </ng-template>
  `,
  styles: `
    .settings-group {
      margin: 0;
      padding: var(--trove-space-3) var(--trove-space-4);
      border: 1px solid var(--trove-border);
      border-radius: var(--trove-radius-l);

      legend {
        padding-inline: var(--trove-space-1);
        font-weight: 600;
      }

      .form-grid {
        margin-block: var(--trove-space-3) 0;
      }
    }
  `,
})
export class FormatSettings {
  /** The members of the schema that apply to the repository's class (`settingNodes`). */
  readonly nodes = input.required<readonly SettingNode[]>();
  /** The pointer of the format member in the document (`/npm`). */
  readonly pointer = input.required<string>();
  /** The content of the format member. */
  readonly value = model.required<JsonObject>();
  /** Server field problems by pointer (`ApiError.fieldErrors`). */
  readonly fieldErrors = input<ReadonlyMap<string, FieldProblem>>(new Map());
  /**
   * Which members it shows (R-UI-083, Q-UI-344): `settings` every member but the format's `mirror` object, `mirror` that object's
   * members, so that a mirror is one topic in the Mirror group.
   */
  readonly part = input<FormatPart>('settings');

  protected readonly shownNodes = computed(() => formatPartNodes(this.nodes(), this.part()));
  protected readonly sectionId = computed(() =>
    controlIdOf(this.part() === 'mirror' ? `${this.pointer()}/mirror` : this.pointer()),
  );

  /** The field problems of the member by the id of the control that shows them. */
  private readonly problems = computed(() => {
    const prefix = this.pointer();
    const byId = new Map<string, FieldProblem[]>();
    for (const [pointer, problem] of this.fieldErrors()) {
      if (pointer !== prefix && !pointer.startsWith(`${prefix}/`)) continue;
      const id = targetOf(this.shownNodes(), pointer, this.sectionId());
      byId.set(id, [...(byId.get(id) ?? []), problem]);
    }
    return byId;
  });

  protected errors(id: string): readonly FieldProblem[] {
    return this.problems().get(id) ?? [];
  }

  protected message(problem: FieldProblem): string {
    return fieldMessage(problem);
  }

  protected invalid(n: SettingNode): 'true' | null {
    return this.errors(n.id).length > 0 ? 'true' : null;
  }

  protected describedBy(n: SettingNode): string | null {
    const ids = [n.description ? `${n.id}-d` : null, this.errors(n.id).length > 0 ? `${n.id}-e` : null].filter(
      (id) => id !== null,
    );
    return ids.length > 0 ? ids.join(' ') : null;
  }

  // Template variables of an ng-template are untyped; these restore the type.
  protected asNodes(items: unknown): readonly SettingNode[] {
    return items as readonly SettingNode[];
  }

  protected asNode(item: unknown): SettingNode {
    return item as SettingNode;
  }

  protected group(n: SettingNode): Of<'group'> {
    return n as Of<'group'>;
  }

  protected choice(n: SettingNode): Of<'choice'> {
    return n as Of<'choice'>;
  }

  /** The field width of a line (Q-UI-335): the member's, `name` for a construct without one. */
  protected widthOf(n: SettingNode): string {
    return n.kind === 'text' || n.kind === 'number' ? n.width : 'name';
  }

  protected inputMode(n: SettingNode): string | null {
    return n.kind === 'number' ? (n.integer ? 'numeric' : 'decimal') : null;
  }

  protected listNode(n: SettingNode): Of<'list'> {
    return n as Of<'list'>;
  }

  protected isMultiline(n: SettingNode): boolean {
    return n.kind === 'text' && n.multiline;
  }

  /** What the user typed into a list, so that a blank line being typed is not rewritten under the cursor. */
  private readonly drafts = new Map<string, string>();

  /** The entries in effect: the one set, else the schema default. */
  protected entries(n: SettingNode): readonly string[] {
    const v = this.effective(n);
    return Array.isArray(v) ? v.filter((e): e is string => typeof e === 'string') : [];
  }

  /** The list as text: what is set, else the schema default (R-UI-084), and a blank line being typed stays. */
  protected listText(n: SettingNode): string {
    const current = this.current(n);
    const set = Array.isArray(current) ? current.map(String) : [];
    const draft = this.drafts.get(n.id);
    if (draft !== undefined && JSON.stringify(entriesOf(draft)) === JSON.stringify(set)) return draft;
    return current === undefined ? this.entries(n).join('\n') : set.join('\n');
  }

  protected listDescribedBy(n: SettingNode): string {
    const ids = this.describedBy(n);
    return ids ? `${ids} ${n.id}-l` : `${n.id}-l`;
  }

  protected typeList(n: SettingNode, text: string): void {
    this.drafts.set(n.id, text);
    const entries = entriesOf(text);
    this.set(n, entries.length === 0 ? undefined : entries);
  }

  protected toggle(n: SettingNode, option: string, on: boolean): void {
    const options = this.listNode(n).options ?? [];
    const now = new Set(this.entries(n));
    if (on) now.add(option);
    else now.delete(option);
    const entries = options.filter((o) => now.has(o));
    this.set(n, entries.length === 0 ? undefined : entries);
  }

  protected isUrl(n: SettingNode): boolean {
    return n.kind === 'text' && n.url;
  }

  protected show = shown;

  protected current(n: SettingNode): unknown {
    return valueAt(this.value(), n.path);
  }

  /** The value in effect: the one set, else the schema default. */
  private effective(n: SettingNode): unknown {
    const v = this.current(n);
    return v === undefined && 'fallback' in n ? n.fallback : v;
  }

  protected checked(n: SettingNode): boolean {
    return this.effective(n) === true;
  }

  protected selected(n: SettingNode): number {
    const v = this.effective(n);
    return this.choice(n).options.findIndex((o) => JSON.stringify(o) === JSON.stringify(v));
  }

  /** The value in the field: the one set, else the schema default (R-UI-084); a derived default has none and stays empty. */
  protected text(n: SettingNode): string {
    const v = this.effective(n);
    return v === undefined || v === null ? '' : shown(v);
  }

  /** A field the user emptied shows its default again when it is left (R-UI-084); the setting stays unwritten. */
  protected showDefaultAgain(n: SettingNode, event: Event): void {
    const field = event.target as HTMLInputElement | HTMLTextAreaElement;
    if (this.current(n) !== undefined || field.value.trim() !== '') return;
    this.drafts.delete(n.id);
    field.value = n.kind === 'list' ? this.listText(n) : this.text(n);
  }

  protected set(n: SettingNode, value: unknown): void {
    this.value.update((m) => withValue(m, n.path, value));
  }

  protected choose(n: SettingNode, index: string): void {
    this.set(n, index === '' ? undefined : this.choice(n).options[Number(index)]);
  }

  protected type(n: SettingNode, text: string): void {
    this.set(n, n.kind === 'number' ? numberOrText(text) : text === '' ? undefined : text);
  }
}
