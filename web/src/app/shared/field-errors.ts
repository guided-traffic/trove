import { Component, ElementRef, Injector, afterNextRender, inject, input, output } from '@angular/core';
import { ApiError, FieldProblem } from '../api/problem';
import { describeProblem } from '../core/http/problem-messages';

/** One line of the error summary: where, what, and which control to focus. */
export interface SummaryItem {
  readonly label: string;
  readonly message: string;
  readonly targetId: string;
}

/** The translated message of one field problem; an unknown code shows the server's English detail. */
export function fieldMessage(problem: FieldProblem): string {
  const text = describeProblem(
    new ApiError(422, { code: problem.code, detail: problem.detail }, null),
  );
  return text.known ? text.text : (problem.detail ?? problem.code);
}

/** The text for the field problem at `pointer` of a failed write, or null. */
export function fieldErrorAt(error: ApiError | null, pointer: string): string | null {
  const problem = error?.fieldErrors.get(pointer);
  return problem ? fieldMessage(problem) : null;
}

/** The problems at a pointer or below it (`/includePatterns` also matches `/includePatterns/3`). */
export function problemsAt(
  errors: ReadonlyMap<string, FieldProblem>,
  pointer: string,
): readonly FieldProblem[] {
  return [...errors.entries()]
    .filter(([p]) => p === pointer || p.startsWith(`${pointer}/`))
    .map(([, problem]) => problem);
}

/**
 * The error summary at the top of a form (R-UI-044, WCAG 3.3.1): every field problem with a
 * button that moves focus to its control. The summary is an alert, so it is announced when it appears.
 */
@Component({
  selector: 'trove-error-summary',
  template: `
    @if (items().length > 0) {
      <div class="summary" role="alert" tabindex="-1">
        <h2 class="title" i18n="@@field-errors.title">Some entries are not valid</h2>
        <ul>
          @for (item of items(); track item.targetId + item.message) {
            <li>
              <button type="button" class="link" (click)="focus(item.targetId)">
                {{ item.label }}
              </button>
              : {{ item.message }}
            </li>
          }
        </ul>
      </div>
    }
  `,
  styles: `
    .summary {
      margin-block: var(--trove-space-3);
      padding: var(--trove-space-3) var(--trove-space-4);
      border: 2px solid var(--trove-status-bad);
      border-radius: var(--trove-radius-m);
    }
    .title {
      margin: 0 0 var(--trove-space-2);
      font: var(--trove-type-title-small);
    }
    ul {
      margin: 0;
      padding-inline-start: var(--trove-space-4);
    }
    .link {
      padding: 0;
      border: 0;
      background: none;
      color: var(--trove-link);
      font: inherit;
      text-decoration: underline;
      cursor: pointer;
    }
  `,
})
export class ErrorSummary {
  readonly items = input.required<readonly SummaryItem[]>();
  /**
   * A link of the summary was chosen: the screen opens the closed region that holds `id` (R-UI-083), and the field is focused once
   * the region is open.
   */
  readonly reveal = output<string>();

  private readonly host = inject(ElementRef<HTMLElement>);
  private readonly injector = inject(Injector);

  /** Moves the focus to the summary, after a submit that it reports (R-UI-086). */
  focusSummary(): void {
    afterNextRender(() => (this.host.nativeElement as HTMLElement).querySelector<HTMLElement>('.summary')?.focus(), {
      injector: this.injector,
    });
  }

  protected focus(id: string): void {
    this.reveal.emit(id);
    document.getElementById(id)?.focus();
    afterNextRender(() => document.getElementById(id)?.focus(), { injector: this.injector });
  }
}
