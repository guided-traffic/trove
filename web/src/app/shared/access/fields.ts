import { Component, DestroyRef, ElementRef, Signal, afterNextRender, computed, inject, input, model, signal } from '@angular/core';

let nextFieldId = 1;

/**
 * When a field may show its error (the owner's rule of 2026-11-12, CLAUDE.md, and the style guide's Forms): once the user has
 * touched and left it, or once a submit of its form was attempted (the form's `submit`, or a click on its submit or confirm
 * button, so a form saved by a button's click is covered too). An error that is there before, from a live check or a preview of
 * an untouched field, waits. A field outside any form shows its error at once: there is no submit to wait for. Every
 * `<trove-text-field>` and `<trove-select-field>` uses it, so every form follows the rule without its own state.
 */
function revealRule(): { readonly touched: () => void; readonly reveal: Signal<boolean> } {
  const host = inject(ElementRef<HTMLElement>).nativeElement as HTMLElement;
  const touched = signal(false);
  const attempted = signal(false);
  const outside = signal(false);
  let release = (): void => undefined;
  afterNextRender(() => {
    const form = host.closest('form');
    if (!form) {
      outside.set(true);
      return;
    }
    const onSubmit = (): void => attempted.set(true);
    const onClick = (e: Event): void => {
      const target = e.target as Element | null;
      if (target?.closest('button[type="submit"], button.btn-confirm, input[type="submit"]')) attempted.set(true);
    };
    form.addEventListener('submit', onSubmit, true);
    form.addEventListener('click', onClick, true);
    release = () => {
      form.removeEventListener('submit', onSubmit, true);
      form.removeEventListener('click', onClick, true);
    };
  });
  inject(DestroyRef).onDestroy(() => release());
  return { touched: () => touched.set(true), reveal: computed(() => touched() || attempted() || outside()) };
}

/**
 * A labelled text input with its error text below (R-UI-044: field errors as text next to the
 * field, 3.3.1). The error is tied to the input with `aria-describedby` and `aria-invalid`; the
 * form's summary is the screen's `<trove-error-notice>`. `type="password"` fields are for secrets
 * only and switch autocomplete and spellcheck off (R-UI-043).
 */
@Component({
  selector: 'trove-text-field',
  template: `
    <div class="field">
      <label class="field-label" [for]="id">{{ label() }}</label>
      @if (type() === 'textarea') {
        <textarea
          class="area-input"
          rows="3"
          [id]="id"
          [name]="name()"
          [value]="value()"
          [readonly]="readonly()"
          [required]="required()"
          [attr.aria-invalid]="shownError() ? 'true' : null"
          [attr.aria-describedby]="describedBy()"
          (input)="value.set($any($event.target).value)"
          (blur)="rule.touched()"
        ></textarea>
      } @else {
        <input
          class="text-input"
          [id]="id"
          [type]="type()"
          [name]="name()"
          [value]="value()"
          [readonly]="readonly()"
          [required]="required()"
          [attr.autocomplete]="autocomplete()"
          [attr.spellcheck]="secret() || type() === 'password' ? 'false' : null"
          [attr.aria-invalid]="shownError() ? 'true' : null"
          [attr.aria-describedby]="describedBy()"
          (input)="value.set($any($event.target).value)"
          (blur)="rule.touched()"
        />
      }
      @if (hint()) {
        <p class="hint" [id]="id + '-hint'">{{ hint() }}</p>
      }
    </div>
    @if (shownError(); as text) {
      <p class="field-error" [id]="id + '-error'">{{ text }}</p>
    }
  `,
  styles: `
    :host {
      display: block;
      inline-size: 100%;
    }
    .field {
      display: flex;
      flex-direction: column;
      gap: var(--trove-space-1);
      margin-block-end: var(--trove-space-3);
    }
    .field:has(+ .field-error) {
      margin-block-end: var(--trove-space-1);
    }
    .area-input {
      inline-size: 100%;
      resize: vertical;
    }
    .text-input {
      inline-size: 100%;
    }
    .hint {
      margin: 0;
    }
    .field-error {
      margin: 0 0 var(--trove-space-3);
      color: var(--trove-status-bad);
      font: var(--trove-type-body-small);
    }
  `,
})
export class TextField {
  readonly label = input.required<string>();
  readonly value = model('');
  readonly name = input('');
  readonly type = input<'text' | 'email' | 'password' | 'date' | 'number' | 'textarea'>('text');
  readonly hint = input('');
  readonly error = input<string | null>(null);
  readonly required = input(false);
  readonly readonly = input(false);
  /** A secret value: no spellcheck, no autocomplete (R-UI-043). */
  readonly secret = input(false);
  readonly autocomplete = input<string | null>('off');

  protected readonly id = `trove-field-${nextFieldId++}`;
  protected readonly rule = revealRule();
  /** The error once the field was touched or its form submitted (`revealRule`). */
  protected readonly shownError = computed(() => (this.rule.reveal() ? this.error() : null));
  protected readonly describedBy = computed(() => {
    const ids: string[] = [];
    if (this.hint()) ids.push(`${this.id}-hint`);
    if (this.shownError()) ids.push(`${this.id}-error`);
    return ids.length > 0 ? ids.join(' ') : null;
  });
}

/** The style guide's five field widths (Forms; Q-UI-334), each a class `field-w-<width>` of `_controls.scss`. */
export type FieldWidth = 'number' | 'duration' | 'bytes' | 'name' | 'full';

export interface SelectOption {
  readonly value: string;
  readonly label: string;
}

/**
 * A native `<select>` in a form field: the platform widget is keyboard-operable and announced correctly. It takes the style
 * guide's field width of a choice (`name`, half the column) unless its place fills a line (`full`), so the selects of one form
 * column line up whatever their options say (Q-UI-334). While its options load (`loading`), it is the same select, held, its one option saying so.
 */
@Component({
  selector: 'trove-select-field',
  host: { '[class.full]': "width() === 'full'" },
  template: `
    <div class="field">
      <label class="field-label" [for]="id">{{ label() }}</label>
      <select
        class="select-input"
        [class]="'field-w-' + width()"
        [id]="id"
        [name]="name()"
        [disabled]="loading() !== null"
        [attr.aria-busy]="loading() !== null ? 'true' : null"
        [attr.aria-invalid]="shownError() ? 'true' : null"
        [attr.aria-describedby]="shownError() ? id + '-error' : null"
        (change)="value.set($any($event.target).value)"
        (blur)="rule.touched()"
      >
        @if (loading(); as text) {
          <option value="" selected>{{ text }}</option>
        } @else {
          @for (o of options(); track o.value) {
            <option [value]="o.value" [selected]="o.value === value()">{{ o.label }}</option>
          }
        }
      </select>
    </div>
    @if (shownError(); as text) {
      <p class="field-error" [id]="id + '-error'">{{ text }}</p>
    }
  `,
  styles: `
    :host {
      display: inline-block;
      max-inline-size: 100%;
    }
    :host(.full) {
      display: block;
    }
    .field {
      display: flex;
      flex-direction: column;
      gap: var(--trove-space-1);
      margin-block-end: var(--trove-space-3);
    }
    .field:has(+ .field-error) {
      margin-block-end: var(--trove-space-1);
    }
    .field-error {
      margin: 0 0 var(--trove-space-3);
      color: var(--trove-status-bad);
      font: var(--trove-type-body-small);
    }
  `,
})
export class SelectField {
  readonly label = input.required<string>();
  readonly options = input.required<readonly SelectOption[]>();
  readonly value = model('');
  readonly name = input('');
  readonly error = input<string | null>(null);
  /** The field width (Q-UI-334): a choice is `name`; `full` where the select fills its line. */
  readonly width = input<Extract<FieldWidth, 'name' | 'full'>>('name');
  /** What the select says while its options load, such as "Loading projects…"; null once they are there. */
  readonly loading = input<string | null>(null);

  protected readonly id = `trove-select-${nextFieldId++}`;
  protected readonly rule = revealRule();
  /** The error once the field was touched or its form submitted (`revealRule`). */
  protected readonly shownError = computed(() => (this.rule.reveal() ? this.error() : null));
}
