import { Component, inject, input } from '@angular/core';
import { ProjectContext } from '../../core/project-context';
import { ProjectMembers } from './project-members';

/** The members of the project and their roles (R-UI-041, Q-UI-129): the screen of `/projects/:project/members`. */
@Component({
  selector: 'trove-project-members-page',
  imports: [ProjectMembers],
  template: `
    <header class="page-header">
      <h1 tabindex="-1" i18n="@@project.members">Members</h1>
    </header>
    <p class="muted" i18n="@@project-members.hint">
      Users and groups with their role in the project. The role decides what a member may do in the project's repositories.
    </p>
    <trove-project-members [project]="project()" (changed)="context.refresh()" />
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class ProjectMembersPage {
  /** Route parameter `:project`. */
  readonly project = input.required<string>();

  protected readonly context = inject(ProjectContext);
}
