import { Component, effect, input, output, signal, untracked } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { TextField } from '../../shared/access/fields';

/** One user or group a picker offers; `ref` is `user:<name>` or `group:<name>`. */
export interface PickablePrincipal {
  readonly ref: string;
  readonly kind: 'user' | 'group';
}

/** Debounce of the search while the person types. */
export const PICKER_DEBOUNCE_MS = 250;

/** The least number of characters the lookup of a project accepts (Q-AUTHZ-182). */
export const PICKER_MIN_CHARS = 2;

/**
 * A search box for users and groups with the matches as buttons (R-UI-041): the project dialog uses it
 * for administrators, which the system administrator finds through the identity lists, and the policy
 * editor of a project uses it for the principal lookup of Q-AUTHZ-182. The caller supplies `search`;
 * the component holds the text, the debounce and the late-answer guard. A pick is emitted and the box
 * empties. Enter never submits a surrounding form.
 */
@Component({
  selector: 'trove-principal-picker',
  // Enter in the search box searches; it must not submit the dialog or form around it.
  host: { '(keydown.enter)': '$event.preventDefault()' },
  imports: [MatButtonModule, TextField],
  template: `
    <div class="picker">
      <trove-text-field
        [label]="label()"
        [name]="name()"
        [(value)]="text"
        [hint]="hint()"
        [error]="failure()"
      />
      <p class="status" role="status">
        @if (searching()) {
          <ng-container i18n="@@picker.searching">Searching.</ng-container>
        } @else if (searched() && results().length === 0 && !failure()) {
          <ng-container i18n="@@picker.none">No user or group matches.</ng-container>
        }
      </p>
      @if (results().length > 0) {
        <ul class="matches" [attr.aria-label]="resultsLabel">
          @for (r of results(); track r.ref) {
            <li>
              <button mat-stroked-button type="button" (click)="pick(r)">
                <span class="material-icons" aria-hidden="true">{{ r.kind === 'group' ? 'group' : 'person' }}</span>
                <span translate="no">{{ r.ref }}</span>
              </button>
            </li>
          }
        </ul>
      }
    </div>
  `,
  styles: `
    :host {
      display: block;
    }
    .status {
      margin: 0;
      min-block-size: 1.25rem;
      color: var(--mat-sys-on-surface-variant);
      font: var(--mat-sys-body-small);
    }
    .matches {
      display: flex;
      flex-wrap: wrap;
      gap: var(--trove-space-2);
      margin: 0 0 var(--trove-space-3);
      padding: 0;
      list-style: none;
    }
    .material-icons {
      margin-inline-end: var(--trove-space-1);
      font-size: 1.125rem;
    }
  `,
})
export class PrincipalPicker {
  readonly label = input.required<string>();
  readonly name = input('');
  readonly hint = input('');
  /** Finds the users and groups whose name starts with the text; the text has two characters at least. */
  readonly search = input.required<(text: string) => Promise<readonly PickablePrincipal[]>>();
  /** References that are chosen already: they are not offered again. */
  readonly exclude = input<readonly string[]>([]);

  readonly picked = output<string>();

  protected readonly text = signal('');
  protected readonly results = signal<readonly PickablePrincipal[]>([]);
  protected readonly searching = signal(false);
  protected readonly searched = signal(false);
  protected readonly failure = signal<string | null>(null);
  protected readonly resultsLabel = $localize`:@@picker.results:Matching users and groups`;
  private generation = 0;

  constructor() {
    effect((onCleanup) => {
      const text = this.text().trim();
      const generation = ++this.generation;
      if (text.length < PICKER_MIN_CHARS) {
        untracked(() => {
          this.results.set([]);
          this.searching.set(false);
          this.searched.set(false);
          this.failure.set(null);
        });
        return;
      }
      const timer = setTimeout(() => void this.run(text, generation), PICKER_DEBOUNCE_MS);
      onCleanup(() => clearTimeout(timer));
    });
  }

  private async run(text: string, generation: number): Promise<void> {
    this.searching.set(true);
    this.failure.set(null);
    try {
      const found = await this.search()(text);
      if (generation !== this.generation) return;
      const skip = new Set(this.exclude());
      this.results.set(found.filter((p) => !skip.has(p.ref)));
      this.searched.set(true);
    } catch {
      if (generation !== this.generation) return;
      this.results.set([]);
      this.failure.set($localize`:@@picker.failed:The search failed. Try again.`);
    } finally {
      if (generation === this.generation) this.searching.set(false);
    }
  }

  protected pick(principal: PickablePrincipal): void {
    this.picked.emit(principal.ref);
    this.text.set('');
  }
}
