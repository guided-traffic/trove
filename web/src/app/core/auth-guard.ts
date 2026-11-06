import { inject } from '@angular/core';
import { CanActivateFn, Router, RouterStateSnapshot } from '@angular/router';
import { LoginApi } from '../api/login-api';
import { SessionStore } from './session-store';

/**
 * `/admin` itself starts at the first screen the caller may use: the dashboard for a caller who may read the
 * installation's facts (Q-OPS-175), else the repositories, or the projects for a project administrator without the
 * repository capability (R-UI-041). It runs after `authGuard`, so the session is known; a redirect function of the
 * route would run before it.
 */
export const adminHomeGuard: CanActivateFn = async () => {
  const session = inject(SessionStore);
  const router = inject(Router);
  await session.load();
  if (session.has('settings:read')) return router.createUrlTree(['/admin', 'dashboard']);
  const projectsOnly =
    !session.has('repositories:read-config') && (session.has('projects:read') || session.isProjectOwner());
  return router.createUrlTree(['/admin', projectsOnly ? 'projects' : 'repositories']);
};

/**
 * Shapes navigation only; the API decides (R-UI-005). A signed-in session passes. Without a
 * session the guard asks `login-options`: with anonymous access the application runs anonymously
 * (R-UI-004), otherwise it goes to `/login?returnTo=<route>`. If the session could not be
 * determined at all (the server is down) the route is entered and the shell shows the failure.
 */
export const authGuard: CanActivateFn = async (_route, state: RouterStateSnapshot) => {
  const session = inject(SessionStore);
  const router = inject(Router);
  const login = inject(LoginApi);

  await session.load();
  switch (session.state()) {
    case 'authenticated':
    case 'failed':
      return true;
    default: {
      try {
        if ((await login.options()).anonymousAccess) return true;
      } catch {
        // No login options: send the user to the sign-in screen, which reports the failure.
      }
      return router.createUrlTree(['/login'], { queryParams: { returnTo: state.url } });
    }
  }
};
