import { Component, computed, inject, input, viewChild } from '@angular/core';
import { ProjectContext } from '../../core/project-context';
import { SessionStore } from '../../core/session-store';
import { ProjectMembers } from './project-members';
import { PageHeader } from '../../shared/ui/page-header';

/** The members of the project and their roles (R-UI-041, Q-UI-129, Q-UI-214): the screen of `/projects/:project/members`. */
@Component({
  selector: 'trove-project-members-page',
  imports: [PageHeader, ProjectMembers],
  template: `
    <trove-page-header heading="Members" i18n-heading="@@project.members" [description]="hint">
      @if (canWrite()) {
        <button actions class="btn btn-confirm" type="button" (click)="list().openAdd()" i18n="@@project-members.add">Add member</button>
      }
    </trove-page-header>
    <trove-project-members [project]="project()" (changed)="context.refresh()" />
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class ProjectMembersPage {
  protected readonly hint = $localize`:@@project-members.hint:Users and groups with their role in the project. The role decides what a member may do in the project's repositories.`;
  /** Route parameter `:project`. */
  readonly project = input.required<string>();

  protected readonly context = inject(ProjectContext);
  protected readonly list = viewChild.required(ProjectMembers);
  private readonly session = inject(SessionStore);
  /** As in the list; the API decides in any case (R-UI-005). */
  protected readonly canWrite = computed(() => this.session.has('projects:write') || this.session.isOwnerOf(this.project()));
}
