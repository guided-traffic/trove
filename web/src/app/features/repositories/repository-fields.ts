import { Component, input, model } from '@angular/core';
import { FieldProblem } from '../../api/problem';
import { fieldMessage, problemsAt } from '../../shared/field-errors';
import { RepositoryForm } from './repository-model';

/** The detail groups of the repository document's common members (R-UI-083, R-UI-087): each is a group of its own. */
export type RepositoryFieldsSection = 'notes' | 'paths' | 'access';

/**
 * The details of the repository document that every format has (R-REPO-004, R-UI-083): the administrator notes, the include and
 * exclude patterns with their syntax, and the hiding of existence. Each is one `section`, a group of the creation form's
 * **Advanced settings** and a closed section of the settings page; the key, the description and the visibility are the forms' own
 * standard fields, and disabling is an action of the settings page's **Advanced** (Q-UI-352). The controls keep the ids a server
 * error at `/adminNotes`, `/includePatterns/…`, `/excludePatterns/…` or `/hideExistence` is sent to (`f-<member>`).
 */
@Component({
  selector: 'trove-repository-fields',
  template: `
    @switch (section()) {
      @case ('notes') {
        <div class="field">
          <label class="field-label" for="f-adminNotes" i18n="@@repo.field.notes-text">Notes (optional)</label>
          <textarea
            id="f-adminNotes"
            class="area-input field-w-full"
            rows="3"
            maxlength="8000"
            [value]="form().adminNotes"
            [readonly]="readonly()"
            [attr.aria-invalid]="invalid('/adminNotes')"
            aria-describedby="d-adminNotes"
            (input)="set({ adminNotes: $any($event.target).value })"
          ></textarea>
          <p id="d-adminNotes" class="hint" i18n="@@repo.field.notes.hint">
            Shown only to people who may read repository configuration.
          </p>
          @for (e of errors('/adminNotes'); track e.code) {
            <p class="field-error">{{ message(e) }}</p>
          }
        </div>
      }
      @case ('paths') {
        <div class="field">
          <label class="field-label" for="f-includePatterns" i18n="@@repo.field.include">Include patterns</label>
          <textarea
            id="f-includePatterns"
            class="area-input field-w-full"
            rows="3"
            spellcheck="false"
            [value]="form().includePatterns"
            [readonly]="readonly()"
            [attr.aria-invalid]="invalid('/includePatterns')"
            aria-describedby="d-patterns"
            (input)="set({ includePatterns: $any($event.target).value })"
          ></textarea>
          @for (e of errors('/includePatterns'); track e.code + e.detail) {
            <p class="field-error">{{ message(e) }}</p>
          }
        </div>
        <div class="field">
          <label class="field-label" for="f-excludePatterns" i18n="@@repo.field.exclude">Exclude patterns</label>
          <textarea
            id="f-excludePatterns"
            class="area-input field-w-full"
            rows="3"
            spellcheck="false"
            [value]="form().excludePatterns"
            [readonly]="readonly()"
            [attr.aria-invalid]="invalid('/excludePatterns')"
            aria-describedby="d-patterns"
            (input)="set({ excludePatterns: $any($event.target).value })"
          ></textarea>
          @for (e of errors('/excludePatterns'); track e.code + e.detail) {
            <p class="field-error">{{ message(e) }}</p>
          }
          <p id="d-patterns" class="hint" i18n="@@repo.field.patterns.hint-short">
            One pattern per line. An empty include list admits every path; an exclude pattern wins.
          </p>
        </div>
        <details class="syntax">
          <summary i18n="@@pattern.help.summary">Pattern syntax</summary>
          <ul>
            <li>
              <code>*</code>
              <span i18n="@@pattern.help.star">matches any characters within one path segment.</span>
            </li>
            <li>
              <code>**</code>
              <span i18n="@@pattern.help.starstar">as a whole segment matches any number of segments.</span>
            </li>
            <li>
              <code>?</code>
              <span i18n="@@pattern.help.question">matches one character within a segment.</span>
            </li>
            <li>
              <code>\\</code>
              <span i18n="@@pattern.help.backslash">makes the next character literal.</span>
            </li>
          </ul>
        </details>
      }
      @case ('access') {
        <div class="field">
          <label class="field-label" for="f-hideExistence" i18n="@@repo.field.hide">Hide existence from callers without access</label>
          <select
            id="f-hideExistence"
            class="select-input field-w-name"
            [disabled]="readonly()"
            [attr.aria-invalid]="invalid('/hideExistence')"
            (change)="set({ hideExistence: $any($event.target).value })"
          >
            <option value="inherit" [selected]="form().hideExistence === 'inherit'" i18n="@@repo.hide.inherit">
              Use the installation setting
            </option>
            <option value="true" [selected]="form().hideExistence === 'true'" i18n="@@repo.hide.true">
              Hide: answer as if it did not exist
            </option>
            <option value="false" [selected]="form().hideExistence === 'false'" i18n="@@repo.hide.false">
              Do not hide: answer 403
            </option>
          </select>
          @for (e of errors('/hideExistence'); track e.code) {
            <p class="field-error">{{ message(e) }}</p>
          }
        </div>
      }
    }
  `,
  styles: `
    :host {
      display: block;
    }
    .field {
      display: flex;
      flex-direction: column;
      gap: var(--trove-space-1);
      margin-block-end: var(--trove-space-3);
    }
    .hint {
      margin: 0;
    }
    .syntax {
      margin-block-end: var(--trove-space-3);
    }
  `,
})
export class RepositoryFields {
  readonly form = model.required<RepositoryForm>();
  readonly section = input.required<RepositoryFieldsSection>();
  /** A caller who may not change the repository sees the values and changes nothing (R-UI-087). */
  readonly readonly = input(false);
  /** Server field problems by pointer (`ApiError.fieldErrors`). */
  readonly fieldErrors = input<ReadonlyMap<string, FieldProblem>>(new Map());

  protected errors(pointer: string): readonly FieldProblem[] {
    return problemsAt(this.fieldErrors(), pointer);
  }

  protected invalid(pointer: string): 'true' | null {
    return this.errors(pointer).length > 0 ? 'true' : null;
  }

  protected message(problem: FieldProblem): string {
    return fieldMessage(problem);
  }

  protected set(change: Partial<{ -readonly [K in keyof RepositoryForm]: RepositoryForm[K] }>): void {
    this.form.update((f) => ({ ...f, ...change }) as RepositoryForm);
  }
}
