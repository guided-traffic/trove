import { Component, computed, inject, input } from '@angular/core';
import { SessionStore } from '../../core/session-store';
import { createState } from './project-access';
import { RouterLink } from '@angular/router';
import { ProjectPolicies } from './project-policies';
import { ProjectServiceAccounts } from './project-service-accounts';
import { PageHeader } from '../../shared/ui/page-header';

/** The permission policies of the project (R-UI-041, R-AUTHZ-026, Q-UI-129): `/projects/:project/policies`. */
@Component({
  selector: 'trove-project-policies-page',
  imports: [PageHeader, ProjectPolicies, RouterLink],
  template: `
    <trove-page-header heading="Permission policies" i18n-heading="@@route.permissions.title">
      @if (state() === 'enabled') {
        <a actions class="btn btn-confirm" [routerLink]="['/projects', project(), 'policies', 'new']" i18n="@@project-policies.create"
          >Create policy</a
        >
      }
    </trove-page-header>
    <p class="muted" i18n="@@project.policies-hint">
      The policies of the project grant its principals the actions they name on the project's repositories, and only on those.
    </p>
    <trove-project-policies [project]="project()" />
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class ProjectPoliciesPage {
  /** Route parameter `:project`. */
  readonly project = input.required<string>();
  private readonly session = inject(SessionStore);
  /** Who may create a policy here: `policies:write` or the project's Owner; the header offers it (Q-UI-307). */
  protected readonly state = computed(() => createState(this.session, this.project(), 'policies:write'));
}

/** The service accounts of the project (R-UI-041, Q-UI-129): `/projects/:project/service-accounts`. */
@Component({
  selector: 'trove-project-service-accounts-page',
  imports: [PageHeader, ProjectServiceAccounts],
  template: `
    <trove-page-header heading="Service accounts" i18n-heading="@@route.service-accounts.title">
      @if (state() === 'enabled') {
        <button actions class="btn btn-confirm" type="button" (click)="accounts.add()" i18n="@@project-sas.create">Create service account</button>
      }
    </trove-page-header>
    <p class="muted" i18n="@@project.service-accounts-hint">
      A service account of the project acts for a pipeline or a partner. Only the project's own policies grant to it; open one to issue its tokens.
    </p>
    <trove-project-service-accounts #accounts [project]="project()" />
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class ProjectServiceAccountsPage {
  /** Route parameter `:project`. */
  readonly project = input.required<string>();
  private readonly session = inject(SessionStore);
  /** Who may create an account here: `identities:write` or the project's Owner; the header offers it (Q-UI-307). */
  protected readonly state = computed(() => createState(this.session, this.project(), 'identities:write'));
}
