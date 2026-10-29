import { Component, computed, input, model } from '@angular/core';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatInputModule } from '@angular/material/input';

let nextFieldId = 1;

/**
 * A labelled text input with its error text below (R-UI-044: field errors as text next to the
 * field, 3.3.1). The error is tied to the input with `aria-describedby` and `aria-invalid`; the
 * form's summary is the screen's `<trove-error-notice>`. `type="password"` fields are for secrets
 * only and switch autocomplete and spellcheck off (R-UI-043).
 */
@Component({
  selector: 'trove-text-field',
  imports: [MatFormFieldModule, MatInputModule],
  template: `
    <mat-form-field class="field" subscriptSizing="dynamic">
      <mat-label>{{ label() }}</mat-label>
      @if (type() === 'textarea') {
        <textarea
          matInput
          rows="3"
          [id]="id"
          [name]="name()"
          [value]="value()"
          [readonly]="readonly()"
          [required]="required()"
          [attr.aria-invalid]="error() ? 'true' : null"
          [attr.aria-describedby]="describedBy()"
          (input)="value.set($any($event.target).value)"
        ></textarea>
      } @else {
        <input
          matInput
          [id]="id"
          [type]="type()"
          [name]="name()"
          [value]="value()"
          [readonly]="readonly()"
          [required]="required()"
          [attr.autocomplete]="autocomplete()"
          [attr.spellcheck]="secret() || type() === 'password' ? 'false' : null"
          [attr.aria-invalid]="error() ? 'true' : null"
          [attr.aria-describedby]="describedBy()"
          (input)="value.set($any($event.target).value)"
        />
      }
      @if (hint()) {
        <mat-hint [id]="id + '-hint'">{{ hint() }}</mat-hint>
      }
    </mat-form-field>
    @if (error(); as text) {
      <p class="field-error" [id]="id + '-error'">{{ text }}</p>
    }
  `,
  styles: `
    :host {
      display: block;
      inline-size: 100%;
    }
    .field {
      inline-size: 100%;
    }
    .field-error {
      margin: 0 0 var(--trove-space-3);
      color: var(--trove-status-bad);
      font: var(--mat-sys-body-small);
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
  protected readonly describedBy = computed(() => {
    const ids: string[] = [];
    if (this.hint()) ids.push(`${this.id}-hint`);
    if (this.error()) ids.push(`${this.id}-error`);
    return ids.length > 0 ? ids.join(' ') : null;
  });
}

export interface SelectOption {
  readonly value: string;
  readonly label: string;
}

/** A native `<select>` in a form field: the platform widget is keyboard-operable and announced correctly. */
@Component({
  selector: 'trove-select-field',
  imports: [MatFormFieldModule, MatInputModule],
  template: `
    <mat-form-field class="field" subscriptSizing="dynamic">
      <mat-label>{{ label() }}</mat-label>
      <select
        matNativeControl
        [id]="id"
        [name]="name()"
        [attr.aria-invalid]="error() ? 'true' : null"
        [attr.aria-describedby]="error() ? id + '-error' : null"
        (change)="value.set($any($event.target).value)"
      >
        @for (o of options(); track o.value) {
          <option [value]="o.value" [selected]="o.value === value()">{{ o.label }}</option>
        }
      </select>
    </mat-form-field>
    @if (error(); as text) {
      <p class="field-error" [id]="id + '-error'">{{ text }}</p>
    }
  `,
  styles: `
    :host {
      display: inline-block;
    }
    .field-error {
      margin: 0 0 var(--trove-space-3);
      color: var(--trove-status-bad);
      font: var(--mat-sys-body-small);
    }
  `,
})
export class SelectField {
  readonly label = input.required<string>();
  readonly options = input.required<readonly SelectOption[]>();
  readonly value = model('');
  readonly name = input('');
  readonly error = input<string | null>(null);

  protected readonly id = `trove-select-${nextFieldId++}`;
}
