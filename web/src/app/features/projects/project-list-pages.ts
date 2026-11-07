import { Component, input } from '@angular/core';
import { ProjectPolicies } from './project-policies';
import { ProjectServiceAccounts } from './project-service-accounts';

/** The permission policies of the project (R-UI-041, R-AUTHZ-026, Q-UI-129): `/projects/:project/policies`. */
@Component({
  selector: 'trove-project-policies-page',
  imports: [ProjectPolicies],
  template: `
    <header class="page-header">
      <h1 tabindex="-1" i18n="@@route.permissions.title">Permission policies</h1>
    </header>
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
}

/** The service accounts of the project (R-UI-041, Q-UI-129): `/projects/:project/service-accounts`. */
@Component({
  selector: 'trove-project-service-accounts-page',
  imports: [ProjectServiceAccounts],
  template: `
    <header class="page-header">
      <h1 tabindex="-1" i18n="@@route.service-accounts.title">Service accounts</h1>
    </header>
    <p class="muted" i18n="@@project.service-accounts-hint">
      A service account of the project acts for a pipeline or a partner. Only the project's own policies grant to it; open one to issue its tokens.
    </p>
    <trove-project-service-accounts [project]="project()" />
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class ProjectServiceAccountsPage {
  /** Route parameter `:project`. */
  readonly project = input.required<string>();
}
