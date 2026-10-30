import { Routes } from '@angular/router';
import { authGuard } from './core/auth-guard';
import { CrumbSpec } from './core/breadcrumbs';

/** The breadcrumb step of a screen area whose routes are loaded lazily and have no title of their own. */
const wrapper = (label: string): { crumbs: CrumbSpec[] } => ({ crumbs: [{ label, link: true }] });

/** The named areas of the breadcrumb trail (R-UI-002); they are names, not pages. */
const area = {
  content: { label: $localize`:@@nav.group.content:Packages and files`, link: false },
  admin: { label: $localize`:@@nav.group.admin:Administration`, link: false },
} satisfies Record<string, CrumbSpec>;

/**
 * The route map of R-UI-003 for the M1 screens. Every area is lazy (R-ARCH-004); the screens that
 * a later work package builds are placeholders with their final path and title. `/login` and
 * `/logged-out` need no session. Everything else sits behind `authGuard`, which only shapes
 * navigation: the API decides (R-UI-005).
 */
export const routes: Routes = [
  {
    path: 'login',
    title: $localize`:@@route.login.title:Sign in`,
    // >>> M1 ui-b
    loadComponent: () => import('./features/login/login').then((m) => m.Login),
    // <<< M1 ui-b
  },
  {
    // The post-logout redirect URI registered at the identity provider (R-UI-004): a contract.
    path: 'logged-out',
    title: $localize`:@@route.logged-out.title:Signed out`,
    // >>> M1 ui-b
    loadComponent: () => import('./features/login/logged-out').then((m) => m.LoggedOut),
    // <<< M1 ui-b
  },
  {
    path: '',
    canActivate: [authGuard],
    children: [
      // The packages list (R-UI-006) is the start page once the packages area exists; until
      // then the file tree is the first content screen (Q-UI-10).
      { path: '', pathMatch: 'full', redirectTo: 'browse' },
      {
        path: 'browse',
        data: { crumbs: [area.content, { label: $localize`:@@nav.browse:Browse`, link: true }] satisfies CrumbSpec[] },
        loadChildren: () => import('./features/browse/browse.routes').then((m) => m.BROWSE_ROUTES),
      },
      // >>> M2 ui-packages
      {
        path: 'packages',
        data: {
          crumbs: [area.content, { label: $localize`:@@nav.packages:Packages`, link: true }] satisfies CrumbSpec[],
        },
        loadChildren: () =>
          import('./features/packages/packages.routes').then((m) => m.PACKAGES_ROUTES),
      },
      {
        path: 'setup',
        loadChildren: () => import('./features/setup/setup.routes').then((m) => m.SETUP_ROUTES),
      },
      // <<< M2 ui-packages
      {
        path: 'profile',
        data: { crumbs: [{ label: $localize`:@@route.profile.title:Profile`, link: true }] satisfies CrumbSpec[] },
        loadChildren: () => import('./features/profile/profile.routes').then((m) => m.PROFILE_ROUTES),
      },
      {
        path: 'admin',
        data: { crumbs: [area.admin] satisfies CrumbSpec[] },
        children: [
          { path: '', pathMatch: 'full', redirectTo: 'repositories' },
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
          {
            path: 'endpoints',
            data: wrapper($localize`:@@route.endpoints.title:Endpoints`),
            loadChildren: () =>
              import('./features/endpoints/endpoints.routes').then((m) => m.ENDPOINTS_ROUTES),
          },
          {
            path: 'audit',
            data: wrapper($localize`:@@route.audit.title:Audit log`),
            loadChildren: () => import('./features/audit/audit.routes').then((m) => m.AUDIT_ROUTES),
          },
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
