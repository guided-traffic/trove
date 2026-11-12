import { Routes } from '@angular/router';
import { adminHomeGuard, authGuard, homeGuard } from './core/auth-guard';
import { CrumbSpec } from './core/breadcrumbs';

/** The breadcrumb step of a screen area whose routes are loaded lazily and have no title of their own. */
const wrapper = (label: string): { crumbs: CrumbSpec[] } => ({ crumbs: [{ label, link: true }] });

/** The named areas of the breadcrumb trail (R-UI-002); they are names, not pages. */
const area = {
  admin: { label: $localize`:@@nav.context.admin:Admin area`, link: false },
} satisfies Record<string, CrumbSpec>;

/**
 * The route map of R-UI-003 for the M1 screens. Every area is lazy (R-ARCH-004); the screens that
 * a later work package builds are placeholders with their final path and title. `/login` and
 * `/logged-out` need no session and stand alone, without the shell (`data.bare`, Q-UI-125).
 * Everything else sits behind `authGuard`, which only shapes navigation: the API decides (R-UI-005).
 */
export const routes: Routes = [
  {
    path: 'login',
    title: $localize`:@@route.login.title:Sign in`,
    // Without a session nothing of the shell can be used: the screen stands alone (Q-UI-125).
    data: { bare: true },
    // >>> M1 ui-b
    loadComponent: () => import('./features/login/login').then((m) => m.Login),
    // <<< M1 ui-b
  },
  {
    // The post-logout redirect URI registered at the identity provider (R-UI-004): a contract.
    path: 'logged-out',
    title: $localize`:@@route.logged-out.title:Signed out`,
    data: { bare: true },
    // >>> M1 ui-b
    loadComponent: () => import('./features/login/logged-out').then((m) => m.LoggedOut),
    // <<< M1 ui-b
  },
  {
    path: '',
    canActivate: [authGuard],
    children: [
      // The start page: a caller with exactly one project starts in it, everyone else on the list (Q-UI-129).
      { path: '', pathMatch: 'full', canActivate: [homeGuard], children: [] },
      {
        path: 'projects',
        data: wrapper($localize`:@@route.projects.title:Projects`),
        loadChildren: () =>
          import('./features/projects/project-context.routes').then((m) => m.PROJECT_CONTEXT_ROUTES),
      },
      {
        path: 'setup',
        loadChildren: () => import('./features/setup/setup.routes').then((m) => m.SETUP_ROUTES),
      },
      // The results page of the top bar's search: a page of the main area, never of the admin area (R-UI-098, R-UI-099).
      {
        path: 'search',
        title: $localize`:@@search.results.heading:Search`,
        data: wrapper($localize`:@@search.results.heading:Search`),
        loadComponent: () => import('./features/search/search-results').then((m) => m.SearchResults),
      },
      {
        path: 'guides',
        data: {
          crumbs: [{ label: $localize`:@@nav.guides:Guides`, link: true }] satisfies CrumbSpec[],
        },
        loadChildren: () => import('./features/guides/guides.routes').then((m) => m.GUIDES_ROUTES),
      },
      // >>> M2 manual
      {
        path: 'help',
        data: wrapper($localize`:@@route.help.title:Documentation`),
        loadChildren: () => import('./features/help/help.routes').then((m) => m.HELP_ROUTES),
      },
      // <<< M2 manual
      {
        path: 'profile',
        data: { crumbs: [{ label: $localize`:@@route.profile.title:Profile`, link: true }] satisfies CrumbSpec[] },
        loadChildren: () => import('./features/profile/profile.routes').then((m) => m.PROFILE_ROUTES),
      },
      {
        path: 'admin',
        data: { crumbs: [area.admin] satisfies CrumbSpec[] },
        children: [
          // The admin area starts at the dashboard (Q-OPS-175); a project Owner without the repository
          // capability starts at the projects (R-UI-041).
          { path: '', pathMatch: 'full', canActivate: [adminHomeGuard], children: [] },
          {
            path: 'dashboard',
            title: $localize`:@@route.dashboard.title:Dashboard`,
            loadComponent: () => import('./features/system/dashboard').then((m) => m.Dashboard),
          },
          {
            path: 'projects',
            data: wrapper($localize`:@@route.projects.title:Projects`),
            loadChildren: () => import('./features/projects/projects.routes').then((m) => m.PROJECTS_ROUTES),
          },
          {
            path: 'repositories',
            data: wrapper($localize`:@@route.repositories.title:Repositories`),
            loadChildren: () =>
              import('./features/repositories/repositories.routes').then((m) => m.REPOSITORIES_ROUTES),
          },
          {
            path: 'users',
            data: wrapper($localize`:@@route.users.title:Users`),
            loadChildren: () => import('./features/users/users.routes').then((m) => m.USERS_ROUTES),
          },
          {
            path: 'groups',
            data: wrapper($localize`:@@route.groups.title:Groups`),
            loadChildren: () => import('./features/groups/groups.routes').then((m) => m.GROUPS_ROUTES),
          },
          {
            path: 'service-accounts',
            data: wrapper($localize`:@@route.service-accounts.title:Service accounts`),
            loadChildren: () =>
              import('./features/service-accounts/service-accounts.routes').then(
                (m) => m.SERVICE_ACCOUNTS_ROUTES,
              ),
          },
          {
            path: 'permissions',
            data: wrapper($localize`:@@route.permissions.title:Permission policies`),
            loadChildren: () =>
              import('./features/permissions/permissions.routes').then((m) => m.PERMISSIONS_ROUTES),
          },
          {
            path: 'roles',
            data: wrapper($localize`:@@route.roles.title:System roles`),
            loadChildren: () => import('./features/roles/roles.routes').then((m) => m.ROLES_ROUTES),
          },
          {
            path: 'tokens',
            data: wrapper($localize`:@@route.tokens.title:Token administration`),
            loadChildren: () => import('./features/tokens/tokens.routes').then((m) => m.TOKENS_ROUTES),
          },
          // >>> M4 ci
          {
            path: 'ci',
            data: wrapper($localize`:@@route.ci.title:CI identity`),
            loadChildren: () => import('./features/ci/ci.routes').then((m) => m.CI_ROUTES),
          },
          // <<< M4 ci
          // >>> M5b bastion
          {
            path: 'bastions',
            data: wrapper($localize`:@@route.bastions.title:Bastions`),
            loadChildren: () => import('./features/bastions/bastions.routes').then((m) => m.BASTIONS_ROUTES),
          },
          // <<< M5b bastion
          {
            path: 'endpoints',
            data: wrapper($localize`:@@route.endpoints.title:Endpoints`),
            loadChildren: () =>
              import('./features/endpoints/endpoints.routes').then((m) => m.ENDPOINTS_ROUTES),
          },
          // >>> T-9 security dashboard
          {
            path: 'security',
            data: wrapper($localize`:@@route.security.title:Security dashboard`),
            loadChildren: () => import('./features/vulnerabilities/security.routes').then((m) => m.SECURITY_ROUTES),
          },
          // <<< T-9 security dashboard
          {
            path: 'audit',
            data: wrapper($localize`:@@route.audit.title:Audit log`),
            loadChildren: () => import('./features/audit/audit.routes').then((m) => m.AUDIT_ROUTES),
          },
          // >>> M4b vulnerabilities
          {
            path: 'advisory-sources',
            title: $localize`:@@route.vulnerabilities.sources.title:Advisory sources`,
            loadComponent: () => import('./features/vulnerabilities/vulnerability-sources').then((m) => m.SourcesPage),
          },
          // <<< M4b vulnerabilities
          {
            path: 'system',
            data: wrapper($localize`:@@route.system.title:System status`),
            loadChildren: () => import('./features/system/system.routes').then((m) => m.SYSTEM_ROUTES),
          },
          {
            path: 'settings',
            data: wrapper($localize`:@@nav.settings:Settings`),
            loadChildren: () =>
              import('./features/settings/settings.routes').then((m) => m.SETTINGS_ROUTES),
          },
        ],
      },
    ],
  },
  {
    path: '**',
    title: $localize`:@@route.notfound.title:Page not found`,
    loadComponent: () => import('./features/not-found/not-found').then((m) => m.NotFound),
  },
];
