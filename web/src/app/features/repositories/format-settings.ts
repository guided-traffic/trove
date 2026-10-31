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

function shown(value: unknown): string {
  return typeof value === 'string' ? value : JSON.stringify(value);
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
      <p class="hint" i18n="@@repo.format.intro">
        Only the settings that apply to this class of repository are shown. A setting left at its
        default is not written into the repository.
      </p>
      @for (e of errors(sectionId()); track $index) {
        <p class="field-error">{{ message(e) }}</p>
      }
      <ng-container *ngTemplateOutlet="list; context: { $implicit: nodes() }" />
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
                  class="select-input"
                  [id]="n.id"
                  [attr.aria-invalid]="invalid(n)"
                  [attr.aria-describedby]="describedBy(n)"
                  (change)="choose(n, $any($event.target).value)"
                >
                  @if (choice(n).fallback === undefined) {
                    <option value="" [selected]="selected(n) < 0" i18n="@@repo.format.unset">
                      Not set
                    </option>
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
                <input
                  class="text-input"
                  [id]="n.id"
                  [type]="isUrl(n) ? 'url' : 'text'"
                  [attr.inputmode]="inputMode(n)"
                  autocomplete="off"
                  spellcheck="false"
                  [value]="text(n)"
                  [attr.aria-invalid]="invalid(n)"
                  [attr.aria-describedby]="describedBy(n)"
                  (input)="type(n, $any($event.target).value)"
                />
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
      @switch (fallbackKind(n)) {
        @case ('on') {
          <p class="hint" [id]="n.id + '-v'" i18n="@@repo.format.default-on">Default: on</p>
        }
        @case ('off') {
          <p class="hint" [id]="n.id + '-v'" i18n="@@repo.format.default-off">Default: off</p>
        }
        @case ('value') {
          <p class="hint" [id]="n.id + '-v'" i18n="@@repo.format.default">
            Default: <code translate="no">{{ fallbackText(n) }}</code>
          </p>
        }
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
      border: 1px solid var(--mat-sys-outline-variant);
      border-radius: var(--mat-sys-corner-medium);

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

  protected readonly sectionId = computed(() => controlIdOf(this.pointer()));

  /** The field problems of the member by the id of the control that shows them. */
  private readonly problems = computed(() => {
    const prefix = this.pointer();
    const byId = new Map<string, FieldProblem[]>();
    for (const [pointer, problem] of this.fieldErrors()) {
      if (pointer !== prefix && !pointer.startsWith(`${prefix}/`)) continue;
      const id = targetOf(this.nodes(), pointer, this.sectionId());
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
    const ids = [
      n.description ? `${n.id}-d` : null,
      this.fallbackKind(n) ? `${n.id}-v` : null,
      this.errors(n.id).length > 0 ? `${n.id}-e` : null,
    ].filter((id) => id !== null);
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

  protected inputMode(n: SettingNode): string | null {
    return n.kind === 'number' ? (n.integer ? 'numeric' : 'decimal') : null;
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

  protected text(n: SettingNode): string {
    const v = this.current(n);
    return v === undefined || v === null ? '' : shown(v);
  }

  protected fallbackKind(n: SettingNode): 'on' | 'off' | 'value' | null {
    if (!('fallback' in n) || n.fallback === undefined || n.fallback === null) return null;
    if (n.kind === 'boolean') return n.fallback ? 'on' : 'off';
    return 'value';
  }

  protected fallbackText(n: SettingNode): string {
    return 'fallback' in n ? shown(n.fallback) : '';
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
