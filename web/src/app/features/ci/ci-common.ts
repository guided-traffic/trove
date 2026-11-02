import { Component, input, output } from '@angular/core';
import { ApiError } from '../../api/problem';
import { CiWarning } from '../../api/ci-api';
import { ErrorNotice } from '../../shared/error-notice';

/** True for the answer of an operation whose work package is not built yet (`501 ci_not_implemented`). */
export function isNotImplemented(e: ApiError | null): boolean {
  return e !== null && (e.status === 501 || e.code === 'ci_not_implemented');
}

/**
 * The failure of a CI call. `501 ci_not_implemented` is the state of a server whose federation is not
 * switched on yet: a calm "not available yet" card, not an error alert (R-UI-035). Every other failure
 * is the shared error notice, which also covers `403 project_switch_off` the way the project screens
 * show it.
 */
@Component({
  selector: 'trove-ci-error',
  imports: [ErrorNotice],
  template: `
    @if (unavailable()) {
      <section class="unavailable" role="status">
        <span class="material-icons" aria-hidden="true">hourglass_empty</span>
        <div>
          <h2 i18n="@@ci.unavailable.title">Not available yet</h2>
          <p i18n="@@ci.unavailable.text">
            CI identity is not switched on for this server yet. The screens work as soon as it is; nothing is broken and nothing needs to be done here.
          </p>
        </div>
      </section>
    } @else {
      <trove-error-notice [error]="error()" (retry)="retry.emit()" />
    }
  `,
  styles: `
    .unavailable {
      display: flex;
      gap: var(--trove-space-3);
      align-items: flex-start;
      margin-block: var(--trove-space-3);
      padding: var(--trove-space-4);
      border: 1px solid var(--trove-border);
      border-radius: var(--trove-radius-m);
      background: var(--trove-surface-raised);
    }
    h2 {
      margin: 0 0 var(--trove-space-1);
      font: var(--mat-sys-title-medium);
    }
    p {
      margin: 0;
      color: var(--trove-text-muted);
    }
  `,
})
export class CiError {
  readonly error = input.required<ApiError>();
  readonly retry = output<void>();
  protected unavailable(): boolean {
    return isNotImplemented(this.error());
  }
}

/** The warnings a save returned (R-CI-010, R-CI-011): each with an icon, its detail and the member it concerns. */
@Component({
  selector: 'trove-ci-warnings',
  template: `
    @if (warnings().length > 0) {
      <section class="warnings" role="status" aria-labelledby="ci-warnings-title">
        <h2 id="ci-warnings-title" i18n="@@ci.warnings.title">Saved, with warnings</h2>
        <ul>
          @for (w of warnings(); track w.code + (w.pointer ?? '')) {
            <li>
              <span class="material-icons" aria-hidden="true">warning</span>
              <span
                ><code translate="no">{{ w.code }}</code
                >: {{ w.detail }}
                @if (w.pointer) {
                  <span class="muted">(<code translate="no">{{ w.pointer }}</code>)</span>
                }
              </span>
            </li>
          }
        </ul>
      </section>
    }
  `,
  styles: `
    .warnings {
      margin-block: var(--trove-space-3);
      padding: var(--trove-space-3) var(--trove-space-4);
      border: 1px solid var(--trove-status-warn);
      border-radius: var(--trove-radius-m);
    }
    h2 {
      margin: 0 0 var(--trove-space-2);
      font: var(--mat-sys-title-small);
    }
    ul {
      margin: 0;
      padding: 0;
      list-style: none;
    }
    li {
      display: flex;
      gap: var(--trove-space-2);
      color: var(--mat-sys-on-surface);
    }
    .material-icons {
      color: var(--trove-status-warn);
      font-size: 1.125rem;
    }
    .muted {
      color: var(--trove-text-muted);
    }
  `,
})
export class CiWarnings {
  readonly warnings = input.required<readonly CiWarning[]>();
}

/** The router commands of the CI screens, which exist once for the system tier and once under a project (Q-CI-26). */
export const ciRoutes = {
  issuer: (issuer: string): string[] => ['/admin/ci', issuer],
  mapping: (project: string | null, issuer: string, name: string | null): string[] =>
    project
      ? ['/admin/projects', project, 'ci', 'mappings', name ?? 'new']
      : ['/admin/ci', issuer, 'mappings', name ?? 'new'],
  /** A project's mapping routes carry the issuer in the query; the system routes carry it in the path. */
  mappingQuery: (project: string | null, issuer: string): Record<string, string> | null =>
    project && issuer ? { issuer } : null,
  test: (project: string | null, issuer: string): string[] =>
    project ? ['/admin/projects', project, 'ci', 'test'] : ['/admin/ci', issuer, 'test'],
  overview: (project: string | null, issuer: string | null): string[] =>
    project ? ['/admin/projects', project, 'ci'] : issuer ? ['/admin/ci', issuer] : ['/admin/ci'],
};

/** The changed top-level members of a write body against the stored record: what the conflict dialog compares. */
export function changedMembers(input: object, stored: object): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  const a = input as Record<string, unknown>;
  const b = stored as Record<string, unknown>;
  for (const k of Object.keys(a)) {
    if (JSON.stringify(a[k]) !== JSON.stringify(b[k] ?? null) && !(a[k] === undefined && b[k] === undefined)) out[k] = a[k];
  }
  return out;
}
