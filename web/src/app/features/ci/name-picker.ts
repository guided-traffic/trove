import { Component, computed, input, model, signal } from '@angular/core';

let nextPickerId = 1;

/**
 * A list of names the user builds one entry at a time (the policies and groups of a mapping, R-UI-035):
 * the chosen names as removable chips, and below them either a choice among `options` or, without
 * options, a text field with "Add". A name that is not among the options can still be typed where
 * `options` is null, because the server is the one that knows whether it exists (`unknown_policy`).
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
          @if (options(); as list) {
            <label class="visually-hidden" [for]="id + '-select'">{{ addLabel() }}</label>
            <select class="field" [id]="id + '-select'" (change)="choose($any($event.target))">
              <option value="" selected>{{ addLabel() }}</option>
              @for (o of available(); track o) {
                <option [value]="o">{{ o }}</option>
              }
            </select>
          } @else {
            <label class="visually-hidden" [for]="id + '-text'">{{ addLabel() }}</label>
            <input
              class="field"
              [id]="id + '-text'"
              autocomplete="off"
              spellcheck="false"
              [placeholder]="addLabel()"
              [value]="typed()"
              (input)="typed.set($any($event.target).value)"
              (keydown.enter)="$event.preventDefault(); addTyped()"
            />
            <button class="btn btn-default" type="button" [disabled]="typed().trim() === ''" (click)="addTyped()" i18n="@@ci.picker.add">Add</button>
          }
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
      flex-wrap: wrap;
      gap: var(--trove-space-2);
      align-items: center;
    }
    .field {
      padding: var(--trove-space-2);
      border: 1px solid var(--trove-border-strong);
      border-radius: var(--trove-radius-s);
      background: var(--trove-surface-raised);
      color: var(--trove-on-surface);
      font: inherit;
      min-inline-size: 14rem;
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
  /** The text of the empty choice, such as "Add a policy". */
  readonly addLabel = input.required<string>();
  /** The names to choose from; null accepts typed names. */
  readonly options = input<readonly string[] | null>(null);
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

  protected choose(select: HTMLSelectElement): void {
    const chosen = select.value;
    select.value = '';
    if (chosen && !this.value().includes(chosen)) this.value.set([...this.value(), chosen]);
  }

  protected addTyped(): void {
    const name = this.typed().trim();
    if (name && !this.value().includes(name)) this.value.set([...this.value(), name]);
    this.typed.set('');
  }
}
