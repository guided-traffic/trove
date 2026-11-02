import { Routes } from '@angular/router';
import { unsavedChangesGuard } from '../../shared/unsaved-guard';

/**
 * The CI identity screens of the administrators (R-UI-035, R-UI-036). The literal paths (`new`,
 * `issuances`) precede `:issuer`, whose children take the issuer from the path; the mapping editor
 * leaves with a question when it holds unsaved changes. The parent carries the title that gives the
 * breadcrumb its issuer step.
 */
export const CI_ROUTES: Routes = [
  {
    path: '',
    title: $localize`:@@route.ci.title:CI identity`,
    loadComponent: () => import('./issuer-list').then((m) => m.IssuerList),
  },
  {
    path: 'new',
    title: $localize`:@@route.ci.new.title:New CI issuer`,
    loadComponent: () => import('./issuer-detail').then((m) => m.IssuerCreate),
  },
  {
    path: 'issuances',
    title: $localize`:@@route.ci.issuances.title:Issued CI tokens`,
    loadComponent: () => import('./issuances-page').then((m) => m.IssuancesPage),
  },
  {
    path: ':issuer',
    title: $localize`:@@route.ci.issuer.title:CI issuer`,
    children: [
      { path: '', loadComponent: () => import('./issuer-detail').then((m) => m.IssuerDetail) },
      {
        path: 'test',
        title: $localize`:@@route.ci.test.title:Claim tester`,
        loadComponent: () => import('./claim-tester').then((m) => m.ClaimTester),
      },
      {
        path: 'mappings/new',
        title: $localize`:@@route.ci.mapping.new.title:New mapping`,
        canDeactivate: [unsavedChangesGuard],
        loadComponent: () => import('./mapping-editor').then((m) => m.MappingEditor),
      },
      {
        path: 'mappings/:mapping',
        title: $localize`:@@route.ci.mapping.title:Mapping`,
        canDeactivate: [unsavedChangesGuard],
        loadComponent: () => import('./mapping-editor').then((m) => m.MappingEditor),
      },
    ],
  },
];

/**
 * The same screens under a project (Q-CI-26), mounted in the projects' routes: the project's overview,
 * its mapping editor (the issuer travels in the query: `?issuer=`) and the dry run over the project's
 * tier.
 */
export const PROJECT_CI_ROUTES: Routes = [
  {
    path: ':key/ci',
    title: $localize`:@@route.project-ci.title:CI identity`,
    children: [
      { path: '', loadComponent: () => import('./project-ci').then((m) => m.ProjectCi) },
      {
        path: 'test',
        title: $localize`:@@route.project-ci.test.title:Claim tester`,
        loadComponent: () => import('./claim-tester').then((m) => m.ClaimTester),
      },
      {
        path: 'mappings/new',
        title: $localize`:@@route.project-ci.mapping.new.title:New mapping`,
        canDeactivate: [unsavedChangesGuard],
        loadComponent: () => import('./mapping-editor').then((m) => m.MappingEditor),
      },
      {
        path: 'mappings/:mapping',
        title: $localize`:@@route.project-ci.mapping.title:Mapping`,
        canDeactivate: [unsavedChangesGuard],
        loadComponent: () => import('./mapping-editor').then((m) => m.MappingEditor),
      },
    ],
  },
];
