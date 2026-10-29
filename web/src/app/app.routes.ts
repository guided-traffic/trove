import { Routes } from '@angular/router';
import { authGuard } from './core/auth-guard';

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
        loadChildren: () => import('./features/browse/browse.routes').then((m) => m.BROWSE_ROUTES),
      },
      {
        path: 'profile',
        loadChildren: () => import('./features/profile/profile.routes').then((m) => m.PROFILE_ROUTES),
      },
      {
        path: 'admin',
        children: [
          { path: '', pathMatch: 'full', redirectTo: 'repositories' },
          {
            path: 'repositories',
            loadChildren: () =>
              import('./features/repositories/repositories.routes').then((m) => m.REPOSITORIES_ROUTES),
          },
          {
            path: 'users',
            loadChildren: () => import('./features/users/users.routes').then((m) => m.USERS_ROUTES),
          },
          {
            path: 'groups',
            loadChildren: () => import('./features/groups/groups.routes').then((m) => m.GROUPS_ROUTES),
          },
          {
            path: 'service-accounts',
            loadChildren: () =>
              import('./features/service-accounts/service-accounts.routes').then(
                (m) => m.SERVICE_ACCOUNTS_ROUTES,
              ),
          },
          {
            path: 'permissions',
            loadChildren: () =>
              import('./features/permissions/permissions.routes').then((m) => m.PERMISSIONS_ROUTES),
          },
          {
            path: 'roles',
            loadChildren: () => import('./features/roles/roles.routes').then((m) => m.ROLES_ROUTES),
          },
          {
            path: 'tokens',
            loadChildren: () => import('./features/tokens/tokens.routes').then((m) => m.TOKENS_ROUTES),
          },
          {
            path: 'endpoints',
            loadChildren: () =>
              import('./features/endpoints/endpoints.routes').then((m) => m.ENDPOINTS_ROUTES),
          },
          {
            path: 'audit',
            loadChildren: () => import('./features/audit/audit.routes').then((m) => m.AUDIT_ROUTES),
          },
          {
            path: 'system',
            loadChildren: () => import('./features/system/system.routes').then((m) => m.SYSTEM_ROUTES),
          },
          {
            path: 'settings',
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
