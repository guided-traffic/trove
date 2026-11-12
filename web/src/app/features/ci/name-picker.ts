import { Component, computed, input, model, signal } from '@angular/core';

let nextPickerId = 1;

/**
 * A list of names the user builds one entry at a time (the policies and groups of a mapping, R-UI-035): the chosen names as
 * removable chips, and below them one control from the start, whatever the list's state (Q-UI-334): a text field that suggests
 * the `options` as the user types (a combobox through the platform's `datalist`, so a long list stays usable) and **Add**.
 * While the options load, the field is held and says so; a name picked from the suggestions is added at once. A name that is
 * not among the options can still be typed, because the server is the one that knows whether it exists (`unknown_policy`);
 * where the options could not be read (`options` null) that is the only way.
 */
@Component({
  selector: 'trove-ci-name-picker',
  imports: [],
  template: `
    <div class="picker">
      <span class="label" [id]="id + '-label'">{{ label() }}</span>
      @if (value().length > 0) {
        <ul class="chips" [attr.aria-labelledby]="id + '-label'">
          @for (v of value(); track v) {
            <li class="chip">
              <span translate="no">{{ v }}</span>
              @if (!readonly()) {
                <button type="button" class="chip-remove" (click)="remove(v)" [attr.aria-label]="removeLabel(v)">
                  <span class="material-icons" aria-hidden="true">close</span>
                </button>
              }
            </li>
          }
        </ul>
      } @else {
        <p class="none" i18n="@@ci.picker.none">None.</p>
      }
      @if (!readonly()) {
        <div class="add">
          <label class="visually-hidden" [for]="id + '-input'">{{ addLabel() }}</label>
          <input
            #field
            class="text-input field-w-name"
            [id]="id + '-input'"
            autocomplete="off"
            spellcheck="false"
            [attr.list]="id + '-options'"
            [disabled]="loading() !== null"
            [attr.aria-busy]="loading() !== null ? 'true' : null"
            [placeholder]="loading() ?? addLabel()"
            (input)="typed.set(field.value)"
            (change)="pickedFromList(field)"
            (keydown.enter)="$event.preventDefault(); add(field)"
          />
          <datalist [id]="id + '-options'">
            @for (o of available(); track o) {
              <option [value]="o"></option>
            }
          </datalist>
          <button class="btn btn-default" type="button" [attr.aria-describedby]="id + '-label'" [disabled]="loading() !== null || typed().trim() === ''" (click)="add(field)" i18n="@@ci.picker.add">Add</button>
        </div>
      }
      @if (error(); as text) {
        <p class="field-error">{{ text }}</p>
      }
    </div>
  `,
  styles: `
    .picker {
      display: flex;
      flex-direction: column;
      gap: var(--trove-space-2);
    }
    .label {
      font: var(--trove-type-label-large);
    }
    .chips {
      display: flex;
      flex-wrap: wrap;
      gap: var(--trove-space-2);
      margin: 0;
      padding: 0;
      list-style: none;
    }
    .chip {
      display: inline-flex;
      align-items: center;
      gap: var(--trove-space-1);
      padding: 0 var(--trove-space-2);
      border: 1px solid var(--trove-border-strong);
      border-radius: var(--trove-radius-full);
      background: var(--trove-surface-raised);
    }
    .chip-remove {
      display: inline-flex;
      padding: 0;
      border: 0;
      background: none;
      color: inherit;
      cursor: pointer;
    }
    .chip-remove .material-icons {
      font-size: 1rem;
    }
    .none {
      margin: 0;
      color: var(--trove-text-muted);
    }
    .add {
      display: flex;
      gap: var(--trove-space-2);
      align-items: center;
    }
    /* The field keeps its width and gives way on a narrow line, so **Add** stays beside it. */
    .add .text-input {
      flex: 0 1 var(--trove-field-width-name);
      min-inline-size: 0;
    }
    .field-error {
      margin: 0;
      color: var(--trove-status-bad);
      font: var(--trove-type-body-small);
    }
    .visually-hidden {
      position: absolute;
      inline-size: 1px;
      block-size: 1px;
      overflow: hidden;
      clip-path: inset(50%);
    }
  `,
})
export class NamePicker {
  readonly label = input.required<string>();
  /** The field's name and its placeholder, such as "Add a policy". */
  readonly addLabel = input.required<string>();
  /** The names to suggest; null where they could not be read: then a typed name is the way. */
  readonly options = input<readonly string[] | null>(null);
  /** What the field says while the options load, such as "Loading policies…": it is held until then. Null once they are read. */
  readonly loading = input<string | null>(null);
  readonly value = model<readonly string[]>([]);
  readonly readonly = input(false);
  readonly error = input<string | null>(null);

  protected readonly id = `trove-ci-picker-${nextPickerId++}`;
  protected readonly typed = signal('');
  protected readonly available = computed(() => (this.options() ?? []).filter((o) => !this.value().includes(o)));

  protected removeLabel(name: string): string {
    return $localize`:@@ci.picker.remove:Remove ${name}:name:`;
  }

  protected remove(name: string): void {
    this.value.set(this.value().filter((v) => v !== name));
  }

  /** A name taken from the suggestions is added at once; a typed one waits for **Add** or Enter. */
  protected pickedFromList(field: HTMLInputElement): void {
    if ((this.options() ?? []).includes(field.value.trim())) this.add(field);
  }

  /** Adds the field's name and empties the field. */
  protected add(field: HTMLInputElement): void {
    const name = field.value.trim();
    if (name && !this.value().includes(name)) this.value.set([...this.value(), name]);
    field.value = '';
    this.typed.set('');
  }
}
