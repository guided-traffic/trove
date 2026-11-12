import { inject } from '@angular/core';
import { CanActivateFn, Router, RouterStateSnapshot } from '@angular/router';
import { LoginApi } from '../api/login-api';
import { ProjectContext } from './project-context';
import { SessionStore } from './session-store';

/**
 * The start page (Q-UI-129): a caller with exactly one project starts in it, everyone else on the list of projects.
 * It runs after `authGuard`, so the session is known.
 */
export const homeGuard: CanActivateFn = async () => {
  const session = inject(SessionStore);
  const router = inject(Router);
  const context = inject(ProjectContext);
  await session.load();
  const projects = await context.ready();
  const only = session.authenticated() && projects.length === 1 ? projects[0] : undefined;
  return router.createUrlTree(only ? ['/projects', only.key] : ['/projects']);
};

/**
 * `/admin` itself starts at the first screen the caller may use: the dashboard for a caller who may read the
 * installation's facts (Q-OPS-175), else the repositories, or the projects for a holder of `projects:read` without the
 * repository capability (R-UI-041). It runs after `authGuard`, so the session is known; a redirect function of the
 * route would run before it.
 */
export const adminHomeGuard: CanActivateFn = async () => {
  const session = inject(SessionStore);
  const router = inject(Router);
  await session.load();
  if (session.has('settings:read')) return router.createUrlTree(['/admin', 'dashboard']);
  const projectsOnly = !session.has('repositories:read-config') && session.has('projects:read');
  if (projectsOnly) return router.createUrlTree(['/admin', 'projects']);
  // An auditor or a security administrator without the configuration capabilities starts at the security dashboard (Q-UI-130).
  const securityOnly =
    !session.has('repositories:read-config') && (session.has('vulnerabilities:triage') || session.has('content:metadata-read'));
  return router.createUrlTree(['/admin', securityOnly ? 'security' : 'repositories']);
};

/**
 * Shapes navigation only; the API decides (R-UI-005). A signed-in session passes. Without a
 * session the guard asks `login-options`: with public content the application runs anonymously
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
        if ((await login.options()).publicContent) return true;
      } catch {
        // No login options: send the user to the sign-in screen, which reports the failure.
      }
      return router.createUrlTree(['/login'], { queryParams: { returnTo: state.url } });
    }
  }
};
