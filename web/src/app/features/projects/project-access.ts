import { Component, input } from '@angular/core';
import { SessionStore } from '../../core/session-store';
import { Needs } from '../../shared/access/needs';

/** Whether a create action of a project screen is offered (R-UI-041). */
export type CreateState = 'enabled' | 'denied';

/**
 * The state of one create action in `project`: enabled for the holder of `capability` and for an Owner of the
 * project; "denied" for everyone else, such as the auditor, who reads. The API decides in any case (R-UI-005).
 */
export function createState(session: SessionStore, project: string, capability: string): CreateState {
  return session.has(capability) || session.isOwnerOf(project) ? 'enabled' : 'denied';
}

/**
 * The reason behind a disabled create action: the missing capability. Renders nothing for an enabled action. Give
 * `id` to the control's `aria-describedby`.
 */
@Component({
  selector: 'trove-project-needs',
  imports: [Needs],
  template: `
    @if (state() === 'denied') {
      <trove-needs [capability]="capability()" />
    }
  `,
})
export class ProjectNeeds {
  readonly state = input.required<CreateState>();
  readonly capability = input.required<string>();
}
