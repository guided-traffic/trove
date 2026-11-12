import { Component, inject, input } from '@angular/core';
import { ProjectContext } from '../../core/project-context';
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
 * The reason behind a disabled action of a project screen: whom to ask, the Owners of the project of the URL by name
 * (Q-UI-318); the missing capability where the screen is not a project's. Renders nothing for an enabled action. Give `id` to
 * the control's `aria-describedby`.
 */
@Component({
  selector: 'trove-project-needs',
  imports: [Needs],
  template: `
    @if (state() === 'denied') {
      <trove-needs [capability]="capability()" [project]="context.key()" />
    }
  `,
})
export class ProjectNeeds {
  readonly state = input.required<CreateState>();
  readonly capability = input.required<string>();
  protected readonly context = inject(ProjectContext);
}
