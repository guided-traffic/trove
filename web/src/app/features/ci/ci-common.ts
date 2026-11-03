import { Component, input } from '@angular/core';
import { CiWarning } from '../../api/ci-api';

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

/**
 * The body of a full replace (PUT) after "Overwrite": the stored record as it is now, with only the members
 * the user changed taken from the form. A member the user did not touch keeps the value another administrator
 * gave it in between (a removed `deploy`, a removed audience, a switch turned on), so the overwrite replaces
 * exactly what the dialog showed (R-UI-042, R-CI-002). `omittedWhenEmpty` names list members the form leaves
 * out when empty; one the user did not empty is taken from the stored record as well.
 */
export function overwriteBody<B extends object>(
  body: B,
  patch: Readonly<Record<string, unknown>>,
  current: object,
  loaded: object,
  omittedWhenEmpty: readonly string[] = [],
): B {
  const now = current as Record<string, unknown>;
  const before = loaded as Record<string, unknown>;
  const mine = body as Record<string, unknown>;
  const out: Record<string, unknown> = {};
  for (const k of Object.keys(mine)) out[k] = k in patch ? patch[k] : k in now ? now[k] : mine[k];
  for (const k of omittedWhenEmpty) {
    const userEmptied = Array.isArray(before[k]) && before[k].length > 0;
    if (!(k in mine) && !userEmptied && Array.isArray(now[k]) && now[k].length > 0) out[k] = now[k];
  }
  return out as B;
}
