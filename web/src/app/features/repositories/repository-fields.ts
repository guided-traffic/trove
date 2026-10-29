import { Component, input, model } from '@angular/core';
import { FieldProblem } from '../../api/problem';
import { fieldMessage, problemsAt } from '../../shared/field-errors';
import { RepositoryForm } from './repository-model';

/**
 * The settings of the repository document that apply to the generic format (R-REPO-004,
 * R-GENERIC-008): description, notes, include and exclude patterns and the three access switches.
 * Used by the wizard and the edit form. Server field errors appear next to their control.
 */
@Component({
  selector: 'trove-repository-fields',
  template: `
    @if (section() === 'basic') {
      <div class="form-grid">
        <div class="field">
          <label class="field-label" for="f-description" i18n="@@repo.field.description"
            >Description</label
          >
          <textarea
            id="f-description"
            class="area-input"
            rows="2"
            maxlength="2000"
            [value]="form().description"
            [attr.aria-invalid]="invalid('/description')"
            [attr.aria-describedby]="describedBy('description')"
            (input)="set({ description: $any($event.target).value })"
          ></textarea>
          <p id="d-description" class="hint" i18n="@@repo.field.description.hint">
            Shown to everyone who can read the repository.
          </p>
          @for (e of errors('/description'); track e.code) {
            <p class="field-error" id="e-description">{{ message(e) }}</p>
          }
        </div>
        <div class="field">
          <label class="field-label" for="f-adminNotes" i18n="@@repo.field.notes"
            >Administrator notes</label
          >
          <textarea
            id="f-adminNotes"
            class="area-input"
            rows="3"
            maxlength="8000"
            [value]="form().adminNotes"
            [attr.aria-invalid]="invalid('/adminNotes')"
            (input)="set({ adminNotes: $any($event.target).value })"
          ></textarea>
          <p class="hint" i18n="@@repo.field.notes.hint">
            Shown only to people who may read repository configuration.
          </p>
          @for (e of errors('/adminNotes'); track e.code) {
            <p class="field-error">{{ message(e) }}</p>
          }
        </div>
      </div>
    } @else {
      <div class="form-grid">
        <div class="field">
          <label class="field-label" for="f-includePatterns" i18n="@@repo.field.include"
            >Include patterns</label
          >
          <textarea
            id="f-includePatterns"
            class="area-input"
            rows="3"
            spellcheck="false"
            [value]="form().includePatterns"
            [attr.aria-invalid]="invalid('/includePatterns')"
            [attr.aria-describedby]="'d-patterns'"
            (input)="set({ includePatterns: $any($event.target).value })"
          ></textarea>
          @for (e of errors('/includePatterns'); track e.code + e.detail) {
            <p class="field-error">{{ message(e) }}</p>
          }
        </div>
        <div class="field">
          <label class="field-label" for="f-excludePatterns" i18n="@@repo.field.exclude"
            >Exclude patterns</label
          >
          <textarea
            id="f-excludePatterns"
            class="area-input"
            rows="3"
            spellcheck="false"
            [value]="form().excludePatterns"
            [attr.aria-invalid]="invalid('/excludePatterns')"
            [attr.aria-describedby]="'d-patterns'"
            (input)="set({ excludePatterns: $any($event.target).value })"
          ></textarea>
          @for (e of errors('/excludePatterns'); track e.code + e.detail) {
            <p class="field-error">{{ message(e) }}</p>
          }
          <p id="d-patterns" class="hint" i18n="@@repo.field.patterns.hint">
            One pattern per line. They select which paths the repository admits; an empty include
            list admits every path, and an exclude pattern wins.
          </p>
        </div>
        <details class="syntax">
          <summary i18n="@@pattern.help.summary">Pattern syntax</summary>
          <ul>
            <li>
              <code>*</code>
              <span i18n="@@pattern.help.star"
                >matches any characters within one path segment.</span
              >
            </li>
            <li>
              <code>**</code>
              <span i18n="@@pattern.help.starstar"
                >as a whole segment matches any number of segments.</span
              >
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
        <label class="check-row" for="f-disabled">
          <input
            id="f-disabled"
            type="checkbox"
            [checked]="form().disabled"
            (change)="set({ disabled: $any($event.target).checked })"
          />
          <span i18n="@@repo.field.disabled">
            Disabled: takes no part in resolution, download or deploy
          </span>
        </label>
        <label class="check-row" for="f-requireAuthentication">
          <input
            id="f-requireAuthentication"
            type="checkbox"
            [checked]="form().requireAuthentication"
            (change)="set({ requireAuthentication: $any($event.target).checked })"
          />
          <span i18n="@@repo.field.require-auth">
            Require authentication, even where anonymous read is granted
          </span>
        </label>
        <div class="field">
          <label class="field-label" for="f-hideExistence" i18n="@@repo.field.hide"
            >Hide existence from callers without access</label
          >
          <select
            id="f-hideExistence"
            class="select-input"
            [value]="form().hideExistence"
            (change)="set({ hideExistence: $any($event.target).value })"
          >
            <option
              value="inherit"
              [selected]="form().hideExistence === 'inherit'"
              i18n="@@repo.hide.inherit"
            >
              Use the installation setting
            </option>
            <option
              value="true"
              [selected]="form().hideExistence === 'true'"
              i18n="@@repo.hide.true"
            >
              Hide: answer as if it did not exist
            </option>
            <option
              value="false"
              [selected]="form().hideExistence === 'false'"
              i18n="@@repo.hide.false"
            >
              Do not hide: answer 403
            </option>
          </select>
        </div>
      </div>
    }
  `,
  styles: `
    .syntax {
      margin-block-end: var(--trove-space-3);
    }
  `,
})
export class RepositoryFields {
  readonly form = model.required<RepositoryForm>();
  readonly section = input.required<'basic' | 'access'>();
  /** Server field problems by pointer (`ApiError.fieldErrors`). */
  readonly fieldErrors = input<ReadonlyMap<string, FieldProblem>>(new Map());

  protected errors(pointer: string): readonly FieldProblem[] {
    return problemsAt(this.fieldErrors(), pointer);
  }

  protected invalid(pointer: string): 'true' | null {
    return this.errors(pointer).length > 0 ? 'true' : null;
  }

  protected describedBy(name: string): string {
    return `d-${name}`;
  }

  protected message(problem: FieldProblem): string {
    return fieldMessage(problem);
  }

  protected set(
    change: Partial<{ -readonly [K in keyof RepositoryForm]: RepositoryForm[K] }>,
  ): void {
    this.form.update((f) => ({ ...f, ...change }) as RepositoryForm);
  }
}
