import { Component, computed, input } from '@angular/core';
import { SessionStore } from '../../core/session-store';
import { Needs } from '../../shared/access/needs';

/** Whether a create action of a project screen is offered, and why not (R-UI-041). */
export type CreateState = 'enabled' | 'switch-off' | 'denied';

/**
 * The state of one create action in `project`: enabled for the holder of `capability` and for an
 * administrator of the project whose switch is on; "switch off" for an administrator whose switch is
 * off (shown disabled with the reason); "denied" for everyone else, such as the auditor, who reads.
 * The API decides in any case (R-UI-005).
 */
export function createState(
  session: SessionStore,
  project: string,
  right: 'managePolicies' | 'manageRepositories',
  capability: string,
): CreateState {
  if (session.has(capability)) return 'enabled';
  const rights = session.projectRights(project);
  if (!rights) return 'denied';
  return rights[right] ? 'enabled' : 'switch-off';
}

/**
 * The reason behind a disabled create action: the project's switch that is off, or the missing
 * capability. Renders nothing for an enabled action. Give `id` to the control's `aria-describedby`.
 */
@Component({
  selector: 'trove-project-needs',
  imports: [Needs],
  template: `
    @switch (state()) {
      @case ('switch-off') {
        <p class="needs" role="note">
          <span class="material-icons" aria-hidden="true">lock</span>
          <span>
            <ng-container i18n="@@project-needs.switch-off">Switched off for this project:</ng-container>{{ ' ' }}<code translate="no">{{ switchName() }}</code>.
            <ng-container i18n="@@project-needs.switch-off-hint">An administrator of trove can switch it on.</ng-container>
          </span>
        </p>
      }
      @case ('denied') {
        <trove-needs [capability]="capability()" />
      }
    }
  `,
  styles: `
    .needs {
      display: flex;
      align-items: center;
      gap: var(--trove-space-2);
      margin-block: var(--trove-space-2);
      color: var(--mat-sys-on-surface-variant);
    }
    .material-icons {
      font-size: 1.125rem;
    }
  `,
})
export class ProjectNeeds {
  readonly state = input.required<CreateState>();
  readonly capability = input.required<string>();
  readonly right = input.required<'managePolicies' | 'manageRepositories'>();
  protected readonly switchName = computed(() => this.right());
}
